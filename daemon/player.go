package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
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
)

// AppPlayer owns the player's state and is the only thing that may touch it.
// Everything it holds is read and written from the one goroutine running Run,
// with no locking, which is what keeps the state consistent without any.
//
// The rule that makes that work: nothing reached from Run's select may block.
// Anything that talks to the network, the disk or a peer belongs on one of the
// lanes — the loader for track and context work, the state pusher for
// connect-state, a detached goroutine for what nobody waits on — and comes back
// as a result applied here. A handler that blocks stalls playback control,
// stops the dealer socket being read, and delays the state pushes controllers
// rely on to know the device is alive.
type AppPlayer struct {
	app  *App
	sess *session.Session

	ctx    context.Context
	cancel context.CancelFunc

	stop       chan struct{}
	closeOnce  sync.Once
	logout     chan *AppPlayer
	logoutOnce sync.Once

	player            *player.Player
	initialVolumeOnce sync.Once
	volumeUpdate      chan float32

	loader *loaderLane

	// loadGen stamps track and context work, prefetchGen what is fetched ahead
	// for the transition after it. They are separate because a queue edit or
	// an option change makes whatever was prefetched wrong without making the
	// track being loaded wrong: only the counter that moved discards results.
	loadGen     uint64
	prefetchGen uint64

	// loadInFlight is set while a track load is outstanding. The player is
	// handed its new stream from the loader lane, so the event announcing
	// playback can reach the loop before the load has been recorded; holding
	// those events until it has keeps handlers from seeing a half-loaded track.
	loadInFlight        bool
	pendingPlayerEvents []player.Event

	statePush         *statePushLane
	stateTimer        *time.Timer
	stateDirty        bool
	statePutScheduled bool
	lastStatePut      time.Time
	stateRetries      int
	stateSeq          uint64

	spotConnId string

	prodInfo *ProductInfo

	// countryCode is written here on the player loop and read from the loader
	// lane while a stream is being built.
	countryCode atomic.Pointer[string]

	hasSpotConnId          bool
	hasInitialConnectState bool
	hasCountryCode         bool
	playbackReadyCh        chan struct{}
	playbackReadyOnce      sync.Once

	state           *State
	primaryStream   *player.Stream
	secondaryStream *player.Stream

	// secondarySource is what the player was handed as the secondary.
	secondarySource librespot.AudioSource

	// narrationJumped records that the upcoming track is being reached by
	// jumping straight to it, so a DJ context introduces it with its jump line
	// rather than the one for arriving in sequence. Consumed by the next load.
	narrationJumped bool

	// resumeFinishedPlaybackId is the playback id of the stream most recently
	// reported as listened to the end, so that unloading it cannot overwrite
	// that with a position a moment short of the end.
	resumeFinishedPlaybackId []byte

	prefetchTimer *time.Timer

	// sleepTimer fires the duration requested by the most recent
	// set_sleep_timer command, pausing playback. Stopped/reset (never left
	// to fire) by a later set_sleep_timer call, matching the "only one timer
	// active at a time" behavior of Spotify's own clients.
	sleepTimer *time.Timer

	// sleepAtEndOfTrack is set by a set_sleep_timer command whose timer_type
	// is "end_of_track": rather than a duration to wait, playback is meant
	// to stop when the current track naturally finishes. Checked (and
	// cleared) in the EventTypeNotPlaying handler, in place of the usual
	// advance to the next track. Mutually exclusive with sleepTimer - only
	// one sleep timer mode is active at a time.
	sleepAtEndOfTrack bool

	// consecutiveUnplayableSkips bounds how many unplayable tracks in a row advanceNext will
	// skip past (Spotify-refused audio keys / restricted media) before giving up — so a run
	// of refused tracks (even at the very start of a context) advances to the first playable
	// one instead of freezing, and can never loop forever. Reset to 0 on any successful load.
	consecutiveUnplayableSkips int
}

// requestLogout hands this player back to the daemon to be torn down. With
// zeroconf the daemon rebuilds the session synchronously on the receiving side
// and carries on; without it there is nothing to swap in, so the daemon stops.
// Either way the handover happens off the player loop.
func (p *AppPlayer) requestLogout() {
	p.logoutOnce.Do(func() {
		go func() {
			select {
			case p.logout <- p:
			case <-p.ctx.Done():
			}
		}()
	})
}

// CountryCode reports the country the account is registered in, empty until the
// accesspoint has said. Safe to call from any goroutine.
func (p *AppPlayer) CountryCode() string {
	if code := p.countryCode.Load(); code != nil {
		return *code
	}
	return ""
}

func (p *AppPlayer) playbackReady() bool {
	select {
	case <-p.playbackReadyCh:
		return true
	default:
		return false
	}
}

func (p *AppPlayer) notifyPlaybackReadyIfNeeded() {
	if !p.hasSpotConnId || !p.hasInitialConnectState || !p.hasCountryCode {
		return
	}

	p.playbackReadyOnce.Do(func() {
		close(p.playbackReadyCh)
		p.app.server.Emit(&ApiEvent{Type: ApiEventTypePlaybackReady})
	})
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
		p.countryCode.Store(pointer(string(payload)))
		p.hasCountryCode = true
		p.notifyPlaybackReadyIfNeeded()
		return nil
	default:
		return nil
	}
}

func (p *AppPlayer) handleDealerMessage(msg dealer.Message) error {
	if strings.HasPrefix(msg.Uri, "hm://pusher/v1/connections/") {
		p.spotConnId = msg.Headers["Spotify-Connection-Id"]
		p.hasSpotConnId = p.spotConnId != ""
		p.app.log.Debugf("received connection id: %s...%s", p.spotConnId[:16], p.spotConnId[len(p.spotConnId)-16:])

		p.pushState(connectpb.PutStateReason_NEW_DEVICE)

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
		p.app.log.WithField("username", librespot.ObfuscateUsername(p.sess.Username())).
			Debugf("requested logout out")
		p.requestLogout()
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

		p.stopPlayback()
		return nil
	}

	return nil
}

func (p *AppPlayer) handlePlayerCommand(req dealer.RequestPayload) error {
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

		if sessId := transferState.CurrentSession.OriginalSessionId; sessId != nil {
			p.state.player.SessionId = *sessId
		} else {
			sessionId := make([]byte, 16)
			_, _ = rand.Read(sessionId)
			p.state.player.SessionId = base64.StdEncoding.EncodeToString(sessionId)
		}

		p.state.setActive(true)

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
		spotCtx := transferState.CurrentSession.Context
		p.state.player.PlayOrigin = transferState.CurrentSession.PlayOrigin
		p.state.player.PlayOrigin.DeviceIdentifier = req.SentByDeviceId
		p.state.player.ContextUri = spotCtx.Uri
		p.state.player.ContextUrl = spotCtx.Url
		p.state.player.ContextRestrictions = spotCtx.Restrictions
		p.state.player.Suppressions = transferState.CurrentSession.Suppressions
		p.state.player.ContextMetadata = contextMetadata(spotCtx.Metadata, nil)

		// Claim the transfer before doing anything slow. The surrounding tracks
		// are cleared rather than left as they are: they still describe the
		// context being transferred away from, and this claim already carries
		// the new context's uri and track.
		contextSpotType := librespot.InferSpotifyIdTypeFromContextUri(p.state.player.ContextUri)
		p.state.player.Track = librespot.ContextTrackToProvidedTrack(contextSpotType, transferState.Playback.CurrentTrack)
		p.state.player.PrevTracks = nil
		p.state.player.NextTracks = nil
		p.state.player.Index = nil
		p.state.player.IsPlaying = true
		p.state.player.IsBuffering = true
		p.state.player.PlaybackSpeed = 0 // not progressing while buffering
		p.flushState()

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeActive,
		})

		p.transferContext(&transferState, pause)

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

		p.loadContext(req.Command.Context, skipTo, req.Command.Options.InitiallyPaused, true, func(err error) {
			if err != nil {
				p.app.log.WithError(err).Warn("failed loading context for play command")
			}
		})

		return nil
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

		if err := p.seek(position); err != nil {
			return fmt.Errorf("failed seeking stream: %w", err)
		}

		return nil
	case "skip_prev":
		return p.skipPrev(req.Command.Options.AllowSeeking)
	case "skip_next":
		return p.skipNext(req.Command.Track)
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
	case "set_sleep_timer":
		// Only one timer (of either mode) is active at a time: stop/drain
		// the duration timer and clear the end-of-track flag before
		// possibly setting either, matching Spotify's own clients (a new
		// call replaces, not stacks with, an earlier one, of either mode).
		if !p.sleepTimer.Stop() {
			select {
			case <-p.sleepTimer.C:
			default:
			}
		}
		p.sleepAtEndOfTrack = false

		// Setting the timer alone has no visible effect on its own: the
		// Spotify app doesn't track this locally, it reads back whether (and
		// when) a timer is active from PlayerState.SleepTimer, so that has
		// to be kept in sync for the app to show anything at all.
		tt := req.Command.TimerType
		switch {
		case tt != nil && tt.Type == "duration" && tt.DurationS > 0:
			duration := time.Duration(tt.DurationS) * time.Second
			p.sleepTimer.Reset(duration)
			p.state.player.SleepTimer = &connectpb.SleepTimer{
				TimerType: &connectpb.SleepTimer_Timestamp_{
					Timestamp: &connectpb.SleepTimer_Timestamp{
						Timestamp: time.Now().Add(duration).UnixMilli(),
					},
				},
			}
		case tt != nil && tt.Type == "end_of_track":
			p.sleepAtEndOfTrack = true
			p.state.player.SleepTimer = &connectpb.SleepTimer{
				TimerType: &connectpb.SleepTimer_EndOfTrack_{
					EndOfTrack: &connectpb.SleepTimer_EndOfTrack{},
				},
			}
		default:
			// "clear" is Spotify's own cancel signal. Anything else we don't
			// recognize is logged rather than silently treated as a cancel,
			// so its actual wire shape can be captured.
			if tt != nil && tt.Type != "" && tt.Type != "clear" {
				p.app.log.Warnf("unsupported set_sleep_timer timer_type payload: %s", req.RawCommand)
			}
			p.state.player.SleepTimer = nil
		}

		p.updateState()
		return nil
	default:
		p.app.log.Warnf("unsupported player command %q payload: %s", req.Command.Endpoint, req.RawCommand)
		return fmt.Errorf("unsupported player command: %s", req.Command.Endpoint)
	}
}

func (p *AppPlayer) handleDealerRequest(req dealer.Request) error {
	switch req.MessageIdent {
	case "hm://connect-state/v1/player/command":
		return p.handlePlayerCommand(req.Payload)
	default:
		p.app.log.Warnf("unknown dealer request: %s", req.MessageIdent)
		return nil
	}
}

func (p *AppPlayer) handleApiRequest(req ApiRequest) (any, error) {
	switch req.Type {
	case ApiRequestTypeRoot:
		return &ApiRoot{PlaybackReady: p.playbackReady()}, nil
	case ApiRequestTypeStatus:
		resp := &ApiStatus{
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
			resp.Track = p.newApiResponseStatusTrack(p.primaryStream, p.state.trackPosition())
		}

		return resp, nil
	case ApiRequestTypeResume:
		_ = p.play()
		return nil, nil
	case ApiRequestTypePause:
		_ = p.pause()
		return nil, nil
	case ApiRequestTypeStop:
		p.stopPlayback()
		return nil, nil
	case ApiRequestTypePlayPause:
		if p.state.player.IsPaused {
			_ = p.play()
		} else {
			_ = p.pause()
		}
		return nil, nil
	case ApiRequestTypeSeek:
		data := req.Data.(ApiSeek)

		var position int64
		if data.Relative {
			position = p.player.PositionMs() + data.Position
		} else {
			position = data.Position
		}

		_ = p.seek(position)
		return nil, nil
	case ApiRequestTypePrev:
		_ = p.skipPrev(true)
		return nil, nil
	case ApiRequestTypeNext:
		data := req.Data.(ApiNext)
		if data.Uri != nil {
			_ = p.skipNext(&connectpb.ContextTrack{Uri: *data.Uri})
		} else {
			_ = p.skipNext(nil)
		}
		return nil, nil
	case ApiRequestTypePlay:
		data := req.Data.(ApiPlay)

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

		// When starting at a position, load paused and seek before unpausing so
		// no audio plays from 0:00 while the track loads. The seek waits for the
		// load to land rather than polling for it.
		loadPaused := data.Paused || data.Position > 0

		p.loadGen++
		p.loader.submit(loaderJob{
			name:  "resolve " + data.Uri,
			class: classLoad,
			gen:   p.loadGen,
			reply: apiReply(req),
			run: func(ctx context.Context) loaderResult {
				spotCtx, err := p.sess.Spclient().ContextResolve(ctx, data.Uri)
				if err != nil {
					return loaderResult{err: fmt.Errorf("failed resolving context: %w", err)}
				}

				return loaderResult{commit: func(p *AppPlayer, err error) {
					if err != nil {
						return
					}

					p.state.setActive(true)
					p.state.setPaused(data.Paused)
					p.state.player.Suppressions = &connectpb.Suppressions{}
					p.state.player.PlayOrigin = &connectpb.PlayOrigin{
						FeatureIdentifier: "go-librespot",
						FeatureVersion:    librespot.VersionNumberString(),
					}

					p.loadContext(spotCtx, skipTo, loadPaused, true, func(err error) {
						if err != nil {
							p.app.log.WithError(err).Warn("failed loading context")
							return
						}

						if data.Position <= 0 {
							return
						}

						if err := p.seek(data.Position); err != nil {
							p.app.log.WithError(err).Warnf("failed seeking to initial position %dms", data.Position)
						}
						if !data.Paused {
							if err := p.play(); err != nil {
								p.app.log.WithError(err).Warnf("failed resuming after initial seek")
							}
						}
					})
				}}
			},
		})

		return nil, errReplyDeferred
	case ApiRequestTypeGetVolume:
		return &ApiVolume{
			Max:   p.app.cfg.VolumeSteps,
			Value: p.apiVolume(),
		}, nil
	case ApiRequestTypeSetVolume:
		data := req.Data.(ApiSetVolume)

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
	case ApiRequestTypeToken:
		// Nothing here touches player state, so it is answered from its own
		// goroutine rather than holding up the loop for a token renewal.
		reply := apiReply(req)
		p.goDetached(tokenTimeout, func(ctx context.Context) {
			accessToken, err := p.sess.Spclient().GetAccessToken(ctx, true)
			if err != nil {
				reply.done(nil, fmt.Errorf("failed getting access token: %w", err))
				return
			}

			reply.done(&ApiToken{Token: accessToken}, nil)
		})

		return nil, errReplyDeferred
	case ApiRequestSetDeviceName:
		p.setDeviceName(req.Data.(string))
		return nil, nil
	case ApiRequestTypeReopenOutput:
		if err := p.player.ReopenOutput(req.Data.(string)); err != nil {
			return nil, fmt.Errorf("failed reopening output: %w", err)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown request type: %s", req.Type)
	}
}

func (p *AppPlayer) setDeviceName(name string) {
	p.app.SetDeviceName(name)

	p.state.device.Name = name
	p.updateState()
}

func pointer[T any](d T) *T {
	return &d
}

func (p *AppPlayer) handleMprisEvent(req mpris.MediaPlayer2PlayerCommand) error {
	switch req.Type {
	case mpris.MediaPlayer2PlayerCommandTypeNext:
		return p.skipNext(nil)
	case mpris.MediaPlayer2PlayerCommandTypePrevious:
		return p.skipPrev(true)
	case mpris.MediaPlayer2PlayerCommandTypePlay:
		return p.play()
	case mpris.MediaPlayer2PlayerCommandTypePause:
		return p.pause()
	case mpris.MediaPlayer2PlayerCommandTypePlayPause:
		if p.state.player.IsPaused {
			return p.play()
		} else {
			return p.pause()
		}
	case mpris.MediaPlayer2PlayerCommandTypeStop:
		p.stopPlayback()
		return nil
	case mpris.MediaPlayer2PlayerCommandLoopStatusChanged:
		p.app.log.Tracef("mpris loop status argument %s", req.Argument)
		dt := req.Argument
		switch dt {
		case mpris.None:
			p.setOptions(pointer(false), pointer(false), nil)
		case mpris.Playlist:
			p.setOptions(pointer(true), pointer(false), nil)
		case mpris.Track:
			p.setOptions(pointer(true), pointer(true), nil)
		default:
			p.app.log.Warnf("mpris loop status argument is invalid (%s)", req.Argument)
		}
		return nil
	case mpris.MediaPlayer2PlayerCommandShuffleChanged:
		sh := req.Argument.(bool)
		p.setOptions(nil, nil, &sh)
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
			if spotifyId != p.state.player.Track.GetUri() {
				return fmt.Errorf("seek tries to jump to different uri, not yet supported (got: %s, expected: %s)", spotifyId, p.state.player.Track.GetUri())
			}
		}

		newPositionAbs := arg.PositionUs / 1000
		return p.seek(newPositionAbs)
	case mpris.MediaPlayer2PlayerCommandTypeSeek:
		newPosAbs := p.player.PositionMs() + req.Argument.(int64)/1000
		return p.seek(newPosAbs)
	case mpris.MediaPlayer2PlayerCommandTypeOpenUri, mpris.MediaPlayer2PlayerCommandRateChanged:
		p.app.log.Warnf("unimplemented mpris event %d", req.Type)
		return fmt.Errorf("unimplemented mpris event %d", req.Type)
	}
	return nil
}

// Close stops the player and releases its session. It may be called while Run
// is still busy serving a command, so it must not assume Run reacts promptly:
// cancelling the context is what actually unblocks in-flight requests.
func (p *AppPlayer) Close() {
	p.closeOnce.Do(func() {
		p.cancel()
		p.stop <- struct{}{}
		p.loader.close()
		p.statePush.close()
		p.player.Close()
		p.sess.Close()
	})
}

func (p *AppPlayer) Run(apiRecv <-chan ApiRequest, mprisRecv <-chan mpris.MediaPlayer2PlayerCommand) {
	err := p.sess.Dealer().Connect(p.ctx)
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

	// The accesspoint and the dealer only close their receivers after giving
	// up on reconnecting, so losing either means the session is gone for good
	// and cannot recover on its own. sessionLost hands the player back to the
	// daemon to be torn down and rebuilt, exactly as a remote logout does.
	sessionLost := false
	loseSession := func() (stop bool) {
		if sessionLost {
			return false
		}
		sessionLost = true

		p.app.log.Warn("lost session, tearing down player to start a new one")

		select {
		case p.logout <- p:
			// The daemon calls Close, which signals p.stop and ends this loop.
			return false
		case <-p.stop:
			// Already being torn down for another reason.
			return true
		}
	}

	for {
		select {
		case <-p.stop:
			return
		case pkt, ok := <-apRecv:
			if !ok {
				apRecv = nil
				if loseSession() {
					return
				}
				continue
			}

			if err := p.handleAccesspointPacket(pkt.Type, pkt.Payload); err != nil {
				p.app.log.WithError(err).Warn("failed handling accesspoint packet")
			}
		case msg, ok := <-msgRecv:
			if !ok {
				msgRecv = nil
				if loseSession() {
					return
				}
				continue
			}

			if err := p.handleDealerMessage(msg); err != nil {
				p.app.log.WithError(err).Warn("failed handling dealer message")
			}
		case req, ok := <-reqRecv:
			if !ok {
				reqRecv = nil
				if loseSession() {
					return
				}
				continue
			}

			if err := p.handleDealerRequest(req); err != nil {
				p.app.log.WithError(err).Warn("failed handling dealer request")
				req.Reply(false)
			} else {
				p.app.log.Debugf("sending successful reply for dealer request")
				req.Reply(true)
			}
		case req, ok := <-apiRecv:
			if !ok {
				apiRecv = nil
				continue
			}

			data, err := p.handleApiRequest(req)
			if errors.Is(err, errReplyDeferred) {
				continue
			}

			req.Reply(data, err)
		case mprisReq, ok := <-mprisRecv:
			if !ok {
				mprisRecv = nil
				continue
			}

			p.app.log.Tracef("new mpris message %v", mprisReq)
			err := p.handleMprisEvent(mprisReq)
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
				playerRecv = nil
				continue
			}

			if p.deferPlayerEvent(ev) {
				continue
			}

			p.handlePlayerEvent(&ev)
		case <-p.prefetchTimer.C:
			p.prefetchNext()
		case <-p.sleepTimer.C:
			// Cleared before pause(), whose own updateState call picks this
			// up - so the app stops showing the timer as active in the same
			// state push that reports playback paused.
			p.state.player.SleepTimer = nil
			if err := p.pause(); err != nil {
				p.app.log.WithError(err).Warn("failed pausing playback for sleep timer")
			}
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
			p.volumeUpdated()
		case res := <-p.loader.results:
			p.applyLoaderResult(res)
		case res := <-p.statePush.results:
			p.applyStatePushResult(res)
		case <-p.stateTimer.C:
			p.statePutScheduled = false
			if !p.stateDirty {
				break
			}
			p.flushState()
		}
	}
}

// flushState hands the latest connect-state to the push lane and records the
// send time. Runs on the Run goroutine.
func (p *AppPlayer) flushState() {
	p.stateDirty = false
	p.lastStatePut = time.Now()
	p.pushState(connectpb.PutStateReason_PLAYER_STATE_CHANGED)
}
