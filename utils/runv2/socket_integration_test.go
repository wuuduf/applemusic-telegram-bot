package runv2

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
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

	"github.com/itouakirai/mp4ff/mp4"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func encryptedFixture(t *testing.T) []byte {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(48000, "audio", "und")
	if err := init.Moov.Trak.SetAACDescriptor(2, 48000); err != nil {
		t.Fatal(err)
	}
	key, iv := make([]byte, 16), make([]byte, 16)
	var kid mp4.UUID = make([]byte, 16)
	protection, err := mp4.InitProtect(init, key, iv, "cbcs", kid, nil)
	if err != nil {
		t.Fatal(err)
	}
	frag, err := mp4.CreateFragment(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	b := bytes.Repeat([]byte{42}, 64)
	frag.AddFullSample(mp4.FullSample{Sample: mp4.NewSample(0, 1024, uint32(len(b)), 0), Data: b})
	if err := mp4.EncryptFragment(frag, key, iv, protection); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := init.Encode(&out); err != nil {
		t.Fatal(err)
	}
	if err := frag.Encode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func fixtureHTTP(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/list" {
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:5\n#EXT-X-TARGETDURATION:10\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://key\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n#EXTINF:10,\n#EXT-X-BYTERANGE:%d@0\n/media\n#EXT-X-ENDLIST\n", len(data))
		} else {
			_, _ = w.Write(data)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func readString(c net.Conn) error {
	var n [1]byte
	if _, err := io.ReadFull(c, n[:]); err != nil {
		return err
	}
	_, err := io.CopyN(io.Discard, c, int64(n[0]))
	return err
}
func receiveFullRequest(c net.Conn) ([]byte, error) {
	if err := readString(c); err != nil {
		return nil, err
	}
	if err := readString(c); err != nil {
		return nil, err
	}
	var n uint32
	if err := binary.Read(c, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n > 1024*1024 {
		return nil, fmt.Errorf("invalid fixture frame %d", n)
	}
	b := make([]byte, n)
	_, err := io.ReadFull(c, b)
	return b, err
}

func TestRunHTTPWatchdogDoesNotKillProgressingDecrypt(t *testing.T) {
	if os.Getenv("AMDL_LONG_WATCHDOG_TEST") != "1" {
		t.Skip("set AMDL_LONG_WATCHDOG_TEST=1 for real 30s HTTP watchdog test")
	}
	t.Setenv("AMDL_RUNV2_IDLE_TIMEOUT_SEC", "30")
	s := fixtureHTTP(t, encryptedFixture(t))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peerResult := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			peerResult <- e
			return
		}
		defer c.Close()
		b, e := receiveFullRequest(c)
		if e != nil {
			peerResult <- e
			return
		}
		block, e := aes.NewCipher(make([]byte, 16))
		if e != nil {
			peerResult <- e
			return
		}
		if len(b) == 0 || len(b)%aes.BlockSize != 0 {
			peerResult <- fmt.Errorf("unexpected encrypted sample size: %d", len(b))
			return
		}
		cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(b, b)
		for _, v := range b {
			time.Sleep(520 * time.Millisecond)
			if _, e = c.Write([]byte{v}); e != nil {
				peerResult <- e
				return
			}
		}
		peerResult <- nil
		_, _ = io.Copy(io.Discard, c)
	}()
	out := filepath.Join(t.TempDir(), "track.m4a")
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- RunWithContext(context.Background(), "1", s.URL+"/list", out, structs.ConfigSet{DecryptM3u8Port: listener.Addr().String()}, func(string, int64, int64) {})
	}()
	select {
	case e := <-peerResult:
		if e != nil {
			t.Fatalf("decrypt closed during HTTP watchdog: %v", e)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("decrypt peer stuck")
	}
	if time.Since(start) < 30*time.Second {
		t.Fatal("fixture did not outlast watchdog")
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("run failed after HTTP watchdog: %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
	result, e := os.ReadFile(out)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(result, bytes.Repeat([]byte{42}, 64)) {
		t.Fatal("decrypted sample missing from output")
	}
}

func TestRunCancellationAfterDecryptRemovesOutputAndSkipsMarker(t *testing.T) {
	s := fixtureHTTP(t, encryptedFixture(t))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	peerResult := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			peerResult <- err
			return
		}
		defer c.Close()
		b, err := receiveFullRequest(c)
		if err == nil {
			var block cipher.Block
			block, err = aes.NewCipher(make([]byte, 16))
			if err == nil {
				cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(b, b)
				_, err = c.Write(b)
			}
		}
		if err != nil {
			peerResult <- err
			return
		}
		var marker [1]byte
		if n, err := c.Read(marker[:]); n != 0 || err != io.EOF {
			peerResult <- fmt.Errorf("expected close without marker, got n=%d err=%v", n, err)
			return
		}
		peerResult <- nil
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := filepath.Join(t.TempDir(), "track.m4a")
	progressCalls := 0
	err = RunWithContext(ctx, "1", s.URL+"/list", out, structs.ConfigSet{DecryptM3u8Port: listener.Addr().String()}, func(string, int64, int64) {
		progressCalls++
		if progressCalls == 2 {
			cancel()
		}
	})
	if progressCalls < 2 || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation after decrypted fragment, calls=%d err=%v", progressCalls, err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("incomplete output retained: %v", err)
	}
	select {
	case err := <-peerResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("decrypt peer did not observe close")
	}
}

func TestRunCancellationWhileDecryptReadBlocked(t *testing.T) {
	s := fixtureHTTP(t, encryptedFixture(t))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan error, 1)
	closed := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			received <- e
			return
		}
		defer c.Close()
		_, e = receiveFullRequest(c)
		received <- e
		if e != nil {
			return
		}
		var b [1]byte
		_, e = c.Read(b[:])
		closed <- e
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := filepath.Join(t.TempDir(), "track.m4a")
	done := make(chan error, 1)
	go func() {
		done <- RunWithContext(ctx, "1", s.URL+"/list", out, structs.ConfigSet{DecryptM3u8Port: listener.Addr().String()}, func(string, int64, int64) {})
	}()
	select {
	case e := <-received:
		if e != nil {
			t.Fatal(e)
		}
	case e := <-done:
		t.Fatalf("premature return: %v", e)
	case <-time.After(5 * time.Second):
		t.Fatal("no decrypt request")
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("expected cancellation: %v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("task did not cancel")
	}
	select {
	case e := <-closed:
		if e != io.EOF {
			t.Fatalf("peer close: %v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("peer not closed")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatalf("partial output retained: %v", e)
	}
}
