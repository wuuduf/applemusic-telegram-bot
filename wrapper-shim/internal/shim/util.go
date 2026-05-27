package shim

import (
	"fmt"
	"io"
)

// byteReader is the subset of io.Reader / io.ByteReader that both bufio.Reader
// and net.Conn (after wrapping) satisfy. We only need single-byte and
// fixed-length reads, so this minimal interface keeps the helpers reusable
// across the m3u8 and decrypt code paths.
type byteReader interface {
	io.Reader
	ReadByte() (byte, error)
}

// readByteAdapter wraps an io.Reader without ReadByte by performing a 1-byte
// io.ReadFull. It is only used for the m3u8 handler where we read directly
// off net.Conn and don't want the overhead of bufio for a few bytes.
type readByteAdapter struct{ io.Reader }

func (a readByteAdapter) ReadByte() (byte, error) {
	var b [1]byte
	if _, err := io.ReadFull(a.Reader, b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}

// readLengthPrefixedString reads a [1 byte length][N bytes] frame.
func readLengthPrefixedString(r io.Reader) (string, error) {
	br, ok := r.(byteReader)
	if !ok {
		br = readByteAdapter{Reader: r}
	}
	lenByte, err := br.ReadByte()
	if err != nil {
		return "", fmt.Errorf("read len: %w", err)
	}
	return readWithLen(br, int(lenByte))
}

// readWithLen reads exactly n bytes and returns them as a string. n==0 yields
// an empty string with no error so callers can distinguish empty payloads from
// I/O failures.
func readWithLen(r io.Reader, n int) (string, error) {
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("read %d bytes: %w", n, err)
	}
	return string(buf), nil
}
