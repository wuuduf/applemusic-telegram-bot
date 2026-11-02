package runv2

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func TestWrapperHTTPWatchdogInterruptsBlockedDecrypt(t *testing.T) {
	t.Setenv(runv2IdleTimeoutEnvKey, "30")
	fixture, err := os.ReadFile("testdata/cbcs_audio.mp4")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	entered := make(chan struct{})
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Consume the actual track/key strings and first framed sample before
		// going silent, so the caller is waiting for a decrypt response.
		for _, want := range []string{"1", "skd://example"} {
			var size [1]byte
			if _, err := io.ReadFull(conn, size[:]); err != nil {
				return
			}
			value := make([]byte, size[0])
			if _, err := io.ReadFull(conn, value); err != nil || string(value) != want {
				return
			}
		}
		var header [4]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(header[:])
		if size == 0 || size > 10<<20 {
			return
		}
		if _, err := io.CopyN(io.Discard, conn, int64(size)); err != nil {
			return
		}
		close(entered)
		_, _ = io.Copy(io.Discard, conn)
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/playlist.m3u8" {
			_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:10\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://example\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n#EXTINF:10,\n#EXT-X-BYTERANGE:%d@0\nmedia.mp4\n#EXT-X-ENDLIST\n", len(fixture))
			return
		}
		_, _ = w.Write(fixture)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := filepath.Join(t.TempDir(), "out.m4a")
	done := make(chan error, 1)
	go func() {
		done <- RunWithContext(ctx, "1", server.URL+"/playlist.m3u8", out, structs.ConfigSet{DecryptM3u8Port: listener.Addr().String()}, nil)
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("run exited before decrypt request: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("decrypt request never reached peer")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("expected HTTP watchdog cause, got %v", err)
		}
	case <-time.After(34 * time.Second):
		t.Fatal("HTTP watchdog did not interrupt blocked decrypt")
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial output not removed: %v", err)
	}
	select {
	case <-peerDone:
	case <-time.After(time.Second):
		t.Fatal("peer not closed")
	}
}

func TestWrapperSingleWriteHasConservativeOperationCapDespitePartialProgress(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := wrapDecryptConn(context.Background(), client, 150*time.Millisecond)
	defer conn.Close()
	go func() {
		buf := make([]byte, 1)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
			time.Sleep(40 * time.Millisecond)
		}
	}()
	n, err := conn.Write(make([]byte, 16))
	var netErr net.Error
	if n <= 0 || n >= 16 || !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected partial progress then per-call timeout, got n=%d err=%v", n, err)
	}
}

func TestWrapperSuccessfulShutdownSendsMarker(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := wrapDecryptConn(context.Background(), client, time.Minute)
	done := make(chan error, 1)
	go func() { done <- Close(conn) }()
	marker := make([]byte, 5)
	if _, err := io.ReadFull(server, marker); err != nil {
		t.Fatal(err)
	}
	for _, b := range marker {
		if b != 0 {
			t.Fatalf("unexpected marker %v", marker)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := server.Read(marker); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestWrapperSuccessfulShutdownIsBounded(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := wrapDecryptConn(context.Background(), client, time.Minute)
	defer conn.Close()
	done := make(chan error, 1)
	go func() { done <- Close(conn) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("successful shutdown blocked on a peer that does not read")
	}
}
