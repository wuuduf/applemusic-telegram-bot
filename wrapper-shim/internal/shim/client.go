// Package shim implements a small TCP listener that speaks the original
// `wrapper` raw-TCP protocol on the front, and translates each request into
// the gRPC API exposed by `wrapper-manager` on the back.
//
// This lets clients written against the single-account `wrapper` (such as
// applemusic-telegram-bot) keep working byte-for-byte while the actual decrypt
// + m3u8 work is served by a multi-account wrapper-manager deployment.
package shim

import (
	"context"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/wuuduf/applemusic-telegram-bot/wrapper-shim/proto"
)

// Client is a thin wrapper around the wrapper-manager gRPC stub. The
// underlying ClientConn handles reconnects and load balancing, so a single
// Client instance is shared by every TCP request handler.
type Client struct {
	addr string
	conn *grpc.ClientConn
	stub pb.WrapperManagerServiceClient
}

// NewClient dials wrapper-manager at addr (e.g. "127.0.0.1:8080") with no
// transport security. wrapper-manager itself does not advertise TLS, so the
// shim is meant to live on the same trusted network segment as the manager.
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dial wrapper-manager %s: %w", addr, err)
	}
	return &Client{
		addr: addr,
		conn: conn,
		stub: pb.NewWrapperManagerServiceClient(conn),
	}, nil
}

// Close releases the gRPC connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Addr reports the configured manager address (used for log lines).
func (c *Client) Addr() string { return c.addr }

// WaitReady polls Status until the manager reports Ready=true or the timeout
// elapses. It is best-effort: returning an error here only logs a warning,
// the listeners still come up so they can serve traffic the moment the manager
// finishes warming up its instances.
func (c *Client) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		resp, err := c.stub.Status(callCtx, &emptypb.Empty{})
		cancel()
		if err == nil && resp != nil && resp.Data != nil && resp.Data.Ready && resp.Data.ClientCount > 0 {
			log.Printf("wrapper-manager ready: %d instance(s), regions=%v",
				resp.Data.ClientCount, resp.Data.Regions)
			return nil
		}
		if err != nil {
			log.Printf("wrapper-manager status: %v (still waiting)", err)
		} else if resp != nil && resp.Data != nil {
			log.Printf("wrapper-manager status: ready=%v instances=%d (still waiting)",
				resp.Data.Ready, resp.Data.ClientCount)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wrapper-manager not ready after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// M3U8 issues the unary M3U8 RPC and returns the enhanced HLS URL or an error.
// An empty URL with no error is treated as "not available" and surfaced to the
// caller so the TCP shim can drop the connection without writing garbage back
// to the bot.
func (c *Client) M3U8(ctx context.Context, adamID string) (string, error) {
	resp, err := c.stub.M3U8(ctx, &pb.M3U8Request{
		Data: &pb.M3U8DataRequest{AdamId: adamID},
	})
	if err != nil {
		return "", fmt.Errorf("M3U8 rpc: %w", err)
	}
	if resp == nil || resp.Header == nil {
		return "", fmt.Errorf("M3U8 rpc: empty reply")
	}
	if resp.Header.Code != 0 {
		return "", fmt.Errorf("M3U8 rpc: code=%d msg=%s", resp.Header.Code, resp.Header.Msg)
	}
	if resp.Data == nil {
		return "", nil
	}
	return resp.Data.M3U8, nil
}

// OpenDecryptStream starts a fresh bidirectional Decrypt stream. The caller
// owns the stream lifecycle: send requests, receive replies, and CloseSend
// when done. Each shim TCP connection corresponds to exactly one such stream.
func (c *Client) OpenDecryptStream(ctx context.Context) (grpc.BidiStreamingClient[pb.DecryptRequest, pb.DecryptReply], error) {
	return c.stub.Decrypt(ctx)
}
