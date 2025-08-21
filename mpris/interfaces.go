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
	MediaPlayer2PlayerCommandTypeNext          MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandTypePrevious      MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandTypePause         MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandTypePlayPause     MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandTypeStop          MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandTypePlay          MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandTypeSeek          MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandTypeSetPosition   MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandTypeOpenUri       MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandLoopStatusChanged MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandRateChanged       MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandShuffleChanged    MediaPlayer2PlayerCommandType = iota
	MediaPlayer2PlayerCommandVolumeChanged     MediaPlayer2PlayerCommandType = iota
)

type MediaPlayer2PlayerCommandResponse struct {
	Err *dbus.Error
}
type MediaPlayer2PlayerCommand struct {
	Type     MediaPlayer2PlayerCommandType
	Argument any

	response chan *MediaPlayer2PlayerCommandResponse
}

func (m *MediaPlayer2PlayerCommand) Reply(resp *MediaPlayer2PlayerCommandResponse) {
	m.response <- resp
}

type MediaPlayer2PlayerInterface struct {
	commands chan *MediaPlayer2PlayerCommand
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

func (p MediaPlayer2PlayerInterface) shuffleChanged(change *prop.Change) *dbus.Error {
	log.Tracef("PlayerInterface::shuffleChanged")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandShuffleChanged,
		Argument: change.Value,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}

func (p MediaPlayer2PlayerInterface) loopStatusChanged(change *prop.Change) *dbus.Error {
	log.Tracef("PlayerInterface::loopStatusChanged")

	// stupid, because sometimes the value gets parsed as LoopStatus, sometime as string, no idea why
	value := change.Value
	if reflect.TypeOf(value) != reflect.TypeOf(None) {
		value = LoopStatus(change.Value.(string))
	}

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandLoopStatusChanged,
		Argument: value,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}

func (p MediaPlayer2PlayerInterface) volumeChanged(change *prop.Change) *dbus.Error {
	log.Tracef("PlayerInterface::volumeChanged")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandVolumeChanged,
		Argument: change.Value,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}

func (p MediaPlayer2PlayerInterface) Next() *dbus.Error {
	log.Tracef("PlayerInterface::Next")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandTypeNext,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}
func (p MediaPlayer2PlayerInterface) Previous() *dbus.Error {
	log.Tracef("PlayerInterface::Previous")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandTypePrevious,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}
func (p MediaPlayer2PlayerInterface) Pause() *dbus.Error {
	log.Tracef("PlayerInterface::Pause")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandTypePause,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}
func (p MediaPlayer2PlayerInterface) PlayPause() *dbus.Error {
	log.Tracef("PlayerInterface::PlayPause")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandTypePlayPause,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}
func (p MediaPlayer2PlayerInterface) Stop() *dbus.Error {
	log.Tracef("PlayerInterface::Stop")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandTypeStop,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}
func (p MediaPlayer2PlayerInterface) Play() *dbus.Error {
	log.Tracef("PlayerInterface::Play")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandTypePlay,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}
func (p MediaPlayer2PlayerInterface) Seek(x int64) *dbus.Error {
	log.Tracef("PlayerInterface::Seek")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandTypeSeek,
		Argument: x,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}
func (p MediaPlayer2PlayerInterface) SetPosition(o dbus.ObjectPath, x int64) *dbus.Error {
	log.Tracef("PlayerInterface::SetPosition")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandTypeSetPosition,
		Argument: [2]any{o, x}, // todo
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}
func (p MediaPlayer2PlayerInterface) OpenUri(s dbus.ObjectPath) *dbus.Error {
	log.Tracef("PlayerInterface::OpenUri")

	rsp := make(chan *MediaPlayer2PlayerCommandResponse)
	p.commands <- &MediaPlayer2PlayerCommand{
		Type:     MediaPlayer2PlayerCommandTypeOpenUri,
		Argument: s,
		response: rsp,
	}
	resp := <-rsp

	return resp.Err
}
