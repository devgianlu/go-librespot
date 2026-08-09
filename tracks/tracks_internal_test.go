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

func TestTrackListInternalSuite(t *testing.T) {
	suite.Run(t, new(TrackListInternalSuite))
}
