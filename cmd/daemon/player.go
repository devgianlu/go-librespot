package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/dealer"
	"github.com/devgianlu/go-librespot/output"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/session"
	"github.com/devgianlu/go-librespot/tracks"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"io"
	"math"
	"strings"
	"sync"
	"time"
)

type AppPlayer struct {
	app  *App
	sess *session.Session

	stop   chan struct{}
	logout chan *AppPlayer

	player               *player.Player
	initialVolumeOnce    sync.Once
	externalVolumeUpdate *output.RingBuffer[float32]

	spotConnId string

	prodInfo    *ProductInfo
	countryCode *string

	state           *State
	primaryStream   *player.Stream
	secondaryStream *player.Stream

	prefetchTimer *time.Timer
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
		log.Debugf("autoplay enabled: %t", p.prodInfo.AutoplayEnabled())
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

		if !p.app.cfg.ExternalVolume && len(*p.app.cfg.MixerDevice) == 0 {
			// update initial volume
			p.initialVolumeOnce.Do(func() {
				p.updateVolume(*p.app.cfg.InitialVolume * player.MaxStateVolume / *p.app.cfg.VolumeSteps)
			})
		}
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/connect/volume") {
		var setVolCmd connectpb.SetVolumeCommand
		if err := proto.Unmarshal(msg.Payload, &setVolCmd); err != nil {
			return fmt.Errorf("failed unmarshalling SetVolumeCommand: %w", err)
		}

		p.updateVolume(uint32(setVolCmd.Volume))
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/connect/logout") {
		// this should happen only with zeroconf enabled
		log.Debugf("requested logout out from %s", p.sess.Username())
		p.logout <- p
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

		p.player.Stop()
		p.primaryStream = nil
		p.secondaryStream = nil

		p.state.reset()
		if err := p.putConnectState(connectpb.PutStateReason_BECAME_INACTIVE); err != nil {
			return fmt.Errorf("failed inactive state put: %w", err)
		}

		p.schedulePrefetchNext()

		if p.app.cfg.ZeroconfEnabled {
			p.logout <- p
		}

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
		p.state.player.IsPaused = transferState.Playback.IsPaused

		if playbackSpeed := transferState.Playback.PlaybackSpeed; playbackSpeed != 0 {
			p.state.player.PlaybackSpeed = playbackSpeed
		} else {
			p.state.player.PlaybackSpeed = 1
		}

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

		contextSpotType := librespot.InferSpotifyIdTypeFromContextUri(p.state.player.ContextUri)
		currentTrack := librespot.ContextTrackToProvidedTrack(contextSpotType, transferState.Playback.CurrentTrack, "context")
		if err := ctxTracks.TrySeek(tracks.ProvidedTrackComparator(contextSpotType, currentTrack)); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}

		// shuffle the context if needed
		if err := ctxTracks.ToggleShuffle(transferState.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}

		// add all tracks from queue
		for _, track := range transferState.Queue.Tracks {
			ctxTracks.AddToQueue(track)
		}
		ctxTracks.SetPlayingQueue(transferState.Queue.IsPlayingQueue)

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

		// apply overrides
		p.state.player.Options.ShufflingContext = req.Command.Options.PlayerOptionsOverride.ShufflingContext
		p.state.player.Options.RepeatingTrack = req.Command.Options.PlayerOptionsOverride.RepeatingTrack
		p.state.player.Options.RepeatingContext = req.Command.Options.PlayerOptionsOverride.RepeatingContext

		var skipTo skipToFunc
		if len(req.Command.Options.SkipTo.TrackUri) > 0 || len(req.Command.Options.SkipTo.TrackUid) > 0 {
			skipTo = func(track *connectpb.ContextTrack) bool {
				if len(req.Command.Options.SkipTo.TrackUid) > 0 && req.Command.Options.SkipTo.TrackUid == track.Uid {
					return true
				} else if len(req.Command.Options.SkipTo.TrackUri) > 0 && req.Command.Options.SkipTo.TrackUri == track.Uri {
					return true
				} else {
					return false
				}
			}
		}

		return p.loadContext(req.Command.Context, skipTo, req.Command.Options.InitiallyPaused)
	case "pause":
		return p.pause()
	case "resume":
		return p.play()
	case "seek_to":
		var position int64
		if req.Command.Relative == "current" {
			position = p.player.PositionMs() + req.Command.Position
		} else if req.Command.Relative == "beginning" {
			position = req.Command.Position
		} else {
			log.Warnf("unsupported seek_to relative position: %s", req.Command.Relative)
			return nil
		}

		if err := p.seek(position); err != nil {
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
		val := req.Command.Value.(bool)
		p.setOptions(&val, nil, nil)
		return nil
	case "set_repeating_track":
		val := req.Command.Value.(bool)
		p.setOptions(nil, &val, nil)
		return nil
	case "set_shuffling_context":
		val := req.Command.Value.(bool)
		p.setOptions(nil, nil, &val)
		return nil
	case "set_options":
		p.setOptions(req.Command.RepeatingContext, req.Command.RepeatingTrack, req.Command.ShufflingContext)
		return nil
	case "set_queue":
		p.setQueue(req.Command.PrevTracks, req.Command.NextTracks)
		return nil
	case "add_to_queue":
		p.addToQueue(req.Command.Track)
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
	case ApiRequestTypeWebApi:
		data := req.Data.(ApiRequestDataWebApi)
		resp, err := p.sess.WebApi(data.Method, data.Path, data.Query, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to send web api request: %w", err)
		}

		defer func() { _ = resp.Body.Close() }()

		// this is the status we want to return to client not just 500
		switch resp.StatusCode {
		case 400:
			return nil, ErrBadRequest
		case 403:
			return nil, ErrForbidden
		case 404:
			return nil, ErrNotFound
		case 405:
			return nil, ErrMethodNotAllowed
		case 429:
			return nil, ErrTooManyRequests
		}

		// check for content type if not application/json
		if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("failed to read response body: %w", err)
			}

			return respBody, nil
		}

		// decode and return json
		var respJson any
		if err = json.NewDecoder(resp.Body).Decode(&respJson); err != nil {
			return nil, fmt.Errorf("failed to decode response body: %w", err)
		}

		return respJson, nil
	case ApiRequestTypeStatus:
		resp := &ApiResponseStatus{
			Username:       p.sess.Username(),
			DeviceId:       p.app.deviceId,
			DeviceType:     p.app.deviceType.String(),
			DeviceName:     *p.app.cfg.DeviceName,
			VolumeSteps:    *p.app.cfg.VolumeSteps,
			Volume:         p.state.device.Volume,
			RepeatContext:  p.state.player.Options.RepeatingContext,
			RepeatTrack:    p.state.player.Options.RepeatingTrack,
			ShuffleContext: p.state.player.Options.ShufflingContext,
			Stopped:        !p.state.player.IsPlaying,
			Paused:         p.state.player.IsPaused,
			Buffering:      p.state.player.IsBuffering,
			PlayOrigin:     p.state.player.PlayOrigin.FeatureIdentifier,
		}

		if p.primaryStream != nil && p.prodInfo != nil {
			resp.Track = NewApiResponseStatusTrack(p.primaryStream.Media, p.prodInfo, p.state.trackPosition())
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

		var skipTo skipToFunc
		if len(data.SkipToUri) > 0 {
			skipToId, err := librespot.SpotifyIdFromUriSafe(data.SkipToUri)
			if err != nil {
				log.WithError(err).Warnf("trying to skip to invalid uri: %s", data.SkipToUri)
				skipToId = nil
			}

			skipTo = func(track *connectpb.ContextTrack) bool {
				if len(track.Uri) > 0 {
					return data.SkipToUri == track.Uri
				} else if len(track.Gid) > 0 {
					return bytes.Equal(skipToId.Id(), track.Gid)
				} else {
					return false
				}
			}
		}

		if err := p.loadContext(ctx, skipTo, data.Paused); err != nil {
			return nil, fmt.Errorf("failed loading context: %w", err)
		}

		return nil, nil
	case ApiRequestTypeGetVolume:
		return &ApiResponseVolume{
			Max:   *p.app.cfg.VolumeSteps,
			Value: uint32(math.Ceil(float64(p.state.device.Volume**p.app.cfg.VolumeSteps) / player.MaxStateVolume)),
		}, nil
	case ApiRequestTypeSetVolume:
		vol := req.Data.(uint32)
		p.updateVolume(vol * player.MaxStateVolume / *p.app.cfg.VolumeSteps)
		return nil, nil
	case ApiRequestTypeSetRepeatingContext:
		val := req.Data.(bool)
		p.setOptions(&val, nil, nil)
		return nil, nil
	case ApiRequestTypeSetRepeatingTrack:
		val := req.Data.(bool)
		p.setOptions(nil, &val, nil)
		return nil, nil
	case ApiRequestTypeSetShufflingContext:
		val := req.Data.(bool)
		p.setOptions(nil, nil, &val)
		return nil, nil
	case ApiRequestTypeAddToQueue:
		p.addToQueue(&connectpb.ContextTrack{Uri: req.Data.(string)})
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
				log.Debugf("sending successful reply for dealer request")
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
