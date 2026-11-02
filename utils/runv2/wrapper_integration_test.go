package runv2

import (
	"context"
	"errors"
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

func TestWrapperContextDeadlineInterruptsRead(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	conn := wrapDecryptConn(ctx, client, time.Minute)
	defer conn.Close()
	_, err := conn.Read(make([]byte, 1))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func TestRunWithContextClosesWrapperOnCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer c.Close()
		close(accepted)
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err = c.Read(make([]byte, 1))
		closed <- err
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/playlist.m3u8" {
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\n#EXT-X-BYTERANGE:1024@0\nmedia.mp4\n#EXT-X-ENDLIST\n")
			return
		}
		w.WriteHeader(http.StatusOK)
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
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("wrapper dial did not connect")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop after cancellation")
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial output not removed: %v", err)
	}
	select {
	case err := <-closed:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected wrapper connection closed, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wrapper connection was not closed")
	}
}
