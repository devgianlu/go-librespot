package mpris

import (
	librespot "github.com/devgianlu/go-librespot"
	"github.com/godbus/dbus/v5"
)

type PlaybackStatus string

const (
	Playing PlaybackStatus = "Playing"
	Paused  PlaybackStatus = "Paused"
	Stopped PlaybackStatus = "Stopped"
)

type LoopStatus string

const (
	None     LoopStatus = "None"
	Track    LoopStatus = "Track"
	Playlist LoopStatus = "Playlist"
)

type MediaPlayer2PlayerCommandType int32

const (
	MediaPlayer2PlayerCommandTypeNext MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandTypePrevious
	MediaPlayer2PlayerCommandTypePause
	MediaPlayer2PlayerCommandTypePlayPause
	MediaPlayer2PlayerCommandTypeStop
	MediaPlayer2PlayerCommandTypePlay
	MediaPlayer2PlayerCommandTypeSeek
	MediaPlayer2PlayerCommandTypeSetPosition
	MediaPlayer2PlayerCommandTypeOpenUri
	MediaPlayer2PlayerCommandLoopStatusChanged
	MediaPlayer2PlayerCommandRateChanged
	MediaPlayer2PlayerCommandShuffleChanged
	MediaPlayer2PlayerCommandVolumeChanged
)

type MediaPlayer2PlayerCommand struct {
	Type     MediaPlayer2PlayerCommandType
	Argument any

	response chan MediaPlayer2PlayerCommandResponse
}

func (m *MediaPlayer2PlayerCommand) Reply(resp MediaPlayer2PlayerCommandResponse) {
	m.response <- resp
}

type MediaPlayer2PlayerCommandResponse struct {
	Err *dbus.Error
}

type MediaState struct {
	PlaybackStatus PlaybackStatus
	LoopStatus     LoopStatus
	Shuffle        bool
	Volume         float64
	PositionMs     int64
	Uri            *string          // nilable
	Media          *librespot.Media // nilable
}

type SeekState struct {
	PositionMs int64
}

type Server interface {
	EmitStateUpdate(state MediaState)
	EmitSeekUpdate(state SeekState)
	Receive() <-chan MediaPlayer2PlayerCommand

	Close() error
}

func GetLoopStatus(repeatingContext bool, repeatingTrack bool) LoopStatus {
	if repeatingTrack {
		return Track
	}
	if repeatingContext {
		return Playlist
	}
	return None
}

type MediaPlayer2CommandSetPositionPayload struct {
	ObjectPath dbus.ObjectPath
	PositionUs int64
}

type DummyServer struct {
}

func (d DummyServer) EmitStateUpdate(_ MediaState) {
}

func (d DummyServer) EmitSeekUpdate(_ SeekState) {
}

func (d DummyServer) Receive() <-chan MediaPlayer2PlayerCommand {
	return make(<-chan MediaPlayer2PlayerCommand)
}

func (d DummyServer) Close() error { return nil }
