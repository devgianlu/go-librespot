package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
)

type KeyProviderError struct {
	Code uint16
}

func (e KeyProviderError) Error() string {
	return fmt.Sprintf("failed retrieving aes key with code %d", e.Code)
}

// A live accesspoint answers a key request within a round trip, so a request
// left unanswered for keyRequestTimeout was most likely sent on a connection
// that went silent: that connection is dropped and the request sent again on
// the one that replaces it, which keyRetryTimeout leaves time to establish.
const (
	keyRequestTimeout = 5 * time.Second
	keyRetryTimeout   = 15 * time.Second
)

var errKeyRequestTimeout = fmt.Errorf("aes key request timed out: %w", context.DeadlineExceeded)

// accesspoint is what KeyProvider needs of *ap.Accesspoint.
type accesspoint interface {
	Done() <-chan struct{}
	Receive(types ...ap.PacketType) <-chan ap.Packet
	SendConn(ctx context.Context, pktType ap.PacketType, payload []byte) (ap.ConnID, error)
	DropConn(id ap.ConnID)
}

type KeyProvider struct {
	ap  accesspoint
	log librespot.Logger

	recvLoopOnce sync.Once

	reqChan chan keyRequest
}

type keyRequest struct {
	gid    []byte
	fileId []byte
	resp   chan keyResponse
	// sent receives the connection the request was written to.
	sent chan ap.ConnID
}

type keyResponse struct {
	key []byte
	err error
}

func NewAudioKeyProvider(log librespot.Logger, ap accesspoint) *KeyProvider {
	p := &KeyProvider{log: log, ap: ap}
	p.reqChan = make(chan keyRequest)
	return p
}

func (p *KeyProvider) startReceiving() {
	p.recvLoopOnce.Do(func() { go p.recvLoop() })
}

func (p *KeyProvider) recvLoop() {
	ch := p.ap.Receive(ap.PacketTypeAesKey, ap.PacketTypeAesKeyError)
	done := p.ap.Done()

	seq := uint32(0)
	reqs := map[uint32]keyRequest{}

	for {
		select {
		case <-done:
			return
		case pkt, ok := <-ch:
			if !ok {
				return
			}

			resp := bytes.NewReader(pkt.Payload)
			var respSeq uint32
			_ = binary.Read(resp, binary.BigEndian, &respSeq)

			req, ok := reqs[respSeq]
			if !ok {
				p.log.Warnf("received aes key with invalid sequence: %d", respSeq)
				continue
			}

			delete(reqs, respSeq)

			switch pkt.Type {
			case ap.PacketTypeAesKey:
				key := make([]byte, 16)
				if _, err := io.ReadFull(resp, key); err != nil {
					req.resp <- keyResponse{err: fmt.Errorf("malformed aes key response for sequence %d: %w", respSeq, err)}
					continue
				}
				req.resp <- keyResponse{key: key}
			case ap.PacketTypeAesKeyError:
				var errCode uint16
				_ = binary.Read(resp, binary.BigEndian, &errCode)
				req.resp <- keyResponse{err: &KeyProviderError{errCode}}
			default:
				panic("unexpected packet type")
			}
		case req := <-p.reqChan:
			reqSeq := seq
			seq++

			var buf bytes.Buffer
			_, _ = buf.Write(req.fileId)
			_, _ = buf.Write(req.gid)
			_ = binary.Write(&buf, binary.BigEndian, reqSeq)
			_ = binary.Write(&buf, binary.BigEndian, uint16(0))

			reqs[reqSeq] = req

			// Background on purpose: this pump writes for every queued request,
			// so one caller's cancellation must not abort the others. Request
			// applies the caller's deadline to the response wait instead, and
			// Send already fails once the accesspoint is closed.
			conn, err := p.ap.SendConn(context.Background(), ap.PacketTypeRequestKey, buf.Bytes())
			if err != nil {
				delete(reqs, reqSeq)
				req.resp <- keyResponse{err: fmt.Errorf("failed sending key request for file %s, gid: %s: %w",
					hex.EncodeToString(req.fileId), librespot.GidToBase62(req.gid), err)}
				continue
			}
			req.sent <- conn

			p.log.Debugf("requested aes key for file %s, gid: %s", hex.EncodeToString(req.fileId), librespot.GidToBase62(req.gid))
		}
	}
}

func (p *KeyProvider) Request(ctx context.Context, gid []byte, fileId []byte) ([]byte, error) {
	key, err := p.request(ctx, gid, fileId, keyRequestTimeout)
	if errors.Is(err, errKeyRequestTimeout) {
		p.log.Warnf("no aes key for file %s after %.0fs, retrying on a new accesspoint connection",
			hex.EncodeToString(fileId), keyRequestTimeout.Seconds())
		key, err = p.request(ctx, gid, fileId, keyRetryTimeout)
	}

	return key, err
}

// request sends one key request, which gets its own sequence number and so
// can only be answered by the answer to that request.
func (p *KeyProvider) request(ctx context.Context, gid []byte, fileId []byte, timeout time.Duration) ([]byte, error) {
	done := p.ap.Done()

	p.startReceiving()

	req := keyRequest{gid: gid, fileId: fileId, resp: make(chan keyResponse, 1), sent: make(chan ap.ConnID, 1)}
	select {
	case <-done:
		return nil, ap.ErrAccesspointClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case p.reqChan <- req:
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var resp keyResponse
	select {
	case resp = <-req.resp:
	case <-timer.C:
		select {
		case conn := <-req.sent:
			p.ap.DropConn(conn)
		default:
			// Still being written, which waits for a reconnection in progress.
		}
		return nil, errKeyRequestTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
		return nil, ap.ErrAccesspointClosed
	}

	if resp.err != nil {
		return nil, resp.err
	}

	return resp.key, nil
}
