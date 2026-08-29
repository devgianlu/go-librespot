//go:build linux

package mpris

import (
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

type DBusInstance struct {
	props *prop.Properties
	conn  *dbus.Conn

	log librespot.Logger
}

func (d *DBusInstance) setProperty(interfaceName string, fieldName string, value interface{}) *dbus.Error {

	err := d.props.Set(
		interfaceName,
		fieldName,
		dbus.MakeVariant(value),
	)

	if err != nil {
		d.log.Warnf("error setting mpris property: %s", *err)
	} else {
		d.log.Tracef("set mpris property \"%s\" on interface \"%s\" to \"%s\"", fieldName, interfaceName, value)
	}
	// can be a ptr to some error or nil
	return err
}

type ConcreteServer struct {
	dbus            *DBusInstance
	rootInterface   MediaPlayer2RootInterface
	playerInterface MediaPlayer2PlayerInterface

	lastUploadedState MediaState

	closeOnce sync.Once
	done      chan struct{}

	log librespot.Logger

	// Latest-wins queues of depth one: only the most recent state is worth
	// publishing, and the emitter — the player loop — must never wait on D-Bus.
	stateChannel chan MediaState
	seekChannel  chan SeekState
}

// offer replaces whatever is queued with val, without ever blocking. Returns
// false once the server is closed.
func offer[T any](done <-chan struct{}, ch chan T, val T) bool {
	select {
	case <-done:
		return false
	default:
	}

	select {
	case <-ch:
	default:
	}

	select {
	case ch <- val:
		return true
	default:
		return false
	}
}

func last[T any](a []T) T {
	return a[len(a)-1]
}

func coverArtUrl(fileId []uint8) string {
	return "https://i.scdn.co/image/" + hex.EncodeToString(fileId)
}

func artistsNames(artists []*metadata.Artist) []*string {
	res := make([]*string, len(artists))
	for idx, it := range artists {
		res[idx] = it.Name
	}
	return res
}

func makeMetadata(uri *string, media *librespot.Media) map[string]any {
	m := make(map[string]any)

	if uri != nil {
		m["mpris:trackid"] = dbus.ObjectPath("/org/go_librespot/" + strings.Replace(*uri, ":", "/", -1))
		m["xesam:url"] = "https://open.spotify.com/track/" + last(strings.Split(*uri, ":"))
	} else {
		m["mpris:trackid"] = dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/NoTrack")
	}

	if media != nil {
		var coverArtFileId []byte = nil
		if media.IsTrack() {
			if coverGroupImages := media.Track().GetAlbum().GetCoverGroup().GetImage(); len(coverGroupImages) > 0 {
				coverArtFileId = coverGroupImages[len(coverGroupImages)-1].FileId
			}

			m["mpris:length"] = media.Track().GetDuration() * 1000 // convert from ms to us
			if coverArtFileId != nil {
				m["mpris:artUrl"] = coverArtUrl(coverArtFileId)
			}
			m["xesam:album"] = media.Track().Album.Name
			m["xesam:albumArtist"] = artistsNames(media.Track().Album.Artist)
			m["xesam:artist"] = artistsNames(media.Track().Artist)
			m["xesam:autoRating"] = float64(*media.Track().Popularity) / 100.0
			m["xesam:discNumber"] = *media.Track().DiscNumber
			m["xesam:title"] = *media.Track().Name
			m["xesam:trackNumber"] = *media.Track().Number
		}
		if media.IsEpisode() {
			if coverGroupImages := media.Episode().GetShow().GetCoverImage().GetImage(); len(coverGroupImages) > 0 {
				coverArtFileId = coverGroupImages[len(coverGroupImages)-1].FileId
			}

			m["mpris:length"] = media.Episode().GetDuration() * 1000
			if coverArtFileId != nil {
				m["mpris:artUrl"] = coverArtUrl(coverArtFileId)
			}
			m["xesam:album"] = media.Episode().GetShow().GetName()
			m["xesam:albumArtist"] = []string{media.Episode().GetShow().GetName()}
			m["xesam:artist"] = []string{media.Episode().GetShow().GetName()}
			m["xesam:autoRating"] = 1
			m["xesam:discNumber"] = 1
			m["xesam:title"] = media.Episode().GetName()
			m["xesam:trackNumber"] = 1
		}
	}
	return m
}

func (s *ConcreteServer) EmitStateUpdate(state MediaState) {
	offer(s.done, s.stateChannel, state)
}

func (s *ConcreteServer) EmitSeekUpdate(state SeekState) {
	offer(s.done, s.seekChannel, state)
}

func (s *ConcreteServer) Receive() <-chan MediaPlayer2PlayerCommand {
	return s.playerInterface.commands
}

func (s *ConcreteServer) executeStateUpdate(state MediaState) *dbus.Error {
	if state.PlaybackStatus != s.lastUploadedState.PlaybackStatus {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "PlaybackStatus", state.PlaybackStatus); err != nil {
			return err
		}
	}
	if state.LoopStatus != s.lastUploadedState.LoopStatus {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "LoopStatus", state.LoopStatus); err != nil {
			return err
		}
	}
	if state.Shuffle != s.lastUploadedState.Shuffle {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "Shuffle", state.Shuffle); err != nil {
			return err
		}
	}
	if state.Volume != s.lastUploadedState.Volume {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "Volume", state.Volume); err != nil {
			return err
		}
	}
	if state.PositionMs != s.lastUploadedState.PositionMs {
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "Position", state.PositionMs); err != nil {
			return err
		}
	}
	if state.Media != s.lastUploadedState.Media {
		mt := makeMetadata(state.Uri, state.Media)
		if err := s.dbus.setProperty("org.mpris.MediaPlayer2.Player", "Metadata", mt); err != nil {
			return err
		}
	}
	return nil
}

func (s *ConcreteServer) executeSeekSignal(state SeekState) error {
	return s.dbus.conn.Emit(
		"/org/mpris/MediaPlayer2",
		"org.mpris.MediaPlayer2.Player.Seeked",
		state.PositionMs*1000,
	)
}

func (s *ConcreteServer) waitOnChannel() {
	for {
		select {
		case <-s.done:
			return
		case state := <-s.stateChannel:
			err := s.executeStateUpdate(state)
			if err != nil {
				continue
			}
			s.lastUploadedState = state
		case seekState := <-s.seekChannel:
			err := s.executeSeekSignal(seekState)
			if err != nil {
				s.log.Warnf("error executing mpris state seek %s", err)
			}
		}
	}
}

func (s *ConcreteServer) Close() error {
	s.closeOnce.Do(func() { close(s.done) })

	return s.dbus.conn.Close()
}

// NewServer opens the dbus connection and registers everything important
func NewServer(logger librespot.Logger) (_ *ConcreteServer, err error) {
	s := &ConcreteServer{
		log: logger,
		rootInterface: MediaPlayer2RootInterface{
			log: logger,
		},
		playerInterface: MediaPlayer2PlayerInterface{
			log: logger,

			commands: make(chan MediaPlayer2PlayerCommand),
		},
		lastUploadedState: MediaState{
			PlaybackStatus: Stopped,
			LoopStatus:     None,
			Shuffle:        false,
			Volume:         0,
			PositionMs:     0,
			Media:          nil,
		},
		done:         make(chan struct{}),
		stateChannel: make(chan MediaState, 1),
		seekChannel:  make(chan SeekState, 1),
	}

	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, err
	}

	s.dbus = &DBusInstance{
		conn: conn,
		log:  logger,
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

	if dbusErr := s.executeStateUpdate(s.lastUploadedState); dbusErr != nil {
		return nil, err
	}
	go s.waitOnChannel()

	s.log.Debugf("created mpris server")

	return s, nil
}
