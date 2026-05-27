package shim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/wuuduf/applemusic-telegram-bot/wrapper-shim/proto"
)

// fakeManager implements just enough of WrapperManagerService to exercise the
// shim's M3U8 unary path and Decrypt bidi path. It records every Decrypt
// request it sees so tests can assert on adamId/key routing decisions.
type fakeManager struct {
	pb.UnimplementedWrapperManagerServiceServer

	mu        sync.Mutex
	decReqs   []*pb.DecryptData
	m3u8For   map[string]string
	m3u8Code  map[string]int32 // optional non-zero codes per adamId
	xorKey    byte             // applied to sample bytes so decrypt produces deterministic output
	closeRecv chan struct{}
}

func newFakeManager() *fakeManager {
	return &fakeManager{
		m3u8For:   map[string]string{},
		m3u8Code:  map[string]int32{},
		xorKey:    0xA5,
		closeRecv: make(chan struct{}, 1),
	}
}

func (f *fakeManager) Status(_ context.Context, _ *emptypb.Empty) (*pb.StatusReply, error) {
	return &pb.StatusReply{
		Header: &pb.ReplyHeader{Code: 0, Msg: "ok"},
		Data: &pb.StatusData{
			Status:      true,
			Regions:     []string{"us"},
			ClientCount: 1,
			Ready:       true,
		},
	}, nil
}

func (f *fakeManager) M3U8(_ context.Context, req *pb.M3U8Request) (*pb.M3U8Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if code, bad := f.m3u8Code[req.GetData().GetAdamId()]; bad {
		return &pb.M3U8Reply{
			Header: &pb.ReplyHeader{Code: code, Msg: "synthetic failure"},
		}, nil
	}
	url := f.m3u8For[req.GetData().GetAdamId()]
	return &pb.M3U8Reply{
		Header: &pb.ReplyHeader{Code: 0, Msg: "ok"},
		Data: &pb.M3U8DataResponse{
			AdamId: req.GetData().GetAdamId(),
			M3U8:   url,
		},
	}, nil
}

func (f *fakeManager) Decrypt(stream grpc.BidiStreamingServer[pb.DecryptRequest, pb.DecryptReply]) error {
	defer func() {
		select {
		case f.closeRecv <- struct{}{}:
		default:
		}
	}()
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		f.mu.Lock()
		// Snapshot the request data so test assertions read a stable copy.
		f.decReqs = append(f.decReqs, &pb.DecryptData{
			AdamId:      req.GetData().GetAdamId(),
			Key:         req.GetData().GetKey(),
			SampleIndex: req.GetData().GetSampleIndex(),
			Sample:      append([]byte(nil), req.GetData().GetSample()...),
		})
		f.mu.Unlock()
		// "Decrypt" by XORing each byte; preserves length so the shim can
		// echo exactly len(sample) bytes back to the bot.
		out := make([]byte, len(req.GetData().GetSample()))
		for i, b := range req.GetData().GetSample() {
			out[i] = b ^ f.xorKey
		}
		if err := stream.Send(&pb.DecryptReply{
			Header: &pb.ReplyHeader{Code: 0, Msg: "ok"},
			Data: &pb.DecryptData{
				AdamId:      req.GetData().GetAdamId(),
				Key:         req.GetData().GetKey(),
				SampleIndex: req.GetData().GetSampleIndex(),
				Sample:      out,
			},
		}); err != nil {
			return err
		}
	}
}

// startFakeManager spins up an in-process gRPC server bound to a random port
// and returns the listen address plus a teardown function.
func startFakeManager(t *testing.T) (*fakeManager, string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	fake := newFakeManager()
	pb.RegisterWrapperManagerServiceServer(srv, fake)
	go func() { _ = srv.Serve(listener) }()
	teardown := func() {
		srv.GracefulStop()
	}
	return fake, listener.Addr().String(), teardown
}

func startShim(t *testing.T, managerAddr string) (m3u8Addr, decryptAddr string, stop func()) {
	t.Helper()
	client, err := NewClient(managerAddr)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// Bind to :0 so multiple parallel tests don't collide on fixed ports.
	m3u8Listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("m3u8 listen: %v", err)
	}
	decListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("decrypt listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	m3u8Srv := &M3U8Server{
		addr:        m3u8Listener.Addr().String(),
		client:      client,
		readTimeout: 5 * time.Second,
		rpcTimeout:  5 * time.Second,
	}
	decSrv := &DecryptServer{
		addr:        decListener.Addr().String(),
		client:      client,
		idleTimeout: 5 * time.Second,
	}

	// Hand the pre-bound listeners to the servers so we know the actual
	// ports and can avoid a startup race in tests.
	go serveM3U8(ctx, m3u8Listener, m3u8Srv)
	go serveDecrypt(ctx, decListener, decSrv)

	stop = func() {
		cancel()
		_ = m3u8Listener.Close()
		_ = decListener.Close()
		_ = client.Close()
	}
	return m3u8Listener.Addr().String(), decListener.Addr().String(), stop
}

// serveM3U8 / serveDecrypt mirror ListenAndServe but with a caller-provided
// listener so the test knows the bound port up-front.
func serveM3U8(ctx context.Context, l net.Listener, s *M3U8Server) {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go s.handle(ctx, conn)
	}
}

func serveDecrypt(ctx context.Context, l net.Listener, s *DecryptServer) {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go s.handle(ctx, conn)
	}
}

// dialInsecure ensures the test gRPC dial returns once the connection is
// usable so subsequent calls don't hit "connection refused" races.
func dialInsecure(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return cc
}

func TestM3U8WireProtocol(t *testing.T) {
	fake, mgrAddr, fakeStop := startFakeManager(t)
	defer fakeStop()
	fake.m3u8For["1234567890"] = "https://example.com/foo.m3u8"

	m3u8Addr, _, stop := startShim(t, mgrAddr)
	defer stop()

	// Wait for shim's gRPC client to come up by issuing a quick Status call.
	cc := dialInsecure(t, mgrAddr)
	_, _ = pb.NewWrapperManagerServiceClient(cc).Status(context.Background(), &emptypb.Empty{})
	_ = cc.Close()

	conn, err := net.DialTimeout("tcp", m3u8Addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial m3u8: %v", err)
	}
	defer conn.Close()
	adamID := "1234567890"
	if _, err := conn.Write([]byte{byte(len(adamID))}); err != nil {
		t.Fatalf("write len: %v", err)
	}
	if _, err := io.WriteString(conn, adamID); err != nil {
		t.Fatalf("write adamId: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(bytes.TrimSpace(resp))
	if got != "https://example.com/foo.m3u8" {
		t.Fatalf("unexpected response: %q", got)
	}
}

func TestM3U8RpcErrorClosesConnection(t *testing.T) {
	fake, mgrAddr, fakeStop := startFakeManager(t)
	defer fakeStop()
	fake.m3u8Code["bad"] = -1

	m3u8Addr, _, stop := startShim(t, mgrAddr)
	defer stop()

	conn, err := net.DialTimeout("tcp", m3u8Addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{byte(len("bad"))}); err != nil {
		t.Fatalf("write len: %v", err)
	}
	if _, err := io.WriteString(conn, "bad"); err != nil {
		t.Fatalf("write adamId: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf, err := io.ReadAll(conn)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read: %v", err)
	}
	if len(buf) != 0 {
		t.Fatalf("expected empty body on rpc error, got %q", buf)
	}
}

// TestDecryptWireProtocol exercises the full state machine: initial state,
// multiple samples, a key switch (with a prefetch-key adamId of "0"), more
// samples, and the close marker. The fake manager XORs each sample so we can
// verify the shim returns exactly len(sample) decrypted bytes per sample and
// that adamId routing falls back to the most recent non-prefetch value.
func TestDecryptWireProtocol(t *testing.T) {
	fake, mgrAddr, fakeStop := startFakeManager(t)
	defer fakeStop()

	_, decAddr, stop := startShim(t, mgrAddr)
	defer stop()

	conn, err := net.DialTimeout("tcp", decAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Initial state: real adamId "9999" + real key.
	mustWriteString := func(s string) {
		t.Helper()
		if _, err := conn.Write([]byte{byte(len(s))}); err != nil {
			t.Fatalf("write len(%q): %v", s, err)
		}
		if _, err := io.WriteString(conn, s); err != nil {
			t.Fatalf("write str(%q): %v", s, err)
		}
	}
	mustWriteString("9999")
	mustWriteString("skd://itunes.apple.com/P000000000/s1/e2")

	mustExchange := func(label string, payload []byte) {
		t.Helper()
		if err := binary.Write(conn, binary.LittleEndian, uint32(len(payload))); err != nil {
			t.Fatalf("%s: write len: %v", label, err)
		}
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("%s: write payload: %v", label, err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("%s: read decrypted: %v", label, err)
		}
		want := make([]byte, len(payload))
		for i, b := range payload {
			want[i] = b ^ fake.xorKey
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: decrypted mismatch\n got=%x\nwant=%x", label, got, want)
		}
	}

	// Two samples on the real key.
	mustExchange("real-1", []byte("sample-one-payload"))
	mustExchange("real-2", []byte("another-sample"))

	// Key switch: prefetch key uses adamId "0" on the wire. Routing to gRPC
	// should still use the real adamId.
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		t.Fatalf("write switch: %v", err)
	}
	const prefetchKeyURI = "skd://itunes.apple.com/P000000000/s1/e1"
	mustWriteString("0")
	mustWriteString(prefetchKeyURI)
	mustExchange("prefetch-1", []byte("XYZ-prefetch"))

	// Switch back to the real key.
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		t.Fatalf("write switch 2: %v", err)
	}
	mustWriteString("9999")
	mustWriteString("skd://itunes.apple.com/P000000000/s1/e3")
	mustExchange("real-3", []byte("after-switch"))

	// Close marker.
	if _, err := conn.Write([]byte{0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("write close: %v", err)
	}
	_ = conn.Close()

	// Wait for fake server to observe the stream close.
	select {
	case <-fake.closeRecv:
	case <-time.After(2 * time.Second):
		t.Fatalf("fake manager never saw stream close")
	}

	// Validate routing decisions.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.decReqs) != 4 {
		t.Fatalf("expected 4 decrypt requests, got %d", len(fake.decReqs))
	}
	for i, want := range []struct{ adamID, key string }{
		{"9999", "skd://itunes.apple.com/P000000000/s1/e2"},
		{"9999", "skd://itunes.apple.com/P000000000/s1/e2"},
		// prefetch sample: still routed under 9999 (the most recent non-"0" id)
		{"9999", "skd://itunes.apple.com/P000000000/s1/e1"},
		{"9999", "skd://itunes.apple.com/P000000000/s1/e3"},
	} {
		got := fake.decReqs[i]
		if got.AdamId != want.adamID || got.Key != want.key {
			t.Errorf("decrypt req[%d] = (adam=%s key=%s), want (adam=%s key=%s)",
				i, got.AdamId, got.Key, want.adamID, want.key)
		}
		if got.SampleIndex != int32(i) {
			t.Errorf("decrypt req[%d] sample_index = %d, want %d", i, got.SampleIndex, i)
		}
	}
}
