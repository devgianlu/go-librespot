package ap

import (
	"encoding/binary"
	"fmt"
	"github.com/chatoooo/shannon"
	"io"
	"net"
	"sync"
)

type shannonConn struct {
	conn net.Conn

	lock sync.Mutex

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

func (c *shannonConn) sendPacket(pktType PacketType, payload []byte) error {
	if len(payload) > 65535 {
		return fmt.Errorf("payload too big: %d", len(payload))
	}

	// assemble packet
	packet := make([]byte, 1+2+len(payload))
	packet[0] = byte(pktType)
	binary.BigEndian.PutUint16(packet[1:3], uint16(len(payload)))
	copy(packet[3:], payload)

	c.lock.Lock()
	defer c.lock.Unlock()

	// set nonce on cipher and increment
	c.sendCipher.NonceU32(c.sendNonce)
	c.sendNonce++

	// encrypt packet content
	c.sendCipher.Encrypt(packet)

	// calculate packet mac
	mac := make([]byte, 4)
	c.sendCipher.Finish(mac)

	// write it all out
	if _, err := c.conn.Write(packet); err != nil {
		return fmt.Errorf("failed writing packet: %w", err)
	} else if _, err := c.conn.Write(mac); err != nil {
		return fmt.Errorf("failed writing packet mac: %w", err)
	}

	return nil
}

func (c *shannonConn) receivePacket() (PacketType, []byte, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// set nonce on cipher and increment
	c.recvCipher.NonceU32(c.recvNonce)
	c.recvNonce++

	// read 8 bytes of packet header
	packetHeader := make([]byte, 4)
	if _, err := io.ReadFull(c.conn, packetHeader); err != nil {
		return 0, nil, fmt.Errorf("failed reading packet header: %w", err)
	}

	// decrypt header
	c.recvCipher.Decrypt(packetHeader)

	// read rest of the payload
	payloadLen := binary.BigEndian.Uint16(packetHeader[1:3])
	if payloadLen < 1 {
		panic("cannot read packet payload smaller than 1 bytes")
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadAtLeast(c.conn, payload[1:], int(payloadLen-1)); err != nil {
		return 0, nil, fmt.Errorf("failed reading packet payload: %w", err)
	}

	copy(payload, packetHeader[3:4])

	// decrypt payload
	c.recvCipher.Decrypt(payload[1:])

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
