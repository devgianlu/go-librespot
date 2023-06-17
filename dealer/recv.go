package dealer

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"strings"
)

type messageReceiver struct {
	uriPrefixes []string
	c           chan Message
}

type Message struct {
	Uri     string
	Headers map[string]string
	// TODO
}

type requestReceiver struct {
	c chan Request
}

type Request struct {
	// TODO
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
	recv, ok := d.requestReceivers[rawMsg.Uri]
	d.requestReceiversLock.RUnlock()

	if !ok {
		log.Warnf("ignoring dealer request for %s", rawMsg.Uri)
		return
	}

	recv.c <- Request{} // TODO
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
