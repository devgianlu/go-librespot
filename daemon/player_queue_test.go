//go:build test_unit

package daemon

import (
	"context"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	extmetadatapb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata"
	"github.com/devgianlu/go-librespot/tracks"
	"github.com/stretchr/testify/require"
)

func entry(uri, uid, provider string) *connectpb.ProvidedTrack {
	return &connectpb.ProvidedTrack{Uri: uri, Uid: uid, Provider: provider}
}

func playerQueue(t *testing.T, p *AppPlayer) *ApiQueue {
	t.Helper()

	resp, err := p.handleApiRequest(ApiRequest{Type: ApiRequestTypeGetQueue})
	require.NoError(t, err)
	return resp.(*ApiQueue)
}

func queueEntryUris(entries []ApiQueueEntry) []string {
	uris := make([]string, 0, len(entries))
	for _, e := range entries {
		uris = append(uris, e.Uri)
	}
	return uris
}

// The queue lists the play order around the current track as Spotify's queue
// view shows it: where each entry comes from, the uid that tells two copies of
// a track apart, and the metadata of the entries already cached.
func TestPlayerQueue(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)

	queue := playerQueue(t, p)
	require.Nil(t, queue.Track, "nothing loaded")
	require.NotNil(t, queue.PrevTracks, "an empty list, not a missing one")
	require.Empty(t, queue.PrevTracks)
	require.NotNil(t, queue.NextTracks)
	require.Empty(t, queue.NextTracks)

	// The shapes measured on a live session: album tracks carry no uid, the
	// user's queue numbers its entries, the same track queued twice included.
	p.state.player.PrevTracks = []*connectpb.ProvidedTrack{entry(trackUri(0x01), "", "context")}
	p.state.player.Track = entry(trackUri(0x02), "", "context")
	p.state.player.NextTracks = []*connectpb.ProvidedTrack{
		entry(trackUri(0x03), "q1", "queue"),
		entry(trackUri(0x03), "q2", "queue"),
		entry(trackUri(0x04), "", "context"),
		entry("spotify:local:Artist:Album:Title:200", "", "context"),
		entry("spotify:delimiter", "", "context"),
	}
	p.app.metaCache.put(trackUri(0x02), librespot.NewMediaFromTrack(metaTrack(gid(0x02), "current")))
	p.app.metaCache.put(trackUri(0x03), librespot.NewMediaFromTrack(metaTrack(gid(0x03), "queued")))

	queue = playerQueue(t, p)
	require.Equal(t, []string{trackUri(0x01)}, queueEntryUris(queue.PrevTracks))
	require.Nil(t, queue.PrevTracks[0].Uid, "no uid to report")
	require.Equal(t, QueueEntryProviderContext, queue.PrevTracks[0].Provider)
	require.Nil(t, queue.PrevTracks[0].Track, "not cached yet")

	require.NotNil(t, queue.Track)
	require.Equal(t, trackUri(0x02), queue.Track.Uri)
	require.Equal(t, "current", queue.Track.Track.Name)

	require.Equal(t, []string{trackUri(0x03), trackUri(0x03), trackUri(0x04), "spotify:local:Artist:Album:Title:200"},
		queueEntryUris(queue.NextTracks), "a local file listed, no delimiter")
	require.Nil(t, queue.NextTracks[3].Track, "a local file has no metadata to name it")
	require.Equal(t, "q1", *queue.NextTracks[0].Uid)
	require.Equal(t, "q2", *queue.NextTracks[1].Uid, "two copies, two uids")
	require.Equal(t, QueueEntryProviderQueue, queue.NextTracks[1].Provider)
	require.Equal(t, "queued", queue.NextTracks[1].Track.Name)
	require.Equal(t, QueueEntryProviderContext, queue.NextTracks[2].Provider)
	require.Nil(t, queue.NextTracks[2].Track)
}

// Without the metadata cache the play order is still listed, every track null.
func TestPlayerQueueWithoutMetadata(t *testing.T) {
	p := newTestAppPlayer(t)
	p.app.cfg = &Config{}
	p.prodInfo = &ProductInfo{}
	p.state.player.Track = entry(trackUri(0x01), "", "autoplay")
	p.state.player.NextTracks = []*connectpb.ProvidedTrack{entry(trackUri(0x02), "e58e6bb1e268e0bb", "autoplay")}

	queue := playerQueue(t, p)
	require.Equal(t, trackUri(0x01), queue.Track.Uri)
	require.Nil(t, queue.Track.Track)
	require.Equal(t, []string{trackUri(0x02)}, queueEntryUris(queue.NextTracks))
	require.Equal(t, QueueEntryProviderAutoplay, queue.NextTracks[0].Provider)
	require.Nil(t, queue.NextTracks[0].Track)
}

func queueEvents(p *AppPlayer) int {
	n := 0
	for _, ev := range apiEvents(p) {
		if ev == ApiEventTypeQueue {
			n++
		}
	}
	return n
}

// turn is what the Run loop does at the top of every turn.
func turn(p *AppPlayer) int {
	p.emitQueueIfMoved()
	return queueEvents(p)
}

func awaitMetaCached(t *testing.T, p *AppPlayer, what string) {
	t.Helper()

	select {
	case <-p.metaCached:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never reported what it cached", what)
	}
}

// A queue event tells clients to read the queue again: once each time what it
// returns changes — its order, or which entries it names — and never for a
// change it would not show.
func TestQueueEvent(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)
	require.Equal(t, 0, turn(p), "nothing to list is nothing to say")

	window := &tracks.Snapshot{
		Current: provided(trackUri(0x02)),
		Prev:    []*connectpb.ProvidedTrack{provided(trackUri(0x01))},
		Next:    []*connectpb.ProvidedTrack{provided(trackUri(0x03))},
	}
	p.publishSnapshot(window)
	require.Equal(t, 1, turn(p), "the window moved")

	p.publishSnapshot(window)
	require.Equal(t, 1, turn(p), "the same window again says nothing")

	p.publishSnapshot(&tracks.Snapshot{
		Current: provided(trackUri(0x02)),
		Prev:    []*connectpb.ProvidedTrack{provided(trackUri(0x01))},
		Next:    []*connectpb.ProvidedTrack{entry(trackUri(0x04), "q1", "queue"), provided(trackUri(0x03))},
	})
	require.Equal(t, 2, turn(p), "a track was queued")

	// The window's metadata lands from a detached fetch, which wakes the loop.
	p.prefetchWindowMetadata()
	awaitMetaCached(t, p, "the window prefetch")
	require.Equal(t, 3, turn(p), "its entries are named now")

	// A fetch that names nothing in the window (a sweep of another context)
	// is no change to report.
	p.scheduleMetaSweep([]string{trackUri(0x05)}, "another context")
	awaitMetaCached(t, p, "the sweep")
	require.Equal(t, 3, turn(p))

	// Nor is metadata cached again as it was (a load caches its stream's):
	// another copy of the same description reads the same.
	p.app.metaCache.put(trackUri(0x03), librespot.NewMediaFromTrack(metaTrack(gid(0x03), "again")))
	require.Equal(t, 4, turn(p), "a new description")
	p.app.metaCache.put(trackUri(0x03), librespot.NewMediaFromTrack(metaTrack(gid(0x03), "again")))
	require.Equal(t, 4, turn(p), "the same description, cached again")
}

// Stopping clears the play order without publishing a window, and clients
// that list it are told.
func TestQueueEventWhenStopped(t *testing.T) {
	p := newTestAppPlayer(t)
	p.app.cfg = &Config{}
	p.prodInfo = &ProductInfo{}
	p.publishSnapshot(&tracks.Snapshot{
		Current: provided(trackUri(0x01)),
		Next:    []*connectpb.ProvidedTrack{provided(trackUri(0x02))},
	})
	require.Equal(t, 1, turn(p))

	p.stopPlayback()
	require.Equal(t, 2, turn(p))
	require.Nil(t, playerQueue(t, p).Track)
}

// The next track's stream carries its metadata; caching it from the prefetch
// names that entry, and clients are told.
func TestQueueEventWhenTheNextTrackIsPrefetched(t *testing.T) {
	p := newTestAppPlayer(t)
	p.app.cfg = &Config{Metadata: MetadataConfig{Enabled: true}}
	p.app.metaCache = newTrackMetaCache(trackMetaCacheLimit)
	p.prodInfo = &ProductInfo{}
	p.publishSnapshot(&tracks.Snapshot{
		Current: provided(trackUri(0x01)),
		Next:    []*connectpb.ProvidedTrack{provided(trackUri(0x02))},
	})
	require.Equal(t, 1, turn(p))

	id, err := librespot.SpotifyIdFromUri(trackUri(0x02))
	require.NoError(t, err)
	p.commitPrefetch(&player.Stream{RequestedId: *id, Media: librespot.NewMediaFromTrack(metaTrack(gid(0x02), "next"))}, nil)

	require.Equal(t, 2, turn(p))
	require.Equal(t, "next", playerQueue(t, p).NextTracks[0].Track.Name)
}

// Tracks are described only once the product info is known; its arrival
// names the cached entries, and clients are told.
func TestQueueEventWhenTheProductInfoArrives(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)
	p.prodInfo = nil
	p.app.metaCache.put(trackUri(0x02), librespot.NewMediaFromTrack(metaTrack(gid(0x02), "next")))
	p.publishSnapshot(&tracks.Snapshot{
		Current: provided(trackUri(0x01)),
		Next:    []*connectpb.ProvidedTrack{provided(trackUri(0x02))},
	})
	require.Equal(t, 1, turn(p))
	require.Nil(t, playerQueue(t, p).NextTracks[0].Track)

	p.prodInfo = &ProductInfo{}
	require.Equal(t, 2, turn(p))
	require.Equal(t, "next", playerQueue(t, p).NextTracks[0].Track.Name)
}

// Every batch that cached tracks wakes the loop, not the end of a sweep: a
// long one is paced over seconds, and the window's entries may be among its
// first batch.
func TestMetaFetchTellsEachBatchThatCached(t *testing.T) {
	var told int
	f := &metaFetcher{log: &librespot.NullLogger{}, cache: newTrackMetaCache(trackMetaCacheLimit), fetch: echoMetadata, cached: func() { told++ }}

	_, err := f.fetchBatch(t.Context(), []string{trackUri(0x01)})
	require.NoError(t, err)
	_, err = f.fetchBatch(t.Context(), []string{trackUri(0x02), trackUri(0x03)})
	require.NoError(t, err)
	require.Equal(t, 2, told)

	f.fetch = func(context.Context, *extmetadatapb.BatchedEntityRequest) (*extmetadatapb.BatchedExtensionResponse, error) {
		return &extmetadatapb.BatchedExtensionResponse{}, nil
	}
	_, err = f.fetchBatch(t.Context(), []string{trackUri(0x04)})
	require.NoError(t, err)
	require.Equal(t, 2, told, "a batch that cached nothing says nothing")
}

// Each part of an entry is part of the answer, and changing it alone is said:
// a track dragged out of the queue keeps its uri and changes provider; the
// current entry is named by the load that caches its stream.
func TestQueueEventFollowsEachPartOfAnEntry(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)
	p.publishSnapshot(&tracks.Snapshot{
		Current: provided(trackUri(0x02)),
		Prev:    []*connectpb.ProvidedTrack{provided(trackUri(0x01))},
		Next:    []*connectpb.ProvidedTrack{entry(trackUri(0x03), "q1", "queue")},
	})
	require.Equal(t, 1, turn(p))

	p.state.player.NextTracks = []*connectpb.ProvidedTrack{entry(trackUri(0x03), "q1", "context")}
	require.Equal(t, 2, turn(p), "the provider alone")

	p.state.player.NextTracks = []*connectpb.ProvidedTrack{entry(trackUri(0x03), "q2", "context")}
	require.Equal(t, 3, turn(p), "the uid alone")

	p.app.metaCache.putStream(trackUri(0x02), librespot.NewMediaFromTrack(metaTrack(gid(0x02), "current")))
	require.Equal(t, 4, turn(p), "the current entry named")

	p.app.metaCache.put(trackUri(0x01), librespot.NewMediaFromTrack(metaTrack(gid(0x01), "before")))
	require.Equal(t, 5, turn(p), "a previous entry named")
}

// The answer an event is raised for is described from the signature that
// decided to raise it, not from a second read of the cache: a fetch that
// evicts in between would leave the two disagreeing, and the next turn,
// finding the signature unchanged, would never say so.
func TestQueueAnswerIsDescribedFromItsSignature(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)
	p.state.player.Track = provided(trackUri(0x01))
	p.app.metaCache.put(trackUri(0x01), librespot.NewMediaFromTrack(metaTrack(gid(0x01), "named")))

	sig := p.queueSignature()
	p.app.metaCache.lru.Remove(trackUri(0x01)) // evicted by a detached fetch

	queue := queueAnswer(sig, p.newApiResponseStatusMedia)
	require.NotNil(t, queue.Track.Track)
	require.Equal(t, "named", queue.Track.Track.Name)
}

// A session that ends takes its play order with it: clients are told, as the
// next session's player starts from nothing listed.
func TestQueueEventWhenTheSessionEnds(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)
	p.emitQueueGone()
	require.Equal(t, 0, queueEvents(p), "nothing was listed")

	p.publishSnapshot(&tracks.Snapshot{Current: provided(trackUri(0x01))})
	require.Equal(t, 1, turn(p))
	p.emitQueueGone()
	require.Equal(t, 2, queueEvents(p))
	p.emitQueueGone()
	require.Equal(t, 2, queueEvents(p), "said once")
}
