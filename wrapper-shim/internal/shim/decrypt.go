package shim

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	pb "github.com/wuuduf/applemusic-telegram-bot/wrapper-shim/proto"
)

// prefetchAdamID is the placeholder identifier the bot writes whenever the
// HLS key URI matches the well-known Apple "prefetch" key. wrapper-manager's
// underlying decrypt instance handles the substitution itself, so the shim
// only uses this value to decide which "real" adamId to forward over gRPC for
// dispatcher routing.
const prefetchAdamID = "0"

// DecryptServer replays the original wrapper's decrypt TCP protocol:
//
//   - On a fresh connection the client first sends two length-prefixed
//     strings: [1B len][adamId][1B len][keyURI]. This sets the active
//     (adamId, key) state.
//   - Afterwards the client sends a stream of samples framed as
//     [uint32 LE len][len bytes]. The server is expected to write back
//     exactly len bytes of decrypted data per sample.
//   - A length value of 0 followed by a non-zero byte is the "switch keys"
//     signal: it is followed by another [1B len][adamId][1B len][keyURI]
//     state update. Subsequent samples use the new state.
//   - The byte sequence 00 00 00 00 00 (a zero u32 followed by a zero byte)
//     is the close marker; the server closes the connection.
//
// Each accepted TCP connection maps to exactly one wrapper-manager.Decrypt
// bidi stream so the manager can keep the underlying wrapper instance pinned
// for the duration of the song.
type DecryptServer struct {
	addr   string
	client *Client
	// idleTimeout caps how long the shim will wait for the next sample on
	// an established connection. Long ALAC songs send hundreds of samples
	// over many seconds; the bot's runv2 caps the TCP idle to 5 minutes by
	// default, so we mirror that.
	idleTimeout time.Duration
}

// NewDecryptServer constructs a DecryptServer listening on addr (e.g. ":10020").
func NewDecryptServer(addr string, client *Client) *DecryptServer {
	return &DecryptServer{
		addr:        addr,
		client:      client,
		idleTimeout: 5 * time.Minute,
	}
}

// ListenAndServe blocks accepting connections until ctx is cancelled.
func (s *DecryptServer) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("decrypt listen %s: %w", s.addr, err)
	}
	defer listener.Close()
	log.Printf("decrypt shim listening on %s -> %s", s.addr, s.client.Addr())

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
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return fmt.Errorf("decrypt accept: %w", err)
		}
		go s.handle(ctx, conn)
	}
}

// handle owns the lifecycle of one bot connection and its paired gRPC stream.
func (s *DecryptServer) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	stream, err := s.client.OpenDecryptStream(streamCtx)
	if err != nil {
		log.Printf("decrypt %s: open stream: %v", remote, err)
		return
	}
	// CloseSend signals the manager to flush the stream cleanly; the
	// goroutine then drains the final reply (if any) so we don't leak.
	defer func() {
		if err := stream.CloseSend(); err != nil && !errors.Is(err, io.EOF) {
			log.Printf("decrypt %s: close send: %v", remote, err)
		}
	}()

	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	defer bw.Flush()

	// State for the active (adamId, key) pair. realAdamID stores the most
	// recent non-prefetch adamId; we use it for gRPC dispatcher routing
	// even while wireAdamID is the placeholder "0".
	var (
		wireAdamID string
		realAdamID string
		key        string
	)

	resetDeadline := func() error {
		return conn.SetReadDeadline(time.Now().Add(s.idleTimeout))
	}

	// Initial state: [1B len][adamId][1B len][keyURI]
	if err := resetDeadline(); err != nil {
		log.Printf("decrypt %s: set deadline: %v", remote, err)
		return
	}
	wireAdamID, key, err = readState(br)
	if err != nil {
		log.Printf("decrypt %s: read initial state: %v", remote, err)
		return
	}
	if wireAdamID != prefetchAdamID {
		realAdamID = wireAdamID
	}
	log.Printf("decrypt %s: opened stream wireAdam=%s realAdam=%s key=%s",
		remote, wireAdamID, realAdamID, truncatedKey(key))

	sampleIndex := int32(0)

	for {
		if err := resetDeadline(); err != nil {
			log.Printf("decrypt %s: set deadline: %v", remote, err)
			return
		}

		// Peek a u32 length. Zero is overloaded as a control marker.
		var lenBuf [4]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			if errors.Is(err, io.EOF) {
				log.Printf("decrypt %s: client closed (eof)", remote)
				return
			}
			log.Printf("decrypt %s: read frame length: %v", remote, err)
			return
		}
		n := binary.LittleEndian.Uint32(lenBuf[:])

		if n == 0 {
			// Either a key switch (followed by [adamLen][adam][keyLen][key])
			// or the close marker (followed by a single 0x00 byte).
			markerByte, err := br.ReadByte()
			if err != nil {
				log.Printf("decrypt %s: read marker byte: %v", remote, err)
				return
			}
			if markerByte == 0x00 {
				log.Printf("decrypt %s: client sent close marker", remote)
				return
			}
			// markerByte is the adamLen of the new state.
			adamID, errRead := readWithLen(br, int(markerByte))
			if errRead != nil {
				log.Printf("decrypt %s: read switched adamId: %v", remote, errRead)
				return
			}
			newKey, errRead := readLengthPrefixedString(br)
			if errRead != nil {
				log.Printf("decrypt %s: read switched key: %v", remote, errRead)
				return
			}
			wireAdamID = adamID
			key = newKey
			if wireAdamID != prefetchAdamID {
				realAdamID = wireAdamID
			}
			log.Printf("decrypt %s: state switched wireAdam=%s realAdam=%s key=%s",
				remote, wireAdamID, realAdamID, truncatedKey(key))
			continue
		}

		// Sample frame: read n bytes and forward to wrapper-manager.
		if n > maxSampleBytes {
			log.Printf("decrypt %s: refusing oversized sample %d bytes", remote, n)
			return
		}
		sample := make([]byte, n)
		if _, err := io.ReadFull(br, sample); err != nil {
			log.Printf("decrypt %s: read sample %d bytes: %v", remote, n, err)
			return
		}

		dispatchAdam := realAdamID
		if dispatchAdam == "" {
			// Should only happen if the very first state had adamId="0".
			dispatchAdam = wireAdamID
		}

		decrypted, err := decryptOne(stream, dispatchAdam, key, sampleIndex, sample)
		if err != nil {
			log.Printf("decrypt %s: rpc sample idx=%d adam=%s: %v",
				remote, sampleIndex, dispatchAdam, err)
			return
		}
		if uint32(len(decrypted)) != n {
			log.Printf("decrypt %s: size mismatch: sent %d got %d", remote, n, len(decrypted))
			return
		}
		if err := conn.SetWriteDeadline(time.Now().Add(s.idleTimeout)); err != nil {
			log.Printf("decrypt %s: set write deadline: %v", remote, err)
			return
		}
		if _, err := bw.Write(decrypted); err != nil {
			log.Printf("decrypt %s: write decrypted: %v", remote, err)
			return
		}
		if err := bw.Flush(); err != nil {
			log.Printf("decrypt %s: flush: %v", remote, err)
			return
		}
		sampleIndex++
	}
}

// maxSampleBytes is a sanity cap to avoid unbounded allocations if the bot
// somehow misframes its stream. ALAC samples are typically under a few MB; we
// pick a generous 64 MiB ceiling.
const maxSampleBytes = 64 * 1024 * 1024

func decryptOne(stream interface {
	Send(*pb.DecryptRequest) error
	Recv() (*pb.DecryptReply, error)
}, adamID, key string, idx int32, sample []byte) ([]byte, error) {
	req := &pb.DecryptRequest{
		Data: &pb.DecryptData{
			AdamId:      adamID,
			Key:         key,
			SampleIndex: idx,
			Sample:      sample,
		},
	}
	if err := stream.Send(req); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	reply, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("recv: %w", err)
	}
	if reply == nil || reply.Header == nil {
		return nil, fmt.Errorf("empty reply")
	}
	if reply.Header.Code != 0 {
		return nil, fmt.Errorf("code=%d msg=%s", reply.Header.Code, reply.Header.Msg)
	}
	if reply.Data == nil {
		return nil, fmt.Errorf("missing reply data")
	}
	return reply.Data.Sample, nil
}

// readState consumes [1B adamLen][adam][1B keyLen][key] and returns the pair.
func readState(r *bufio.Reader) (adamID, key string, err error) {
	adamID, err = readLengthPrefixedString(r)
	if err != nil {
		return "", "", fmt.Errorf("adamId: %w", err)
	}
	key, err = readLengthPrefixedString(r)
	if err != nil {
		return "", "", fmt.Errorf("key: %w", err)
	}
	return adamID, key, nil
}

// truncatedKey returns a short, log-friendly representation of an HLS key URI
// so we don't dump the full skd:// URI on every state transition.
func truncatedKey(s string) string {
	if len(s) <= 20 {
		return s
	}
	return s[:17] + "..."
}
