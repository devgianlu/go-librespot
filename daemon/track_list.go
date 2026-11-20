package daemon

import (
	"fmt"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
)

// trackListContext is the context /player/play plays for a list of uris: the
// items inline, in one page, with no uri of a context of its own, like a list
// the Web API plays for "uris". It needs no resolving. It is nil without uris.
func trackListContext(uris []string) *connectpb.Context {
	if len(uris) == 0 {
		return nil
	}

	tracks := make([]*connectpb.ContextTrack, len(uris))
	for i, uri := range uris {
		tracks[i] = &connectpb.ContextTrack{Uri: uri, Metadata: map[string]string{}}
	}

	return &connectpb.Context{
		Metadata: map[string]string{},
		Pages:    []*connectpb.ContextPage{{Tracks: tracks}},
	}
}

// playJobName names the loader job of a /player/play request in the log.
func playJobName(data ApiPlay) string {
	if len(data.Uris) > 0 {
		return fmt.Sprintf("play a list of %d items", len(data.Uris))
	}
	return "resolve " + data.Uri
}
