package daemon

import (
	"reflect"
	"slices"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
)

// delimiterUri marks where a context's tracks end in the play order; it is
// not an entry of it.
const delimiterUri = "spotify:delimiter"

func isQueueEntry(t *connectpb.ProvidedTrack) bool {
	return t != nil && t.Uri != "" && t.Uri != delimiterUri
}

// apiQueue describes the play order around the current track as the daemon
// holds it: the window Spotify Connect publishes, with the user's queue and
// shuffling applied. Runs on the Run goroutine.
func (p *AppPlayer) apiQueue() *ApiQueue {
	return queueAnswer(p.queueSignature(), p.newApiResponseStatusMedia)
}

// queueAnswer describes the play order from its keys alone. Taking the
// metadata from the keys rather than reading the cache again keeps the answer
// an event is raised for the one its signature was taken from: a fetch that
// caches or evicts between two reads would leave them disagreeing, and the
// next turn, finding the signature unchanged, would never say so.
func queueAnswer(sig queueSignature, describe func(*librespot.Media, int64) *ApiTrack) *ApiQueue {
	entry := func(k queueKey) ApiQueueEntry {
		// Entries the metadata service does not describe, such as local
		// files, are listed all the same, with a null track.
		e := ApiQueueEntry{Uri: k.uri, Provider: QueueEntryProvider(k.provider)}
		if k.uid != "" {
			uid := k.uid
			e.Uid = &uid
		}
		if k.media != nil && sig.prodInfo != nil {
			e.Track = describe(k.media, 0)
		}
		return e
	}
	entries := func(keys []queueKey) []ApiQueueEntry {
		out := make([]ApiQueueEntry, 0, len(keys))
		for _, k := range keys {
			out = append(out, entry(k))
		}
		return out
	}

	queue := &ApiQueue{PrevTracks: entries(sig.prev), NextTracks: entries(sig.next)}
	if sig.current.uri != "" {
		current := entry(sig.current)
		queue.Track = &current
	}
	return queue
}

// queueKey is what an entry of GET /player/queue is made of: comparing keys
// tells cheaply whether the answer may have changed, without describing
// every track, and the answer is described from them.
type queueKey struct {
	uri, uid, provider string
	media              *librespot.Media
}

// queueSignature is the keys of the whole answer, with the product info the
// cover URLs are built from.
type queueSignature struct {
	prodInfo   *ProductInfo
	current    queueKey
	prev, next []queueKey
}

func (s queueSignature) equal(o queueSignature) bool {
	return s.prodInfo == o.prodInfo && s.current == o.current && slices.Equal(s.prev, o.prev) && slices.Equal(s.next, o.next)
}

func (p *AppPlayer) queueSignature() queueSignature {
	key := func(t *connectpb.ProvidedTrack) queueKey {
		if !isQueueEntry(t) {
			return queueKey{}
		}
		return queueKey{t.Uri, t.Uid, t.Provider, p.app.metaCache.peek(t.Uri)}
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
		prodInfo: p.prodInfo,
		current:  key(p.state.player.Track),
		prev:     keys(p.state.player.PrevTracks),
		next:     keys(p.state.player.NextTracks),
	}
}

// emitQueueIfMoved raises the queue event when what GET /player/queue returns
// has changed since the event was last raised, and only then. The Run loop
// calls it on every turn, so no path that moves the play order or caches its
// metadata has to remember to: the signature keeps a turn that changed
// nothing cheap, and the answer itself is compared before anything is said.
func (p *AppPlayer) emitQueueIfMoved() {
	sig := p.queueSignature()
	if sig.equal(p.queueSigned) {
		return
	}
	p.queueSigned = sig

	queue := queueAnswer(sig, p.newApiResponseStatusMedia)
	listed := p.queueListed
	if listed == nil {
		// Nothing was said yet, which a client reads as nothing to list.
		listed = &ApiQueue{PrevTracks: []ApiQueueEntry{}, NextTracks: []ApiQueueEntry{}}
	}
	if reflect.DeepEqual(queue, listed) {
		return
	}
	p.queueListed = queue
	p.app.server.Emit(&ApiEvent{Type: ApiEventTypeQueue})
}

// emitQueueGone tells clients the play order they listed is gone with the
// session: the next session's player starts from nothing listed, and a
// takeover or a logout would otherwise leave them drawing the last one. Runs
// as the Run loop ends.
func (p *AppPlayer) emitQueueGone() {
	if q := p.queueListed; q == nil || (q.Track == nil && len(q.PrevTracks) == 0 && len(q.NextTracks) == 0) {
		return
	}
	p.queueListed = nil
	p.app.server.Emit(&ApiEvent{Type: ApiEventTypeQueue})
}

// signalMetaCached wakes the Run loop after a metadata fetch cached tracks,
// which may name entries of the play order. Called from the detached fetches.
func (p *AppPlayer) signalMetaCached() {
	select {
	case p.metaCached <- struct{}{}:
	default: // one already pending says the same
	}
}
