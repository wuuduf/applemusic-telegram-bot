package runv2

import (
	"context"
	"net"
	"time"
)

// wrapperConn bounds each socket operation, not the lifetime of the download.
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
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		if c.ctx.Err() != nil {
			return 0, c.ctx.Err()
		}
		return 0, err
	}
	n, err := c.Conn.Read(p)
	if err != nil && c.ctx.Err() != nil {
		return n, c.ctx.Err()
	}
	return n, err
}

func (c *wrapperConn) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		if c.ctx.Err() != nil {
			return 0, c.ctx.Err()
		}
		return 0, err
	}
	n, err := c.Conn.Write(p)
	if err != nil && c.ctx.Err() != nil {
		return n, c.ctx.Err()
	}
	return n, err
}

func (c *wrapperConn) Close() error {
	c.stop()
	return c.Conn.Close()
}
