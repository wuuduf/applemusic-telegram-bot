package runv2

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/grafov/m3u8"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func TestCancelAfterFinalDecryptRemovesOutput(t *testing.T) {
	fixture, err := os.ReadFile("testdata/cbcs_audio.mp4")
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := wrapDecryptConn(ctx, client, time.Second)
	defer conn.Close()
	go func() {
		for i := 0; i < 2; i++ {
			var h [1]byte
			if _, err := io.ReadFull(server, h[:]); err != nil {
				return
			}
			if _, err := io.CopyN(io.Discard, server, int64(h[0])); err != nil {
				return
			}
		}
		for {
			var h [4]byte
			if _, err := io.ReadFull(server, h[:]); err != nil {
				return
			}
			n := binary.LittleEndian.Uint32(h[:])
			if n == 0 || n > 10<<20 {
				return
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(server, b); err != nil {
				return
			}
			if _, err := server.Write(b); err != nil {
				return
			}
		}
	}()
	out := filepath.Join(t.TempDir(), "out.m4a")
	calls := 0
	progress := func(string, int64, int64) {
		calls++
		if calls == 2 {
			cancel()
		}
	}
	segments := []*m3u8.MediaSegment{{Key: &m3u8.Key{URI: "skd://example"}}}
	err = downloadAndDecryptFile(ctx, conn, bytes.NewReader(fixture), out, "1", segments, int64(len(fixture)), structs.ConfigSet{}, progress, "test")
	if calls < 2 {
		t.Fatal("did not reach post-decrypt progress callback")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial output not removed: %v", err)
	}
}
