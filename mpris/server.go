package mpris

import (
	"encoding/hex"
	"errors"
	"strings"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	log "github.com/sirupsen/logrus"
)

type DBusInstance struct {
	props *prop.Properties
	conn  *dbus.Conn
}

func (d *DBusInstance) setProperty(interfaceName string, fieldName string, value interface{}) *dbus.Error {
	err := d.props.Set(
		interfaceName,
		fieldName,
		dbus.MakeVariant(value),
	)
	if err != nil {
		return err
	}
	return nil
}

type MprisState struct {
	PlaybackStatus PlaybackStatus
	LoopStatus     LoopStatus
	Shuffle        bool
	Volume         float64
	PositionMs     int64
	Media          *librespot.Media
}

type MprisSeekState struct {
	PositionMs int64
}

type Server interface {
	EmitStateUpdate(state MprisState)
	EmitSeekUpdate(state MprisSeekState)
	Receive() <-chan MediaPlayer2PlayerCommand
}

type ConcreteServer struct {
	dbus            *DBusInstance
	rootInterface   MediaPlayer2RootInterface
	playerInterface MediaPlayer2PlayerInterface

	lastUploadedState MprisState

	stateChannel chan MprisState
	seekChannel  chan MprisSeekState
}

func last[T any](a []*T) *T {
	return a[len(a)-1]
}
func last_[T any](a []T) T {
	return a[len(a)-1]
}

func Map[T any, S any](a []*T, f func(*T) *S) []*S {
	result := make([]*S, len(a))
	for idx, it := range a {
		result[idx] = f(it)
	}
	return result
}

func artUrl(fileId []uint8) string {
	return "https://i.scdn.co/image/" + hex.EncodeToString(fileId)
}

func makeMetadata(media *librespot.Media) map[string]any {
	m := make(map[string]any)

	if media.IsTrack() {
		m["mpris:trackid"] = dbus.ObjectPath("/com/" + strings.Replace(*media.Track().TrackUri, ":", "/", -1))
		m["mpris:length"] = media.Track().GetDuration() * 1000 // convert from ms to us
		m["mpris:artUrl"] = artUrl(last(media.Track().GetAlbum().GetCoverGroup().GetImage()).FileId)
		m["xesam:album"] = media.Track().Album.Name
		m["xesam:albumArtist"] = Map(media.Track().Album.Artist, func(f *metadata.Artist) *string { return f.Name })
		m["xesam:artist"] = Map(media.Track().Artist, func(f *metadata.Artist) *string { return f.Name })
		m["xesam:autoRating"] = 1
		m["xesam:discNumber"] = *media.Track().DiscNumber
		m["xesam:title"] = *media.Track().Name
		m["xesam:trackNumber"] = *media.Track().Number
		m["xesam:url"] = "https://open.spotify.com/track/" + last_(strings.Split(*media.Track().TrackUri, ":"))
	}
	if media.IsEpisode() {
		m["mpris:trackid"] = "" // todo: "/com/spotify/episode/" + hex.EncodeToString(media.Episode().Gid
		m["mpris:length"] = media.Episode().GetDuration() * 1000
		m["mpris:artUrl"] = artUrl(last(media.Episode().GetCoverImage().GetImage()).FileId)
		m["xesam:album"] = media.Episode().GetShow().GetName()
		m["xesam:albumArtist"] = [1]string{media.Episode().GetShow().GetName()}
		m["xesam:artist"] = [1]string{media.Episode().GetShow().GetName()}
		m["xesam:autoRating"] = 1
		m["xesam:discNumber"] = 1
		m["xesam:title"] = media.Episode().GetName()
		m["xesam:trackNumber"] = 1
		m["xesam:url"] = "" // todo
	}
	return m
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

func (s *ConcreteServer) EmitStateUpdate(state MprisState) {
	s.stateChannel <- state
}

func (s *ConcreteServer) EmitSeekUpdate(state MprisSeekState) {
	s.seekChannel <- state
}

func (s *ConcreteServer) Receive() <-chan MediaPlayer2PlayerCommand {
	return s.playerInterface.commands
}

func (s *ConcreteServer) executeStateUpdate(state MprisState) *dbus.Error {
	if state.PlaybackStatus != s.lastUploadedState.PlaybackStatus {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "PlaybackStatus", state.PlaybackStatus); err != nil {
			log.Warnf("error executing mpris state update (playbackStatus) %s", err)
			return err
		}
	}
	if state.LoopStatus != s.lastUploadedState.LoopStatus {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "LoopStatus", state.LoopStatus); err != nil {
			log.Warnf("error executing mpris state update (loopStatus) %s", err)
			return err
		}
	}
	if state.Shuffle != s.lastUploadedState.Shuffle {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "Shuffle", state.Shuffle); err != nil {
			log.Warnf("error executing mpris state update (shuffle) %s", err)
			return err
		}
	}
	if state.Volume != s.lastUploadedState.Volume {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "Volume", state.Volume); err != nil {
			log.Warnf("error executing mpris state update (volume) %s", err)
			return err
		}
	}
	if state.PositionMs != s.lastUploadedState.PositionMs {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "Position", state.PositionMs); err != nil {
			log.Warnf("error executing mpris state update (position) %s", err)
			return err
		}
	}
	if state.Media != s.lastUploadedState.Media {
		mt := makeMetadata(state.Media)
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "Metadata", mt); err != nil {
			log.Warnf("error executing mpris state update (media) %s", err)
			return err
		}
		log.Tracef("successfully updated metadata %s", mt)
	}
	return nil
}

func (s *ConcreteServer) executeSeekSignal(state MprisSeekState) error {
	return s.dbus.conn.Emit(
		"/org/mpris/MediaPlayer2",
		"org.mpris.MediaPlayer2.Player.Seeked",
		state.PositionMs*1000,
	)
}

func (s *ConcreteServer) waitOnChannel() {
	for {
		select {
		case state := <-s.stateChannel:
			err := s.executeStateUpdate(state)
			if err != nil {
				continue
			}
			s.lastUploadedState = state
		case seekState := <-s.seekChannel:
			err := s.executeSeekSignal(seekState)
			if err != nil {
				log.Warnf("error executing mpris state seek %s", err)
			}
		}
	}
}

func (s *ConcreteServer) Close() {
	close(s.seekChannel)
	close(s.stateChannel)
	s.dbus.conn.Close()
}

// opens the dbus connection and registers everything important
func NewServer() (_ *ConcreteServer, err error) {
	s := &ConcreteServer{}

	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, err
	}

	s.dbus = &DBusInstance{
		conn: conn,
	}

	s.rootInterface = MediaPlayer2RootInterface{}
	s.playerInterface = MediaPlayer2PlayerInterface{
		commands: make(chan MediaPlayer2PlayerCommand),
	}

	s.dbus.props, err = prop.Export(
		conn,
		"/org/mpris/MediaPlayer2",
		map[string]map[string]*prop.Prop{
			"org.mpris.MediaPlayer2":        mediaPlayer2Props,
			"org.mpris.MediaPlayer2.Player": s.playerInterface.Props(),
		},
	)

	reply, err := conn.RequestName("org.mpris.MediaPlayer2.go-librespot", dbus.NameFlagReplaceExisting)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return s, errors.New("mpris name is already taken")
	}

	err = conn.Export(s.rootInterface, "/org/mpris/MediaPlayer2", "org.mpris.MediaPlayer2")
	if err != nil {
		return nil, err
	}
	err = conn.Export(s.playerInterface, "/org/mpris/MediaPlayer2", "org.mpris.MediaPlayer2.Player")
	if err != nil {
		return nil, err
	}

	s.lastUploadedState = MprisState{
		PlaybackStatus: Stopped,
		LoopStatus:     None,
		Shuffle:        false,
		Volume:         0,
		PositionMs:     0,
		Media:          nil,
	}

	if dbusErr := s.executeStateUpdate(s.lastUploadedState); dbusErr != nil {
		return nil, err
	}
	s.stateChannel = make(chan MprisState)
	s.seekChannel = make(chan MprisSeekState)

	go s.waitOnChannel()

	log.Debugf("created mpris server")

	return s, nil
}

type DummyServer struct {
}

func (d DummyServer) EmitStateUpdate(_ MprisState) {
}

func (d DummyServer) EmitSeekUpdate(_ MprisSeekState) {
}

func (d DummyServer) Receive() <-chan MediaPlayer2PlayerCommand {
	return make(<-chan MediaPlayer2PlayerCommand)
}
