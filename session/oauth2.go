package session

import (
	"context"
	"fmt"
	"net"
	"net/http"

	log "github.com/sirupsen/logrus"
)

func NewOAuth2Server(ctx context.Context, callbackPort int) (int, chan string, error) {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", callbackPort))
	if err != nil {
		return 0, nil, fmt.Errorf("failed to listen: %w", err)
	}

	errCh := make(chan error, 1)
	resCh := make(chan string, 1)
	go func() {
		errCh <- http.Serve(lis, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			resCh <- r.URL.Query().Get("code")
			_, _ = rw.Write([]byte("Go back to go-librespot!"))
		}))
	}()

	go func() {
		select {
		case <-ctx.Done():
			_ = lis.Close()
		case err := <-errCh:
			if err != nil {
				log.WithError(err).Errorf("failed service oauth2 server")
				resCh <- ""
			}
		}
	}()

	return lis.Addr().(*net.TCPAddr).Port, resCh, nil
}
