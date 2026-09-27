//go:build test_unit

package mpris

import (
	"testing"

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
