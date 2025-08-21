package mpris

import (
	"reflect"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	log "github.com/sirupsen/logrus"
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

func newProp(value interface{}, cb func(*prop.Change) *dbus.Error) *prop.Prop {
	return &prop.Prop{
		Value:    value,
		Writable: true,
		Emit:     prop.EmitTrue,
		Callback: cb,
	}
}

var mediaPlayer2Props = map[string]*prop.Prop{
	"CanQuit":             newProp(false, nil), // disable quit
	"CanRaise":            newProp(false, nil), // disable raise
	"HasTrackList":        newProp(false, nil), // disable track list
	"Identity":            newProp("go-librespot", nil),
	"SupportedUriSchemes": newProp([]string{}, nil),
	"SupportedMimeTypes":  newProp([]string{}, nil),
}

// MediaPlayer2RootInterface : empty, does not do much
type MediaPlayer2RootInterface struct {
}

func (r MediaPlayer2RootInterface) Raise() *dbus.Error {
	// never implement, because there is no "window-raising" to be done in a cli application
	log.Tracef("PlayerInterface::Raise")
	return nil
}

func (r MediaPlayer2RootInterface) Quit() *dbus.Error {
	// not yet implemented, because the web api also does not support it
	log.Tracef("PlayerInterface::Quit")
	return nil
}

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

type MediaPlayer2PlayerCommandResponse struct {
	Err *dbus.Error
}
type MediaPlayer2PlayerCommand struct {
	Type     MediaPlayer2PlayerCommandType
	Argument any

	response chan MediaPlayer2PlayerCommandResponse
}

func (m *MediaPlayer2PlayerCommand) Reply(resp MediaPlayer2PlayerCommandResponse) {
	m.response <- resp
}

type MediaPlayer2PlayerInterface struct {
	commands chan MediaPlayer2PlayerCommand
}

func (p MediaPlayer2PlayerInterface) Props() map[string]*prop.Prop {
	return map[string]*prop.Prop{
		"PlaybackStatus": newProp(Playing, nil),
		"Metadata":       newProp(map[string]interface{}{}, nil),

		"Volume":     newProp(float64(100), p.volumeChanged),
		"Shuffle":    newProp(false, p.shuffleChanged),
		"LoopStatus": newProp(None, p.loopStatusChanged),

		"Position":      newProp(int64(0), nil),
		"Rate":          newProp(1.0, nil),
		"MinimumRate":   newProp(1.0, nil),
		"MaximumRate":   newProp(1.0, nil),
		"CanGoNext":     newProp(true, nil),
		"CanGoPrevious": newProp(true, nil),
		"CanPlay":       newProp(true, nil),
		"CanPause":      newProp(true, nil),
		"CanSeek":       newProp(true, nil),
		"CanControl":    newProp(true, nil),
	}
}

// enqueue command in channel,
func (p MediaPlayer2PlayerInterface) enqueueCommand(command MediaPlayer2PlayerCommand) *dbus.Error {
	command.response = make(chan MediaPlayer2PlayerCommandResponse)

	select {
	case p.commands <- command:
		resp := <-command.response

		if resp.Err != nil {
			log.Tracef("mpris command %v returned an error %s", command, resp.Err)
		}

		return resp.Err
	default:
		log.Tracef("mpris command not enqueued, because there was no listener registered")
		return nil
	}
}

func (p MediaPlayer2PlayerInterface) shuffleChanged(change *prop.Change) *dbus.Error {
	log.Tracef("PlayerInterface::shuffleChanged")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandShuffleChanged,
			Argument: change.Value,
		},
	)
}

func (p MediaPlayer2PlayerInterface) loopStatusChanged(change *prop.Change) *dbus.Error {
	log.Tracef("PlayerInterface::loopStatusChanged")

	// stupid, because sometimes the value gets parsed as LoopStatus, sometime as string, no idea why
	value := change.Value
	if reflect.TypeOf(value) != reflect.TypeOf(None) {
		value = LoopStatus(change.Value.(string))
	}

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandLoopStatusChanged,
			Argument: value,
		},
	)
}

func (p MediaPlayer2PlayerInterface) volumeChanged(change *prop.Change) *dbus.Error {
	log.Tracef("PlayerInterface::volumeChanged")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandVolumeChanged,
			Argument: change.Value,
		},
	)
}

func (p MediaPlayer2PlayerInterface) Next() *dbus.Error {
	log.Tracef("PlayerInterface::Next")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypeNext,
		},
	)
}
func (p MediaPlayer2PlayerInterface) Previous() *dbus.Error {
	log.Tracef("PlayerInterface::Previous")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypePrevious,
		},
	)

}
func (p MediaPlayer2PlayerInterface) Pause() *dbus.Error {
	log.Tracef("PlayerInterface::Pause")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypePause,
		},
	)
}
func (p MediaPlayer2PlayerInterface) PlayPause() *dbus.Error {
	log.Tracef("PlayerInterface::PlayPause")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypePlayPause,
		},
	)
}
func (p MediaPlayer2PlayerInterface) Stop() *dbus.Error {
	log.Tracef("PlayerInterface::Stop")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypeStop,
		},
	)
}
func (p MediaPlayer2PlayerInterface) Play() *dbus.Error {
	log.Tracef("PlayerInterface::Play")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypePlay,
		},
	)
}
func (p MediaPlayer2PlayerInterface) Seek(x int64) *dbus.Error {
	log.Tracef("PlayerInterface::Seek")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandTypeSeek,
			Argument: x,
		},
	)
}

type MediaPlayer2CommandSetPositionPayload struct {
	ObjectPath dbus.ObjectPath
	PositionUs int64
}

func (p MediaPlayer2PlayerInterface) SetPosition(o dbus.ObjectPath, x int64) *dbus.Error {
	log.Tracef("PlayerInterface::SetPosition")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandTypeSetPosition,
			Argument: MediaPlayer2CommandSetPositionPayload{o, x},
		},
	)
}
func (p MediaPlayer2PlayerInterface) OpenUri(s dbus.ObjectPath) *dbus.Error {
	log.Tracef("PlayerInterface::OpenUri")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandTypeOpenUri,
			Argument: s,
		},
	)
}
