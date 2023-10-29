package main

import (
	"encoding/xml"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/ap"
	"go-librespot/dealer"
	"go-librespot/player"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	"go-librespot/session"
	"go-librespot/tracks"
	"google.golang.org/protobuf/proto"
	"strings"
)

type AppPlayer struct {
	app  *App
	sess *session.Session

	stop chan struct{}

	player *player.Player

	spotConnId string

	// TODO: can this be factored better?
	prodInfo    *ProductInfo
	countryCode *string

	state  *State
	stream *player.Stream
}

func (p *AppPlayer) handleAccesspointPacket(pktType ap.PacketType, payload []byte) error {
	switch pktType {
	case ap.PacketTypeProductInfo:
		var prod ProductInfo
		if err := xml.Unmarshal(payload, &prod); err != nil {
			return fmt.Errorf("failed umarshalling ProductInfo: %w", err)
		}

		if len(prod.Products) != 1 {
			return fmt.Errorf("invalid ProductInfo")
		}

		p.prodInfo = &prod
		return nil
	case ap.PacketTypeCountryCode:
		*p.countryCode = string(payload)
		return nil
	default:
		return nil
	}
}

func (p *AppPlayer) handleDealerMessage(msg dealer.Message) error {
	if strings.HasPrefix(msg.Uri, "hm://pusher/v1/connections/") {
		p.spotConnId = msg.Headers["Spotify-Connection-Id"]
		log.Debugf("received connection id: %s", p.spotConnId)

		// put the initial state
		if err := p.putConnectState(connectpb.PutStateReason_NEW_DEVICE); err != nil {
			return fmt.Errorf("failed initial state put: %w", err)
		}
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/connect/volume") {
		var setVolCmd connectpb.SetVolumeCommand
		if err := proto.Unmarshal(msg.Payload, &setVolCmd); err != nil {
			return fmt.Errorf("failed unmarshalling SetVolumeCommand: %w", err)
		}

		p.updateVolume(uint32(setVolCmd.Volume))
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/connect/logout") {
		// TODO: we should do this only when using zeroconf (?)
		log.Infof("logging out from %s", p.sess.Username())
		p.Close()
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/cluster") {
		var clusterUpdate connectpb.ClusterUpdate
		if err := proto.Unmarshal(msg.Payload, &clusterUpdate); err != nil {
			return fmt.Errorf("failed unmarshalling ClusterUpdate: %w", err)
		}

		stopBeingActive := p.state.active && clusterUpdate.Cluster.ActiveDeviceId != p.app.deviceId

		// We are still the active device, do not quit
		if !stopBeingActive {
			return nil
		}

		if p.stream != nil {
			p.stream.Stop()
			p.stream = nil
		}

		p.state.reset()
		if err := p.putConnectState(connectpb.PutStateReason_BECAME_INACTIVE); err != nil {
			return fmt.Errorf("failed inactive state put: %w", err)
		}

		// TODO: logout if using zeroconf (?)

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeInactive,
		})
	}

	return nil
}

func (p *AppPlayer) handlePlayerCommand(req dealer.RequestPayload) error {
	p.state.lastCommand = &req

	log.Debugf("handling %s player command from %s", req.Command.Endpoint, req.SentByDeviceId)

	switch req.Command.Endpoint {
	case "transfer":
		var transferState connectpb.TransferState
		if err := proto.Unmarshal(req.Command.Data, &transferState); err != nil {
			return fmt.Errorf("failed unmarshalling TransferState: %w", err)
		}

		ctxTracks, err := tracks.NewTrackListFromContext(p.sess.Spclient(), transferState.CurrentSession.Context)
		if err != nil {
			return fmt.Errorf("failed creating track list: %w", err)
		}

		p.state.setActive(true)
		p.state.player.IsPlaying = false
		p.state.player.IsBuffering = false
		p.state.player.IsPaused = false

		// options
		p.state.player.Options = transferState.Options

		// playback
		p.state.player.Timestamp = transferState.Playback.Timestamp
		p.state.player.PositionAsOfTimestamp = int64(transferState.Playback.PositionAsOfTimestamp)
		p.state.player.PlaybackSpeed = transferState.Playback.PlaybackSpeed
		p.state.player.IsPaused = transferState.Playback.IsPaused

		// current session
		p.state.player.PlayOrigin = transferState.CurrentSession.PlayOrigin
		p.state.player.ContextUri = transferState.CurrentSession.Context.Uri
		p.state.player.ContextUrl = transferState.CurrentSession.Context.Url
		p.state.player.ContextRestrictions = transferState.CurrentSession.Context.Restrictions
		p.state.player.Suppressions = transferState.CurrentSession.Suppressions

		p.state.player.ContextMetadata = map[string]string{}
		for k, v := range transferState.CurrentSession.Context.Metadata {
			p.state.player.ContextMetadata[k] = v
		}
		for k, v := range ctxTracks.Metadata() {
			p.state.player.ContextMetadata[k] = v
		}

		// queue
		// TODO: transfer queue

		contextSpotType := librespot.InferSpotifyIdTypeFromContextUri(p.state.player.ContextUri)
		currentTrack := librespot.ContextTrackToProvidedTrack(contextSpotType, transferState.Playback.CurrentTrack)
		if err := ctxTracks.Seek(func(track *connectpb.ContextTrack) bool {
			if len(track.Uid) > 0 && track.Uid == currentTrack.Uid {
				return true
			} else if len(track.Uri) > 0 && track.Uri == currentTrack.Uri {
				return true
			} else if len(track.Gid) > 0 && librespot.SpotifyIdFromGid(contextSpotType, track.Gid).Uri() == currentTrack.Uri {
				return true
			} else {
				return false
			}
		}); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}

		p.state.tracks = ctxTracks
		p.state.player.Track = ctxTracks.CurrentTrack()
		p.state.player.PrevTracks = ctxTracks.PrevTracks()
		p.state.player.NextTracks = ctxTracks.NextTracks()
		p.state.player.Index = ctxTracks.Index()

		// load current track into stream
		if err := p.loadCurrentTrack(transferState.Playback.IsPaused); err != nil {
			return fmt.Errorf("failed loading current track (transfer): %w", err)
		}

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeActive,
		})

		return nil
	case "play":
		p.state.player.PlayOrigin = req.Command.PlayOrigin
		p.state.player.Suppressions = req.Command.Options.Suppressions

		return p.loadContext(
			req.Command.Context,
			func(track *connectpb.ContextTrack) bool {
				if len(req.Command.Options.SkipTo.TrackUid) > 0 && req.Command.Options.SkipTo.TrackUid == track.Uid {
					return true
				} else if len(req.Command.Options.SkipTo.TrackUri) > 0 && req.Command.Options.SkipTo.TrackUri == track.Uri {
					return true
				} else {
					return false
				}
			},
			req.Command.Options.InitiallyPaused,
		)
	case "pause":
		return p.pause()
	case "resume":
		return p.play()
	case "seek_to":
		if req.Command.Relative != "beginning" {
			log.Warnf("unsupported seek_to relative position: %s", req.Command.Relative)
			return nil
		}

		if err := p.seek(req.Command.Position); err != nil {
			return fmt.Errorf("failed seeking stream: %w", err)
		}

		return nil
	case "skip_prev":
		return p.skipPrev()
	case "skip_next":
		return p.skipNext()
	case "update_context":
		if req.Command.Context.Uri != p.state.player.ContextUri {
			log.Warnf("ignoring context update for wrong uri: %s", req.Command.Context.Uri)
			return nil
		}

		p.state.player.ContextRestrictions = req.Command.Context.Restrictions
		if p.state.player.ContextMetadata == nil {
			p.state.player.ContextMetadata = map[string]string{}
		}
		for k, v := range req.Command.Context.Metadata {
			p.state.player.ContextMetadata[k] = v
		}

		p.updateState()
		return nil
	case "set_repeating_context":
		p.setRepeatingContext(req.Command.Value.(bool))
		return nil
	case "set_repeating_track":
		p.setRepeatingTrack(req.Command.Value.(bool))
		return nil
	case "set_shuffling_context":
		p.setShufflingContext(req.Command.Value.(bool))
		return nil
	default:
		return fmt.Errorf("unsupported player command: %s", req.Command.Endpoint)
	}
}

func (p *AppPlayer) handleDealerRequest(req dealer.Request) error {
	switch req.MessageIdent {
	case "hm://connect-state/v1/player/command":
		return p.handlePlayerCommand(req.Payload)
	default:
		log.Warnf("unknown dealer request: %s", req.MessageIdent)
		return nil
	}
}

func (p *AppPlayer) handleApiRequest(req ApiRequest) (any, error) {
	switch req.Type {
	case ApiRequestTypeStatus:
		resp := &ApiResponseStatus{
			Username:       p.sess.Username(),
			DeviceId:       p.app.deviceId,
			DeviceType:     p.app.deviceType.String(),
			DeviceName:     p.app.cfg.DeviceName,
			VolumeSteps:    p.app.cfg.VolumeSteps,
			Volume:         p.state.device.Volume,
			RepeatContext:  p.state.player.Options.RepeatingContext,
			RepeatTrack:    p.state.player.Options.RepeatingTrack,
			ShuffleContext: p.state.player.Options.ShufflingContext,
			Stopped:        !p.state.player.IsPlaying,
			Paused:         p.state.player.IsPaused,
			Buffering:      p.state.player.IsBuffering,
			PlayOrigin:     p.state.player.PlayOrigin.FeatureIdentifier,
		}

		if p.stream != nil && p.prodInfo != nil {
			resp.Track = NewApiResponseStatusTrack(p.stream.Media, p.prodInfo, p.state.trackPosition())
		}

		return resp, nil
	case ApiRequestTypeResume:
		_ = p.play()
		return nil, nil
	case ApiRequestTypePause:
		_ = p.pause()
		return nil, nil
	case ApiRequestTypeSeek:
		_ = p.seek(req.Data.(int64))
		return nil, nil
	case ApiRequestTypePrev:
		_ = p.skipPrev()
		return nil, nil
	case ApiRequestTypeNext:
		_ = p.skipNext()
		return nil, nil
	case ApiRequestTypePlay:
		data := req.Data.(ApiRequestDataPlay)
		ctx, err := p.sess.Spclient().ContextResolve(data.Uri)
		if err != nil {
			return nil, fmt.Errorf("failed resolving context: %w", err)
		}

		p.state.setActive(true)
		p.state.player.PlaybackSpeed = 1
		p.state.player.Suppressions = &connectpb.Suppressions{}
		p.state.player.PlayOrigin = &connectpb.PlayOrigin{
			FeatureIdentifier: "go-librespot",
			FeatureVersion:    librespot.VersionNumberString(),
		}

		if err := p.loadContext(ctx, func(track *connectpb.ContextTrack) bool {
			return len(data.SkipToUri) != 0 && data.SkipToUri == track.Uri
		}, data.Paused); err != nil {
			return nil, fmt.Errorf("failed loading context: %w", err)
		}

		return nil, nil
	case ApiRequestTypeGetVolume:
		return &ApiResponseVolume{
			Max:   p.app.cfg.VolumeSteps,
			Value: p.state.device.Volume * p.app.cfg.VolumeSteps / player.MaxStateVolume,
		}, nil
	case ApiRequestTypeSetVolume:
		vol := req.Data.(uint32)
		p.updateVolume(vol * player.MaxStateVolume / p.app.cfg.VolumeSteps)
		return nil, nil
	case ApiRequestTypeSetRepeatingContext:
		p.setRepeatingContext(req.Data.(bool))
		return nil, nil
	case ApiRequestTypeSetRepeatingTrack:
		p.setRepeatingTrack(req.Data.(bool))
		return nil, nil
	case ApiRequestTypeSetShufflingContext:
		p.setShufflingContext(req.Data.(bool))
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown request type: %s", req.Type)
	}
}

func (p *AppPlayer) Close() {
	p.stop <- struct{}{}
	p.player.Close()
	p.sess.Close()
}

func (p *AppPlayer) Run(apiRecv <-chan ApiRequest) {
	apRecv := p.sess.Accesspoint().Receive(ap.PacketTypeProductInfo, ap.PacketTypeCountryCode)
	msgRecv := p.sess.Dealer().ReceiveMessage("hm://pusher/v1/connections/", "hm://connect-state/v1/")
	reqRecv := p.sess.Dealer().ReceiveRequest("hm://connect-state/v1/player/command")
	playerRecv := p.player.Receive()

	for {
		select {
		case <-p.stop:
			return
		case pkt := <-apRecv:
			if err := p.handleAccesspointPacket(pkt.Type, pkt.Payload); err != nil {
				log.WithError(err).Warn("failed handling accesspoint packet")
			}
		case msg := <-msgRecv:
			if err := p.handleDealerMessage(msg); err != nil {
				log.WithError(err).Warn("failed handling dealer message")
			}
		case req := <-reqRecv:
			if err := p.handleDealerRequest(req); err != nil {
				log.WithError(err).Warn("failed handling dealer request")
				req.Reply(false)
			} else {
				log.Debugf("sending successful reply for delaer request")
				req.Reply(true)
			}
		case req := <-apiRecv:
			data, err := p.handleApiRequest(req)
			req.Reply(data, err)
		case ev := <-playerRecv:
			p.handlePlayerEvent(&ev)
		}
	}
}
