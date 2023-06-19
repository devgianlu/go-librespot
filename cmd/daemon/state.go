package main

import (
	librespot "go-librespot"
	"go-librespot/dealer"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	"time"
)

type State struct {
	isActive   bool
	deviceInfo *connectpb.DeviceInfo

	playerState *connectpb.PlayerState

	lastCommand *dealer.RequestPayload
}

func (s *Session) initState() {
	s.state = &State{
		isActive:    false,
		lastCommand: nil,
		deviceInfo: &connectpb.DeviceInfo{
			CanPlay:               true,
			Volume:                VolumeSteps,
			Name:                  s.app.deviceName,
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
				VolumeSteps:                VolumeSteps,
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
		playerState: &connectpb.PlayerState{
			IsSystemInitiated: true,
			Options:           &connectpb.ContextPlayerOptions{},
		},
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
