package runv2

import (
	"context"
	"net"
	"sync"
	"time"
)

// wrapperConn caps each Write and the entire contiguous response read phase.
// A subsequent request Write starts a new phase. Thus tiny response reads cannot
// extend one sample response indefinitely, while separate samples can continue
// beyond the timeout. The cap is absolute, not a byte-progress idle timeout.
// AfterFunc closes the connection on context cancellation, including blocked I/O.
type wrapperConn struct {
	net.Conn
	ctx          context.Context
	timeout      time.Duration
	stop         func() bool
	mu           sync.Mutex
	readDeadline time.Time
}

func wrapDecryptConn(ctx context.Context, conn net.Conn, timeout time.Duration) net.Conn {
	c := &wrapperConn{Conn: conn, ctx: ctx, timeout: timeout}
	c.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	return c
}

func (c *wrapperConn) Read(p []byte) (int, error) {
	if err := context.Cause(c.ctx); err != nil {
		return 0, err
	}
	c.mu.Lock()
	if c.readDeadline.IsZero() {
		c.readDeadline = time.Now().Add(c.timeout)
	}
	deadline := c.readDeadline
	c.mu.Unlock()
	if err := c.Conn.SetReadDeadline(deadline); err != nil {
		if context.Cause(c.ctx) != nil {
			return 0, context.Cause(c.ctx)
		}
		return 0, err
	}
	n, err := c.Conn.Read(p)
	if err != nil && context.Cause(c.ctx) != nil {
		return n, context.Cause(c.ctx)
	}
	return n, err
}

func (c *wrapperConn) Write(p []byte) (int, error) {
	if err := context.Cause(c.ctx); err != nil {
		return 0, err
	}
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		if context.Cause(c.ctx) != nil {
			return 0, context.Cause(c.ctx)
		}
		return 0, err
	}
	c.mu.Lock()
	c.readDeadline = time.Time{}
	c.mu.Unlock()
	n, err := c.Conn.Write(p)
	if err != nil && context.Cause(c.ctx) != nil {
		return n, context.Cause(c.ctx)
	}
	return n, err
}

func (c *wrapperConn) Close() error {
	c.stop()
	return c.Conn.Close()
}
