package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devgianlu/go-librespot/mpris"
	"github.com/godbus/dbus/v5"
	"google.golang.org/protobuf/proto"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/dealer"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/session"
	"github.com/devgianlu/go-librespot/tracks"
)

type AppPlayer struct {
	app  *App
	sess *session.Session

	stop   chan struct{}
	logout chan *AppPlayer

	player            *player.Player
	initialVolumeOnce sync.Once
	volumeUpdate      chan float32

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
		return nil
	case ap.PacketTypeCountryCode:
		*p.countryCode = string(payload)
		return nil
	default:
		return nil
	}
}

func (p *AppPlayer) handleDealerMessage(ctx context.Context, msg dealer.Message) error {
	// Limit ourselves to 30 seconds for handling dealer messages
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if strings.HasPrefix(msg.Uri, "hm://pusher/v1/connections/") {
		p.spotConnId = msg.Headers["Spotify-Connection-Id"]
		p.app.log.Debugf("received connection id: %s...%s", p.spotConnId[:16], p.spotConnId[len(p.spotConnId)-16:])

		// put the initial state
		if err := p.putConnectState(ctx, connectpb.PutStateReason_NEW_DEVICE); err != nil {
			return fmt.Errorf("failed initial state put: %w", err)
		}

		if !p.app.cfg.ExternalVolume && len(p.app.cfg.MixerDevice) == 0 {
			// update initial volume
			p.initialVolumeOnce.Do(func() {
				if lastVolume := p.app.state.LastVolume; !p.app.cfg.IgnoreLastVolume && lastVolume != nil {
					p.updateVolume(*lastVolume)
				} else {
					p.updateVolume(p.app.cfg.InitialVolume * player.MaxStateVolume / p.app.cfg.VolumeSteps)
				}
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
		p.app.log.WithField("username", librespot.ObfuscateUsername(p.sess.Username())).
			Debugf("requested logout out")
		p.logout <- p
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/cluster") {
		var clusterUpdate connectpb.ClusterUpdate
		if err := proto.Unmarshal(msg.Payload, &clusterUpdate); err != nil {
			return fmt.Errorf("failed unmarshalling ClusterUpdate: %w", err)
		}

		stopBeingActive := p.state.active && clusterUpdate.Cluster.ActiveDeviceId != p.app.deviceId && clusterUpdate.Cluster.PlayerState.Timestamp > p.state.lastTransferTimestamp

		// We are still the active device, do not quit
		if !stopBeingActive {
			return nil
		}

		name := "<unknown>"
		if device := clusterUpdate.Cluster.Device[clusterUpdate.Cluster.ActiveDeviceId]; device != nil {
			name = device.Name
		}
		p.app.log.Infof("playback was transferred to %s", name)

		return p.stopPlayback(ctx)
	}

	return nil
}

func (p *AppPlayer) handlePlayerCommand(ctx context.Context, req dealer.RequestPayload) error {
	p.state.lastCommand = &req

	p.app.log.Debugf("handling %s player command from %s", req.Command.Endpoint, req.SentByDeviceId)

	switch req.Command.Endpoint {
	case "transfer":
		if len(req.Command.Data) == 0 {
			p.app.server.Emit(&ApiEvent{
				Type: ApiEventTypeActive,
			})

			return nil
		}

		var transferState connectpb.TransferState
		if err := proto.Unmarshal(req.Command.Data, &transferState); err != nil {
			return fmt.Errorf("failed unmarshalling TransferState: %w", err)
		}
		p.state.lastTransferTimestamp = transferState.Playback.Timestamp

		ctxTracks, err := tracks.NewTrackListFromContext(ctx, p.app.log, p.sess.Spclient(), transferState.CurrentSession.Context)
		if err != nil {
			return fmt.Errorf("failed creating track list: %w", err)
		}

		if sessId := transferState.CurrentSession.OriginalSessionId; sessId != nil {
			p.state.player.SessionId = *sessId
		} else {
			sessionId := make([]byte, 16)
			_, _ = rand.Read(sessionId)
			p.state.player.SessionId = base64.StdEncoding.EncodeToString(sessionId)
		}

		p.state.setActive(true)
		p.state.player.IsPlaying = false
		p.state.player.IsBuffering = false

		// options
		p.state.player.Options = transferState.Options
		pause := transferState.Playback.IsPaused && req.Command.Options.RestorePaused != "resume"
		// playback
		// Note: this sets playback speed to 0 or 1 because that's all we're
		// capable of, depending on whether the playback is paused or not.
		p.state.player.Timestamp = transferState.Playback.Timestamp
		p.state.player.PositionAsOfTimestamp = int64(transferState.Playback.PositionAsOfTimestamp)
		p.state.setPaused(pause)

		// current session
		p.state.player.PlayOrigin = transferState.CurrentSession.PlayOrigin
		p.state.player.PlayOrigin.DeviceIdentifier = req.SentByDeviceId
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
		currentTrack := librespot.ContextTrackToProvidedTrack(contextSpotType, transferState.Playback.CurrentTrack)
		if err := ctxTracks.TrySeek(ctx, tracks.ProvidedTrackComparator(contextSpotType, currentTrack)); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}

		// shuffle the context if needed
		if err := ctxTracks.ToggleShuffle(ctx, transferState.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}

		// Set queueID to the highest queue ID found in the queue.
		// The UIDs are of the form q0, q1, q2, etc.
		// Spotify apps don't seem to do this (they start again at 0 after
		// transfer), which means that queue IDs get duplicated when tracks are
		// added before and after the transfer and reordering will lead to weird
		// effects. But we can do better :)
		p.state.queueID = 0
		for _, track := range transferState.Queue.Tracks {
			if track.Uid == "" || track.Uid[0] != 'q' {
				continue // not of the "q<number>" format
			}
			n, err := strconv.ParseUint(track.Uid[1:], 10, 64)
			if err != nil {
				continue // not of the "q<number>" format
			}
			p.state.queueID = max(p.state.queueID, n)
		}

		// add all tracks from queue
		for _, track := range transferState.Queue.Tracks {
			ctxTracks.AddToQueue(track)
		}
		ctxTracks.SetPlayingQueue(transferState.Queue.IsPlayingQueue)

		p.state.tracks = ctxTracks
		p.state.player.Track = ctxTracks.CurrentTrack()
		p.state.player.PrevTracks = ctxTracks.PrevTracks()
		p.state.player.NextTracks = ctxTracks.NextTracks(ctx, nil)
		p.state.player.Index = ctxTracks.Index()

		// load current track into stream
		if err := p.loadCurrentTrack(ctx, pause, true); err != nil {
			return fmt.Errorf("failed loading current track (transfer): %w", err)
		}

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeActive,
		})

		return nil
	case "play":
		p.state.setActive(true)

		p.state.player.PlayOrigin = req.Command.PlayOrigin
		p.state.player.PlayOrigin.DeviceIdentifier = req.SentByDeviceId
		p.state.player.Suppressions = req.Command.Options.Suppressions

		// apply overrides
		if req.Command.Options.PlayerOptionsOverride != nil {
			p.state.player.Options.ShufflingContext = req.Command.Options.PlayerOptionsOverride.ShufflingContext
			p.state.player.Options.RepeatingTrack = req.Command.Options.PlayerOptionsOverride.RepeatingTrack
			p.state.player.Options.RepeatingContext = req.Command.Options.PlayerOptionsOverride.RepeatingContext
		}

		var skipTo skipToFunc
		if len(req.Command.Options.SkipTo.TrackUri) > 0 || len(req.Command.Options.SkipTo.TrackUid) > 0 || req.Command.Options.SkipTo.TrackIndex > 0 {
			index := -1
			skipTo = func(track *connectpb.ContextTrack) bool {
				if len(req.Command.Options.SkipTo.TrackUid) > 0 && req.Command.Options.SkipTo.TrackUid == track.Uid {
					return true
				} else if len(req.Command.Options.SkipTo.TrackUri) > 0 && req.Command.Options.SkipTo.TrackUri == track.Uri {
					return true
					// the following length checks are needed, because the TrackIndex corresponds to an offset relative to the current playlist or album
					// If there are multiple albums in the current context (e.g. when starting from an artists page, the TrackIndex would indicate, that
					// you started the xth track vom the first album, even if you started the xth track from the second or third album etc.)
				} else if req.Command.Options.SkipTo.TrackIndex != 0 && len(req.Command.Options.SkipTo.TrackUri) == 0 && len(req.Command.Options.SkipTo.TrackUid) == 0 {
					index += 1
					return index == req.Command.Options.SkipTo.TrackIndex
				} else {
					return false
				}
			}
		}

		return p.loadContext(ctx, req.Command.Context, skipTo, req.Command.Options.InitiallyPaused, true)
	case "pause":
		return p.pause(ctx)
	case "resume":
		return p.play(ctx)
	case "seek_to":
		var position int64
		if req.Command.Relative == "current" {
			position = p.player.PositionMs() + req.Command.Position
		} else if req.Command.Relative == "beginning" {
			position = req.Command.Position
		} else if req.Command.Relative == "" {
			if pos, ok := req.Command.Value.(float64); ok {
				position = int64(pos)
			} else {
				p.app.log.Warnf("unsupported seek_to position type: %T", req.Command.Value)
				return nil
			}
		} else {
			p.app.log.Warnf("unsupported seek_to relative position: %s", req.Command.Relative)
			return nil
		}

		if err := p.seek(ctx, position); err != nil {
			return fmt.Errorf("failed seeking stream: %w", err)
		}

		return nil
	case "skip_prev":
		return p.skipPrev(ctx, req.Command.Options.AllowSeeking)
	case "skip_next":
		return p.skipNext(ctx, req.Command.Track)
	case "update_context":
		if req.Command.Context.Uri != p.state.player.ContextUri {
			p.app.log.Warnf("ignoring context update for wrong uri: %s", req.Command.Context.Uri)
			return nil
		}

		p.state.player.ContextRestrictions = req.Command.Context.Restrictions
		if p.state.player.ContextMetadata == nil {
			p.state.player.ContextMetadata = map[string]string{}
		}
		for k, v := range req.Command.Context.Metadata {
			p.state.player.ContextMetadata[k] = v
		}

		p.updateState(ctx)
		return nil
	case "set_repeating_context":
		val := req.Command.Value.(bool)
		p.setOptions(ctx, &val, nil, nil)
		return nil
	case "set_repeating_track":
		val := req.Command.Value.(bool)
		p.setOptions(ctx, nil, &val, nil)
		return nil
	case "set_shuffling_context":
		val := req.Command.Value.(bool)
		p.setOptions(ctx, nil, nil, &val)
		return nil
	case "set_options":
		p.setOptions(ctx, req.Command.RepeatingContext, req.Command.RepeatingTrack, req.Command.ShufflingContext)
		return nil
	case "set_queue":
		p.setQueue(ctx, req.Command.PrevTracks, req.Command.NextTracks)
		return nil
	case "add_to_queue":
		p.addToQueue(ctx, req.Command.Track)
		return nil
	default:
		return fmt.Errorf("unsupported player command: %s", req.Command.Endpoint)
	}
}

func (p *AppPlayer) handleDealerRequest(ctx context.Context, req dealer.Request) error {
	// Limit ourselves to 30 seconds for handling dealer requests
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch req.MessageIdent {
	case "hm://connect-state/v1/player/command":
		return p.handlePlayerCommand(ctx, req.Payload)
	default:
		p.app.log.Warnf("unknown dealer request: %s", req.MessageIdent)
		return nil
	}
}

func (p *AppPlayer) handleApiRequest(ctx context.Context, req ApiRequest) (any, error) {
	// Limit ourselves to 30 seconds for handling API requests
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch req.Type {
	case ApiRequestTypeWebApi:
		data := req.Data.(ApiRequestDataWebApi)
		resp, err := p.sess.WebApi(ctx, data.Method, data.Path, data.Query, nil, nil)
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
			DeviceName:     p.app.cfg.DeviceName,
			VolumeSteps:    p.app.cfg.VolumeSteps,
			Volume:         p.apiVolume(),
			RepeatContext:  p.state.player.Options.RepeatingContext,
			RepeatTrack:    p.state.player.Options.RepeatingTrack,
			ShuffleContext: p.state.player.Options.ShufflingContext,
			Stopped:        !p.state.player.IsPlaying,
			Paused:         p.state.player.IsPaused,
			Buffering:      p.state.player.IsBuffering,
			PlayOrigin:     p.state.player.PlayOrigin.FeatureIdentifier,
		}

		if p.primaryStream != nil && p.prodInfo != nil {
			resp.Track = p.newApiResponseStatusTrack(p.primaryStream.Media, p.state.trackPosition())
		}

		return resp, nil
	case ApiRequestTypeResume:
		_ = p.play(ctx)
		return nil, nil
	case ApiRequestTypePause:
		_ = p.pause(ctx)
		return nil, nil
	case ApiRequestTypePlayPause:
		if p.state.player.IsPaused {
			_ = p.play(ctx)
		} else {
			_ = p.pause(ctx)
		}
		return nil, nil
	case ApiRequestTypeSeek:
		data := req.Data.(ApiRequestDataSeek)

		var position int64
		if data.Relative {
			position = p.player.PositionMs() + data.Position
		} else {
			position = data.Position
		}

		_ = p.seek(ctx, position)
		return nil, nil
	case ApiRequestTypePrev:
		_ = p.skipPrev(ctx, true)
		return nil, nil
	case ApiRequestTypeNext:
		data := req.Data.(ApiRequestDataNext)
		if data.Uri != nil {
			_ = p.skipNext(ctx, &connectpb.ContextTrack{Uri: *data.Uri})
		} else {
			_ = p.skipNext(ctx, nil)
		}
		return nil, nil
	case ApiRequestTypePlay:
		data := req.Data.(ApiRequestDataPlay)
		spotCtx, err := p.sess.Spclient().ContextResolve(ctx, data.Uri)
		if err != nil {
			return nil, fmt.Errorf("failed resolving context: %w", err)
		}

		p.state.setActive(true)
		p.state.setPaused(data.Paused)
		p.state.player.Suppressions = &connectpb.Suppressions{}
		p.state.player.PlayOrigin = &connectpb.PlayOrigin{
			FeatureIdentifier: "go-librespot",
			FeatureVersion:    librespot.VersionNumberString(),
		}

		var skipTo skipToFunc
		if len(data.SkipToUri) > 0 {
			skipToId, err := librespot.SpotifyIdFromUri(data.SkipToUri)
			if err != nil {
				p.app.log.WithError(err).Warnf("trying to skip to invalid uri: %s", data.SkipToUri)
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

		if err := p.loadContext(ctx, spotCtx, skipTo, data.Paused, true); err != nil {
			return nil, fmt.Errorf("failed loading context: %w", err)
		}

		return nil, nil
	case ApiRequestTypeGetVolume:
		return &ApiResponseVolume{
			Max:   p.app.cfg.VolumeSteps,
			Value: p.apiVolume(),
		}, nil
	case ApiRequestTypeSetVolume:
		data := req.Data.(ApiRequestDataVolume)

		var volume int32
		if data.Relative {
			volume = int32(p.apiVolume())
			volume += data.Volume
			volume = max(min(volume, int32(p.app.cfg.VolumeSteps)), 0)
		} else {
			volume = data.Volume
		}

		p.updateVolume(uint32(volume) * player.MaxStateVolume / p.app.cfg.VolumeSteps)
		return nil, nil
	case ApiRequestTypeSetRepeatingContext:
		val := req.Data.(bool)
		p.setOptions(ctx, &val, nil, nil)
		return nil, nil
	case ApiRequestTypeSetRepeatingTrack:
		val := req.Data.(bool)
		p.setOptions(ctx, nil, &val, nil)
		return nil, nil
	case ApiRequestTypeSetShufflingContext:
		val := req.Data.(bool)
		p.setOptions(ctx, nil, nil, &val)
		return nil, nil
	case ApiRequestTypeAddToQueue:
		p.addToQueue(ctx, &connectpb.ContextTrack{Uri: req.Data.(string)})
		return nil, nil
	case ApiRequestTypeToken:
		accessToken, err := p.sess.Spclient().GetAccessToken(ctx, true)
		if err != nil {
			return nil, fmt.Errorf("failed getting access token: %w", err)
		}
		return &ApiResponseToken{
			Token: accessToken,
		}, nil
	default:
		return nil, fmt.Errorf("unknown request type: %s", req.Type)
	}
}

func pointer[T any](d T) *T {
	return &d
}

func (p *AppPlayer) handleMprisEvent(ctx context.Context, req mpris.MediaPlayer2PlayerCommand) error {
	// Limit ourselves to 30 seconds for handling mpris commands
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch req.Type {
	case mpris.MediaPlayer2PlayerCommandTypeNext:
		return p.skipNext(ctx, nil)
	case mpris.MediaPlayer2PlayerCommandTypePrevious:
		return p.skipPrev(ctx, true)
	case mpris.MediaPlayer2PlayerCommandTypePlay:
		return p.play(ctx)
	case mpris.MediaPlayer2PlayerCommandTypePause:
		return p.pause(ctx)
	case mpris.MediaPlayer2PlayerCommandTypePlayPause:
		if p.state.player.IsPaused {
			return p.play(ctx)
		} else {
			return p.pause(ctx)
		}
	case mpris.MediaPlayer2PlayerCommandTypeStop:
		return p.stopPlayback(ctx)
	case mpris.MediaPlayer2PlayerCommandLoopStatusChanged:
		p.app.log.Tracef("mpris loop status argument %s", req.Argument)
		dt := req.Argument
		switch dt {
		case mpris.None:
			p.setOptions(ctx, pointer(false), pointer(false), nil)
		case mpris.Playlist:
			p.setOptions(ctx, pointer(true), pointer(false), nil)
		case mpris.Track:
			p.setOptions(ctx, pointer(true), pointer(true), nil)
		default:
			p.app.log.Warnf("mpris loop status argument is invalid (%s)", req.Argument)
		}
		return nil
	case mpris.MediaPlayer2PlayerCommandShuffleChanged:
		sh := req.Argument.(bool)
		p.setOptions(ctx, nil, nil, &sh)
		return nil
	case mpris.MediaPlayer2PlayerCommandVolumeChanged:
		volRelative := req.Argument.(float64)
		volAbs := uint32(player.MaxStateVolume * volRelative)

		p.updateVolume(volAbs)
		return nil
	case mpris.MediaPlayer2PlayerCommandTypeSetPosition:
		arg := req.Argument.(mpris.MediaPlayer2CommandSetPositionPayload)

		p.app.log.Tracef("media player set position argument: %v", arg)

		if arg.ObjectPath.IsValid() {
			spotifyId := strings.Join(strings.Split(string(arg.ObjectPath), "/")[3:], ":")
			if spotifyId != p.state.player.Track.Uri {
				return fmt.Errorf("seek tries to jump to different uri, not yet supported (got: %s, expected: %s)", spotifyId, p.state.player.Track.Uri)
			}
		}

		newPositionAbs := arg.PositionUs / 1000
		return p.seek(ctx, newPositionAbs)
	case mpris.MediaPlayer2PlayerCommandTypeSeek:
		newPosAbs := p.player.PositionMs() + req.Argument.(int64)/1000
		return p.seek(ctx, newPosAbs)
	case mpris.MediaPlayer2PlayerCommandTypeOpenUri, mpris.MediaPlayer2PlayerCommandRateChanged:
		p.app.log.Warnf("unimplemented mpris event %d", req.Type)
		return fmt.Errorf("unimplemented mpris event %d", req.Type)
	}
	return nil
}

func (p *AppPlayer) Close() {
	p.stop <- struct{}{}
	p.player.Close()
	p.sess.Close()
}

func (p *AppPlayer) Run(ctx context.Context, apiRecv <-chan ApiRequest, mprisRecv <-chan mpris.MediaPlayer2PlayerCommand) {
	err := p.sess.Dealer().Connect(ctx)
	if err != nil {
		p.app.log.WithError(err).Error("failed connecting to dealer")
		p.Close()
		return
	}

	apRecv := p.sess.Accesspoint().Receive(ap.PacketTypeProductInfo, ap.PacketTypeCountryCode)
	msgRecv := p.sess.Dealer().ReceiveMessage("hm://pusher/v1/connections/", "hm://connect-state/v1/")
	reqRecv := p.sess.Dealer().ReceiveRequest("hm://connect-state/v1/player/command")
	playerRecv := p.player.Receive()

	volumeTimer := time.NewTimer(time.Minute)
	volumeTimer.Stop() // don't emit a volume change event at start

	for {
		select {
		case <-p.stop:
			return
		case pkt, ok := <-apRecv:
			if !ok {
				continue
			}

			if err := p.handleAccesspointPacket(pkt.Type, pkt.Payload); err != nil {
				p.app.log.WithError(err).Warn("failed handling accesspoint packet")
			}
		case msg, ok := <-msgRecv:
			if !ok {
				continue
			}

			if err := p.handleDealerMessage(ctx, msg); err != nil {
				p.app.log.WithError(err).Warn("failed handling dealer message")
			}
		case req, ok := <-reqRecv:
			if !ok {
				continue
			}

			if err := p.handleDealerRequest(ctx, req); err != nil {
				p.app.log.WithError(err).Warn("failed handling dealer request")
				req.Reply(false)
			} else {
				p.app.log.Debugf("sending successful reply for dealer request")
				req.Reply(true)
			}
		case req, ok := <-apiRecv:
			if !ok {
				continue
			}

			data, err := p.handleApiRequest(ctx, req)
			req.Reply(data, err)
		case mprisReq, ok := <-mprisRecv:
			if !ok {
				continue
			}

			p.app.log.Tracef("new mpris message %v", mprisReq)
			err := p.handleMprisEvent(ctx, mprisReq)
			dbusError := mpris.MediaPlayer2PlayerCommandResponse{
				Err: &dbus.Error{},
			}
			if err != nil {
				dbusError.Err.Name = err.Error()
			} else {
				dbusError.Err = nil
			}
			mprisReq.Reply(dbusError)
		case ev, ok := <-playerRecv:
			if !ok {
				continue
			}

			p.handlePlayerEvent(ctx, &ev)
		case <-p.prefetchTimer.C:
			p.prefetchNext(ctx)
		case volume := <-p.volumeUpdate:
			// Received a new volume: from Spotify Connect, from the REST API,
			// or from the system volume mixer.
			// Because these updates can be quite frequent, we have to rate
			// limit them (otherwise we get HTTP error 429: Too many requests
			// for user).
			p.state.device.Volume = uint32(math.Round(float64(volume * player.MaxStateVolume)))
			volumeTimer.Reset(100 * time.Millisecond)
		case <-volumeTimer.C:
			// We've gone some time without update, send the new value now.
			p.volumeUpdated(ctx)
		}
	}
}
