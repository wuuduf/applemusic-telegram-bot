package runv2

import (
	"context"
	"net"
	"time"
)

// decryptConn is used serially by the protocol's single reader/writer. A phase
// begins on the first underlying write or read; partial I/O does not extend it.
type decryptConn struct {
	net.Conn
	ctx     context.Context
	stop    func() bool
	timeout time.Duration
	phase   byte
}

func newDecryptConn(ctx context.Context, conn net.Conn, timeout time.Duration) *decryptConn {
	c := &decryptConn{Conn: conn, ctx: ctx, timeout: timeout}
	c.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	return c
}

func (c *decryptConn) Read(p []byte) (int, error) {
	if c.phase != 'r' {
		if err := c.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
			if c.ctx.Err() != nil {
				return 0, c.ctx.Err()
			}
			return 0, err
		}
		c.phase = 'r'
	}
	n, err := c.Conn.Read(p)
	if err != nil && c.ctx.Err() != nil {
		return n, c.ctx.Err()
	}
	return n, err
}

func (c *decryptConn) Write(p []byte) (int, error) {
	if c.phase != 'w' {
		if err := c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
			if c.ctx.Err() != nil {
				return 0, c.ctx.Err()
			}
			return 0, err
		}
		c.phase = 'w'
	}
	n, err := c.Conn.Write(p)
	if err != nil && c.ctx.Err() != nil {
		return n, c.ctx.Err()
	}
	return n, err
}

func (c *decryptConn) Close() error {
	c.stop()
	return c.Conn.Close()
}
