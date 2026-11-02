package runv2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/itouakirai/mp4ff/mp4"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testDecryptConn(ctx context.Context, c net.Conn, timeout time.Duration) net.Conn {
	return newDecryptConn(ctx, c, timeout)
}

func TestDecryptSilentResponseIsBounded(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	defer client.Close()
	c := testDecryptConn(context.Background(), client, 120*time.Millisecond)
	defer c.Close()
	go func() {
		var n uint32
		if binary.Read(peer, binary.LittleEndian, &n) == nil {
			_, _ = io.CopyN(io.Discard, peer, int64(n))
		}
	}()
	done := make(chan error, 1)
	go func() {
		rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
		done <- cbcsFullSubsampleDecrypt(make([]byte, 16), rw)
	}()
	select {
	case err := <-done:
		if e, ok := err.(net.Error); !ok || !e.Timeout() {
			t.Fatalf("expected network timeout, got %v", err)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("decrypt response blocked indefinitely")
	}
}

func TestRunCancellationBeforeOutputCommit(t *testing.T) {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(48000, "audio", "und")
	if err := init.Moov.Trak.SetAACDescriptor(2, 48000); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := init.Encode(&data); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/list" {
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\n#EXT-X-BYTERANGE:%d@0\n/media\n#EXT-X-ENDLIST\n", data.Len())
		} else {
			_, _ = w.Write(data.Bytes())
		}
	}))
	defer server.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		c, e := listener.Accept()
		if e == nil {
			defer c.Close()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := filepath.Join(t.TempDir(), "track.m4a")
	err = RunWithContext(ctx, "1", server.URL+"/list", out, structs.ConfigSet{DecryptM3u8Port: listener.Addr().String()}, func(string, int64, int64) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled before commit, got %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("incomplete output retained: %v", err)
	}
}

func TestCloseDoesNotHangWhenPeerDoesNotRead(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- Close(client) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout closing an unresponsive connection")
		}
	case <-time.After(1200 * time.Millisecond):
		t.Fatal("graceful shutdown blocked indefinitely")
	}
}
