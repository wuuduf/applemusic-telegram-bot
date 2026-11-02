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

func TestRunWithContextCancelAfterFinalDecryptRemovesOutput(t *testing.T) {
	fixture, err := os.ReadFile("testdata/cbcs_audio.mp4")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peerDone := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer c.Close()
		for i := 0; i < 2; i++ {
			var size [1]byte
			if _, err := io.ReadFull(c, size[:]); err != nil {
				peerDone <- err
				return
			}
			if _, err := io.CopyN(io.Discard, c, int64(size[0])); err != nil {
				peerDone <- err
				return
			}
		}
		for {
			var header [4]byte
			if _, err := io.ReadFull(c, header[:]); err != nil {
				if errors.Is(err, io.EOF) {
					peerDone <- nil
				} else {
					peerDone <- err
				}
				return
			}
			size := binary.LittleEndian.Uint32(header[:])
			if size == 0 || size > 10<<20 {
				peerDone <- fmt.Errorf("unexpected frame: %d", size)
				return
			}
			b := make([]byte, size)
			if _, err := io.ReadFull(c, b); err != nil {
				peerDone <- err
				return
			}
			if _, err := c.Write(b); err != nil {
				peerDone <- err
				return
			}
		}
	}()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/playlist.m3u8" {
			_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:10\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://example\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n#EXTINF:10,\n#EXT-X-BYTERANGE:%d@0\nmedia.mp4\n#EXT-X-ENDLIST\n", len(fixture))
			return
		}
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()
	deadline, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()
	calls := 0
	out := filepath.Join(t.TempDir(), "out.m4a")
	err = RunWithContext(ctx, "1", srv.URL+"/playlist.m3u8", out, structs.ConfigSet{DecryptM3u8Port: listener.Addr().String()}, func(string, int64, int64) {
		calls++
		if calls == 2 {
			cancel()
		}
	})
	if calls < 2 {
		t.Fatal("did not reach post-decrypt progress callback")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial output not removed: %v", err)
	}
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatalf("wrapper: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wrapper not closed")
	}
}
