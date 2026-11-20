package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/devgianlu/go-librespot/spclient"
	"github.com/devgianlu/go-librespot/tracks"

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

// updateList replaces the items of the list playing with uris, in place (see
// tracks.List.ReplaceItems).
func (p *AppPlayer) updateList(uris []string, reply replyTo) {
	log, sp := p.app.log, p.sess.Spclient()
	p.listJob("update list", classMutate, nil,
		func(ctx context.Context, list *tracks.List) error {
			return list.ReplaceItems(ctx, uris, func(uris []string) (tracks.ContextResolver, error) {
				return spclient.NewContextResolver(ctx, log, sp, trackListContext(uris))
			})
		},
		func(p *AppPlayer, snap *tracks.Snapshot, err error) {
			if errors.Is(err, ErrNoContext) {
				err = fmt.Errorf("%w: %w", tracks.ErrNotATrackList, err)
			}
			if err != nil {
				reply.done(nil, err)
				return
			}

			p.state.player.Index = snap.Index
			p.publishUpcoming(snap)

			// What plays next may have changed: a stream prefetched under the
			// old list must not be switched or faded into.
			p.invalidateUpcoming()
			p.schedulePrefetchNext()
			p.updateState()
			reply.done(nil, nil)
		})
}
