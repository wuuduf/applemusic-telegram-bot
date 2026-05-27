package shim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

// M3U8Server replays the original wrapper's GetM3U8 TCP protocol.
//
// On every accepted connection the client sends:
//
//	[1 byte: adamIdLen][adamIdLen bytes: adamId]
//
// and expects to read back a UTF-8 line terminated by '\n':
//
//	<enhanced m3u8 url>\n
//
// On any error we close the connection without sending anything; bot-side
// readers will simply observe EOF and fall back to the original m3u8 URL.
type M3U8Server struct {
	addr   string
	client *Client
	// readTimeout is how long we wait for the bot to finish writing the
	// adamId. Mirrors the wrapper-manager side timeouts.
	readTimeout time.Duration
	// rpcTimeout is the deadline for each upstream gRPC M3U8 call.
	rpcTimeout time.Duration
}

// NewM3U8Server constructs an M3U8Server listening on addr (e.g. ":20020").
func NewM3U8Server(addr string, client *Client) *M3U8Server {
	return &M3U8Server{
		addr:        addr,
		client:      client,
		readTimeout: 10 * time.Second,
		rpcTimeout:  20 * time.Second,
	}
}

// ListenAndServe blocks accepting connections until ctx is cancelled or the
// listener fails fatally.
func (s *M3U8Server) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("m3u8 listen %s: %w", s.addr, err)
	}
	defer listener.Close()
	log.Printf("m3u8 shim listening on %s -> %s", s.addr, s.client.Addr())

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// Transient accept errors should not kill the server.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return fmt.Errorf("m3u8 accept: %w", err)
		}
		go s.handle(ctx, conn)
	}
}

func (s *M3U8Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()

	if err := conn.SetReadDeadline(time.Now().Add(s.readTimeout)); err != nil {
		log.Printf("m3u8 %s: set read deadline: %v", remote, err)
		return
	}

	adamID, err := readLengthPrefixedString(conn)
	if err != nil {
		log.Printf("m3u8 %s: read adamId: %v", remote, err)
		return
	}
	if adamID == "" {
		log.Printf("m3u8 %s: empty adamId", remote)
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, s.rpcTimeout)
	defer cancel()
	url, err := s.client.M3U8(callCtx, adamID)
	if err != nil {
		log.Printf("m3u8 %s: rpc adamId=%s: %v", remote, adamID, err)
		return
	}
	if url == "" {
		log.Printf("m3u8 %s: empty url for adamId=%s", remote, adamID)
		return
	}

	if err := conn.SetWriteDeadline(time.Now().Add(s.readTimeout)); err != nil {
		log.Printf("m3u8 %s: set write deadline: %v", remote, err)
		return
	}
	if _, err := io.WriteString(conn, url); err != nil {
		log.Printf("m3u8 %s: write url: %v", remote, err)
		return
	}
	if _, err := conn.Write([]byte{'\n'}); err != nil {
		log.Printf("m3u8 %s: write newline: %v", remote, err)
		return
	}
	log.Printf("m3u8 %s: served adamId=%s", remote, adamID)
}
