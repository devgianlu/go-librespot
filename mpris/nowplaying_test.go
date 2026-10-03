//go:build test_unit

package mpris

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNowPlayingInfoFromTrack(t *testing.T) {
	uri := "spotify:track:abc"
	media := librespot.NewMediaFromTrack(&metadata.Track{
		Gid:      make([]byte, 16),
		Name:     proto.String("Spooky"),
		Duration: proto.Int32(155000),
		Artist:   []*metadata.Artist{{Name: proto.String("Dusty Springfield")}, {Name: proto.String("Someone")}},
		Album: &metadata.Album{
			Name:       proto.String("Lock, Stock"),
			CoverGroup: &metadata.ImageGroup{Image: []*metadata.Image{{FileId: []byte{0x01}}, {FileId: []byte{0xab, 0xcd}}}},
		},
	})

	info := nowPlayingInfoFrom(MediaState{PlaybackStatus: Playing, PositionMs: 1500, Uri: &uri, Media: media})
	require.Equal(t, nowPlayingInfo{
		key:         uri,
		title:       "Spooky",
		artist:      "Dusty Springfield, Someone",
		album:       "Lock, Stock",
		artUrl:      "https://i.scdn.co/image/abcd",
		durationSec: 155,
		positionSec: 1.5,
		state:       nowPlayingPlaying,
	}, info, "the largest cover is used, like MPRIS")
}

func TestNowPlayingInfoFromEpisode(t *testing.T) {
	media := librespot.NewMediaFromEpisode(&metadata.Episode{
		Gid:      make([]byte, 16),
		Name:     proto.String("Episode 1"),
		Duration: proto.Int32(60000),
		Show:     &metadata.Show{Name: proto.String("The Show")},
	})

	info := nowPlayingInfoFrom(MediaState{PlaybackStatus: Paused, Media: media})
	require.Equal(t, "Episode 1", info.title)
	require.Equal(t, "The Show", info.artist)
	require.Equal(t, "The Show", info.album)
	require.Equal(t, nowPlayingPaused, info.state)
	require.Empty(t, info.artUrl)
}

func TestNowPlayingInfoWithoutMediaIsStopped(t *testing.T) {
	info := nowPlayingInfoFrom(MediaState{PlaybackStatus: Playing, PositionMs: 5000})
	require.Equal(t, nowPlayingInfo{state: nowPlayingStopped}, info)
}

// At the end of a context the player stops with the last track still loaded.
// The widget must be cleared all the same, not keep showing that track.
func TestNowPlayingInfoStoppedIsEmpty(t *testing.T) {
	uri := "spotify:track:abc"
	media := librespot.NewMediaFromTrack(&metadata.Track{Gid: make([]byte, 16), Name: proto.String("Spooky")})

	info := nowPlayingInfoFrom(MediaState{PlaybackStatus: Stopped, PositionMs: 5000, Uri: &uri, Media: media})
	require.Equal(t, nowPlayingInfo{state: nowPlayingStopped}, info)
}

func TestNowPlayingCommands(t *testing.T) {
	for command, want := range map[nowPlayingCommand]MediaPlayer2PlayerCommandType{
		nowPlayingTogglePlayPause: MediaPlayer2PlayerCommandTypePlayPause,
		nowPlayingPlay:            MediaPlayer2PlayerCommandTypePlay,
		nowPlayingPause:           MediaPlayer2PlayerCommandTypePause,
		nowPlayingNext:            MediaPlayer2PlayerCommandTypeNext,
		nowPlayingPrevious:        MediaPlayer2PlayerCommandTypePrevious,
		nowPlayingStop:            MediaPlayer2PlayerCommandTypeStop,
	} {
		cmd, ok := command.playerCommand(0)
		require.True(t, ok, command.String())
		require.Equal(t, want, cmd.Type, command.String())

		cmd.Reply(MediaPlayer2PlayerCommandResponse{}) // must not block: nobody waits for it
	}

	seek, ok := nowPlayingSeek.playerCommand(12.5)
	require.True(t, ok)
	require.Equal(t, MediaPlayer2PlayerCommandTypeSetPosition, seek.Type)
	require.Equal(t, MediaPlayer2CommandSetPositionPayload{PositionUs: 12_500_000}, seek.Argument,
		"an absolute position without a track path, so the player does not check the uri")

	_, ok = nowPlayingCommand(99).playerCommand(0)
	require.False(t, ok)
	require.Equal(t, "unknown", nowPlayingCommand(99).String())
}

func TestDownloadArtworkRejectsAnOversizedCover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := 10
		if r.URL.Path == "/large" {
			size = 11
		}
		_, _ = w.Write([]byte(strings.Repeat("x", size)))
	}))
	t.Cleanup(srv.Close)

	data, err := downloadArtwork(t.Context(), srv.Client(), srv.URL+"/fits", 10)
	require.NoError(t, err)
	require.Len(t, data, 10, "a cover of exactly the limit is fine")

	_, err = downloadArtwork(t.Context(), srv.Client(), srv.URL+"/large", 10)
	require.ErrorIs(t, err, errArtworkTooLarge, "not cut off and handed on")
}

// testArtwork drives an artworkLoader whose downloads are answered by hand.
type testArtwork struct {
	loader    *artworkLoader
	requests  chan artworkRequest
	delivered chan string
}

type artworkRequest struct {
	ctx    context.Context
	url    string
	answer chan error
}

func newTestArtwork() *testArtwork {
	a := &testArtwork{requests: make(chan artworkRequest, 8), delivered: make(chan string, 8)}
	a.loader = &artworkLoader{
		download: func(ctx context.Context, url string) ([]byte, error) {
			req := artworkRequest{ctx: ctx, url: url, answer: make(chan error, 1)}
			a.requests <- req
			select {
			case err := <-req.answer:
				return []byte(url), err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		deliver: func(key string, data []byte) { a.delivered <- key + "=" + string(data) },
		failed:  func(string, error) {},
	}
	return a
}

func (a *testArtwork) nextRequest(t *testing.T) artworkRequest {
	t.Helper()
	select {
	case req := <-a.requests:
		return req
	case <-time.After(time.Second):
		t.Fatal("no download started")
		return artworkRequest{}
	}
}

func (a *testArtwork) noRequest(t *testing.T) {
	t.Helper()
	select {
	case req := <-a.requests:
		t.Fatalf("unexpected download of %s", req.url)
	case <-time.After(20 * time.Millisecond):
	}
}

func (a *testArtwork) nextDelivery(t *testing.T) string {
	t.Helper()
	select {
	case d := <-a.delivered:
		return d
	case <-time.After(time.Second):
		t.Fatal("no cover delivered")
		return ""
	}
}

// waitIdle waits until the loader has dealt with a finished download.
func (a *testArtwork) waitIdle(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		a.loader.mu.Lock()
		defer a.loader.mu.Unlock()
		return a.loader.fetching == nil
	}, time.Second, time.Millisecond)
}

func TestArtworkLoaderFetchesOncePerTrack(t *testing.T) {
	a := newTestArtwork()
	a.loader.update("A", "a.jpg")
	a.loader.update("A", "a.jpg") // pause, resume, seek: no second download
	a.nextRequest(t).answer <- nil
	require.Equal(t, "A=a.jpg", a.nextDelivery(t))

	a.loader.update("A", "a.jpg")
	a.noRequest(t)
}

// The widget drops its cover on every key change, so coming back to a track
// needs its cover again, even when the key in between had no cover at all.
func TestArtworkLoaderRefetchesAfterAKeyWithoutCover(t *testing.T) {
	a := newTestArtwork()
	a.loader.update("A", "a.jpg")
	a.nextRequest(t).answer <- nil
	a.nextDelivery(t)

	a.loader.update("", "") // stopped
	a.loader.update("A", "a.jpg")
	a.nextRequest(t).answer <- nil
	require.Equal(t, "A=a.jpg", a.nextDelivery(t))
}

func TestArtworkLoaderRetriesAFailedDownload(t *testing.T) {
	a := newTestArtwork()
	a.loader.update("A", "a.jpg")
	a.nextRequest(t).answer <- errors.New("network down")
	a.waitIdle(t)

	a.loader.update("A", "a.jpg")
	a.nextRequest(t).answer <- nil
	require.Equal(t, "A=a.jpg", a.nextDelivery(t))
}

func TestArtworkLoaderCancelsTheDownloadForAnOldTrack(t *testing.T) {
	a := newTestArtwork()
	a.loader.update("A", "a.jpg")
	old := a.nextRequest(t)

	a.loader.update("B", "b.jpg")
	select {
	case <-old.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the download for A was not cancelled")
	}
	a.nextRequest(t).answer <- nil
	require.Equal(t, "B=b.jpg", a.nextDelivery(t))
}

func TestArtworkLoaderStopsWhenClosed(t *testing.T) {
	a := newTestArtwork()
	a.loader.update("A", "a.jpg")
	req := a.nextRequest(t)

	a.loader.close()
	<-req.ctx.Done()
	a.loader.update("B", "b.jpg")
	a.noRequest(t)
}
