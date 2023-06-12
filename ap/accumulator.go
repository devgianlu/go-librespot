package ap

import (
	"bytes"
	"net"
)

type connAccumulator struct {
	net.Conn

	data bytes.Buffer
}

func (c *connAccumulator) Dump() []byte {
	return c.data.Bytes()
}

func (c *connAccumulator) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	if err != nil {
		return n, err
	}

	_, _ = c.data.Write(b[:n])
	return n, err
}

func (c *connAccumulator) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	if err != nil {
		return n, err
	}

	_, _ = c.data.Write(b[:n])
	return n, err
}
