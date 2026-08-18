package session

import (
	"context"
	"fmt"
	librespot "github.com/devgianlu/go-librespot"
	"golang.org/x/oauth2"
	spotifyoauth2 "golang.org/x/oauth2/spotify"
	"net"
	"net/http"
)

// deviceAuthURL is Spotify's RFC 8628 device authorization endpoint. It is not
// part of golang.org/x/oauth2/spotify's Endpoint, so it is spelled out here.
//
// Spotify only enables the device flow for some client IDs, and rejects the
// others with unauthorized_client. Ours is accepted.
const deviceAuthURL = "https://accounts.spotify.com/oauth2/device/authorize"

// oauthScopes is the set of scopes requested by every OAuth flow here.
var oauthScopes = []string{
	"app-remote-control",
	"playlist-modify",
	"playlist-modify-private",
	"playlist-modify-public",
	"playlist-read",
	"playlist-read-collaborative",
	"playlist-read-private",
	"streaming",
	"ugc-image-upload",
	"user-follow-modify",
	"user-follow-read",
	"user-library-modify",
	"user-library-read",
	"user-modify",
	"user-modify-playback-state",
	"user-modify-private",
	"user-personalized",
	"user-read-birthdate",
	"user-read-currently-playing",
	"user-read-email",
	"user-read-play-history",
	"user-read-playback-position",
	"user-read-playback-state",
	"user-read-private",
	"user-read-recently-played",
	"user-top-read",
}

// newOAuthConfig builds the Spotify OAuth2 config. redirectURL is empty for
// flows that never redirect a user agent, such as the device flow.
func newOAuthConfig(redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:    librespot.ClientIdHex,
		RedirectURL: redirectURL,
		Scopes:      oauthScopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:       spotifyoauth2.Endpoint.AuthURL,
			TokenURL:      spotifyoauth2.Endpoint.TokenURL,
			DeviceAuthURL: deviceAuthURL,
		},
	}
}

func NewOAuth2Server(ctx context.Context, log librespot.Logger, callbackPort int) (int, chan string, error) {
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
