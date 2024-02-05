package main

import (
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/dealer"
	"go-librespot/player"
	connectpb "go-librespot/proto/spotify/connectstate"
	"go-librespot/tracks"
	"time"
)

type State struct {
	active      bool
	activeSince time.Time

	device *connectpb.DeviceInfo
	player *connectpb.PlayerState

	tracks *tracks.List

	lastCommand *dealer.RequestPayload
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

func (s *State) playOrigin() string {
	return s.player.PlayOrigin.FeatureIdentifier
}

func (p *AppPlayer) initState() {
	p.state = &State{
		lastCommand: nil,
		device: &connectpb.DeviceInfo{
			CanPlay:               true,
			Volume:                player.MaxStateVolume,
			Name:                  *p.app.cfg.DeviceName,
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
				VolumeSteps:                int32(*p.app.cfg.VolumeSteps),
				SupportedTypes:             []string{"audio/track", "audio/episode"},
				CommandAcks:                true,
				SupportsRename:             false,
				Hidden:                     false,
				DisableVolume:              false,
				ConnectDisabled:            false,
				SupportsPlaylistV2:         true,
				IsControllable:             true,
				SupportsExternalEpisodes:   false, // TODO: support external episodes
				SupportsSetBackendMetadata: false,
				SupportsTransferCommand:    true,
				SupportsCommandRequest:     true,
				IsVoiceEnabled:             false,
				NeedsFullPlayerState:       false,
				SupportsGzipPushes:         true,
				SupportsSetOptionsCommand:  false,
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
