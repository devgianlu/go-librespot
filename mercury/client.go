package mercury

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	spotifypb "github.com/devgianlu/go-librespot/proto/spotify"
	"google.golang.org/protobuf/proto"
	"strings"
	"sync"
	"time"
)

type hermesRequest struct {
	header *spotifypb.MercuryHeader
	parts  [][]byte

	resp chan hermesResponse
}

type hermesResponse struct {
	header *spotifypb.MercuryHeader
	parts  [][]byte

	err error
}

// eventSubscriber receives Mercury AP push events (PacketTypeMercuryEvent) whose
// URI matches one of the registered prefixes. Used for playlist section pushes.
type eventSubscriber struct {
	uriPrefixes []string
	c           chan eventMessage
}

// EventMessage carries the URI and raw payload of a Mercury AP push event.
type eventMessage struct {
	Uri     string
	Payload []byte
}

type Client struct {
	log librespot.Logger
	ap  *ap.Accesspoint

	recvLoopOnce sync.Once

	reqChan  chan hermesRequest
	stopChan chan struct{}

	eventSubsLock sync.RWMutex
	eventSubs     []eventSubscriber
}

func NewClient(log librespot.Logger, accesspoint *ap.Accesspoint) *Client {
	c := &Client{log: log, ap: accesspoint}
	c.reqChan = make(chan hermesRequest)
	c.stopChan = make(chan struct{}, 1)
	// Start receiving immediately so MercuryEvent packets that arrive right after
	// AP authentication (before any Request() is called) are not dropped.
	c.startReceiving()
	return c
}

func (c *Client) startReceiving() {
	c.recvLoopOnce.Do(func() { go c.recvLoop() })
}

func (c *Client) recvLoop() {
	ch := c.ap.Receive(ap.PacketTypeMercuryReq, ap.PacketTypeMercurySub, ap.PacketTypeMercuryUnsub, ap.PacketTypeMercuryEvent)

	seq := uint64(0)
	reqs := map[uint64]hermesRequest{}

	for {
		select {
		case <-c.stopChan:
			c.stopChan <- struct{}{}
			return
		case pkt := <-ch:
			if pkt.Type == ap.PacketTypeMercuryEvent {
				// Decode and log the event so we can inspect what URI/payload arrives
				// immediately after a DJ transfer (these come via the AP Mercury channel).
				evResp := bytes.NewReader(pkt.Payload)
				var evSeqLen uint16
				_ = binary.Read(evResp, binary.BigEndian, &evSeqLen)
				var evSeq uint64
				switch evSeqLen {
				case 8:
					_ = binary.Read(evResp, binary.BigEndian, &evSeq)
				case 4:
					var s uint32
					_ = binary.Read(evResp, binary.BigEndian, &s)
					evSeq = uint64(s)
				case 2:
					var s uint16
					_ = binary.Read(evResp, binary.BigEndian, &s)
					evSeq = uint64(s)
				}
				var evFlags uint8
				_ = binary.Read(evResp, binary.BigEndian, &evFlags)
				var evPartsCount uint16
				_ = binary.Read(evResp, binary.BigEndian, &evPartsCount)
				evParts := make([][]byte, evPartsCount)
				for i := uint16(0); i < evPartsCount; i++ {
					var partLen uint16
					_ = binary.Read(evResp, binary.BigEndian, &partLen)
					part := make([]byte, partLen)
					_, _ = evResp.Read(part)
					evParts[i] = part
				}
				if len(evParts) > 0 {
					var evHeader spotifypb.MercuryHeader
					if err := proto.Unmarshal(evParts[0], &evHeader); err == nil {
						uri := evHeader.GetUri()
						var payload []byte
						if len(evParts) > 1 {
							payload = evParts[1]
						}
						payloadLen := 0
						for _, p := range evParts[1:] {
							payloadLen += len(p)
						}
						c.log.Debugf("mercury event: seq=%d flags=%d uri=%s statusCode=%v parts=%d payloadLen=%d",
							evSeq, evFlags, uri, evHeader.StatusCode, len(evParts), payloadLen)
						// Route to any registered event subscribers.
						c.eventSubsLock.RLock()
						for _, sub := range c.eventSubs {
							for _, prefix := range sub.uriPrefixes {
								if strings.HasPrefix(uri, prefix) {
									select {
									case sub.c <- eventMessage{Uri: uri, Payload: payload}:
									default:
										c.log.Debugf("mercury event subscriber full, dropping %s", uri)
									}
									break
								}
							}
						}
						c.eventSubsLock.RUnlock()
					} else {
						c.log.Debugf("mercury event: seq=%d flags=%d totalPayload=%d (header parse err: %v)", evSeq, evFlags, len(pkt.Payload), err)
					}
				} else {
					c.log.Debugf("mercury event: seq=%d flags=%d totalPayload=%d (no parts)", evSeq, evFlags, len(pkt.Payload))
				}
				continue
			} else if pkt.Type != ap.PacketTypeMercuryReq {
				c.log.Warnf("skipping mercury packet with type: %s", pkt.Type.String())
				continue
			}

			resp := bytes.NewReader(pkt.Payload)

			var seqLen uint16
			_ = binary.Read(resp, binary.BigEndian, &seqLen)

			var respSeq uint64
			switch seqLen {
			case 8:
				_ = binary.Read(resp, binary.BigEndian, &respSeq)
			case 4:
				var seq32 uint32
				_ = binary.Read(resp, binary.BigEndian, &seq32)
				respSeq = uint64(seq32)
			case 2:
				var seq16 uint16
				_ = binary.Read(resp, binary.BigEndian, &seq16)
				respSeq = uint64(seq16)
			default:
				c.log.Warnf("received mercury response with invalid sequence length: %d", seqLen)
				continue
			}

			var flags uint8
			_ = binary.Read(resp, binary.BigEndian, &flags)

			if flags != 1 {
				c.log.Warnf("received unsupported partial mercury response: %d", flags)
				continue
			}

			var partsCount uint16
			_ = binary.Read(resp, binary.BigEndian, &partsCount)

			req, ok := reqs[respSeq]
			if !ok {
				c.log.Warnf("received mercury response with invalid sequence: %d", respSeq)
				continue
			}

			delete(reqs, respSeq)

			parts := make([][]byte, partsCount)
			for i := uint16(0); i < partsCount; i++ {
				var partLen uint16
				_ = binary.Read(resp, binary.BigEndian, &partLen)

				part := make([]byte, partLen)
				_, _ = resp.Read(part)
				parts[i] = part
			}

			if len(parts) == 0 {
				req.resp <- hermesResponse{err: fmt.Errorf("received empty mercury response")}
				continue
			}

			var header spotifypb.MercuryHeader
			if err := proto.Unmarshal(parts[0], &header); err != nil {
				req.resp <- hermesResponse{err: fmt.Errorf("failed unmarshaling mercury header: %w", err)}
				continue
			}

			req.resp <- hermesResponse{header: &header, parts: parts[1:]}
		case req := <-c.reqChan:
			reqSeq := seq
			seq++

			var buf bytes.Buffer
			_ = binary.Write(&buf, binary.BigEndian, uint16(8))                // sequence length
			_ = binary.Write(&buf, binary.BigEndian, reqSeq)                   // sequence
			_ = binary.Write(&buf, binary.BigEndian, uint8(1))                 // flags
			_ = binary.Write(&buf, binary.BigEndian, uint16(1+len(req.parts))) // parts count

			headerBytes, err := proto.Marshal(req.header)
			if err != nil {
				req.resp <- hermesResponse{err: fmt.Errorf("failed marshaling mercury header: %w", err)}
				continue
			}

			_ = binary.Write(&buf, binary.BigEndian, uint16(len(headerBytes)))
			_, _ = buf.Write(headerBytes)

			for _, part := range req.parts {
				_ = binary.Write(&buf, binary.BigEndian, uint16(len(part)))
				_, _ = buf.Write(part)
			}

			reqs[reqSeq] = req

			if err := c.ap.Send(context.TODO(), ap.PacketTypeMercuryReq, buf.Bytes()); err != nil {
				delete(reqs, reqSeq)
				req.resp <- hermesResponse{err: fmt.Errorf("failed sending mercury request: %w", err)}
				continue
			}
		}
	}
}

func (c *Client) Request(ctx context.Context, method, uri string, fields map[string][]byte, payload []byte) ([]byte, error) {
	c.startReceiving()

	header := &spotifypb.MercuryHeader{
		Method: proto.String(method),
		Uri:    proto.String(uri),
	}

	if fields != nil {
		for k, v := range fields {
			header.UserFields = append(header.UserFields, &spotifypb.MercuryUserField{
				Key: proto.String(k), Value: v,
			})
		}
	}

	var parts [][]byte
	for i := 0; i < len(payload); i += 0xffff {
		parts = append(parts, payload[i:min(len(payload), i+0xffff)])
	}

	req := hermesRequest{header: header, parts: parts, resp: make(chan hermesResponse, 1)}
	c.reqChan <- req

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	select {
	case <-ctx.Done():
		return nil, context.DeadlineExceeded
	case resp := <-req.resp:
		if resp.err != nil {
			return nil, resp.err
		}

		if *resp.header.StatusCode != 200 {
			return nil, fmt.Errorf("mercury request failed with status code: %d", *resp.header.StatusCode)
		}

		var respPayload []byte
		for _, part := range resp.parts {
			respPayload = append(respPayload, part...)
		}

		return respPayload, nil
	}
}

func (c *Client) Close() {
	c.stopChan <- struct{}{}
	<-c.stopChan
}

// SubscribeEvent returns a channel that receives Mercury AP push events (PacketTypeMercuryEvent)
// whose URI starts with one of the given prefixes. The channel is buffered to avoid blocking
// the receive loop when the caller is temporarily busy.
func (c *Client) SubscribeEvent(uriPrefixes ...string) <-chan eventMessage {
	ch := make(chan eventMessage, 64)
	c.eventSubsLock.Lock()
	c.eventSubs = append(c.eventSubs, eventSubscriber{uriPrefixes: uriPrefixes, c: ch})
	c.eventSubsLock.Unlock()
	return ch
}
