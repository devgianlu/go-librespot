//go:build test_unit

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	extmetadatapb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/devgianlu/go-librespot/tracks"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func trackUri(b byte) string {
	return librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeTrack, gid(b)).Uri()
}

func episodeUri(b byte) string {
	return librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeEpisode, gid(b)).Uri()
}

// metaTrack is a track with every field the API response dereferences.
func metaTrack(id []byte, name string) *metadatapb.Track {
	return &metadatapb.Track{
		Gid:        id,
		Name:       proto.String(name),
		Duration:   proto.Int32(1000),
		Number:     proto.Int32(1),
		DiscNumber: proto.Int32(1),
		Album:      &metadatapb.Album{Gid: gid(0xa1), Name: proto.String("Album"), Date: &metadatapb.Date{Year: proto.Int32(2000)}},
		Artist:     []*metadatapb.Artist{{Gid: gid(0xa2), Name: proto.String("Artist")}},
	}
}

func mediaFixture(name string) *librespot.Media {
	return librespot.NewMediaFromTrack(metaTrack(gid(0x01), name))
}

type fetchFunc = func(context.Context, *extmetadatapb.BatchedEntityRequest) (*extmetadatapb.BatchedExtensionResponse, error)

// echoMetadata answers an extended-metadata request with a track for every
// entity it asked about, the way the backend does for tracks that exist.
func echoMetadata(_ context.Context, req *extmetadatapb.BatchedEntityRequest) (*extmetadatapb.BatchedExtensionResponse, error) {
	array := &extmetadatapb.EntityExtensionDataArray{ExtensionKind: extmetadatapb.ExtensionKind_TRACK_V4}
	for _, entity := range req.EntityRequest {
		id, err := librespot.SpotifyIdFromUri(entity.EntityUri)
		if err != nil {
			return nil, err
		}

		data, err := anypb.New(metaTrack(id.Id(), entity.EntityUri))
		if err != nil {
			return nil, err
		}

		array.ExtensionData = append(array.ExtensionData, &extmetadatapb.EntityExtensionData{
			Header:        &extmetadatapb.EntityExtensionDataHeader{StatusCode: 200},
			EntityUri:     entity.EntityUri,
			ExtensionData: data,
		})
	}

	return &extmetadatapb.BatchedExtensionResponse{ExtendedMetadata: []*extmetadatapb.EntityExtensionDataArray{array}}, nil
}

// recordingFetch wraps fetch so the uris of every request can be read back.
func recordingFetch(fetch fetchFunc) (fetchFunc, func() [][]string) {
	var mu sync.Mutex
	var calls [][]string

	return func(ctx context.Context, req *extmetadatapb.BatchedEntityRequest) (*extmetadatapb.BatchedExtensionResponse, error) {
			var uris []string
			for _, entity := range req.EntityRequest {
				uris = append(uris, entity.EntityUri)
			}

			mu.Lock()
			calls = append(calls, uris)
			mu.Unlock()

			return fetch(ctx, req)
		}, func() [][]string {
			mu.Lock()
			defer mu.Unlock()
			return append([][]string(nil), calls...)
		}
}

// newMetaTestPlayer builds a player with the metadata cache enabled, whose
// network seams are fetch and resolve. The Run loop is not started: the tests
// drive the timer fire and the handler by hand.
func newMetaTestPlayer(t *testing.T, fetch fetchFunc, resolve func(context.Context, string) (tracks.ContextResolver, error)) *AppPlayer {
	t.Helper()

	app := &App{
		log:          &librespot.NullLogger{},
		cfg:          &Config{Metadata: MetadataConfig{Enabled: true}},
		metaCache:    newTrackMetaCache(),
		contextLists: newContextListCache(),
	}

	p := &AppPlayer{
		app:               app,
		ctx:               t.Context(),
		meta:              &metaFetcher{log: app.log, cache: app.metaCache, fetch: fetch, resolve: resolve},
		metaPrefetchTimer: time.NewTimer(math.MaxInt64),
		prodInfo:          &ProductInfo{},
		state:             &State{},
	}
	p.metaPrefetchTimer.Stop()
	p.state.reset()

	return p
}

func provided(uri string) *connectpb.ProvidedTrack {
	return &connectpb.ProvidedTrack{Uri: uri}
}

func TestTrackMetaCachePutGet(t *testing.T) {
	c := newTrackMetaCache()

	require.Nil(t, c.get("spotify:track:a"), "miss on an empty cache")

	m := mediaFixture("A")
	c.put("spotify:track:a", m)
	require.Same(t, m, c.get("spotify:track:a"))

	// nil / empty are ignored
	c.put("", m)
	c.put("spotify:track:b", nil)
	require.Nil(t, c.get("spotify:track:b"), "nil media must not be cached")
}

// A loaded stream is known under the uri it was asked for and under the uri of
// what came back: the state window names it by the former, the media by the
// latter, and either may be looked up.
func TestTrackMetaCachePutStreamRelinked(t *testing.T) {
	c := newTrackMetaCache()

	m := mediaFixture("A")
	c.putStream(trackUri(0x02), m)

	require.Same(t, m, c.get(trackUri(0x02)))
	require.Same(t, m, c.get(m.Id().Uri()))
}

func TestTrackMetaCacheMissing(t *testing.T) {
	c := newTrackMetaCache()
	c.put("spotify:track:a", mediaFixture("A"))

	missing := c.missing([]string{"spotify:track:a", "spotify:track:b", "spotify:track:b", "", "spotify:track:c"})
	require.Equal(t, []string{"spotify:track:b", "spotify:track:c"}, missing, "deduplicated misses, in order")
}

func TestTrackMetaCacheEviction(t *testing.T) {
	c := newTrackMetaCache()

	for i := 0; i <= trackMetaCacheLimit; i++ {
		c.put(fmt.Sprintf("spotify:track:%d", i), mediaFixture("x"))
	}

	require.Nil(t, c.get("spotify:track:0"), "the oldest entry is evicted")
	require.NotNil(t, c.get(fmt.Sprintf("spotify:track:%d", trackMetaCacheLimit)), "the newest entry is kept")
	require.Equal(t, trackMetaCacheLimit, c.lru.Len())
}

// A client polling a filling sweep asks for the same context every second or
// two; enumerating pages over the network, so those polls must be served from
// memory rather than re-paging the whole playlist each time.
func TestContextListCacheServesRepeatLookups(t *testing.T) {
	c := newContextListCache()

	_, ok := c.get("spotify:playlist:a")
	require.False(t, ok, "miss on an empty cache")

	c.put("spotify:playlist:a", []string{"spotify:track:1", "spotify:track:2"})

	uris, ok := c.get("spotify:playlist:a")
	require.True(t, ok)
	require.Len(t, uris, 2)
}

// The listing carries no revision, so a stale entry is the only way a client
// could miss an edit; the TTL bounds how long that can last.
func TestContextListCacheExpires(t *testing.T) {
	c := newContextListCache()

	now := time.Now()
	c.now = func() time.Time { return now }

	c.put("spotify:playlist:a", []string{"spotify:track:1"})

	now = now.Add(contextListTTL)
	_, ok := c.get("spotify:playlist:a")
	require.True(t, ok, "an entry exactly as old as the TTL is still served")

	now = now.Add(time.Second)
	_, ok = c.get("spotify:playlist:a")
	require.False(t, ok, "an entry older than the TTL is a miss")
	require.True(t, c.beginFetch("spotify:playlist:a"), "an expired listing is re-enumerated")
}

func TestContextListCacheEviction(t *testing.T) {
	c := newContextListCache()

	for i := 0; i < contextListCacheLimit+3; i++ {
		c.put(fmt.Sprintf("spotify:playlist:%d", i), []string{"spotify:track:1"})
	}

	require.Equal(t, contextListCacheLimit, c.lru.Len())

	_, ok := c.get("spotify:playlist:0")
	require.False(t, ok, "the oldest context is evicted")

	_, ok = c.get(fmt.Sprintf("spotify:playlist:%d", contextListCacheLimit+2))
	require.True(t, ok, "the newest context is kept")
}

// A client polls this endpoint while the enumeration runs; each poll must find
// the job already claimed rather than start another one, and a cached listing
// needs no enumeration at all.
func TestContextListCacheSingleFlightsEnumeration(t *testing.T) {
	c := newContextListCache()

	require.True(t, c.beginFetch("spotify:playlist:a"), "the first caller claims the enumeration")
	require.False(t, c.beginFetch("spotify:playlist:a"), "a concurrent caller is turned away")

	c.endFetch("spotify:playlist:a")
	require.True(t, c.beginFetch("spotify:playlist:a"), "claimable again once the previous one finished")
	c.endFetch("spotify:playlist:a")

	c.put("spotify:playlist:a", []string{"spotify:track:1"})
	require.False(t, c.beginFetch("spotify:playlist:a"), "a cached listing needs no enumeration")
}

// The entire feature is opt-in: with metadata.enabled false the caches are
// never constructed, and every path must treat that as a no-op — no
// goroutines, no requests, no panics.
func TestMetadataDisabledIsNoop(t *testing.T) {
	var tc *trackMetaCache
	require.Nil(t, tc.get("spotify:track:a"))
	tc.put("spotify:track:a", mediaFixture("a"))
	tc.putStream("spotify:track:a", mediaFixture("a"))
	require.Nil(t, tc.missing([]string{"spotify:track:a"}), "a nil cache has nothing fetchable")

	var cl *contextListCache
	_, ok := cl.get("spotify:playlist:a")
	require.False(t, ok)
	require.False(t, cl.beginFetch("spotify:playlist:a"), "a nil cache never claims an enumeration")

	// A disabled player: no timer is armed and no session is touched.
	p := &AppPlayer{app: &App{cfg: &Config{}}, state: &State{}}
	p.state.reset()
	p.publishSnapshot(&tracks.Snapshot{Current: provided(trackUri(0x01)), Next: []*connectpb.ProvidedTrack{provided(trackUri(0x02))}})
	p.scheduleContextEnumerate("spotify:playlist:a")
	p.scheduleMetaSweep([]string{"spotify:track:a"}, "test")
	p.scheduleContextMetaPrefetch("spotify:playlist:a")
	require.Empty(t, p.lastFullMetaContext, "no sweep bookkeeping while disabled")

	_, err := p.handleApiRequest(ApiRequest{Type: ApiRequestTypeContextTracks, Data: ApiRequestDataContextTracks{Uri: "spotify:playlist:a"}})
	require.ErrorIs(t, err, ErrNotFound)
}

// The whole-context sweep is a second opt-in on top of the cache itself.
func TestContextSweepRequiresItsOwnOptIn(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)

	p.scheduleContextMetaPrefetch("spotify:playlist:a")
	require.Empty(t, p.lastFullMetaContext, "no context sweep without metadata.context_sweep")
}

func TestMetaMaxTracksDefault(t *testing.T) {
	p := &AppPlayer{app: &App{cfg: &Config{}}}
	require.Equal(t, defaultMetaMaxTracks, p.metaMaxTracks())

	p.app.cfg.Metadata.MaxTracks = 100
	require.Equal(t, 100, p.metaMaxTracks())
}

// Each uri resolves under its own extended-metadata kind: a TRACK_V4 query
// for an episode returns nothing, which is why shows used to enumerate to an
// empty listing.
func TestMetaExtensionKind(t *testing.T) {
	cases := []struct {
		uri  string
		kind extmetadatapb.ExtensionKind
		ok   bool
	}{
		{"spotify:track:4cOdK2wGLETKBW3PvgPWqT", extmetadatapb.ExtensionKind_TRACK_V4, true},
		{"spotify:episode:4rOoJ6Egrf8K2IrywzwOMk", extmetadatapb.ExtensionKind_EPISODE_V4, true},
		{"spotify:local:a:b:c:1", 0, false},
		{"spotify:artist:0OdUWJ0sBjDrqHygGUXeCF", 0, false},
		{"", 0, false},
	}

	for _, tc := range cases {
		kind, ok := metaExtensionKind(tc.uri)
		require.Equal(t, tc.ok, ok, tc.uri)
		require.Equal(t, tc.kind, kind, tc.uri)
	}
}

// The listing accepts any context whose item type can be classified —
// playlist, album, show, a user's Liked Songs — and rejects what cannot be:
// non-context entities and the malformed. Classifiable-but-nonexistent uris
// are the resolver's to reject.
func TestIsListableContextUri(t *testing.T) {
	cases := []struct {
		uri string
		ok  bool
	}{
		{"spotify:playlist:0hgSZmY9xhzx51hlLB2arI", true},
		{"spotify:album:4rxfprnLYz3592ZGaeqcON", true},
		{"spotify:show:4rOoJ6Egrf8K2IrywzwOMk", true},
		{"spotify:user:someone:collection", true},
		{"spotify:user:someone:collection:your-episodes", true},
		{"spotify:concert:3Ph3fvw2WeVfvBBjT13yeN", false},
		{"not a uri", false},
		{"", false},
	}

	for _, tc := range cases {
		require.Equal(t, tc.ok, isListableContextUri(tc.uri), tc.uri)
	}
}

// A batched response is unpacked under each entity's own kind, and entries the
// backend refused are left out rather than cached as nothing.
func TestMetaFetcherFetchBatch(t *testing.T) {
	cache := newTrackMetaCache()
	f := &metaFetcher{log: &librespot.NullLogger{}, cache: cache, fetch: func(ctx context.Context, req *extmetadatapb.BatchedEntityRequest) (*extmetadatapb.BatchedExtensionResponse, error) {
		require.Len(t, req.EntityRequest, 2, "a local file has no metadata to ask for")
		require.Equal(t, extmetadatapb.ExtensionKind_TRACK_V4, req.EntityRequest[0].Query[0].ExtensionKind)
		require.Equal(t, extmetadatapb.ExtensionKind_EPISODE_V4, req.EntityRequest[1].Query[0].ExtensionKind)

		track, err := anypb.New(metaTrack(gid(0x01), "one"))
		require.NoError(t, err)

		return &extmetadatapb.BatchedExtensionResponse{ExtendedMetadata: []*extmetadatapb.EntityExtensionDataArray{
			{ExtensionKind: extmetadatapb.ExtensionKind_TRACK_V4, ExtensionData: []*extmetadatapb.EntityExtensionData{
				{Header: &extmetadatapb.EntityExtensionDataHeader{StatusCode: 200}, EntityUri: trackUri(0x01), ExtensionData: track},
			}},
			{ExtensionKind: extmetadatapb.ExtensionKind_EPISODE_V4, ExtensionData: []*extmetadatapb.EntityExtensionData{
				{Header: &extmetadatapb.EntityExtensionDataHeader{StatusCode: 404}, EntityUri: episodeUri(0x02)},
			}},
		}}, nil
	}}

	cached, err := f.fetchBatch(t.Context(), []string{trackUri(0x01), episodeUri(0x02), "spotify:local:a:b:c:1"})
	require.NoError(t, err)
	require.Equal(t, 1, cached)
	require.Equal(t, "one", cache.get(trackUri(0x01)).Name())
	require.Nil(t, cache.get(episodeUri(0x02)), "a refused entry is not cached")
}

// A sweep that fails stops: retrying against a backend that just refused is how
// a daemon earns a rate limit. And a sweep that is cancelled between batches
// must not sit out the pause first.
func TestMetaFetcherSweepAbortsOnErrorAndCancel(t *testing.T) {
	var uris []string
	for i := 0; i < 3*maxMetaBatch; i++ {
		uris = append(uris, fmt.Sprintf("spotify:track:%d", i))
	}

	var calls int
	f := &metaFetcher{log: &librespot.NullLogger{}, cache: newTrackMetaCache(), fetch: func(context.Context, *extmetadatapb.BatchedEntityRequest) (*extmetadatapb.BatchedExtensionResponse, error) {
		calls++
		return nil, errors.New("boom")
	}}
	f.sweep(t.Context(), uris, "failing")
	require.Equal(t, 1, calls, "the first failure ends the sweep")

	ctx, cancel := context.WithCancel(t.Context())
	calls = 0
	f.fetch = func(context.Context, *extmetadatapb.BatchedEntityRequest) (*extmetadatapb.BatchedExtensionResponse, error) {
		calls++
		cancel()
		return &extmetadatapb.BatchedExtensionResponse{}, nil
	}

	started := time.Now()
	f.sweep(ctx, uris, "cancelled")
	require.Equal(t, 1, calls)
	require.Less(t, time.Since(started), metaSweepBatchPause, "cancellation must not wait out the batch pause")
}

// Only the newest waiting sweep matters: by the time a third context arrives
// the user has moved on from the second.
func TestMetaSweepQueueKeepsOnlyTheNewestWaiting(t *testing.T) {
	var q metaSweepQueue

	require.True(t, q.enqueue(metaSweepJob{label: "a"}), "the first job starts the worker")
	job, ok := q.next()
	require.True(t, ok)
	require.Equal(t, "a", job.label)

	require.False(t, q.enqueue(metaSweepJob{label: "b"}), "a running worker picks the next one up")
	require.False(t, q.enqueue(metaSweepJob{label: "c"}))

	job, ok = q.next()
	require.True(t, ok)
	require.Equal(t, "c", job.label, "b was superseded")

	_, ok = q.next()
	require.False(t, ok, "the worker stops when nothing waits")
	require.True(t, q.enqueue(metaSweepJob{label: "d"}), "and is started again by the next job")
}

func contextTrack(uri string, id []byte) *connectpb.ContextTrack {
	return &connectpb.ContextTrack{Uri: uri, Gid: id}
}

// Enumeration walks the context page by page until the resolver reports the
// end, in the context's own order.
func TestEnumerateContextTracksStopsAtEOF(t *testing.T) {
	resolver := tracks.NewMockContextResolver(t)
	resolver.EXPECT().Type().Return(librespot.SpotifyIdTypeTrack)
	resolver.EXPECT().Page(mock.Anything, 0).Return([]*connectpb.ContextTrack{contextTrack(trackUri(0x01), nil), contextTrack(trackUri(0x02), nil)}, nil)
	resolver.EXPECT().Page(mock.Anything, 1).Return([]*connectpb.ContextTrack{contextTrack(trackUri(0x03), nil)}, nil)
	resolver.EXPECT().Page(mock.Anything, 2).Return(nil, io.EOF)

	uris, err := enumerateContextTracks(t.Context(), resolver, defaultMetaMaxTracks)
	require.NoError(t, err)
	require.Equal(t, []string{trackUri(0x01), trackUri(0x02), trackUri(0x03)}, uris)
}

// A failed page is reported rather than papered over with a short listing.
func TestEnumerateContextTracksReportsPageErrors(t *testing.T) {
	boom := errors.New("boom")

	resolver := tracks.NewMockContextResolver(t)
	resolver.EXPECT().Type().Return(librespot.SpotifyIdTypeTrack)
	resolver.EXPECT().Page(mock.Anything, 0).Return(nil, boom)

	_, err := enumerateContextTracks(t.Context(), resolver, defaultMetaMaxTracks)
	require.ErrorIs(t, err, boom)
}

// A generated context hands out pages forever; the enumeration gives up where
// a seek would.
func TestEnumerateContextTracksCapsPages(t *testing.T) {
	resolver := tracks.NewMockContextResolver(t)
	resolver.EXPECT().Type().Return(librespot.SpotifyIdTypeTrack)
	resolver.EXPECT().Page(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, idx int) ([]*connectpb.ContextTrack, error) {
		require.Less(t, idx, maxContextPages)
		return []*connectpb.ContextTrack{contextTrack("", gid(byte(idx)))}, nil
	})

	uris, err := enumerateContextTracks(t.Context(), resolver, 10*maxContextPages)
	require.NoError(t, err)
	require.Len(t, uris, maxContextPages)
}

// Long contexts are truncated to metadata.max_tracks without fetching the
// pages beyond it.
func TestEnumerateContextTracksCapsTracks(t *testing.T) {
	var page []*connectpb.ContextTrack
	for i := range 10 {
		page = append(page, contextTrack("", gid(byte(i))))
	}

	resolver := tracks.NewMockContextResolver(t)
	resolver.EXPECT().Type().Return(librespot.SpotifyIdTypeTrack)
	resolver.EXPECT().Page(mock.Anything, 0).Return(page, nil)

	uris, err := enumerateContextTracks(t.Context(), resolver, 3)
	require.NoError(t, err)
	require.Equal(t, []string{trackUri(0), trackUri(1), trackUri(2)}, uris)
}

// Context tracks often carry only a gid, which says nothing about what it
// identifies: the context type decides. Entries without metadata to fetch —
// local files, malformed items — are left out.
func TestEnumerateContextTracksDerivesUris(t *testing.T) {
	items := []*connectpb.ContextTrack{
		contextTrack("", gid(0x01)),
		contextTrack(episodeUri(0x02), nil),
		contextTrack("spotify:local:a:b:c:1", nil),
		contextTrack("", []byte{0x01, 0x02}),
	}

	trackCtx := tracks.NewMockContextResolver(t)
	trackCtx.EXPECT().Type().Return(librespot.SpotifyIdTypeTrack)
	trackCtx.EXPECT().Page(mock.Anything, 0).Return(items, nil)
	trackCtx.EXPECT().Page(mock.Anything, 1).Return(nil, io.EOF)

	uris, err := enumerateContextTracks(t.Context(), trackCtx, defaultMetaMaxTracks)
	require.NoError(t, err)
	require.Equal(t, []string{trackUri(0x01), episodeUri(0x02)}, uris)

	showCtx := tracks.NewMockContextResolver(t)
	showCtx.EXPECT().Type().Return(librespot.SpotifyIdTypeEpisode)
	showCtx.EXPECT().Page(mock.Anything, 0).Return(items, nil)
	showCtx.EXPECT().Page(mock.Anything, 1).Return(nil, io.EOF)

	uris, err = enumerateContextTracks(t.Context(), showCtx, defaultMetaMaxTracks)
	require.NoError(t, err)
	require.Equal(t, []string{episodeUri(0x01), episodeUri(0x02)}, uris, "a gid in a show is an episode")
}

func waitTimer(t *testing.T, timer *time.Timer, msg string) {
	t.Helper()

	select {
	case <-timer.C:
	case <-time.After(3 * metaPrefetchDelay):
		t.Fatal(msg)
	}
}

// A burst of skips publishes a snapshot per press. Fetching per press would be
// a request per button: the window is fetched once, for wherever the burst
// landed, and a fire while a fetch is in flight re-arms instead of doubling up.
func TestMetaPrefetchCoalescesWindowChanges(t *testing.T) {
	release := make(chan struct{})
	fetch, calls := recordingFetch(func(ctx context.Context, req *extmetadatapb.BatchedEntityRequest) (*extmetadatapb.BatchedExtensionResponse, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return echoMetadata(ctx, req)
	})
	p := newMetaTestPlayer(t, fetch, nil)

	var last *tracks.Snapshot
	for i := byte(1); i <= 5; i++ {
		last = &tracks.Snapshot{
			Current: provided(trackUri(i)),
			Prev:    []*connectpb.ProvidedTrack{provided(trackUri(i - 1))},
			Next:    []*connectpb.ProvidedTrack{provided(trackUri(i + 1)), provided("spotify:local:a:b:c:1")},
		}
		p.publishSnapshot(last)
	}
	require.Empty(t, calls(), "nothing is fetched per press")

	waitTimer(t, p.metaPrefetchTimer, "the burst did not arm the timer")
	p.prefetchWindowMetadata()
	require.Eventually(t, func() bool { return len(calls()) == 1 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{trackUri(5), trackUri(4), trackUri(6)}, calls()[0], "only the window the burst landed on, minus what has no metadata")

	select {
	case <-p.metaPrefetchTimer.C:
		t.Fatal("the burst armed the timer more than once")
	default:
	}

	// A fire while the fetch is in flight neither doubles it up nor is lost.
	p.prefetchWindowMetadata()
	require.Len(t, calls(), 1)
	waitTimer(t, p.metaPrefetchTimer, "a fire during a fetch must re-arm")

	close(release)
	require.Eventually(t, func() bool { return p.meta.cache.get(trackUri(6)) != nil }, 2*time.Second, 10*time.Millisecond)

	// Everything landed, so the re-armed fire has nothing left to ask for.
	p.prefetchWindowMetadata()
	require.Len(t, calls(), 1)
}

// The listing never waits on the network: the first call starts the
// enumeration and answers not-ready, later calls answer from the caches as the
// enumeration and then the sweep fill them.
func TestContextTracksHandler(t *testing.T) {
	const playlist = "spotify:playlist:0hgSZmY9xhzx51hlLB2arI"
	want := []string{trackUri(0x01), trackUri(0x02), trackUri(0x03)}

	resolver := tracks.NewMockContextResolver(t)
	resolver.EXPECT().Type().Return(librespot.SpotifyIdTypeTrack)
	resolver.EXPECT().Page(mock.Anything, 0).Return([]*connectpb.ContextTrack{
		contextTrack(want[0], nil), contextTrack(want[1], nil), contextTrack(want[2], nil),
	}, nil)
	resolver.EXPECT().Page(mock.Anything, 1).Return(nil, io.EOF)

	var resolved int
	fetch, calls := recordingFetch(echoMetadata)
	p := newMetaTestPlayer(t, fetch, func(_ context.Context, uri string) (tracks.ContextResolver, error) {
		require.Equal(t, playlist, uri)
		resolved++
		return resolver, nil
	})

	list := func(uri string) (*ApiContextTracks, error) {
		data, err := p.handleApiRequest(ApiRequest{Type: ApiRequestTypeContextTracks, Data: ApiRequestDataContextTracks{Uri: uri}})
		if err != nil {
			return nil, err
		}
		return data.(*ApiContextTracks), nil
	}

	_, err := list("spotify:concert:3Ph3fvw2WeVfvBBjT13yeN")
	require.ErrorIs(t, err, ErrBadRequest)

	resp, err := list(playlist)
	require.NoError(t, err)
	require.False(t, resp.Ready)
	require.Zero(t, resp.Length)
	require.Empty(t, resp.Tracks)

	require.Eventually(t, func() bool {
		resp, err := list(playlist)
		require.NoError(t, err)
		return resp.Ready
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, 1, resolved, "polling while enumerating must not enumerate again")

	require.Eventually(t, func() bool {
		resp, err := list(playlist)
		require.NoError(t, err)
		return resp.Cached == resp.Length
	}, 2*time.Second, 10*time.Millisecond, "the sweep fills the listing in")

	resp, err = list(playlist)
	require.NoError(t, err)
	require.Equal(t, 3, resp.Length)
	for i, entry := range resp.Tracks {
		require.Equal(t, want[i], entry.Uri)
		require.NotNil(t, entry.Track)
		require.Equal(t, want[i], entry.Track.Name, "rendered from the metadata that was swept")
	}

	require.Len(t, calls(), 1, "one sweep, one batch")
	require.Equal(t, 1, resolved)
}

// The status response names the upcoming track only once its metadata is
// known, so it costs nothing while the cache is cold.
func TestStatusNextTrack(t *testing.T) {
	disabled := &AppPlayer{app: &App{cfg: &Config{}}, prodInfo: &ProductInfo{}, state: &State{}}
	disabled.state.reset()
	disabled.state.player.NextTracks = []*connectpb.ProvidedTrack{provided(trackUri(0x02))}
	require.Nil(t, disabled.apiNextTrack())

	p := newMetaTestPlayer(t, echoMetadata, nil)
	require.Nil(t, p.apiNextTrack(), "nothing upcoming")

	p.state.player.NextTracks = []*connectpb.ProvidedTrack{provided(trackUri(0x02))}
	require.Nil(t, p.apiNextTrack(), "upcoming, but not cached yet")

	p.app.metaCache.put(trackUri(0x02), librespot.NewMediaFromTrack(metaTrack(gid(0x02), "next")))
	next := p.apiNextTrack()
	require.NotNil(t, next)
	require.Equal(t, "next", next.Name)
	require.Equal(t, trackUri(0x02), next.Uri)
}
