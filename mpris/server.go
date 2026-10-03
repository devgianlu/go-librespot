//go:build linux

package mpris

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// busRetryInterval is how long the server waits before looking for the session
// bus again, while there is none or after it went away.
var busRetryInterval = 10 * time.Second

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

// ConcreteServer publishes the player on the D-Bus session bus. The bus is
// owned by the user's session, which a system service can start before: the
// server waits for it rather than failing, and comes back when it does after
// losing it.
type ConcreteServer struct {
	rootInterface   MediaPlayer2RootInterface
	playerInterface MediaPlayer2PlayerInterface

	closeOnce sync.Once
	done      chan struct{}
	stopped   chan struct{}

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

func makeMetadata(uri *string, media *librespot.Media) map[string]any {
	m := make(map[string]any)

	if uri != nil {
		m["mpris:trackid"] = dbus.ObjectPath("/org/go_librespot/" + strings.Replace(*uri, ":", "/", -1))
		m["xesam:url"] = "https://open.spotify.com/track/" + last(strings.Split(*uri, ":"))
	} else {
		m["mpris:trackid"] = dbus.ObjectPath("/org/mpris/MediaPlayer2/TrackList/NoTrack")
	}

	if media != nil {
		if artUrl := mediaCoverUrl(media); artUrl != "" {
			m["mpris:artUrl"] = artUrl
		}
		if media.IsTrack() {
			m["mpris:length"] = media.Track().GetDuration() * 1000 // convert from ms to us
			m["xesam:album"] = media.Track().Album.Name
			m["xesam:albumArtist"] = artistsNames(media.Track().Album.Artist)
			m["xesam:artist"] = artistsNames(media.Track().Artist)
			m["xesam:autoRating"] = float64(*media.Track().Popularity) / 100.0
			m["xesam:discNumber"] = *media.Track().DiscNumber
			m["xesam:title"] = *media.Track().Name
			m["xesam:trackNumber"] = *media.Track().Number
		}
		if media.IsEpisode() {
			m["mpris:length"] = media.Episode().GetDuration() * 1000
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

// playerMethodNames maps Go method names on the player interface to the D-Bus
// names MPRIS gives them, where the two differ.
var playerMethodNames = map[string]string{"SeekBy": "Seek"}

func (s *ConcreteServer) EmitStateUpdate(state MediaState) {
	offer(s.done, s.stateChannel, state)
}

func (s *ConcreteServer) EmitSeekUpdate(state SeekState) {
	offer(s.done, s.seekChannel, state)
}

func (s *ConcreteServer) Receive() <-chan MediaPlayer2PlayerCommand {
	return s.playerInterface.commands
}

// executeStateUpdate publishes what changed in state since last, or all of it
// when last is nil, as on a fresh connection.
func (d *DBusInstance) executeStateUpdate(state MediaState, last *MediaState) *dbus.Error {
	if last == nil || state.PlaybackStatus != last.PlaybackStatus {
		if err := d.setProperty("org.mpris.MediaPlayer2.Player", "PlaybackStatus", state.PlaybackStatus); err != nil {
			return err
		}
	}
	if last == nil || state.LoopStatus != last.LoopStatus {
		if err := d.setProperty("org.mpris.MediaPlayer2.Player", "LoopStatus", state.LoopStatus); err != nil {
			return err
		}
	}
	if last == nil || state.Shuffle != last.Shuffle {
		if err := d.setProperty("org.mpris.MediaPlayer2.Player", "Shuffle", state.Shuffle); err != nil {
			return err
		}
	}
	if last == nil || state.Volume != last.Volume {
		if err := d.setProperty("org.mpris.MediaPlayer2.Player", "Volume", state.Volume); err != nil {
			return err
		}
	}
	if last == nil || state.PositionMs != last.PositionMs {
		if err := d.setProperty("org.mpris.MediaPlayer2.Player", "Position", state.PositionMs*1000); err != nil { // in microseconds
			return err
		}
	}
	if last == nil || state.Media != last.Media {
		mt := makeMetadata(state.Uri, state.Media)
		if err := d.setProperty("org.mpris.MediaPlayer2.Player", "Metadata", mt); err != nil {
			return err
		}
	}
	return nil
}

func (d *DBusInstance) executeSeekSignal(state SeekState) error {
	return d.conn.Emit(
		"/org/mpris/MediaPlayer2",
		"org.mpris.MediaPlayer2.Player.Seeked",
		state.PositionMs*1000,
	)
}

// connect opens the session bus and exports the player on it. It never
// autolaunches a bus: dbus-launch would start one of our own that nothing else
// listens on, and is usually not even installed where this runs as a service.
func (s *ConcreteServer) connect() (_ *DBusInstance, err error) {
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()

	if err := conn.Auth(nil); err != nil {
		return nil, fmt.Errorf("failed authenticating to the session bus: %w", err)
	}
	if err := conn.Hello(); err != nil {
		return nil, fmt.Errorf("failed greeting the session bus: %w", err)
	}

	d := &DBusInstance{conn: conn, log: s.log}
	d.props, err = prop.Export(
		conn,
		"/org/mpris/MediaPlayer2",
		map[string]map[string]*prop.Prop{
			"org.mpris.MediaPlayer2":        mediaPlayer2Props,
			"org.mpris.MediaPlayer2.Player": s.playerInterface.Props(),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed exporting mpris properties: %w", err)
	}

	reply, err := conn.RequestName("org.mpris.MediaPlayer2.go-librespot", dbus.NameFlagReplaceExisting)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, errors.New("mpris name is already taken")
	}

	if err := conn.Export(s.rootInterface, "/org/mpris/MediaPlayer2", "org.mpris.MediaPlayer2"); err != nil {
		return nil, err
	}
	if err := conn.ExportWithMap(s.playerInterface, playerMethodNames, "/org/mpris/MediaPlayer2", "org.mpris.MediaPlayer2.Player"); err != nil {
		return nil, err
	}

	return d, nil
}

// serve publishes updates on d until the server is closed or the bus goes
// away. It starts by publishing want in full, and returns the latest state it
// was handed, for the next connection to start from.
func (s *ConcreteServer) serve(d *DBusInstance, want MediaState) MediaState {
	var last *MediaState
	if err := d.executeStateUpdate(want, nil); err == nil {
		last = &want
	}

	for {
		select {
		case <-s.done:
			return want
		case <-d.conn.Context().Done():
			return want
		case want = <-s.stateChannel:
			// On failure last stays put, so the next update retries what
			// did not get through.
			if err := d.executeStateUpdate(want, last); err == nil {
				uploaded := want
				last = &uploaded
			}
		case seekState := <-s.seekChannel:
			if err := d.executeSeekSignal(seekState); err != nil {
				s.log.Warnf("error executing mpris state seek %s", err)
			}
		}
	}
}

func (s *ConcreteServer) run(want MediaState) {
	defer close(s.stopped)

	waiting := false
	for {
		d, err := s.connect()
		if err == nil {
			waiting = false
			s.log.Debugf("created mpris server")

			want = s.serve(d, want)
			_ = d.conn.Close()

			select {
			case <-s.done:
				return
			default:
			}
			s.log.Warn("lost the D-Bus session bus, mpris waits for it to come back")
		} else if !waiting {
			waiting = true
			s.log.WithError(err).Warn("no D-Bus session bus for mpris yet, waiting for one")
		} else {
			s.log.WithError(err).Tracef("still no D-Bus session bus for mpris")
		}

		retry := time.NewTimer(busRetryInterval)
	wait:
		for {
			select {
			case <-s.done:
				retry.Stop()
				return
			case want = <-s.stateChannel:
			case <-s.seekChannel:
				// A seek is a signal, not state: with no bus, nobody missed it.
			case <-retry.C:
				break wait
			}
		}
	}
}

func (s *ConcreteServer) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	<-s.stopped
	return nil
}

// NewServer starts publishing the player on the session bus. It does not fail
// for want of a bus: it connects in the background, as soon as there is one.
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
		done:         make(chan struct{}),
		stopped:      make(chan struct{}),
		stateChannel: make(chan MediaState, 1),
		seekChannel:  make(chan SeekState, 1),
	}

	go s.run(MediaState{
		PlaybackStatus: Stopped,
		LoopStatus:     None,
	})

	return s, nil
}
