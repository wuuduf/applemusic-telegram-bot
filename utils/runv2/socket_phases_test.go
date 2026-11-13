package runv2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func decryptSample(c net.Conn, sample []byte) error {
	rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
	return cbcsFullSubsampleDecrypt(sample, rw)
}

func receiveRequest(c net.Conn) ([]byte, error) {
	var n uint32
	if err := binary.Read(c, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	b := make([]byte, n)
	_, err := io.ReadFull(c, b)
	return b, err
}

func expectTimeout(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		var e net.Error
		if !errors.As(err, &e) || !e.Timeout() {
			t.Fatalf("expected timeout, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not terminate")
	}
}

func TestDecryptSilentRequestIsBounded(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	c := newDecryptConn(context.Background(), raw, 120*time.Millisecond)
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- decryptSample(c, make([]byte, 64)) }()
	expectTimeout(t, done)
}

func TestDecryptContextDeadlineInterruptsRead(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	c := newDecryptConn(ctx, raw, 10*time.Second)
	defer c.Close()
	ready := make(chan error, 1)
	go func() { _, err := receiveRequest(peer); ready <- err }()
	done := make(chan error, 1)
	go func() { done <- decryptSample(c, make([]byte, 64)) }()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("deadline did not interrupt read")
	}
}

func TestDecryptGradualResponseStillHasAbsoluteLimit(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	c := newDecryptConn(context.Background(), raw, 160*time.Millisecond)
	defer c.Close()
	progressed := make(chan int, 1)
	go func() {
		if _, err := receiveRequest(peer); err != nil {
			progressed <- 0
			return
		}
		count := 0
		for count < 16 {
			if _, err := peer.Write([]byte{42}); err != nil {
				break
			}
			count++
			time.Sleep(30 * time.Millisecond)
		}
		progressed <- count
	}()
	done := make(chan error, 1)
	go func() { done <- decryptSample(c, make([]byte, 16)) }()
	expectTimeout(t, done)
	peer.Close()
	if n := <-progressed; n < 2 || n >= 16 {
		t.Fatalf("expected partial response progress, got %d", n)
	}
}

func TestDecryptGradualRequestStillHasAbsoluteLimit(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	c := newDecryptConn(context.Background(), raw, 160*time.Millisecond)
	defer c.Close()
	progressed := make(chan int, 1)
	go func() {
		count := 0
		for {
			var b [1]byte
			_, err := peer.Read(b[:])
			if err != nil {
				break
			}
			count++
			time.Sleep(30 * time.Millisecond)
		}
		progressed <- count
	}()
	done := make(chan error, 1)
	go func() { done <- decryptSample(c, make([]byte, 64)) }()
	expectTimeout(t, done)
	peer.Close()
	if n := <-progressed; n < 2 || n >= 68 {
		t.Fatalf("expected partial request progress, got %d", n)
	}
}

func TestDecryptMultipleTimelyFramesExceedOnePhaseLimit(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	c := newDecryptConn(context.Background(), raw, 250*time.Millisecond)
	defer c.Close()
	peerErr := make(chan error, 1)
	go func() {
		for i := 0; i < 4; i++ {
			b, err := receiveRequest(peer)
			if err != nil {
				peerErr <- err
				return
			}
			time.Sleep(80 * time.Millisecond)
			for j := range b {
				b[j] ^= 0xff
			}
			if _, err := peer.Write(b); err != nil {
				peerErr <- err
				return
			}
		}
		peerErr <- nil
	}()
	start := time.Now()
	rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
	for i := 0; i < 4; i++ {
		b := bytes.Repeat([]byte{byte(i)}, 16)
		if err := cbcsFullSubsampleDecrypt(b, rw); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, bytes.Repeat([]byte{^byte(i)}, 16)) {
			t.Fatalf("bad response %x", b)
		}
	}
	if time.Since(start) <= 250*time.Millisecond {
		t.Fatal("fixture did not exceed one phase limit")
	}
	if err := <-peerErr; err != nil {
		t.Fatal(err)
	}
}

func TestDecryptParentDeadlineSpansMultipleTimelyFrames(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c := newDecryptConn(ctx, raw, time.Second)
	defer c.Close()
	go func() {
		for {
			b, err := receiveRequest(peer)
			if err != nil {
				return
			}
			time.Sleep(80 * time.Millisecond)
			if _, err = peer.Write(b); err != nil {
				return
			}
		}
	}()
	rw := bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c))
	completed := 0
	for {
		err := cbcsFullSubsampleDecrypt(make([]byte, 16), rw)
		if err != nil {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected parent deadline, got %v", err)
			}
			break
		}
		completed++
	}
	if completed < 2 {
		t.Fatalf("expected multiple timely frames before parent deadline, got %d", completed)
	}
}

func TestDecryptAlreadyCanceledReturnsContextError(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	c := newDecryptConn(ctx, raw, time.Second)
	defer c.Close()
	cancel()
	// Ensure the cancellation callback has actually closed the underlying socket.
	var b [1]byte
	if _, err := peer.Read(b[:]); err != io.EOF {
		t.Fatalf("expected closed peer: %v", err)
	}
	if err := decryptSample(c, make([]byte, 16)); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestDecryptCancelWhileBlocked(t *testing.T) {
	for _, phase := range []string{"read", "write"} {
		t.Run(phase, func(t *testing.T) {
			raw, peer := net.Pipe()
			defer peer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := newDecryptConn(ctx, raw, 10*time.Second)
			defer c.Close()
			ready := make(chan struct{})
			go func() {
				if phase == "read" {
					_, _ = receiveRequest(peer)
				} else {
					var b [1]byte
					_, _ = peer.Read(b[:])
				}
				close(ready)
			}()
			done := make(chan error, 1)
			go func() { done <- decryptSample(c, make([]byte, 64)) }()
			select {
			case <-ready:
			case <-time.After(time.Second):
				t.Fatal("peer did not observe I/O")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation stuck")
			}
		})
	}
}

func TestCloseWritesMarkerAndCloses(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	done := make(chan error, 1)
	go func() { done <- Close(client) }()
	got, err := io.ReadAll(peer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, make([]byte, 5)) {
		t.Fatalf("wrong marker %x", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPDerivedCancellationDoesNotCloseDecryptSocket(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	httpCtx, cancelHTTP := context.WithCancelCause(parent)
	raw, peer := net.Pipe()
	defer peer.Close()
	c := newDecryptConn(parent, raw, time.Second)
	defer c.Close()
	ready := make(chan struct{})
	go func() {
		b, err := receiveRequest(peer)
		if err != nil {
			return
		}
		close(ready)
		<-httpCtx.Done()
		_, _ = peer.Write(b)
	}()
	done := make(chan error, 1)
	go func() { done <- decryptSample(c, make([]byte, 16)) }()
	<-ready
	cancelHTTP(ErrTimeout)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy decrypt did not complete")
	}
}
