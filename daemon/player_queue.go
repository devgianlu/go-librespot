package daemon

import (
	"slices"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"google.golang.org/protobuf/proto"
)

// delimiterUri marks the end of the context's tracks in the window.
const delimiterUri = "spotify:delimiter"

func isQueueEntry(t *connectpb.ProvidedTrack) bool {
	return t != nil && t.Uri != "" && t.Uri != delimiterUri
}

// apiQueue describes the window around the current track, as published to
// Spotify Connect. Runs on the Run goroutine.
func (p *AppPlayer) apiQueue() *ApiQueue {
	sig := p.queueSignature()

	entry := func(k queueKey, position int64) ApiQueueEntry {
		// Entries without metadata, such as local files, have a null track.
		e := ApiQueueEntry{Uri: k.uri, Uid: nonEmpty(k.uid), Provider: QueueEntryProvider(k.provider)}
		if k.media != nil {
			e.Track = p.newApiResponseStatusMedia(k.media, position)
		}
		return e
	}
	entries := func(keys []queueKey) []ApiQueueEntry {
		out := make([]ApiQueueEntry, 0, len(keys))
		for _, k := range keys {
			out = append(out, entry(k, 0))
		}
		return out
	}

	queue := &ApiQueue{PrevTracks: entries(sig.prev), NextTracks: entries(sig.next)}
	if sig.current.uri != "" {
		// A loading track is marked playing while buffering, and
		// trackPosition would run on from its anchor.
		position := p.state.player.PositionAsOfTimestamp
		if !p.state.player.IsBuffering {
			position = p.state.trackPosition()
		}
		current := entry(sig.current, position)
		// A track that ended stays current until the next one loads.
		if t := current.Track; t != nil && t.Duration > 0 {
			t.Position = min(t.Position, int64(t.Duration))
		}
		queue.Track = &current
	}
	return queue
}

// queueKey is an entry of GET /player/queue minus the playing position, so
// that a change can be detected without describing every track.
type queueKey struct {
	uri, uid, provider string
	media              *librespot.Media
}

// equal compares media by content: a load caches a new copy of metadata that
// may already be cached.
func (k queueKey) equal(o queueKey) bool {
	if k.uri != o.uri || k.uid != o.uid || k.provider != o.provider {
		return false
	}

	switch a, b := k.media, o.media; {
	case a == b:
		return true
	case a == nil || b == nil || a.IsTrack() != b.IsTrack():
		return false
	case a.IsTrack():
		return proto.Equal(a.Track(), b.Track())
	default:
		return proto.Equal(a.Episode(), b.Episode())
	}
}

// queueSignature is the keys of a whole GET /player/queue answer.
type queueSignature struct {
	current    queueKey
	prev, next []queueKey
}

func (s queueSignature) equal(o queueSignature) bool {
	return s.current.equal(o.current) &&
		slices.EqualFunc(s.prev, o.prev, queueKey.equal) &&
		slices.EqualFunc(s.next, o.next, queueKey.equal)
}

func (s queueSignature) empty() bool {
	return s.current.uri == "" && len(s.prev) == 0 && len(s.next) == 0
}

func (p *AppPlayer) queueSignature() queueSignature {
	// Anything that is not an entry is the zero key.
	key := func(t *connectpb.ProvidedTrack) queueKey {
		if !isQueueEntry(t) {
			return queueKey{}
		}

		k := queueKey{uri: t.Uri, uid: t.Uid, provider: t.Provider}
		// get marks the entries used: this runs after every cached batch, so
		// sweeping another context evicts around the window. Cover URLs need
		// the product info.
		if p.prodInfo != nil {
			k.media = p.app.metaCache.get(t.Uri)
		}
		return k
	}
	keys := func(window []*connectpb.ProvidedTrack) []queueKey {
		out := make([]queueKey, 0, len(window))
		for _, t := range window {
			if k := key(t); k.uri != "" {
				out = append(out, k)
			}
		}
		return out
	}
	return queueSignature{
		current: key(p.state.player.Track),
		prev:    keys(p.state.player.PrevTracks),
		next:    keys(p.state.player.NextTracks),
	}
}

// emitQueue raises the queue event if GET /player/queue would answer
// differently than when it was last raised, the playing position aside.
func (p *AppPlayer) emitQueue() {
	sig := p.queueSignature()
	if sig.equal(p.lastQueue) {
		return
	}
	p.lastQueue = sig
	p.app.server.Emit(&ApiEvent{Type: ApiEventTypeQueue})
}

// emitQueueGone raises the queue event when the session ends with entries
// listed, so that clients do not keep showing them.
func (p *AppPlayer) emitQueueGone() {
	if p.lastQueue.empty() {
		return
	}
	p.app.server.Emit(&ApiEvent{Type: ApiEventTypeQueue})
}
