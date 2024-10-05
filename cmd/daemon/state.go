package main

import (
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/dealer"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/tracks"
	log "github.com/sirupsen/logrus"
)

type State struct {
	active      bool
	activeSince time.Time

	device *connectpb.DeviceInfo
	player *connectpb.PlayerState

	tracks  *tracks.List
	queueID uint64

	lastCommand           *dealer.RequestPayload
	lastTransferTimestamp int64
}

// Set the IsPaused flag, and also the PlaybackSpeed as well.
// PlaybackSpeed must be 0 when paused, or Spotify Android will have subtle
// bugs.
func (s *State) setPaused(val bool) {
	s.player.IsPaused = val
	if val {
		s.player.PlaybackSpeed = 0
	} else {
		s.player.PlaybackSpeed = 1
	}
}

func (s *State) setActive(val bool) {
	if val {
		if s.active {
			return
		}

		s.active = true
		s.activeSince = time.Now()
	} else {
		s.active = false
		s.activeSince = time.Time{}
	}
}

func (s *State) reset() {
	s.active = false
	s.activeSince = time.Time{}
	s.player = &connectpb.PlayerState{
		IsSystemInitiated: true,
		PlaybackSpeed:     1,
		PlayOrigin:        &connectpb.PlayOrigin{},
		Suppressions:      &connectpb.Suppressions{},
		Options:           &connectpb.ContextPlayerOptions{},
	}
}

func (s *State) trackPosition() int64 {
	if s.player.IsPaused {
		return s.player.PositionAsOfTimestamp
	} else {
		return time.Now().UnixMilli() - s.player.Timestamp + s.player.PositionAsOfTimestamp
	}
}

// Update timestamp, and updating the player position timestamp according to how
// much time has passed since the last update.
func (s *State) updateTimestamp() {
	// Use single timestamp throughout, for consistency.
	now := time.Now()

	// How many milliseconds the playback has advanced since the last update to
	// PositionAsOfTimestamp.
	advancedTimeMillis := now.UnixMilli() - s.player.Timestamp

	// How far the playback position has advanced during that time.
	// (For example, PlaybackSpeed is 0 when paused so the position doesn't
	// change).
	advancedPositionMillis := int64(float64(advancedTimeMillis) * s.player.PlaybackSpeed)

	// Update the timestamps accordingly.
	s.player.PositionAsOfTimestamp += advancedPositionMillis
	s.player.Timestamp = now.UnixMilli()
}

func (s *State) playOrigin() string {
	return s.player.PlayOrigin.FeatureIdentifier
}

func (p *AppPlayer) initState() {
	p.state = &State{
		lastCommand: nil,
		device: &connectpb.DeviceInfo{
			CanPlay:               true,
			Volume:                player.MaxStateVolume,
			Name:                  p.app.cfg.DeviceName,
			DeviceId:              p.app.deviceId,
			DeviceType:            p.app.deviceType,
			DeviceSoftwareVersion: librespot.VersionString(),
			ClientId:              librespot.ClientIdHex,
			SpircVersion:          "3.2.6",
			Capabilities: &connectpb.Capabilities{
				CanBePlayer:                true,
				RestrictToLocal:            false,
				GaiaEqConnectId:            true,
				SupportsLogout:             p.app.cfg.ZeroconfEnabled,
				IsObservable:               true,
				VolumeSteps:                int32(p.app.cfg.VolumeSteps),
				SupportedTypes:             []string{"audio/track", "audio/episode"},
				CommandAcks:                true,
				SupportsRename:             false,
				Hidden:                     false,
				DisableVolume:              false,
				ConnectDisabled:            false,
				SupportsPlaylistV2:         true,
				IsControllable:             true,
				SupportsExternalEpisodes:   false, // TODO: support external episodes
				SupportsSetBackendMetadata: true,
				SupportsTransferCommand:    true,
				SupportsCommandRequest:     true,
				IsVoiceEnabled:             false,
				NeedsFullPlayerState:       false,
				SupportsGzipPushes:         true,
				SupportsSetOptionsCommand:  true,
				SupportsHifi:               nil, // TODO: nice to have?
				ConnectCapabilities:        "",
			},
		},
	}
	p.state.reset()
}

func (p *AppPlayer) updateState() {
	if err := p.putConnectState(connectpb.PutStateReason_PLAYER_STATE_CHANGED); err != nil {
		log.WithError(err).Error("failed put state after update")
	}
}

func (p *AppPlayer) putConnectState(reason connectpb.PutStateReason) error {
	if reason == connectpb.PutStateReason_BECAME_INACTIVE {
		return p.sess.Spclient().PutConnectStateInactive(p.spotConnId, false)
	}

	putStateReq := &connectpb.PutStateRequest{
		ClientSideTimestamp: uint64(time.Now().UnixMilli()),
		MemberType:          connectpb.MemberType_CONNECT_STATE,
		PutStateReason:      reason,
	}

	if t := p.state.activeSince; !t.IsZero() {
		putStateReq.StartedPlayingAt = uint64(t.UnixMilli())
	}
	if t := p.player.HasBeenPlayingFor(); t > 0 {
		putStateReq.HasBeenPlayingForMs = uint64(t.Milliseconds())
	}

	putStateReq.IsActive = p.state.active
	putStateReq.Device = &connectpb.Device{
		DeviceInfo:  p.state.device,
		PlayerState: p.state.player,
	}

	if p.state.lastCommand != nil {
		putStateReq.LastCommandMessageId = p.state.lastCommand.MessageId
		putStateReq.LastCommandSentByDeviceId = p.state.lastCommand.SentByDeviceId
	}

	// finally send the state update
	return p.sess.Spclient().PutConnectState(p.spotConnId, putStateReq)
}
