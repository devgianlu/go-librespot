package ap

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/devgianlu/shannon"
)

type shannonConn struct {
	conn net.Conn

	sendLock sync.Mutex
	recvLock sync.Mutex

	sendCipher *shannon.Shannon
	sendNonce  uint32

	recvCipher *shannon.Shannon
	recvNonce  uint32
}

func newShannonConn(conn net.Conn, sendKey []byte, recvKey []byte) *shannonConn {
	return &shannonConn{
		conn:       conn,
		sendCipher: shannon.New(sendKey),
		sendNonce:  0,
		recvCipher: shannon.New(recvKey),
		recvNonce:  0,
	}
}

func (c *shannonConn) sendPacket(ctx context.Context, pktType PacketType, payload []byte) error {
	if len(payload) > 65535 {
		return fmt.Errorf("payload too big: %d", len(payload))
	}

	// assemble packet
	packet := make([]byte, 1+2+len(payload))
	packet[0] = byte(pktType)
	binary.BigEndian.PutUint16(packet[1:3], uint16(len(payload)))
	copy(packet[3:], payload)

	c.sendLock.Lock()
	defer c.sendLock.Unlock()

	// set nonce on cipher and increment
	c.sendCipher.NonceU32(c.sendNonce)
	c.sendNonce++

	// encrypt packet content
	c.sendCipher.Encrypt(packet)

	// calculate packet mac
	mac := make([]byte, 4)
	c.sendCipher.Finish(mac)

	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
		defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	}

	// write it all out
	if _, err := c.conn.Write(packet); err != nil {
		return fmt.Errorf("failed writing packet: %w", err)
	} else if _, err := c.conn.Write(mac); err != nil {
		return fmt.Errorf("failed writing packet mac: %w", err)
	}

	return nil
}

func (c *shannonConn) receivePacket(ctx context.Context) (PacketType, []byte, error) {
	c.recvLock.Lock()
	defer c.recvLock.Unlock()

	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
		defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	}

	// set nonce on cipher and increment
	c.recvCipher.NonceU32(c.recvNonce)
	c.recvNonce++

	// read 8 bytes of packet header
	packetHeader := make([]byte, 3)
	if _, err := io.ReadFull(c.conn, packetHeader); err != nil {
		return 0, nil, fmt.Errorf("failed reading packet header: %w", err)
	}

	// decrypt header
	c.recvCipher.Decrypt(packetHeader)

	// read rest of the payload
	payloadLen := binary.BigEndian.Uint16(packetHeader[1:3])
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return 0, nil, fmt.Errorf("failed reading packet payload: %w", err)
	}

	// decrypt payload
	c.recvCipher.Decrypt(payload)

	// read expected mac
	expectedMac := make([]byte, 4)
	if _, err := io.ReadFull(c.conn, expectedMac); err != nil {
		return 0, nil, fmt.Errorf("failed reading packet mac: %w", err)
	}

	// check mac is correct
	if c.recvCipher.CheckMac(expectedMac) != nil {
		return 0, nil, fmt.Errorf("invalid packet mac")
	}

	return PacketType(packetHeader[0]), payload, nil
}
