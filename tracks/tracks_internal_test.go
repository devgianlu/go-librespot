//go:build test_unit

package tracks

import (
	"context"
	"fmt"
	"io"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
)

const stationUri = "spotify:station:track:test"

func trackUri(i int) string { return fmt.Sprintf("spotify:track:%06d", i) }

type TrackListInternalSuite struct {
	suite.Suite

	resolver *MockContextResolver
	list     *List
}

func (suite *TrackListInternalSuite) SetupTest() {
	suite.resolver = NewMockContextResolver(suite.T())

	// Identity is read whenever a track is described or a message logged, which
	// is incidental to what these tests assert.
	suite.resolver.EXPECT().Type().Return(librespot.SpotifyIdTypeTrack).Maybe()
	suite.resolver.EXPECT().Uri().Return(stationUri).Maybe()

	suite.list = newTrackList(&librespot.NullLogger{}, suite.resolver)
}

// expectEndlessPages models a station: every page yields more tracks and there
// is always another page, exactly the shape that makes an unbounded seek run
// forever.
func (suite *TrackListInternalSuite) expectEndlessPages() {
	const perPage = 5

	suite.resolver.EXPECT().Page(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, idx int) ([]*connectpb.ContextTrack, error) {
			tracks := make([]*connectpb.ContextTrack, perPage)
			for i := range tracks {
				tracks[i] = &connectpb.ContextTrack{Uri: trackUri(idx*perPage + i)}
			}
			return tracks, nil
		})
}

// TestSeekStopsAtPageCap covers the core of issue #288: a station's pagination
// never reports an end, so a seek for a track it does not serve has to give up
// on its own rather than fetch pages until the caller's deadline expires.
func (suite *TrackListInternalSuite) TestSeekStopsAtPageCap() {
	suite.expectEndlessPages()

	err := suite.list.Seek(context.Background(), func(*connectpb.ContextTrack) bool { return false })
	suite.Error(err)
	suite.Contains(err.Error(), "gave up seeking")

	// Every page up to and including the cap is fetched, and not one more.
	suite.resolver.AssertNumberOfCalls(suite.T(), "Page", seekMaxPages+1)
	suite.resolver.AssertCalled(suite.T(), "Page", mock.Anything, seekMaxPages)
	suite.resolver.AssertNotCalled(suite.T(), "Page", mock.Anything, seekMaxPages+1)
}

// TestSeekFindsTrackWithinCap makes sure the bound does not get in the way of
// an ordinary seek.
func (suite *TrackListInternalSuite) TestSeekFindsTrackWithinCap() {
	suite.expectEndlessPages()

	target := &connectpb.ContextTrack{Uri: trackUri(42)}
	suite.NoError(suite.list.TrySeekTo(context.Background(), target))

	suite.Equal(trackUri(42), suite.list.CurrentTrack().Uri)
	suite.Equal(&connectpb.ContextIndex{Page: 8, Track: 2}, suite.list.Index())
}

// TestTrySeekToPlaysUnfoundTrack locks in the behaviour that matters to the
// user on a transfer: when the track cannot be located, it is played anyway
// instead of the context silently starting over on some other song.
func (suite *TrackListInternalSuite) TestTrySeekToPlaysUnfoundTrack() {
	suite.expectEndlessPages()

	target := &connectpb.ContextTrack{Uri: "spotify:track:notinthiscontext"}
	suite.NoError(suite.list.TrySeekTo(context.Background(), target))

	suite.Equal("spotify:track:notinthiscontext", suite.list.CurrentTrack().Uri)

	// The track is not part of the context, so it has no index in it.
	suite.Equal(&connectpb.ContextIndex{}, suite.list.Index())

	// Nothing precedes it, and playback carries on into the context from its
	// first track.
	suite.Empty(suite.list.PrevTracks())
	suite.True(suite.list.GoNext(context.Background()))
	suite.Equal(trackUri(0), suite.list.CurrentTrack().Uri)
}

// TestTrySeekToInjectsOnFetchFailure covers the failure actually seen in the
// wild: the walk dies part-way with a deadline exceeded. The requested track
// must still play.
func (suite *TrackListInternalSuite) TestTrySeekToInjectsOnFetchFailure() {
	suite.resolver.EXPECT().Page(mock.Anything, mock.Anything).Return(nil, context.DeadlineExceeded)

	target := &connectpb.ContextTrack{Uri: trackUri(7)}
	suite.NoError(suite.list.TrySeekTo(context.Background(), target))

	suite.Equal(trackUri(7), suite.list.CurrentTrack().Uri)
	suite.Equal(1, suite.list.tracks.len())

	// The injected track must not have consumed page 0: once the context is
	// reachable again, playback continues into it from the beginning.
	suite.Equal(-1, suite.list.tracks.list[suite.list.tracks.len()-1].pageIdx)
}

// TestTrySeekToOnFiniteContext checks the ordinary miss, where the context
// simply ends without holding the track.
func (suite *TrackListInternalSuite) TestTrySeekToOnFiniteContext() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).
		Return([]*connectpb.ContextTrack{{Uri: trackUri(0)}, {Uri: trackUri(1)}}, nil).Once()
	suite.resolver.EXPECT().Page(mock.Anything, 1).Return(nil, io.EOF).Once()

	target := &connectpb.ContextTrack{Uri: trackUri(99)}
	suite.NoError(suite.list.TrySeekTo(context.Background(), target))

	suite.Equal(trackUri(99), suite.list.CurrentTrack().Uri)
	suite.True(suite.list.GoNext(context.Background()))
	suite.Equal(trackUri(0), suite.list.CurrentTrack().Uri)
}

// TestShuffleStopsAtPageCap makes sure shuffling an endless context terminates
// too: it fetches "all" tracks up front, which has the same unbounded walk.
func (suite *TrackListInternalSuite) TestShuffleStopsAtPageCap() {
	suite.expectEndlessPages()

	suite.NoError(suite.list.TrySeekTo(context.Background(), &connectpb.ContextTrack{Uri: trackUri(0)}))
	suite.NoError(suite.list.ToggleShuffle(context.Background(), true))

	suite.resolver.AssertNumberOfCalls(suite.T(), "Page", seekMaxPages+1)
}

// playingContext sets the list up the way production always hands it to a
// jump: a finite context of n tracks with the cursor resting on the first one.
func (suite *TrackListInternalSuite) playingContext(n int) {
	tracks := make([]*connectpb.ContextTrack, n)
	for i := range tracks {
		tracks[i] = &connectpb.ContextTrack{Uri: trackUri(i), Uid: fmt.Sprintf("c%d", i)}
	}

	suite.resolver.EXPECT().Page(mock.Anything, 0).Return(tracks, nil).Maybe()
	suite.resolver.EXPECT().Page(mock.Anything, mock.Anything).Return(nil, io.EOF).Maybe()

	suite.Require().NoError(suite.list.TrySeekTo(context.Background(), tracks[0]))
}

func queued(uid, uri string) *connectpb.ContextTrack {
	return &connectpb.ContextTrack{Uri: uri, Uid: uid, Metadata: map[string]string{"is_queued": "true"}}
}

// TestSeekToContextTrackWhileQueuePlaying is issue #218: with a manually queued
// track playing, jumping to a track in the context moved the context cursor but
// left the queue in charge, so the queued track simply restarted.
func (suite *TrackListInternalSuite) TestSeekToContextTrackWhileQueuePlaying() {
	suite.playingContext(5)

	suite.list.AddToQueue(queued("q1", "spotify:track:queued1"))
	suite.list.SetPlayingQueue(true)
	suite.Require().Equal("spotify:track:queued1", suite.list.CurrentTrack().Uri)

	// Jump to a track from the context.
	suite.NoError(suite.list.TrySeekTo(context.Background(), &connectpb.ContextTrack{Uri: trackUri(3), Uid: "c3"}))

	suite.Equal(trackUri(3), suite.list.CurrentTrack().Uri)
	suite.Equal(&connectpb.ContextIndex{Page: 0, Track: 3}, suite.list.Index())

	// The queued track was jumped away from, so advancing carries on into the
	// context rather than playing it a second time.
	suite.True(suite.list.GoNext(context.Background()))
	suite.Equal(trackUri(4), suite.list.CurrentTrack().Uri)
}

// TestSeekToQueuedTrack covers the second half of the issue: jumping to another
// queued track must play it and consume the ones skipped over, rather than
// leave it sitting in the queue.
func (suite *TrackListInternalSuite) TestSeekToQueuedTrack() {
	suite.playingContext(5)

	suite.list.AddToQueue(queued("q1", "spotify:track:queued1"))
	suite.list.AddToQueue(queued("q2", "spotify:track:queued2"))
	suite.list.AddToQueue(queued("q3", "spotify:track:queued3"))
	suite.list.SetPlayingQueue(true)

	suite.NoError(suite.list.TrySeekTo(context.Background(), queued("q3", "spotify:track:queued3")))

	suite.Equal("spotify:track:queued3", suite.list.CurrentTrack().Uri)

	// q1 and q2 were skipped past and q3 is playing rather than pending, so
	// nothing queued is upcoming any more.
	next := suite.list.NextTracks(context.Background(), nil)
	suite.Require().NotEmpty(next)
	suite.Equal(trackUri(1), next[0].Uri)

	// Once it ends, playback continues into the context.
	suite.True(suite.list.GoNext(context.Background()))
	suite.Equal(trackUri(1), suite.list.CurrentTrack().Uri)
}

// TestSeekToContextTrackPrefersContextCopy guards the uid disambiguation: a
// song can sit in the queue and in the context at once, and clicking the
// context copy must not hijack the queued one.
func (suite *TrackListInternalSuite) TestSeekToContextTrackPrefersContextCopy() {
	suite.playingContext(5)

	suite.list.AddToQueue(queued("q1", trackUri(3)))

	suite.NoError(suite.list.TrySeekTo(context.Background(), &connectpb.ContextTrack{Uri: trackUri(3), Uid: "c3"}))

	suite.Equal(trackUri(3), suite.list.CurrentTrack().Uri)
	suite.Equal(&connectpb.ContextIndex{Page: 0, Track: 3}, suite.list.Index())

	// The queued copy was not consumed and still plays next.
	suite.True(suite.list.GoNext(context.Background()))
	suite.Equal(&connectpb.ContextIndex{}, suite.list.Index())
	suite.Equal(trackUri(3), suite.list.CurrentTrack().Uri)
}

// TestSeekToQueuedTrackWithoutUid covers the API path, which names a track by
// uri alone.
func (suite *TrackListInternalSuite) TestSeekToQueuedTrackWithoutUid() {
	suite.playingContext(5)

	suite.list.AddToQueue(queued("q1", "spotify:track:queued1"))
	suite.list.AddToQueue(queued("q2", "spotify:track:queued2"))

	suite.NoError(suite.list.TrySeekTo(context.Background(), &connectpb.ContextTrack{Uri: "spotify:track:queued2"}))

	suite.Equal("spotify:track:queued2", suite.list.CurrentTrack().Uri)
}

func TestTrackListInternalSuite(t *testing.T) {
	suite.Run(t, new(TrackListInternalSuite))
}
