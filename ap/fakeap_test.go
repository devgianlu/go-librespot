//go:build test_unit

package ap

import (
	"bytes"
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// cuttableConn drops everything read and written once cut, like a connection
// whose network path went away without a FIN or an RST.
type cuttableConn struct {
	net.Conn
	cut *atomic.Bool
}

func (c *cuttableConn) Read(b []byte) (int, error) {
	for {
		n, err := c.Conn.Read(b)
		if err != nil || !c.cut.Load() {
			return n, err
		}
	}
}

func (c *cuttableConn) Write(b []byte) (int, error) {
	if c.cut.Load() {
		return len(b), nil
	}
	return c.Conn.Write(b)
}

// fakeAP is the accesspoint's end of an authenticated connection: it pings the
// device every pingEvery and acknowledges each pong, until the test cuts it.
type fakeAP struct {
	enc    *shannonConn
	cut    atomic.Bool
	closed chan struct{}

	mu       sync.Mutex
	lastAck  time.Time
	closedAt time.Time
	pending  sync.WaitGroup
}

// attachFakeAP gives the accesspoint a connection to a fakeAP, as a completed
// handshake would. The accesspoint has no APWelcome, so once that connection
// is closed it gives up instead of reconnecting.
func attachFakeAP(accesspoint *Accesspoint, firstPing, pingEvery time.Duration) *fakeAP {
	device, server := net.Pipe()
	sendKey, recvKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)

	f := &fakeAP{enc: newShannonConn(server, recvKey, sendKey), closed: make(chan struct{})}
	accesspoint.setConn(&cuttableConn{Conn: device, cut: &f.cut})
	accesspoint.encConn = newShannonConn(accesspoint.conn, sendKey, recvKey)

	go f.read()
	go f.ping(firstPing, pingEvery)
	return f
}

func (f *fakeAP) read() {
	for {
		pkt, _, err := f.enc.receivePacket(context.Background())
		if err != nil {
			f.mu.Lock()
			f.closedAt = time.Now()
			f.mu.Unlock()
			close(f.closed)
			f.pending.Wait()
			return
		}
		if pkt != PacketTypePong {
			continue
		}
		// net.Pipe has no buffer: answer from another goroutine so this loop
		// keeps reading while the device is not, as a socket buffer would.
		f.pending.Add(1)
		go func() {
			defer f.pending.Done()
			if f.enc.sendPacket(context.Background(), PacketTypePongAck, nil) == nil && !f.cut.Load() {
				f.mu.Lock()
				f.lastAck = time.Now()
				f.mu.Unlock()
			}
		}()
	}
}

func (f *fakeAP) ping(first, every time.Duration) {
	next := first
	for {
		select {
		case <-f.closed:
			return
		case <-time.After(next):
			_ = f.enc.sendPacket(context.Background(), PacketTypePing, []byte{0, 0, 0, 0})
			next = every
		}
	}
}

func (f *fakeAP) lastAckAt() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAck
}

func (f *fakeAP) closedAfter(t time.Time) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closedAt.Sub(t)
}

func (f *fakeAP) isClosed() bool {
	select {
	case <-f.closed:
		return true
	default:
		return false
	}
}
