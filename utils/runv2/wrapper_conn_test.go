package runv2

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type observingConn struct {
	net.Conn
	started chan struct{}
}

func (c *observingConn) Read(p []byte) (int, error) {
	close(c.started)
	return c.Conn.Read(p)
}

func (c *observingConn) Write(p []byte) (int, error) {
	close(c.started)
	return c.Conn.Write(p)
}

func TestWrapperSilentReadTimesOut(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := wrapDecryptConn(context.Background(), client, 80*time.Millisecond)
	defer conn.Close()
	go func() { _, _ = io.ReadFull(server, make([]byte, 20)) }()
	done := make(chan error, 1)
	go func() {
		done <- cbcsFullSubsampleDecrypt(make([]byte, 16), bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn)))
	}()
	select {
	case err := <-done:
		if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
			t.Fatalf("expected read timeout, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("decrypt read remained blocked on silent wrapper")
	}
}

func TestWrapperSilentWriteTimesOut(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := wrapDecryptConn(context.Background(), client, 80*time.Millisecond)
	defer conn.Close()
	_, err := conn.Write([]byte("blocked"))
	if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
		t.Fatalf("expected write timeout, got %v", err)
	}
}

func TestWrapperCancellationInterruptsIO(t *testing.T) {
	for _, op := range []string{"read", "write"} {
		t.Run(op, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			conn := wrapDecryptConn(ctx, &observingConn{Conn: client, started: started}, time.Minute)
			defer conn.Close()
			done := make(chan error, 1)
			go func() {
				var err error
				if op == "read" {
					_, err = conn.Read(make([]byte, 1))
				} else {
					_, err = conn.Write([]byte("x"))
				}
				done <- err
			}()
			<-started
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected context.Canceled, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt I/O")
			}
		})
	}
}

func TestWrapperSequentialOperationsRenewDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := wrapDecryptConn(context.Background(), client, 200*time.Millisecond)
	defer conn.Close()
	done := make(chan error, 1)
	go func() {
		defer close(done)
		for i := 0; i < 4; i++ {
			var b [1]byte
			if _, err := io.ReadFull(server, b[:]); err != nil {
				done <- err
				return
			}
			time.Sleep(70 * time.Millisecond)
			if _, err := server.Write(b[:]); err != nil {
				done <- err
				return
			}
		}
	}()
	for i := 0; i < 4; i++ {
		if _, err := conn.Write([]byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
		var got [1]byte
		if _, err := io.ReadFull(conn, got[:]); err != nil {
			t.Fatal(err)
		}
		if got[0] != byte(i) {
			t.Fatalf("got %d want %d", got[0], i)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWrapperDecryptTransfer(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := wrapDecryptConn(context.Background(), client, time.Second)
	defer conn.Close()
	done := make(chan error, 1)
	go func() {
		request := make([]byte, 20)
		if _, err := io.ReadFull(server, request); err != nil {
			done <- err
			return
		}
		if !bytes.Equal(request[:4], []byte{16, 0, 0, 0}) {
			done <- errors.New("invalid length header")
			return
		}
		_, err := server.Write(bytes.Repeat([]byte{42}, 16))
		done <- err
	}()
	sample := make([]byte, 16)
	if err := cbcsFullSubsampleDecrypt(sample, bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sample, bytes.Repeat([]byte{42}, 16)) {
		t.Fatalf("unexpected sample %v", sample)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
