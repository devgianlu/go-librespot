package main

import (
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/dealer"
	"go-librespot/player"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	"time"
)

type State struct {
	isActive    bool
	deviceInfo  *connectpb.DeviceInfo
	playerState *connectpb.PlayerState

	tracks *TracksList

	lastCommand *dealer.RequestPayload
}

func (s *State) reset() {
	s.isActive = false
	s.playerState = &connectpb.PlayerState{
		IsSystemInitiated: true,
		PlayOrigin:        &connectpb.PlayOrigin{},
		Suppressions:      &connectpb.Suppressions{},
		Options:           &connectpb.ContextPlayerOptions{},
	}
}

func (s *State) trackPosition() int64 {
	if s.playerState.IsPaused {
		return s.playerState.PositionAsOfTimestamp
	} else {
		return time.Now().UnixMilli() - s.playerState.Timestamp + s.playerState.PositionAsOfTimestamp
	}
}

func (s *Session) initState() {
	s.state = &State{
		lastCommand: nil,
		deviceInfo: &connectpb.DeviceInfo{
			CanPlay:               true,
			Volume:                player.MaxStateVolume,
			Name:                  s.app.cfg.DeviceName,
			DeviceId:              s.app.deviceId,
			DeviceType:            s.app.deviceType,
			DeviceSoftwareVersion: librespot.VersionString(),
			ClientId:              librespot.ClientId,
			SpircVersion:          "3.2.6",
			Capabilities: &connectpb.Capabilities{
				CanBePlayer:                true,
				RestrictToLocal:            false,
				GaiaEqConnectId:            true,
				SupportsLogout:             true,
				IsObservable:               true,
				VolumeSteps:                int32(s.app.cfg.VolumeSteps),
				SupportedTypes:             []string{"audio/track"}, // TODO: support episodes
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
	s.state.reset()
}

func (s *Session) updateState(f func(s *State)) {
	s.stateLock.Lock()
	f(s.state)
	s.stateLock.Unlock()

	if err := s.putConnectState(connectpb.PutStateReason_PLAYER_STATE_CHANGED); err != nil {
		log.WithError(err).Error("failed put state after update")
	}
}

func (s *Session) withState(f func(s *State)) {
	s.stateLock.Lock()
	f(s.state)
	s.stateLock.Unlock()
}

func (s *Session) putConnectState(reason connectpb.PutStateReason) error {
	putStateReq := &connectpb.PutStateRequest{
		ClientSideTimestamp: uint64(time.Now().UnixMilli()),
		MemberType:          connectpb.MemberType_CONNECT_STATE,
		PutStateReason:      reason,
	}

	if t := s.player.StartedPlayingAt(); !t.IsZero() {
		putStateReq.StartedPlayingAt = uint64(t.UnixMilli())
	}
	if t := s.player.HasBeenPlayingFor(); t > 0 {
		putStateReq.HasBeenPlayingForMs = uint64(t.Milliseconds())
	}

	s.withState(func(s *State) {
		putStateReq.IsActive = s.isActive
		putStateReq.Device = &connectpb.Device{
			DeviceInfo:  s.deviceInfo,
			PlayerState: s.playerState,
		}

		if s.lastCommand != nil {
			putStateReq.LastCommandMessageId = s.lastCommand.MessageId
			putStateReq.LastCommandSentByDeviceId = s.lastCommand.SentByDeviceId
		}
	})

	// finally send the state update
	return s.sp.PutConnectState(s.spotConnId, putStateReq)
}
