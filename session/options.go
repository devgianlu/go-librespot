package session

import (
	"github.com/devgianlu/go-librespot/apresolve"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
)

type Options struct {
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
}

type InteractiveCredentials struct {
	CallbackPort int
}

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
