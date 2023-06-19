package dealer

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"io"
	"strings"
)

type messageReceiver struct {
	uriPrefixes []string
	c           chan Message
}

type Message struct {
	Uri     string
	Headers map[string]string
}

type requestReceiver struct {
	c chan Request
}

type Request struct {
	resp chan bool

	MessageIdent string
	Payload      RequestPayload
}

func (req Request) Reply(success bool) {
	req.resp <- success
}

type RequestPayload struct {
	MessageId      uint32 `json:"message_id"`
	TargetAliasId  string `json:"target_alias_id"`
	SentByDeviceId string `json:"sent_by_device_id"`
	Command        struct {
		Endpoint string `json:"endpoint"`
		Data     []byte `json:"data"`
		Options  struct {
			RestorePaused   string `json:"restore_paused"`
			RestorePosition string `json:"restore_position"`
			RestoreTrack    string `json:"restore_track"`
			License         string `json:"license"`
		} `json:"options"`
		FromDeviceIdentifier string `json:"from_device_identifier"`
	} `json:"command"`
}

func (d *Dealer) handleMessage(rawMsg *RawMessage) {
	var matchedReceivers []messageReceiver

	// lookup receivers that want to match this message
	d.messageReceiversLock.RLock()
	for _, recv := range d.messageReceivers {
		for _, uriPrefix := range recv.uriPrefixes {
			if strings.HasPrefix(rawMsg.Uri, uriPrefix) {
				matchedReceivers = append(matchedReceivers, recv)
				break
			}
		}
	}
	d.messageReceiversLock.RUnlock()

	if len(matchedReceivers) == 0 {
		log.Debugf("skipping dealer message for %s", rawMsg.Uri)
		return
	}

	msg := Message{
		Uri:     rawMsg.Uri,
		Headers: rawMsg.Headers,
	}

	for _, recv := range matchedReceivers {
		recv.c <- msg
	}
}

func (d *Dealer) ReceiveMessage(uriPrefixes ...string) <-chan Message {
	if len(uriPrefixes) == 0 {
		panic("uri prefixes list cannot be empty")
	}

	d.messageReceiversLock.Lock()
	defer d.messageReceiversLock.Unlock()

	// create new receiver
	c := make(chan Message)
	d.messageReceivers = append(d.messageReceivers, messageReceiver{uriPrefixes, c})

	// start receiving if necessary
	d.startReceiving()

	return c
}

func (d *Dealer) handleRequest(rawMsg *RawMessage) {
	d.requestReceiversLock.RLock()
	recv, ok := d.requestReceivers[rawMsg.MessageIdent]
	d.requestReceiversLock.RUnlock()

	if !ok {
		log.Warnf("ignoring dealer request for %s", rawMsg.MessageIdent)
		return
	}

	var payloadBytes []byte
	if transEnc, ok := rawMsg.Headers["Transfer-Encoding"]; ok {
		switch transEnc {
		case "gzip":
			gz, err := gzip.NewReader(bytes.NewReader(rawMsg.Payload.Compressed))
			if err != nil {
				log.WithError(err).Error("invalid gzip stream")
				return
			}

			payloadBytes, err = io.ReadAll(gz)
			if err != nil {
				_ = gz.Close()
				log.WithError(err).Error("failed decompressing gzip payload")
				return
			}

			_ = gz.Close()
		default:
			log.Warnf("unsupported transfer encoding: %s", transEnc)
			return
		}

		delete(rawMsg.Headers, "Transfer-Encoding")
	}

	var payload RequestPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		log.WithError(err).Error("failed unmarshalling dealer request payload")
		return
	}

	// dispatch request
	resp := make(chan bool)
	recv.c <- Request{
		resp:         resp,
		MessageIdent: rawMsg.MessageIdent,
		Payload:      payload,
	}

	// wait for response and send it
	success := <-resp
	if err := d.sendReply(rawMsg.Key, success); err != nil {
		log.WithError(err).Error("failed sending dealer reply")
		return
	}
}

func (d *Dealer) ReceiveRequest(uri string) <-chan Request {
	d.requestReceiversLock.Lock()
	defer d.requestReceiversLock.Unlock()

	// check that there isn't another receiver for this uri
	_, ok := d.requestReceivers[uri]
	if ok {
		panic(fmt.Sprintf("cannot have more request receivers for %s", uri))
	}

	// create new receiver
	c := make(chan Request)
	d.requestReceivers[uri] = requestReceiver{c}

	// start receiving if necessary
	d.startReceiving()

	return c
}
