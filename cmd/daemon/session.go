package main

import (
	"encoding/xml"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/ap"
	"go-librespot/audio"
	"go-librespot/dealer"
	"go-librespot/login5"
	"go-librespot/player"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	credentialspb "go-librespot/proto/spotify/login5/v3/credentials"
	"go-librespot/spclient"
	"google.golang.org/protobuf/proto"
	"strings"
)

type Session struct {
	app *App

	stop chan struct{}

	ap     *ap.Accesspoint
	login5 *login5.Login5
	sp     *spclient.Spclient
	dealer *dealer.Dealer

	audioKey *audio.KeyProvider

	player *player.Player

	spotConnId string

	// TODO: can this be factored better?
	prodInfo    *ProductInfo
	countryCode *string

	state  *State
	stream *player.Stream
}

func (s *Session) handleAccesspointPacket(pktType ap.PacketType, payload []byte) error {
	switch pktType {
	case ap.PacketTypeProductInfo:
		var prod ProductInfo
		if err := xml.Unmarshal(payload, &prod); err != nil {
			return fmt.Errorf("failed umarshalling ProductInfo: %w", err)
		}

		if len(prod.Products) != 1 {
			return fmt.Errorf("invalid ProductInfo")
		}

		s.prodInfo = &prod
		return nil
	case ap.PacketTypeCountryCode:
		*s.countryCode = string(payload)
		return nil
	default:
		return nil
	}
}

func (s *Session) handleDealerMessage(msg dealer.Message) error {
	if strings.HasPrefix(msg.Uri, "hm://pusher/v1/connections/") {
		s.spotConnId = msg.Headers["Spotify-Connection-Id"]
		log.Debugf("received connection id: %s", s.spotConnId)

		// put the initial state
		if err := s.putConnectState(connectpb.PutStateReason_NEW_DEVICE); err != nil {
			return fmt.Errorf("failed initial state put: %w", err)
		}
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/connect/volume") {
		var setVolCmd connectpb.SetVolumeCommand
		if err := proto.Unmarshal(msg.Payload, &setVolCmd); err != nil {
			return fmt.Errorf("failed unmarshalling SetVolumeCommand: %w", err)
		}

		s.updateVolume(uint32(setVolCmd.Volume))
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/connect/logout") {
		// TODO: we should do this only when using zeroconf (?)
		log.Infof("logging out from %s", s.ap.Username())
		s.Close()
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/cluster") {
		var clusterUpdate connectpb.ClusterUpdate
		if err := proto.Unmarshal(msg.Payload, &clusterUpdate); err != nil {
			return fmt.Errorf("failed unmarshalling ClusterUpdate: %w", err)
		}

		stopBeingActive := s.state.active && clusterUpdate.Cluster.ActiveDeviceId != s.app.deviceId

		// We are still the active device, do not quit
		if !stopBeingActive {
			return nil
		}

		if s.stream != nil {
			s.stream.Stop()
			s.stream = nil
		}

		s.state.reset()
		if err := s.putConnectState(connectpb.PutStateReason_BECAME_INACTIVE); err != nil {
			return fmt.Errorf("failed inactive state put: %w", err)
		}

		// TODO: logout if using zeroconf (?)

		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeInactive,
		})
	}

	return nil
}

func (s *Session) handlePlayerCommand(req dealer.RequestPayload) error {
	s.state.lastCommand = &req

	log.Debugf("handling %s player command from %s", req.Command.Endpoint, req.SentByDeviceId)

	switch req.Command.Endpoint {
	case "transfer":
		var transferState connectpb.TransferState
		if err := proto.Unmarshal(req.Command.Data, &transferState); err != nil {
			return fmt.Errorf("failed unmarshalling TransferState: %w", err)
		}

		tracks, err := NewTrackListFromContext(s.sp, transferState.CurrentSession.Context)
		if err != nil {
			return fmt.Errorf("failed creating track list: %w", err)
		}

		s.state.setActive(true)
		s.state.player.IsPlaying = false
		s.state.player.IsBuffering = false
		s.state.player.IsPaused = false

		// options
		s.state.player.Options = transferState.Options

		// playback
		s.state.player.Timestamp = transferState.Playback.Timestamp
		s.state.player.PositionAsOfTimestamp = int64(transferState.Playback.PositionAsOfTimestamp)
		s.state.player.PlaybackSpeed = transferState.Playback.PlaybackSpeed
		s.state.player.IsPaused = transferState.Playback.IsPaused

		// current session
		s.state.player.PlayOrigin = transferState.CurrentSession.PlayOrigin
		s.state.player.ContextUri = transferState.CurrentSession.Context.Uri
		s.state.player.ContextUrl = transferState.CurrentSession.Context.Url
		s.state.player.ContextRestrictions = transferState.CurrentSession.Context.Restrictions
		s.state.player.Suppressions = transferState.CurrentSession.Suppressions

		s.state.player.ContextMetadata = map[string]string{}
		for k, v := range transferState.CurrentSession.Context.Metadata {
			s.state.player.ContextMetadata[k] = v
		}
		for k, v := range tracks.Metadata() {
			s.state.player.ContextMetadata[k] = v
		}

		// queue
		// TODO: transfer queue

		currentTrack := librespot.ContextTrackToProvidedTrack(transferState.Playback.CurrentTrack)
		if err := tracks.Seek(func(track *connectpb.ContextTrack) bool {
			if len(track.Uid) > 0 && track.Uid == currentTrack.Uid {
				return true
			} else if len(track.Uri) > 0 && track.Uri == currentTrack.Uri {
				return true
			} else if len(track.Gid) > 0 && librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeTrack, track.Gid).Uri() == currentTrack.Uri /* FIXME: this might not always be a track */ {
				return true
			} else {
				return false
			}
		}); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}

		s.state.tracks = tracks
		s.state.player.Track = tracks.CurrentTrack()
		s.state.player.PrevTracks = tracks.PrevTracks()
		s.state.player.NextTracks = tracks.NextTracks()
		s.state.player.Index = tracks.Index()

		// load current track into stream
		if err := s.loadCurrentTrack(transferState.Playback.IsPaused); err != nil {
			return fmt.Errorf("failed loading current track (transfer): %w", err)
		}

		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeActive,
		})

		return nil
	case "play":
		s.state.player.PlayOrigin = req.Command.PlayOrigin
		s.state.player.Suppressions = req.Command.Options.Suppressions

		return s.loadContext(
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
		return s.pause()
	case "resume":
		return s.play()
	case "seek_to":
		if req.Command.Relative != "beginning" {
			log.Warnf("unsupported seek_to relative position: %s", req.Command.Relative)
			return nil
		}

		if err := s.seek(req.Command.Position); err != nil {
			return fmt.Errorf("failed seeking stream: %w", err)
		}

		return nil
	case "skip_prev":
		return s.skipPrev()
	case "skip_next":
		return s.skipNext()
	case "update_context":
		if req.Command.Context.Uri != s.state.player.ContextUri {
			log.Warnf("ignoring context update for wrong uri: %s", req.Command.Context.Uri)
			return nil
		}

		s.state.player.ContextRestrictions = req.Command.Context.Restrictions
		if s.state.player.ContextMetadata == nil {
			s.state.player.ContextMetadata = map[string]string{}
		}
		for k, v := range req.Command.Context.Metadata {
			s.state.player.ContextMetadata[k] = v
		}

		s.updateState()
		return nil
	case "set_repeating_context":
		s.setRepeatingContext(req.Command.Value.(bool))
		return nil
	case "set_repeating_track":
		s.setRepeatingTrack(req.Command.Value.(bool))
		return nil
	case "set_shuffling_context":
		s.setShufflingContext(req.Command.Value.(bool))
		return nil
	default:
		return fmt.Errorf("unsupported player command: %s", req.Command.Endpoint)
	}
}

func (s *Session) handleDealerRequest(req dealer.Request) error {
	switch req.MessageIdent {
	case "hm://connect-state/v1/player/command":
		return s.handlePlayerCommand(req.Payload)
	default:
		log.Warnf("unknown dealer request: %s", req.MessageIdent)
		return nil
	}
}

func (s *Session) handleApiRequest(req ApiRequest) (any, error) {
	switch req.Type {
	case ApiRequestTypeStatus:
		resp := &ApiResponseStatus{
			Username:       s.ap.Username(),
			DeviceId:       s.app.deviceId,
			DeviceType:     s.app.deviceType.String(),
			DeviceName:     s.app.cfg.DeviceName,
			VolumeSteps:    s.app.cfg.VolumeSteps,
			Volume:         s.state.device.Volume,
			RepeatContext:  s.state.player.Options.RepeatingContext,
			RepeatTrack:    s.state.player.Options.RepeatingTrack,
			ShuffleContext: s.state.player.Options.ShufflingContext,
			Stopped:        !s.state.player.IsPlaying,
			Paused:         s.state.player.IsPaused,
			Buffering:      s.state.player.IsBuffering,
			PlayOrigin:     s.state.player.PlayOrigin.FeatureIdentifier,
		}

		if s.stream != nil && s.prodInfo != nil {
			resp.Track = NewApiResponseStatusTrack(s.stream.Track, s.prodInfo, s.state.trackPosition())
		}

		return resp, nil
	case ApiRequestTypeResume:
		_ = s.play()
		return nil, nil
	case ApiRequestTypePause:
		_ = s.pause()
		return nil, nil
	case ApiRequestTypeSeek:
		_ = s.seek(req.Data.(int64))
		return nil, nil
	case ApiRequestTypePrev:
		_ = s.skipPrev()
		return nil, nil
	case ApiRequestTypeNext:
		_ = s.skipNext()
		return nil, nil
	case ApiRequestTypePlay:
		data := req.Data.(ApiRequestDataPlay)
		ctx, err := s.sp.ContextResolve(data.Uri)
		if err != nil {
			return nil, fmt.Errorf("failed resolving context: %w", err)
		}

		s.state.setActive(true)
		s.state.player.PlaybackSpeed = 1
		s.state.player.Suppressions = &connectpb.Suppressions{}
		s.state.player.PlayOrigin = &connectpb.PlayOrigin{
			FeatureIdentifier: "go-librespot",
			FeatureVersion:    librespot.VersionNumberString(),
		}

		if err := s.loadContext(ctx, func(track *connectpb.ContextTrack) bool {
			return len(data.SkipToUri) != 0 && data.SkipToUri == track.Uri
		}, data.Paused); err != nil {
			return nil, fmt.Errorf("failed loading context: %w", err)
		}

		return nil, nil
	case ApiRequestTypeGetVolume:
		return &ApiResponseVolume{
			Max:   s.app.cfg.VolumeSteps,
			Value: s.state.device.Volume * s.app.cfg.VolumeSteps / player.MaxStateVolume,
		}, nil
	case ApiRequestTypeSetVolume:
		vol := req.Data.(uint32)
		s.updateVolume(vol * player.MaxStateVolume / s.app.cfg.VolumeSteps)
		return nil, nil
	case ApiRequestTypeSetRepeatingContext:
		s.setRepeatingContext(req.Data.(bool))
		return nil, nil
	case ApiRequestTypeSetRepeatingTrack:
		s.setRepeatingTrack(req.Data.(bool))
		return nil, nil
	case ApiRequestTypeSetShufflingContext:
		s.setShufflingContext(req.Data.(bool))
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown request type: %s", req.Type)
	}
}

func (s *Session) Connect(creds SessionCredentials) (err error) {
	s.stop = make(chan struct{}, 1)

	// init login5
	s.login5 = login5.NewLogin5(s.app.deviceId, s.app.clientToken)

	// connect and authenticate to the accesspoint
	apAddr, err := s.app.resolver.GetAccesspoint()
	if err != nil {
		return fmt.Errorf("failed getting accesspoint from resolver: %w", err)
	}

	s.ap, err = ap.NewAccesspoint(apAddr, s.app.deviceId)
	if err != nil {
		return fmt.Errorf("failed initializing accesspoint: %w", err)
	}

	// choose proper credentials
	switch creds := creds.(type) {
	case SessionStoredCredentials:
		if err = s.ap.ConnectStored(creds.Username, creds.Data); err != nil {
			return fmt.Errorf("failed authenticating accesspoint with stored credentials: %w", err)
		}
	case SessionUserPassCredentials:
		if err = s.ap.ConnectUserPass(creds.Username, creds.Password); err != nil {
			return fmt.Errorf("failed authenticating accesspoint with username and password: %w", err)
		}
	case SessionSpotifyTokenCredentials:
		if err = s.ap.ConnectSpotifyToken(creds.Username, creds.Token); err != nil {
			return fmt.Errorf("failed authenticating accesspoint with username and spotify token: %w", err)
		}
	case SessionBlobCredentials:
		if err = s.ap.ConnectBlob(creds.Username, creds.Blob); err != nil {
			return fmt.Errorf("failed authenticating accesspoint with blob: %w", err)
		}
	default:
		panic("unknown credentials")
	}

	// authenticate with login5 and get token
	if err = s.login5.Login(&credentialspb.StoredCredential{
		Username: s.ap.Username(),
		Data:     s.ap.StoredCredentials(),
	}); err != nil {
		return fmt.Errorf("failed authenticating with login5: %w", err)
	}

	// initialize spclient
	spAddr, err := s.app.resolver.GetSpclient()
	if err != nil {
		return fmt.Errorf("failed getting spclient from resolver: %w", err)
	}

	s.sp, err = spclient.NewSpclient(spAddr, s.login5.AccessToken(), s.app.deviceId, s.app.clientToken)
	if err != nil {
		return fmt.Errorf("failed initializing spclient: %w", err)
	}

	// initialize dealer
	dealerAddr, err := s.app.resolver.GetDealer()
	if err != nil {
		return fmt.Errorf("failed getting dealer from resolver: %w", err)
	}

	s.dealer, err = dealer.NewDealer(dealerAddr, s.login5.AccessToken())
	if err != nil {
		return fmt.Errorf("failed connecting to dealer: %w", err)
	}

	// init internal state
	s.initState()

	// init audio key provider
	s.audioKey = audio.NewAudioKeyProvider(s.ap)

	// init player
	s.player, err = player.NewPlayer(s.sp, s.audioKey, s.countryCode, s.app.cfg.AudioDevice, s.app.cfg.VolumeSteps)
	if err != nil {
		return fmt.Errorf("failed initializing player: %w", err)
	}

	return nil
}

func (s *Session) Close() {
	s.stop <- struct{}{}
	s.player.Close()
	s.audioKey.Close()
	s.dealer.Close()
	s.ap.Close()
}

func (s *Session) Run(apiRecv <-chan ApiRequest) {
	apRecv := s.ap.Receive(ap.PacketTypeProductInfo, ap.PacketTypeCountryCode)
	msgRecv := s.dealer.ReceiveMessage("hm://pusher/v1/connections/", "hm://connect-state/v1/")
	reqRecv := s.dealer.ReceiveRequest("hm://connect-state/v1/player/command")
	playerRecv := s.player.Receive()

	for {
		select {
		case <-s.stop:
			return
		case pkt := <-apRecv:
			if err := s.handleAccesspointPacket(pkt.Type, pkt.Payload); err != nil {
				log.WithError(err).Warn("failed handling accesspoint packet")
			}
		case msg := <-msgRecv:
			if err := s.handleDealerMessage(msg); err != nil {
				log.WithError(err).Warn("failed handling dealer message")
			}
		case req := <-reqRecv:
			if err := s.handleDealerRequest(req); err != nil {
				log.WithError(err).Warn("failed handling dealer request")
				req.Reply(false)
			} else {
				log.Debugf("sending successful reply for delaer request")
				req.Reply(true)
			}
		case req := <-apiRecv:
			data, err := s.handleApiRequest(req)
			req.Reply(data, err)
		case ev := <-playerRecv:
			s.handlePlayerEvent(&ev)
		}
	}
}
