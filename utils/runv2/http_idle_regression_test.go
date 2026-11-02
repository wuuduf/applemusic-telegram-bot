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

func TestHTTPIdleDoesNotInterruptProgressingDecrypt(t *testing.T) {
	// Short injected values preserve the production 30s minimum and 300s default.
	fixture, err := os.ReadFile("testdata/cbcs_audio.mp4")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	wrapperDone := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			wrapperDone <- err
			return
		}
		defer c.Close()
		for _, want := range []string{"1", "skd://example"} {
			var l [1]byte
			if _, err = io.ReadFull(c, l[:]); err != nil {
				wrapperDone <- err
				return
			}
			b := make([]byte, int(l[0]))
			if _, err = io.ReadFull(c, b); err != nil {
				wrapperDone <- err
				return
			}
			if string(b) != want {
				wrapperDone <- fmt.Errorf("unexpected header %q", b)
				return
			}
		}
		first := true
		for {
			var h [4]byte
			if _, err = io.ReadFull(c, h[:]); err != nil {
				wrapperDone <- err
				return
			}
			n := binary.LittleEndian.Uint32(h[:])
			if n == 0 {
				wrapperDone <- nil
				return
			}
			if n > 10<<20 {
				wrapperDone <- fmt.Errorf("bad frame %d", n)
				return
			}
			b := make([]byte, n)
			if _, err = io.ReadFull(c, b); err != nil {
				wrapperDone <- err
				return
			}
			if first {
				first = false
				if len(b) < 16 {
					wrapperDone <- fmt.Errorf("first sample too short: %d", len(b))
					return
				}
				for i := 0; i < 16; i++ {
					time.Sleep(30 * time.Millisecond)
					if _, err = c.Write(b[i : i+1]); err != nil {
						wrapperDone <- err
						return
					}
				}
				b = b[16:]
			}
			if _, err = c.Write(b); err != nil {
				wrapperDone <- err
				return
			}
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/playlist.m3u8" {
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:10\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://example\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n#EXTINF:10,\n#EXT-X-BYTERANGE:%d@0\nmedia.mp4\n#EXT-X-ENDLIST\n", len(fixture))
			return
		}
		w.Write(fixture)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := filepath.Join(t.TempDir(), "out.m4a")
	err = runWithTimeouts(ctx, "1", server.URL+"/playlist.m3u8", out, structs.ConfigSet{DecryptM3u8Port: ln.Addr().String()}, func(string, int64, int64) {}, 100*time.Millisecond, time.Second)
	if err != nil {
		select {
		case peerErr := <-wrapperDone:
			t.Fatalf("progressing wrapper was interrupted: %v (HTTP timeout: %v), peer: %v", err, errors.Is(err, ErrTimeout), peerErr)
		default:
			t.Fatalf("progressing wrapper was interrupted: %v (HTTP timeout: %v)", err, errors.Is(err, ErrTimeout))
		}
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("missing output: %v", err)
	}
	select {
	case err := <-wrapperDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wrapper did not finish")
	}
}
