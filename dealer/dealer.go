package dealer

import (
	"context"
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"net/http"
	"nhooyr.io/websocket"
	"sync"
	"time"
)

type Dealer struct {
	conn *websocket.Conn

	recvLoopOnce sync.Once

	pingTickerStop chan struct{}
	recvLoopStop   chan struct{}

	messageReceivers     []messageReceiver
	messageReceiversLock sync.RWMutex

	requestReceivers     map[string]requestReceiver
	requestReceiversLock sync.RWMutex
}

func NewDealer(dealerAddr librespot.GetAddressFunc, accessToken string) (*Dealer, error) {
	conn, _, err := websocket.Dial(context.TODO(), fmt.Sprintf("wss://%s/?access_token=%s", dealerAddr(), accessToken), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"User-Agent": []string{librespot.UserAgent()},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed opening dealer connection: %w", err)
	}

	dealer := &Dealer{conn: conn}
	dealer.requestReceivers = map[string]requestReceiver{}
	dealer.recvLoopStop = make(chan struct{}, 1)
	dealer.pingTickerStop = make(chan struct{}, 1)

	go dealer.pingTicker()

	log.Debugf("dealer connection opened")
	return dealer, nil
}

func (d *Dealer) Close() {
	d.recvLoopStop <- struct{}{}
	d.pingTickerStop <- struct{}{}
	_ = d.conn.Close(websocket.StatusGoingAway, "closing")
}

func (d *Dealer) startReceiving() {
	d.recvLoopOnce.Do(func() { go d.recvLoop() })
}

func (d *Dealer) pingTicker() {
	t := time.NewTicker(30 * time.Second)

loop:
	for {
		select {
		case <-d.pingTickerStop:
			break loop
		case <-t.C:
			if err := d.conn.Write(context.TODO(), websocket.MessageText, []byte("{\"type\":\"ping\"}")); err != nil {
				log.WithError(err).Warnf("failed sending dealer ping")
				d.Close() // TODO: reconnect
				break loop
			}
		}
	}

	t.Stop()
}

func (d *Dealer) recvLoop() {
loop:
	for {
		select {
		case <-d.recvLoopStop:
			break loop
		default:
			msgType, messageBytes, err := d.conn.Read(context.TODO())
			if err != nil {
				log.WithError(err).Errorf("failed receiving message")
				break loop
			} else if msgType != websocket.MessageText {
				log.WithError(err).Warnf("unsupported message type: %v, len: %d", msgType, len(messageBytes))
				continue
			}

			var message RawMessage
			if err := json.Unmarshal(messageBytes, &message); err != nil {
				log.WithError(err).Error("failed unmarshalling message")
				break loop
			}

			switch message.Type {
			case "message":
				d.handleMessage(&message)
				break
			case "request":
				d.handleRequest(&message)
				break
			case "ping":
				// we never receive ping messages
				break
			case "pong":
				// TODO: track pongs to determine if we need to reconnect
				break
			default:
				log.Warnf("unknown dealer message type: %s", message.Type)
				break
			}
		}
	}

	d.requestReceiversLock.RLock()
	for _, recv := range d.requestReceivers {
		close(recv.c)
	}
	d.requestReceiversLock.RUnlock()

	d.messageReceiversLock.RLock()
	for _, recv := range d.messageReceivers {
		close(recv.c)
	}
	d.messageReceiversLock.RUnlock()

	_ = d.conn.Close(websocket.StatusInternalError, "")
	// TODO: reconnect
}
