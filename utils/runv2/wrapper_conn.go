package runv2

import (
	"context"
	"net"
	"time"
)

// wrapperConn caps each Read or Write call, not the lifetime of the download.
// This is deliberately not a byte-idle timeout: one large Write can time out
// despite partial progress because net.Conn.Write may not return until completion.
// The separate HTTP watchdog can also cancel runCtx while decrypt is in progress.
// AfterFunc closes the connection on cancellation, including while an I/O is blocked.
// Its stop function is released when the connection is closed normally.
type wrapperConn struct {
	net.Conn
	ctx     context.Context
	timeout time.Duration
	stop    func() bool
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
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
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
