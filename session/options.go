package session

import (
	"net/http"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/apresolve"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
)

type Options struct {
	// Log is the base logger entry to use.
	Log librespot.Logger

	// DeviceType is the Spotify showed device type, required.
	DeviceType devicespb.DeviceType
	// DeviceId is the Spotify device ID, required.
	DeviceId string
	// Credentials is the credentials to be used for authentication, required.
	Credentials any

	// ClientToken is the Spotify client token, leave empty to let the server generate one.
	ClientToken string
	// Resolver is an instance of apresolve.ApResolver, leave nil to use the default one.
	Resolver *apresolve.ApResolver
	// PreferFirewallFriendlyPorts prefers accesspoints on 443 and 80 over the
	// default 4070. Only consulted when Resolver is nil, since a supplied
	// resolver already carries its own preference.
	PreferFirewallFriendlyPorts bool

	// Client is the HTTP client to use for the session, leave empty for a new one.
	Client *http.Client

	// StateStore is the state store to use for the session.
	StateStore librespot.StateStore
}

type InteractiveCredentials struct {
	CallbackPort int
}

// DeviceAuthCredentials authenticates with the OAuth 2.0 device authorization
// flow: Spotify issues a code that the user enters on another device. Nothing
// listens on a port and no browser is needed on this machine.
type DeviceAuthCredentials struct{}

type SpotifyTokenCredentials struct {
	Username string
	Token    string
}

type StoredCredentials struct {
	Username string
	Data     []byte
}

type BlobCredentials struct {
	Username string
	Blob     []byte
}
