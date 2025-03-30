package session

import (
	"context"
	"github.com/devgianlu/go-librespot/mercury"
	"github.com/devgianlu/go-librespot/player"
	"net/http"
	"net/url"

	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/devgianlu/go-librespot/dealer"
	"github.com/devgianlu/go-librespot/spclient"
)

func (s *Session) Username() string {
	return s.ap.Username()
}

func (s *Session) StoredCredentials() []byte {
	return s.ap.StoredCredentials()
}

func (s *Session) Spclient() *spclient.Spclient {
	return s.sp
}

func (s *Session) Events() player.EventManager {
	return s.events
}

func (s *Session) AudioKey() *audio.KeyProvider {
	return s.audioKey
}

func (s *Session) Dealer() *dealer.Dealer {
	return s.dealer
}

func (s *Session) Accesspoint() *ap.Accesspoint {
	return s.ap
}

func (s *Session) Mercury() *mercury.Client {
	return s.hg
}

func (s *Session) WebApi(ctx context.Context, method string, path string, query url.Values, header http.Header, body []byte) (*http.Response, error) {
	return s.sp.WebApiRequest(ctx, method, path, query, header, body)
}
