//go:build test_unit

package daemon

import (
	"encoding/binary"
	"math"
	"sync"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/session"
	"github.com/devgianlu/go-librespot/tracks"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
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

func queueEvents(p *AppPlayer) int {
	n := 0
	for _, ev := range apiEvents(p) {
		if ev == ApiEventTypeQueue {
			n++
		}
	}
	return n
}

// newQueueTestPlayer is a player with the metadata cache on, over a session
// with nothing behind it: contexts that carry their tracks resolve without
// it, and nothing else reaches it.
func newQueueTestPlayer(t *testing.T) *AppPlayer {
	t.Helper()

	p := newTestAppPlayer(t)
	p.ctx = t.Context()
	p.sess = &session.Session{}
	p.app.cfg = &Config{Metadata: MetadataConfig{Enabled: true}}
	p.app.metaCache = newTrackMetaCache(trackMetaCacheLimit)
	p.app.contextLists = newContextListCache()
	p.meta = newMetaFetcher(p.app.log, p.app.metaCache, nil)
	p.meta.fetch = echoMetadata
	p.metaPrefetchTimer = time.NewTimer(math.MaxInt64)
	p.metaPrefetchTimer.Stop()
	p.prodInfo = &ProductInfo{}

	return p
}

func streamOf(t *testing.T, uri, name string) *player.Stream {
	t.Helper()

	id, err := librespot.SpotifyIdFromUri(uri)
	require.NoError(t, err)
	return &player.Stream{RequestedId: *id, Media: librespot.NewMediaFromTrack(metaTrack(id.Id(), name))}
}

// The queue lists the play order around the current track as Spotify's queue
// view shows it: where each entry comes from, the uid that tells two copies of
// a track apart, and the metadata of the entries already cached.
func TestPlayerQueue(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)
	require.Equal(t, &ApiQueue{PrevTracks: []ApiQueueEntry{}, NextTracks: []ApiQueueEntry{}}, playerQueue(t, p))

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

	queue := playerQueue(t, p)
	require.Equal(t, []string{trackUri(0x01)}, queueEntryUris(queue.PrevTracks))
	require.Nil(t, queue.PrevTracks[0].Uid)
	require.Equal(t, QueueEntryProviderContext, queue.PrevTracks[0].Provider)
	require.Nil(t, queue.PrevTracks[0].Track, "not cached")

	require.Equal(t, trackUri(0x02), queue.Track.Uri)
	require.Equal(t, "current", queue.Track.Track.Name)

	require.Equal(t, []string{trackUri(0x03), trackUri(0x03), trackUri(0x04), "spotify:local:Artist:Album:Title:200"},
		queueEntryUris(queue.NextTracks), "a local file listed, no delimiter")
	require.Nil(t, queue.NextTracks[3].Track)
	require.Equal(t, "q1", *queue.NextTracks[0].Uid)
	require.Equal(t, "q2", *queue.NextTracks[1].Uid)
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

// The current entry carries the playing position: it stands still while the
// track loads and at the end of a track that ended but is still current. The
// other entries have not started.
func TestPlayerQueueCurrentEntryPosition(t *testing.T) {
	for name, tc := range map[string]struct {
		paused, buffering bool
		duration          int32
		want              int64
	}{
		"playing": {duration: 300_000, want: 45_000},
		"paused":  {paused: true, duration: 300_000, want: 42_000},
		"loading": {buffering: true, duration: 300_000, want: 42_000},
		"ended":   {duration: 1000, want: 1000},
	} {
		t.Run(name, func(t *testing.T) {
			p := newMetaTestPlayer(t, echoMetadata, nil)
			p.publishSnapshot(&tracks.Snapshot{
				Current: provided(trackUri(0x02)),
				Next:    []*connectpb.ProvidedTrack{provided(trackUri(0x03))},
			})
			current := metaTrack(gid(0x02), "current")
			current.Duration = proto.Int32(tc.duration)
			p.app.metaCache.put(trackUri(0x02), librespot.NewMediaFromTrack(current))
			p.app.metaCache.put(trackUri(0x03), librespot.NewMediaFromTrack(metaTrack(gid(0x03), "next")))

			p.state.player.IsPlaying = true
			p.state.player.IsPaused = tc.paused
			p.state.player.IsBuffering = tc.buffering
			p.state.player.Timestamp = time.Now().Add(-3 * time.Second).UnixMilli()
			p.state.player.PositionAsOfTimestamp = 42_000

			queue := playerQueue(t, p)
			require.InDelta(t, tc.want, queue.Track.Track.Position, 500)
			require.Zero(t, queue.NextTracks[0].Track.Position)
		})
	}
}

// A queue event is raised once each time what GET /player/queue returns
// changes, and never for a change it would not show.
func TestQueueEvent(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)

	window := &tracks.Snapshot{
		Current: provided(trackUri(0x02)),
		Prev:    []*connectpb.ProvidedTrack{provided(trackUri(0x01))},
		Next:    []*connectpb.ProvidedTrack{provided(trackUri(0x03))},
	}
	p.publishSnapshot(window)
	require.Equal(t, 1, queueEvents(p))
	p.publishSnapshot(window)
	require.Equal(t, 1, queueEvents(p), "the same window")

	queued := func(uid, provider string) *tracks.Snapshot {
		return &tracks.Snapshot{
			Current: provided(trackUri(0x02)),
			Prev:    []*connectpb.ProvidedTrack{provided(trackUri(0x01))},
			Next:    []*connectpb.ProvidedTrack{entry(trackUri(0x04), uid, provider), provided(trackUri(0x03))},
		}
	}
	p.publishSnapshot(queued("q1", "queue"))
	require.Equal(t, 2, queueEvents(p), "a track queued")
	p.publishSnapshot(queued("q1", "context"))
	require.Equal(t, 3, queueEvents(p), "the provider alone")
	p.publishSnapshot(queued("q2", "context"))
	require.Equal(t, 4, queueEvents(p), "the uid alone")
	p.publishUpcoming(&tracks.Snapshot{Next: []*connectpb.ProvidedTrack{provided(trackUri(0x03))}})
	require.Equal(t, 5, queueEvents(p), "the surroundings alone")

	p.state.player.PositionAsOfTimestamp = 42_000
	p.emitQueue()
	require.Equal(t, 5, queueEvents(p), "the playhead alone")

	// A load caches its stream's description again, as a new copy.
	describe := func(name string) {
		p.app.metaCache.put(trackUri(0x03), librespot.NewMediaFromTrack(metaTrack(gid(0x03), name)))
		p.emitQueue()
	}
	describe("next")
	require.Equal(t, 6, queueEvents(p), "described")
	describe("next")
	require.Equal(t, 6, queueEvents(p), "the same description")
	describe("renamed")
	require.Equal(t, 7, queueEvents(p), "a new description")
}

// Changes that move the play order or describe its entries outside a
// published window raise the event too.
func TestQueueEventOutsideAWindow(t *testing.T) {
	transfer := func(t *testing.T, p *AppPlayer) {
		require.NoError(t, p.handlePlayerCommand(transferCommand(t, &connectpb.TransferState{
			Options:        &connectpb.ContextPlayerOptions{},
			CurrentSession: &connectpb.Session{Context: &connectpb.Context{Uri: "spotify:playlist:37i9dQZF1DWVKDF4ycOESi"}},
			Playback:       &connectpb.Playback{CurrentTrack: &connectpb.ContextTrack{Uri: jamTrackUri}},
		})))
	}

	for name, tc := range map[string]struct {
		change func(t *testing.T, p *AppPlayer)
		events int
	}{
		"stopped": {func(t *testing.T, p *AppPlayer) { p.stopPlayback() }, 2},
		"context started": {func(t *testing.T, p *AppPlayer) {
			p.loadContext(&connectpb.Context{Uri: "spotify:unsupported:37i9dQZF1DX8vwRmUsEIMT"}, nil, false, true, func(error) {})
			runQueuedJob(t, p)
		}, 2},
		// The track handed over, then nothing once the context fails.
		"transfer given up": {func(t *testing.T, p *AppPlayer) {
			p.resolveTrackList = failResolve
			transfer(t, p)
			runQueuedJob(t, p)
		}, 3},
		"taken over idle": {func(t *testing.T, p *AppPlayer) {
			req := transferCommand(t, &connectpb.TransferState{
				Options:        &connectpb.ContextPlayerOptions{},
				CurrentSession: &connectpb.Session{},
				Playback:       &connectpb.Playback{Timestamp: 1790340000000},
			})
			req.Command.Options.RestoreTrack = "always_play_something"
			require.NoError(t, p.handlePlayerCommand(req))
		}, 2},
		"next track prefetched": {func(t *testing.T, p *AppPlayer) {
			p.commitPrefetch(streamOf(t, trackUri(0x02), "next"), nil)
		}, 2},
		"current track loaded": {func(t *testing.T, p *AppPlayer) {
			p.commitLoad(streamOf(t, trackUri(0x01), "current"), trackUri(0x01), false, 0, false)
		}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			p := newQueueTestPlayer(t)
			playingTrack(t, p, trackUri(0x01), "u1")
			p.publishSnapshot(&tracks.Snapshot{Current: p.state.player.Track, Next: []*connectpb.ProvidedTrack{provided(trackUri(0x02))}})
			require.Equal(t, 1, queueEvents(p))

			tc.change(t, p)
			require.Equal(t, tc.events, queueEvents(p))
		})
	}
}

// Tracks are described only once the product info is known, and its arrival
// names the entries already cached.
func TestQueueEventWhenTheProductInfoArrives(t *testing.T) {
	p := newQueueTestPlayer(t)
	p.prodInfo = nil
	p.app.metaCache.put(trackUri(0x01), librespot.NewMediaFromTrack(metaTrack(gid(0x01), "current")))
	p.publishSnapshot(&tracks.Snapshot{Current: provided(trackUri(0x01))})
	require.Equal(t, 1, queueEvents(p))
	require.Nil(t, playerQueue(t, p).Track.Track)

	products := `<products><product><type>premium</type><image-url>https://i.scdn.co/image/{file_id}</image-url></product></products>`
	require.NoError(t, p.handleAccesspointPacket(ap.PacketTypeProductInfo, []byte(products)))
	require.Equal(t, 2, queueEvents(p))
	require.Equal(t, "current", playerQueue(t, p).Track.Track.Name)
}

// serveLoop runs p's loop without a session until the test ends, and returns
// a func that stops it. Requests sent to api are served by the loop.
func serveLoop(t *testing.T, p *AppPlayer, api chan ApiRequest) (stop func()) {
	t.Helper()

	p.stop = make(chan struct{}, 1)
	p.sleepTimer = time.NewTimer(math.MaxInt64)
	p.sleepTimer.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.serve(nil, nil, nil, nil, api, nil)
	}()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			p.stop <- struct{}{}
			<-done
		})
	}
	t.Cleanup(stop)
	return stop
}

// Metadata fetched off the loop wakes it, and clients are told the window's
// entries are named; a fetch that names nothing in the window says nothing.
func TestQueueEventWhenMetadataIsFetched(t *testing.T) {
	p := newQueueTestPlayer(t)
	p.publishSnapshot(&tracks.Snapshot{Current: provided(trackUri(0x02))})
	require.Equal(t, 1, queueEvents(p))

	api := make(chan ApiRequest)
	serveLoop(t, p, api)
	fetch := func(uri string) *ApiQueue {
		_, err := p.meta.fetchBatch(t.Context(), []string{uri})
		require.NoError(t, err)
		require.Eventually(t, func() bool { return len(p.meta.cached) == 0 }, 5*time.Second, time.Millisecond)

		req, wait := NewApiRequest(ApiRequestTypeGetQueue, nil)
		api <- req
		resp, err := wait(t.Context())
		require.NoError(t, err)
		return resp.(*ApiQueue)
	}

	require.NotNil(t, fetch(trackUri(0x02)).Track.Track)
	require.Equal(t, 2, queueEvents(p))
	fetch(trackUri(0x05))
	require.Equal(t, 2, queueEvents(p), "not in the window")
}

// A session that ends takes its play order with it, and clients are told.
func TestQueueEventWhenTheSessionEnds(t *testing.T) {
	quiet := newQueueTestPlayer(t)
	serveLoop(t, quiet, nil)()
	require.Zero(t, queueEvents(quiet), "nothing was listed")

	p := newQueueTestPlayer(t)
	p.publishSnapshot(&tracks.Snapshot{Current: provided(trackUri(0x01))})
	require.Equal(t, 1, queueEvents(p))
	serveLoop(t, p, nil)()
	require.Equal(t, 2, queueEvents(p))
}

// otherContextUris names n tracks of another context, none of them in the
// window.
func otherContextUris(base uint32, n int) []string {
	uris := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := make([]byte, 16)
		id[0] = 0xff
		binary.BigEndian.PutUint32(id[12:], base+uint32(i))
		uris = append(uris, librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeTrack, id).Uri())
	}
	return uris
}

// The window's entries stay the most recently used part of the metadata cache,
// so that sweeping or listing other contexts evicts around them: each wake
// marks them, and so does a listing after its own tracks.
func TestQueueKeepsItsEntriesNamed(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)
	p.publishSnapshot(&tracks.Snapshot{
		Current: provided(trackUri(0x02)),
		Prev:    []*connectpb.ProvidedTrack{provided(trackUri(0x01))},
		Next:    []*connectpb.ProvidedTrack{provided(trackUri(0x03))},
	})
	fetch := func(uris []string) {
		for len(uris) > 0 {
			batch := uris[:min(len(uris), maxMetaBatch)]
			uris = uris[len(batch):]
			_, err := p.meta.fetchBatch(t.Context(), batch)
			require.NoError(t, err)
			p.emitQueue() // as the loop does when the batch wakes it
		}
	}
	fetch(p.windowMetaUris())

	swept := otherContextUris(0, trackMetaCacheLimit)
	fetch(swept)
	_ = p.contextTracksResponse("spotify:playlist:37i9dQZF1DX8vwRmUsEIMT", swept[3:], true)
	fetch(otherContextUris(trackMetaCacheLimit, maxMetaBatch))

	queue := p.apiQueue()
	require.NotNil(t, queue.PrevTracks[0].Track)
	require.NotNil(t, queue.Track.Track)
	require.NotNil(t, queue.NextTracks[0].Track)
	require.Equal(t, 2, queueEvents(p), "named once, never unnamed")
}
