package audio

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/ap"
)

type KeyProvider struct {
	ap *ap.Accesspoint

	reqChan  chan keyRequest
	stopChan chan struct{}
}

type keyRequest struct {
	gid    []byte
	fileId []byte
	resp   chan keyResponse
}

type keyResponse struct {
	key []byte
	err error
}

func NewAudioKeyProvider(ap *ap.Accesspoint) *KeyProvider {
	p := &KeyProvider{ap: ap}
	p.reqChan = make(chan keyRequest)
	p.stopChan = make(chan struct{}, 1)
	go p.recvLoop()
	return p
}

func (p *KeyProvider) recvLoop() {
	ch := p.ap.Receive(ap.PacketTypeAesKey, ap.PacketTypeAesKeyError)

	seq := uint32(0)
	reqs := map[uint32]keyRequest{}

	for {
		select {
		case <-p.stopChan:
			return
		case pkt := <-ch:
			resp := bytes.NewReader(pkt.Payload)
			var respSeq uint32
			_ = binary.Read(resp, binary.BigEndian, &respSeq)

			req, ok := reqs[respSeq]
			if !ok {
				log.Warnf("received aes key with invalid sequence: %d", respSeq)
				continue
			}

			delete(reqs, respSeq)

			switch pkt.Type {
			case ap.PacketTypeAesKey:
				key := make([]byte, 16)
				_, _ = resp.Read(key)
				req.resp <- keyResponse{key: key}
			case ap.PacketTypeAesKeyError:
				var errCode uint16
				_ = binary.Read(resp, binary.BigEndian, &errCode)
				req.resp <- keyResponse{err: fmt.Errorf("failed retrieving aes key with code %x", errCode)}
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

			if err := p.ap.Send(ap.PacketTypeRequestKey, buf.Bytes()); err != nil {
				delete(reqs, seq)
				req.resp <- keyResponse{err: fmt.Errorf("failed sending key request for file %s, track: %s: %w",
					hex.EncodeToString(req.fileId), librespot.TrackId(req.gid).Uri(), err)}
			}

			log.Debugf("requested aes key for file %s, track: %s", hex.EncodeToString(req.fileId), librespot.TrackId(req.gid).Uri())
		}
	}
}

func (p *KeyProvider) Request(gid []byte, fileId []byte) ([]byte, error) {
	req := keyRequest{gid: gid, fileId: fileId, resp: make(chan keyResponse)}
	p.reqChan <- req

	resp := <-req.resp
	if resp.err != nil {
		return nil, resp.err
	}

	return resp.key, nil
}

func (p *KeyProvider) Close() {
	p.stopChan <- struct{}{}
}
