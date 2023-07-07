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
	"sync"
	"time"
)

const VolumeSteps = 64

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

	// TODO: consider not locking this if we are modifying it always from the same routine
	state     *State
	stateLock sync.Mutex

	// TODO: this should probably be locked
	stream *player.Stream
}

func (s *Session) handleAccesspointPacket(pktType ap.PacketType, payload []byte) error {
	switch pktType {
	case ap.PacketTypeProductInfo:
		var prod ProductInfo
		if err := xml.Unmarshal(payload, &prod); err != nil {
			return fmt.Errorf("failed umarshalling ProductInfo: %w", err)
		}

		// TODO: we may need this
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

		var stopBeingActive bool
		s.withState(func(ss *State) {
			stopBeingActive = ss.isActive && clusterUpdate.Cluster.ActiveDeviceId != s.app.deviceId
		})

		// We are still the active device, do not quit
		if !stopBeingActive {
			return nil
		}

		if s.stream != nil {
			s.stream.Stop()
		}

		// FIXME: Fails with 422: Ignoring BECAME_INACTIVE
		s.withState(func(s *State) { s.reset() })
		if err := s.putConnectState(connectpb.PutStateReason_BECAME_INACTIVE); err != nil {
			return fmt.Errorf("failed inactive state put: %w", err)
		}

		// TODO: logout if using zeroconf (?)
	}

	return nil
}

func (s *Session) handlePlayerCommand(req dealer.RequestPayload) error {
	s.withState(func(s *State) { s.lastCommand = &req })

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

		s.withState(func(s *State) {
			s.isActive = true
			s.playerState.IsPlaying = false
			s.playerState.IsBuffering = false
			s.playerState.IsPaused = false

			// options
			s.playerState.Options = transferState.Options

			// playback
			s.playerState.Timestamp = transferState.Playback.Timestamp
			s.playerState.PositionAsOfTimestamp = int64(transferState.Playback.PositionAsOfTimestamp)
			s.playerState.PlaybackSpeed = transferState.Playback.PlaybackSpeed
			s.playerState.IsPaused = transferState.Playback.IsPaused

			// current session
			s.playerState.PlayOrigin = transferState.CurrentSession.PlayOrigin
			s.playerState.ContextUri = transferState.CurrentSession.Context.Uri
			s.playerState.ContextUrl = transferState.CurrentSession.Context.Url
			s.playerState.ContextRestrictions = transferState.CurrentSession.Context.Restrictions
			s.playerState.Suppressions = transferState.CurrentSession.Suppressions

			s.playerState.ContextMetadata = map[string]string{}
			for k, v := range transferState.CurrentSession.Context.Metadata {
				s.playerState.ContextMetadata[k] = v
			}
			for k, v := range tracks.Metadata() {
				s.playerState.ContextMetadata[k] = v
			}

			// queue
			// TODO: transfer queue
		})

		currentTrack := librespot.ContextTrackToProvidedTrack(transferState.Playback.CurrentTrack)
		if err := tracks.Seek(func(track *connectpb.ContextTrack) bool {
			if len(track.Uid) > 0 && track.Uid == currentTrack.Uid {
				return true
			} else if len(track.Uri) > 0 && track.Uri == currentTrack.Uri {
				return true
			} else if len(track.Gid) > 0 && librespot.TrackId(track.Gid).Uri() == currentTrack.Uri {
				return true
			} else {
				return false
			}
		}); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}

		s.withState(func(s *State) {
			s.tracks = tracks
			s.playerState.Track = tracks.CurrentTrack()
			s.playerState.PrevTracks = tracks.PrevTracks()
			s.playerState.NextTracks = tracks.NextTracks()
			s.playerState.Index = tracks.Index()
		})

		// load current track into stream
		if err := s.loadCurrentTrack(); err != nil {
			return fmt.Errorf("failed loading current track: %w", err)
		}

		// start playing if not initially paused
		if !transferState.Playback.IsPaused {
			if err := s.play(); err != nil {
				return fmt.Errorf("failed playing: %w", err)
			}
		}

		return nil
	case "play":
		tracks, err := NewTrackListFromContext(s.sp, req.Command.Context)
		if err != nil {
			return fmt.Errorf("failed creating track list: %w", err)
		}

		s.withState(func(s *State) {
			s.playerState.IsPaused = req.Command.Options.InitiallyPaused

			s.playerState.PlayOrigin = req.Command.PlayOrigin
			s.playerState.ContextUri = req.Command.Context.Uri
			s.playerState.ContextUrl = req.Command.Context.Url
			s.playerState.ContextRestrictions = req.Command.Context.Restrictions
			s.playerState.Suppressions = req.Command.Options.Suppressions

			s.playerState.Timestamp = time.Now().UnixMilli()
			s.playerState.PositionAsOfTimestamp = 0
		})

		// if we fail to seek, just fallback to the first track
		tracks.TrySeek(func(track *connectpb.ContextTrack) bool {
			if len(req.Command.Options.SkipTo.TrackUid) > 0 && req.Command.Options.SkipTo.TrackUid == track.Uid {
				return true
			} else if len(req.Command.Options.SkipTo.TrackUri) > 0 && req.Command.Options.SkipTo.TrackUri == track.Uri {
				return true
			} else {
				return false
			}
		})

		s.withState(func(s *State) {
			s.tracks = tracks
			s.playerState.Track = tracks.CurrentTrack()
			s.playerState.PrevTracks = tracks.PrevTracks()
			s.playerState.NextTracks = tracks.NextTracks()
			s.playerState.Index = tracks.Index()
		})

		// load current track into stream
		if err := s.loadCurrentTrack(); err != nil {
			return fmt.Errorf("failed loading current track: %w", err)
		}

		// start playing if not initially paused
		if !req.Command.Options.InitiallyPaused {
			if err := s.play(); err != nil {
				return fmt.Errorf("failed playing: %w", err)
			}
		}

		return nil
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

		s.app.server.Emit(&ApiEvent{
			Type: "seek",
			Data: ApiEventDataSeek{
				Position: int(req.Command.Position),
				Duration: int(*s.stream.Track.Duration),
			},
		})
		return nil
	case "skip_prev":
		// TODO: handle rewinding track if pos < 3000ms
		return s.skipPrev()
	case "skip_next":
		return s.skipNext()
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

func (s *Session) Connect(creds_ SessionCredentials) (err error) {
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
	switch creds := creds_.(type) {
	case SessionUserPassCredentials:
		if err = s.ap.ConnectUserPass(creds.Username, creds.Password); err != nil {
			return fmt.Errorf("failed authenticating accesspoint with username and password: %w", err)
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
	s.player, err = player.NewPlayer(s.sp, s.audioKey)
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

func (s *Session) Run() {
	apRecv := s.ap.Receive(ap.PacketTypeProductInfo)
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
		case ev := <-playerRecv:
			s.handlePlayerEvent(&ev)
		}
	}
}
