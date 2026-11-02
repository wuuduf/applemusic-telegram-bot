package runv2

import (
	"context"
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

func TestHTTPIdleCancelsBlockedHeadersAndBody(t *testing.T) {
	for _, stage := range []string{"headers", "body"} {
		t.Run(stage, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			peerDone := make(chan error, 1)
			go func() {
				c, err := ln.Accept()
				if err != nil {
					peerDone <- err
					return
				}
				defer c.Close()
				_, err = io.Copy(io.Discard, c)
				peerDone <- err
			}()
			handlerDone := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/playlist.m3u8" {
					fmt.Fprint(w, "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\n#EXT-X-BYTERANGE:1024@0\nmedia.mp4\n#EXT-X-ENDLIST\n")
					return
				}
				if stage == "body" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
				close(handlerDone)
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			out := filepath.Join(t.TempDir(), "out.m4a")
			err = runWithTimeouts(ctx, "1", srv.URL+"/playlist.m3u8", out, structs.ConfigSet{DecryptM3u8Port: ln.Addr().String()}, nil, 150*time.Millisecond, time.Second)
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("expected HTTP timeout at %s, got %v", stage, err)
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial output not removed: %v", err)
			}
			select {
			case <-handlerDone:
			case <-time.After(time.Second):
				t.Fatal("HTTP request did not close")
			}
			if stage == "body" {
				select {
				case err := <-peerDone:
					if err != nil {
						t.Fatalf("wrapper close: %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("wrapper not closed")
				}
			}
		})
	}
}
