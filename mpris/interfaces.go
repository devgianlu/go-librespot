//go:build linux

package mpris

import (
	"reflect"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
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
	log librespot.Logger
}

func (r MediaPlayer2RootInterface) Raise() *dbus.Error {
	// never implement, because there is no "window-raising" to be done in a cli application
	r.log.Tracef("PlayerInterface::Raise")
	return nil
}

func (r MediaPlayer2RootInterface) Quit() *dbus.Error {
	// not yet implemented, because the web api also does not support it
	r.log.Tracef("PlayerInterface::Quit")
	return nil
}

type MediaPlayer2PlayerInterface struct {
	log librespot.Logger

	commands chan MediaPlayer2PlayerCommand
}

type MediaPlayer2PlayerInterfaceInterface interface {
	Next() *dbus.Error
	Previous() *dbus.Error
	Pause() *dbus.Error
	PlayPause() *dbus.Error
	Stop() *dbus.Error
	Play() *dbus.Error
	Seek(int64) *dbus.Error
	SetPosition(dbus.ObjectPath, int64) *dbus.Error
	OpenUri(dbus.ObjectPath) *dbus.Error
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

func (p MediaPlayer2PlayerInterface) enqueueCommand(command MediaPlayer2PlayerCommand) *dbus.Error {
	command.response = make(chan MediaPlayer2PlayerCommandResponse)

	select {
	case p.commands <- command:
		resp := <-command.response

		if resp.Err != nil {
			p.log.Tracef("mpris command %v returned an error %s", command, resp.Err)
		}

		return resp.Err
	default:
		p.log.Tracef("mpris command not enqueued, because there was no listener registered")
		return nil
	}
}

func (p MediaPlayer2PlayerInterface) shuffleChanged(change *prop.Change) *dbus.Error {
	p.log.Tracef("PlayerInterface::shuffleChanged")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandShuffleChanged,
			Argument: change.Value,
		},
	)
}

func (p MediaPlayer2PlayerInterface) loopStatusChanged(change *prop.Change) *dbus.Error {
	p.log.Tracef("PlayerInterface::loopStatusChanged")

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
	p.log.Tracef("PlayerInterface::volumeChanged")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandVolumeChanged,
			Argument: change.Value,
		},
	)
}

func (p MediaPlayer2PlayerInterface) Next() *dbus.Error {
	p.log.Tracef("PlayerInterface::Next")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypeNext,
		},
	)
}
func (p MediaPlayer2PlayerInterface) Previous() *dbus.Error {
	p.log.Tracef("PlayerInterface::Previous")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypePrevious,
		},
	)

}
func (p MediaPlayer2PlayerInterface) Pause() *dbus.Error {
	p.log.Tracef("PlayerInterface::Pause")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypePause,
		},
	)
}
func (p MediaPlayer2PlayerInterface) PlayPause() *dbus.Error {
	p.log.Tracef("PlayerInterface::PlayPause")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypePlayPause,
		},
	)
}
func (p MediaPlayer2PlayerInterface) Stop() *dbus.Error {
	p.log.Tracef("PlayerInterface::Stop")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypeStop,
		},
	)
}
func (p MediaPlayer2PlayerInterface) Play() *dbus.Error {
	p.log.Tracef("PlayerInterface::Play")

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type: MediaPlayer2PlayerCommandTypePlay,
		},
	)
}
func (p MediaPlayer2PlayerInterface) Seek(x int64) *dbus.Error {
	p.log.Tracef("PlayerInterface::Seek (%d)", x)

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandTypeSeek,
			Argument: x,
		},
	)
}

func (p MediaPlayer2PlayerInterface) SetPosition(o dbus.ObjectPath, x int64) *dbus.Error {
	p.log.Tracef("PlayerInterface::SetPosition (%s, %d)", o, x)

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandTypeSetPosition,
			Argument: MediaPlayer2CommandSetPositionPayload{o, x},
		},
	)
}
func (p MediaPlayer2PlayerInterface) OpenUri(s dbus.ObjectPath) *dbus.Error {
	p.log.Tracef("PlayerInterface::OpenUri (%s)", s)

	return p.enqueueCommand(
		MediaPlayer2PlayerCommand{
			Type:     MediaPlayer2PlayerCommandTypeOpenUri,
			Argument: s,
		},
	)
}
