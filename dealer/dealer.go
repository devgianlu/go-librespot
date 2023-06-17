package dealer

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"net/http"
	"nhooyr.io/websocket"
)

type Dealer struct {
	conn *websocket.Conn
}

func NewDealer(dealerAddr string, accessToken string) (*Dealer, error) {
	conn, _, err := websocket.Dial(context.TODO(), fmt.Sprintf("wss://%s/?access_token=%s", dealerAddr, accessToken), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"User-Agent": []string{librespot.UserAgent()},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed opening dealer connection: %w", err)
	}

	log.Debugf("dealer connection opened")
	return &Dealer{conn: conn}, nil
}
