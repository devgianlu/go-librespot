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
	"github.com/devgianlu/go-librespot/spclient"
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

	hasSpotConnId          bool
	hasInitialConnectState bool
	hasCountryCode         bool
	playbackReadyCh        chan struct{}
	playbackReadyOnce      sync.Once

	state           *State
	primaryStream   *player.Stream
	secondaryStream *player.Stream

	prefetchTimer *time.Timer

	// djPollTimer periodically retries ContextResolve for the DJ playlist after a transfer
	// with no tracks, in case the playlist becomes available before Spotify sends the ~53s push.
	djPollTimer    *time.Timer
	djPollAttempts int

	// djPendingMusicId is set when a DJ narration clip is playing as the
	// primary stream. It holds the SpotifyId of the actual music track that
	// should start once the narration finishes (EventTypeNotPlaying).
	djPendingMusicId *librespot.SpotifyId

	// djAwaitingLoad is set when a DJ play command was accepted but context resolution
	// returned no tracks (empty spclient pages). Cleared once the first DJ track loads.
	// This lets the ClusterUpdate handler distinguish "transitioning into DJ" (should load)
	// from "already playing music within DJ" (should not reload).
	djAwaitingLoad bool
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
		*p.countryCode = string(payload)
		p.hasCountryCode = true
		p.notifyPlaybackReadyIfNeeded()
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
		p.hasSpotConnId = p.spotConnId != ""
		p.app.log.Debugf("received connection id: %s...%s", p.spotConnId[:16], p.spotConnId[len(p.spotConnId)-16:])

		// put the initial state
		if err := p.putConnectState(ctx, connectpb.PutStateReason_NEW_DEVICE); err != nil {
			return fmt.Errorf("failed initial state put: %w", err)
		}

		p.hasInitialConnectState = true
		p.notifyPlaybackReadyIfNeeded()

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
	} else if strings.HasPrefix(msg.Uri, "hm://playlist/v2/playlist/") {
		// Spotify responds to djAwaitingLoad (IsPlaying=false + DJ context) by updating a
		// companion playlist with the next batch of DJ tracks and pushing this notification.
		// Extract the playlist ID, fetch its content, and use the tracks to resume playback.
		playlistId := strings.TrimPrefix(msg.Uri, "hm://playlist/v2/playlist/")
		if idx := strings.IndexByte(playlistId, '/'); idx >= 0 {
			playlistId = playlistId[:idx]
		}
		p.app.log.Debugf("playlist update notification: %s (payloadLen=%d, djAwaitingLoad=%t, djContextUri=%q)",
			playlistId, len(msg.Payload), p.djAwaitingLoad, p.app.djCachedContextUri)

		// Only process if we are in a known DJ context — otherwise this is an unrelated update.
		if p.app.djCachedContextUri == "" {
			return nil
		}

		playlistUri := "spotify:playlist:" + playlistId
		spotCtx, err := p.sess.Spclient().ContextResolve(ctx, playlistUri)
		if err != nil {
			p.app.log.WithError(err).Debugf("failed resolving playlist update %s", playlistUri)
			return nil
		}

		// Collect all tracks from the resolved context.
		var newTracks []*connectpb.ContextTrack
		for _, page := range spotCtx.Pages {
			for _, track := range page.Tracks {
				if track.Uri != "spotify:delimiter" {
					newTracks = append(newTracks, track)
				}
			}
		}

		if len(newTracks) == 0 {
			p.app.log.Debugf("playlist update %s resolved with 0 tracks (ignoring)", playlistUri)
			return nil
		}

		p.app.log.Infof("DJ playlist update %s: %d tracks (djAwaitingLoad=%t)", playlistUri, len(newTracks), p.djAwaitingLoad)
		p.app.djCachedNextTracks = newTracks
		p.app.djCacheIsOurs = true
		p.djPollTimer.Stop() // cancel any in-progress poll — we got the tracks via push

		if p.djAwaitingLoad {
			// Load the first track immediately — same as the pendingDJ path in the cluster handler.
			currentTrack := newTracks[0]
			ctxTracks := make([]*connectpb.ContextTrack, len(newTracks))
			copy(ctxTracks, newTracks)
			resolver := spclient.NewStaticContextResolver(p.app.log, p.app.djCachedContextUri, ctxTracks)
			newList := tracks.NewTrackListFromResolver(p.app.log, resolver)
			ctxType := librespot.InferSpotifyIdTypeFromContextUri(p.app.djCachedContextUri)
			_ = newList.TrySeek(ctx, tracks.ContextTrackComparator(ctxType, currentTrack))

			p.state.tracks = newList
			p.state.player.Track = p.state.tracks.CurrentTrack()
			p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, nil)
			p.state.player.PositionAsOfTimestamp = 0

			p.djAwaitingLoad = false
			p.app.log.Infof("loading DJ track from playlist update (%d next tracks)", len(newTracks)-1)
			if err := p.loadCurrentTrack(ctx, false, true); err != nil {
				p.app.log.WithError(err).Warn("failed loading DJ track from playlist update, reverting to djAwaitingLoad")
				p.djAwaitingLoad = true
			}
		} else {
			// Buffer this section for later use when the queue runs low. We buffer
			// unconditionally here (not gated on ContextUri) because the push can
			// arrive while a temporary regular-playlist context is active (e.g.
			// during a Switch-it-up transition), and we must not silently drop it.
			p.app.djSectionBuffer = append(p.app.djSectionBuffer, newTracks)
			p.app.log.Debugf("buffered DJ section %d (%d tracks) from playlist update", len(p.app.djSectionBuffer), len(newTracks))
		}
		return nil
	} else if strings.HasPrefix(msg.Uri, "hm://connect-state/v1/cluster") {
		var clusterUpdate connectpb.ClusterUpdate
		if err := proto.Unmarshal(msg.Payload, &clusterUpdate); err != nil {
			return fmt.Errorf("failed unmarshalling ClusterUpdate: %w", err)
		}

		stopBeingActive := p.state.active && clusterUpdate.Cluster.ActiveDeviceId != p.app.deviceId && clusterUpdate.Cluster.PlayerState.Timestamp > p.state.lastTransferTimestamp
		p.app.log.Debugf("cluster decision: activeDeviceId=%q ourDeviceId=%q clusterPlayerTs=%d lastTransferTs=%d stateActive=%t stopBeingActive=%t djAwaitingLoad=%t stateContextUri=%q",
			clusterUpdate.Cluster.ActiveDeviceId, p.app.deviceId,
			clusterUpdate.Cluster.PlayerState.Timestamp, p.state.lastTransferTimestamp,
			p.state.active, stopBeingActive, p.djAwaitingLoad, p.state.player.ContextUri)

		// We are still the active device, do not quit
		if !stopBeingActive {
			clusterState := clusterUpdate.Cluster.GetPlayerState()
			nextCount := 0
			if clusterState != nil {
				nextCount = len(clusterState.NextTracks)
			}
			isDJCluster := false
			if clusterState != nil {
				// Primary check: PlayOrigin.FeatureIdentifier == "dynamic-sessions".
				// This is reliably set by the server for DJ sessions on both desktop
				// and speaker/zeroconf devices.
				if clusterState.PlayOrigin != nil && clusterState.PlayOrigin.FeatureIdentifier == "dynamic-sessions" {
					isDJCluster = true
				}
				// Fallback: check source.components on individual tracks (populated
				// on desktop/interactive clients but often absent on speaker devices).
				if !isDJCluster {
					for _, t := range clusterState.NextTracks {
						if player.IsDJTrack(t) {
							isDJCluster = true
							break
						}
					}
				}
				// Third check: context URI matches our known DJ playlist URI.
				// Spotify sometimes sends featureId="home" when DJ is pressed from the
				// home/browse screen rather than the now-playing DJ button. The cluster
				// still carries the DJ playlist URI and next tracks — treat it as DJ.
				clusterCtxUri := clusterState.ContextUri
				if clusterCtxUri == "" {
					clusterCtxUri = p.state.player.ContextUri
				}
				if !isDJCluster && p.app.djCachedContextUri != "" && clusterCtxUri == p.app.djCachedContextUri {
					isDJCluster = true
				}
				// Fourth check: we are explicitly waiting for a DJ cluster (djAwaitingLoad=true)
				// and the cluster's context URI matches the DJ context URI we accepted in the
				// play command. This catches fresh-start DJ sessions where featureId="home" is
				// sent and djCachedContextUri is not yet populated (empty on first boot/restart).
				if !isDJCluster && p.djAwaitingLoad && clusterCtxUri != "" && clusterCtxUri == p.state.player.ContextUri {
					isDJCluster = true
				}
			}
			p.app.log.Debugf("cluster update received (active=%t, nextTracks=%d, djCluster=%t, featureId=%s)",
				p.state.active, nextCount, isDJCluster, func() string {
					if clusterState != nil && clusterState.PlayOrigin != nil {
						return clusterState.PlayOrigin.FeatureIdentifier
					}
					return ""
				}())

			// Log what the server echoes back about our device's capabilities.
			if ourDevice := clusterUpdate.Cluster.Device[p.app.deviceId]; ourDevice != nil && ourDevice.Capabilities != nil {
				caps := ourDevice.Capabilities
				p.app.log.Debugf("server-reflected caps: SupportsDj=%t IsVoiceEnabled=%t", caps.SupportsDj, caps.IsVoiceEnabled)
			}

			if isDJCluster {
				// Cache the DJ next tracks for use when a transfer command arrives shortly after.
				contextUri := clusterState.ContextUri
				if contextUri == "" {
					contextUri = p.state.player.ContextUri
				}
				if nextCount > 0 {
					p.app.djCachedContextUri = contextUri
					p.app.djCachedNextTracks = make([]*connectpb.ContextTrack, 0, nextCount)
					for _, t := range clusterState.NextTracks {
						if t.Uri != "spotify:delimiter" {
							p.app.djCachedNextTracks = append(p.app.djCachedNextTracks, librespot.ProvidedTrackToContextTrack(t))
						}
					}
					p.app.djCacheIsOurs = clusterUpdate.Cluster.ActiveDeviceId == p.app.deviceId
					p.app.log.Debugf("cached DJ next tracks from cluster push (%d tracks for %s, ours=%t)", nextCount, contextUri, p.app.djCacheIsOurs)
				} else {
					p.app.log.Debugf("skipping DJ cache update for %s — cluster has 0 next tracks (keeping %d cached)", contextUri, len(p.app.djCachedNextTracks))
				}

				// Update the live track list if we are the active player with a DJ context.
				// This covers two cases:
				// (a) Already playing a DJ track — refresh queue in place.
				// (b) Accepted a DJ play command but have no tracks yet (ContextUri set,
				//     no track loaded) — start playing from the cluster's current track.
				// Use djCachedContextUri to detect active DJ sessions. IsDJTrack() is
				// unreliable because regular music tracks in a DJ queue don't carry
				// YourDJ source metadata, and transferred tracks never do.
				// Guard with !djAwaitingLoad so this path doesn't fire during the initial
				// DJ selection (when we're still waiting for the first track from the
				// cluster) — that case is handled by pendingDJ below.
				alreadyDJ := p.state.active && p.state.player.Track != nil && contextUri == p.app.djCachedContextUri && !p.djAwaitingLoad
				pendingDJ := p.djAwaitingLoad && p.state.player.ContextUri == contextUri
				p.app.log.Debugf("DJ path eval: alreadyDJ=%t pendingDJ=%t djAwaitingLoad=%t stateContextUri=%q clusterContextUri=%q clusterTrack=%v nextTracks=%d",
					alreadyDJ, pendingDJ, p.djAwaitingLoad, p.state.player.ContextUri, contextUri,
					func() string {
						if clusterState.Track != nil {
							return clusterState.Track.Uri
						}
						return "<nil>"
					}(), nextCount)
				if alreadyDJ || pendingDJ {
					// If this is a pendingDJ activation cluster but it has no next tracks,
					// Spotify sent a lightweight heartbeat instead of the full queue.
					// Stay in djAwaitingLoad and wait for the real cluster with tracks.
					if nextCount == 0 {
						p.app.log.Debugf("DJ cluster has 0 next tracks (pendingDJ=%t alreadyDJ=%t) — ignoring, keeping %d cached tracks", pendingDJ, alreadyDJ, len(p.app.djCachedNextTracks))
					} else {
						currentTrack := func() *connectpb.ContextTrack {
							if alreadyDJ {
								return librespot.ProvidedTrackToContextTrack(p.state.player.Track)
							}
							// For pendingDJ (cold start): Spotify sets clusterState.Track to whatever
							// was already playing - not a new DJ track. Use djCachedNextTracks[0]
							// so we start on the actual first DJ track, same as the cache path.
							if len(p.app.djCachedNextTracks) > 0 {
								return p.app.djCachedNextTracks[0]
							}
							if clusterState.Track != nil {
								return librespot.ProvidedTrackToContextTrack(clusterState.Track)
							}
							return nil
						}()
						ctxTracks := make([]*connectpb.ContextTrack, 0, nextCount+1)
						ctxTracks = append(ctxTracks, currentTrack)
						if alreadyDJ {
							ctxTracks = append(ctxTracks, p.app.djCachedNextTracks...)
						} else if len(p.app.djCachedNextTracks) > 1 {
							ctxTracks = append(ctxTracks, p.app.djCachedNextTracks[1:]...)
						}
						resolver := spclient.NewStaticContextResolver(p.app.log, contextUri, ctxTracks)
						newList := tracks.NewTrackListFromResolver(p.app.log, resolver)
						ctxType := librespot.InferSpotifyIdTypeFromContextUri(contextUri)
						_ = newList.TrySeek(ctx, tracks.ContextTrackComparator(ctxType, currentTrack))
						p.state.tracks = newList
						p.state.player.Track = p.state.tracks.CurrentTrack()
						p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, nil)
						p.app.log.Debugf("updated active DJ track list (%d next tracks, pendingDJ=%t)", nextCount, pendingDJ)

						// If we were waiting for the first DJ track (pendingDJ), load it now.
						// Also restart if stuck (alreadyDJ but no primary stream — e.g. after back-to-back narration clips).
						stuckDJ := alreadyDJ && p.primaryStream == nil
						if pendingDJ || stuckDJ {
							p.djAwaitingLoad = false
							p.state.player.ContextUri = contextUri
							p.state.player.PositionAsOfTimestamp = 0
							p.app.log.Debugf("loading DJ track from cluster (pendingDJ=%t stuckDJ=%t)", pendingDJ, stuckDJ)
							if err := p.loadCurrentTrack(ctx, false, true); err != nil {
								p.app.log.WithError(err).Warn("failed loading DJ track from cluster push")
							}
						}
					} // end else (nextCount > 0)
				}
			}
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

		// Log transfer context metadata and options modes for DJ debugging
		p.app.log.Debugf("transfer context metadata: %v", transferState.CurrentSession.Context.Metadata)
		if transferState.Options != nil {
			p.app.log.Debugf("transfer options modes: %v", transferState.Options.Modes)
		}

		ctxTracks, err := tracks.NewTrackListFromContext(ctx, p.app.log, p.sess.Spclient(), transferState.CurrentSession.Context)
		if err != nil {
			// Dynamic contexts (e.g. Spotify DJ) return empty pages from spclient.
			// Use cached DJ next tracks from a recent ClusterUpdate if available,
			// otherwise fall back to the current track + queue from the transfer state.
			contextUri := transferState.CurrentSession.Context.Uri
			staticTracks := []*connectpb.ContextTrack{transferState.Playback.CurrentTrack}
			if len(p.app.djCachedNextTracks) > 0 && p.app.djCachedContextUri == contextUri {
				staticTracks = append(staticTracks, p.app.djCachedNextTracks...)
				p.app.log.WithError(err).Warnf("context resolution failed, using cached DJ queue for %s (%d tracks)", contextUri, len(staticTracks))
			} else {
				// Try lexicon-session-provider to get the full DJ queue immediately.
				lexCtx, lexErr := p.sess.Spclient().LexiconContextResolve(ctx, contextUri, "state_restore")
				if lexErr == nil {
					var lexTracks []*connectpb.ContextTrack
					for _, page := range lexCtx.GetPages() {
						for _, t := range page.GetTracks() {
							if t.Uri != "spotify:delimiter" && t.Uri != "" {
								lexTracks = append(lexTracks, t)
							}
						}
					}
					if len(lexTracks) > 0 {
						p.app.log.Infof("lexicon: got %d DJ tracks for transfer %s", len(lexTracks), contextUri)
						staticTracks = append(staticTracks, lexTracks...)
						p.app.djCachedNextTracks = lexTracks
						p.app.djCacheIsOurs = true
						// Apply DJ context metadata from lexicon response.
						if transferState.CurrentSession.Context.Metadata == nil {
							transferState.CurrentSession.Context.Metadata = map[string]string{}
						}
						for k, v := range lexCtx.Metadata {
							transferState.CurrentSession.Context.Metadata[k] = v
						}
					} else {
						p.app.log.Debugf("lexicon: 0 tracks for transfer %s, falling back to djPoll", contextUri)
						staticTracks = append(staticTracks, transferState.Queue.Tracks...)
					}
				} else {
					p.app.log.Debugf("lexicon: transfer resolve failed (%v), falling back to djPoll", lexErr)
					staticTracks = append(staticTracks, transferState.Queue.Tracks...)
				}
				p.app.log.WithError(err).Warnf("context resolution failed, building static track list for %s (tracks=%d)", contextUri, len(staticTracks))
				p.app.djCachedContextUri = contextUri
				if len(staticTracks) <= 1 {
					// Lexicon failed — fall back to polling.
					p.djPollAttempts = 0
					p.djPollTimer.Reset(3 * time.Second)
				}
			}
			resolver := spclient.NewStaticContextResolver(p.app.log, contextUri, staticTracks)
			ctxTracks = tracks.NewTrackListFromResolver(p.app.log, resolver)
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
		// Pin Timestamp to now so updateTimestamp() doesn't advance position by
		// stale elapsed time. The raw PositionAsOfTimestamp from the transfer is
		// the position the phone was at when it sent the command; we start from there.
		p.state.player.Timestamp = time.Now().UnixMilli()
		p.state.player.PositionAsOfTimestamp = int64(transferState.Playback.PositionAsOfTimestamp)
		p.state.setPaused(pause)

		// current session
		p.state.player.PlayOrigin = transferState.CurrentSession.PlayOrigin
		p.state.player.PlayOrigin.DeviceIdentifier = req.SentByDeviceId
		p.app.log.Debugf("transfer PlayOrigin.FeatureIdentifier=%q", p.state.player.PlayOrigin.FeatureIdentifier)
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
		p.app.log.Debugf("play command: contextUri=%s featureId=%s prevContextUri=%s",
			req.Command.Context.GetUri(), req.Command.PlayOrigin.GetFeatureIdentifier(), p.state.player.ContextUri)

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
		// If we are waiting for DJ tracks, a resume cannot succeed (no stream yet).
		// Acknowledge it silently — the state we already sent shows IsPlaying=false,
		// which should stop the phone from retrying indefinitely.
		if p.djAwaitingLoad {
			p.app.log.Debugf("resume while djAwaitingLoad — ignoring (waiting for playlist update)")
			return nil
		}
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

// djPollContextResolve is called from the Run() select loop when djPollTimer fires.
// It retries ContextResolve for the current DJ playlist to see if tracks are available
// before Spotify sends the ~53s background push notification.
func (p *AppPlayer) djPollContextResolve(ctx context.Context) {
	const maxAttempts = 20
	const pollInterval = 3 * time.Second

	if p.app.djCachedContextUri == "" {
		return // nothing to poll
	}

	p.djPollAttempts++
	p.app.log.Debugf("djPoll: attempt %d for %s", p.djPollAttempts, p.app.djCachedContextUri)

	lexCtx, err := p.sess.Spclient().LexiconContextResolve(ctx, p.app.djCachedContextUri, "state_restore")
	if err != nil || lexCtx == nil {
		if p.djPollAttempts < maxAttempts {
			p.djPollTimer.Reset(pollInterval)
		} else {
			p.app.log.Debugf("djPoll: giving up after %d attempts for %s", p.djPollAttempts, p.app.djCachedContextUri)
		}
		return
	}

	var newTracks []*connectpb.ContextTrack
	for _, page := range lexCtx.GetPages() {
		for _, track := range page.GetTracks() {
			if track.Uri != "spotify:delimiter" && track.Uri != "" {
				newTracks = append(newTracks, track)
			}
		}
	}

	if len(newTracks) == 0 {
		if p.djPollAttempts < maxAttempts {
			p.djPollTimer.Reset(pollInterval)
		}
		return
	}

	// Merge DJ metadata from lexicon response (includes interactivity fields)
	if lexCtx.Metadata != nil {
		if p.state.player.ContextMetadata == nil {
			p.state.player.ContextMetadata = map[string]string{}
		}
		for k, v := range lexCtx.Metadata {
			p.state.player.ContextMetadata[k] = v
		}
	}
	p.state.player.ContextMetadata["dj.interactivity_enabled"] = "true"

	p.app.log.Infof("djPoll: resolved %d tracks for %s (volatile_id=%s)", len(newTracks), p.app.djCachedContextUri, lexCtx.Metadata["playlist_volatile_context_id"])
	p.app.djCachedNextTracks = newTracks
	p.app.djCacheIsOurs = true

	if p.djAwaitingLoad {
		currentTrack := newTracks[0]
		ctxTracks := make([]*connectpb.ContextTrack, len(newTracks))
		copy(ctxTracks, newTracks)
		resolver := spclient.NewStaticContextResolver(p.app.log, p.app.djCachedContextUri, ctxTracks)
		newList := tracks.NewTrackListFromResolver(p.app.log, resolver)
		ctxType := librespot.InferSpotifyIdTypeFromContextUri(p.app.djCachedContextUri)
		_ = newList.TrySeek(ctx, tracks.ContextTrackComparator(ctxType, currentTrack))
		p.state.tracks = newList
		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, nil)
		p.state.player.PositionAsOfTimestamp = 0
		p.djAwaitingLoad = false
		if err := p.loadCurrentTrack(ctx, false, true); err != nil {
			p.app.log.WithError(err).Warn("djPoll: failed loading first DJ track")
			p.djAwaitingLoad = true
		}
	} else if p.state.active && p.state.player.ContextUri == p.app.djCachedContextUri {
		// Prefer a buffered vibe section over repeating the same lexicon tracks.
		// djSectionBuffer is populated by the hm://playlist/ push handler at DJ startup;
		// each entry is one section's worth of different tracks. Using it here prevents
		// the same 15 lexicon tracks from looping.
		var combined []*connectpb.ContextTrack
		if p.state.player.Track != nil {
			combined = append(combined, &connectpb.ContextTrack{Uri: p.state.player.Track.Uri})
		}

		if len(p.app.djSectionBuffer) > 0 {
			// Pop the oldest section and use its tracks.
			sectionTracks := p.app.djSectionBuffer[0]
			p.app.djSectionBuffer = p.app.djSectionBuffer[1:]
			combined = append(combined, sectionTracks...)
			p.app.log.Infof("djPoll: using buffered section (%d tracks, %d sections remaining)", len(sectionTracks), len(p.app.djSectionBuffer))
		} else {
			// Buffer exhausted — fall back to repeating the lexicon tracks.
			combined = append(combined, newTracks...)
			p.app.log.Infof("djPoll: section buffer empty, repeating lexicon tracks (%d tracks)", len(newTracks))
		}

		resolver := spclient.NewStaticContextResolver(p.app.log, p.app.djCachedContextUri, combined)
		newList := tracks.NewTrackListFromResolver(p.app.log, resolver)
		if p.state.player.Track != nil {
			ctxType := librespot.InferSpotifyIdTypeFromContextUri(p.app.djCachedContextUri)
			_ = newList.TrySeek(ctx, tracks.ContextTrackComparator(ctxType, librespot.ProvidedTrackToContextTrack(p.state.player.Track)))
		}
		p.state.tracks = newList
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, nil)
		p.updateState(ctx)
		p.app.log.Debugf("djPoll: refreshed queue (%d next tracks ahead)", len(p.state.player.NextTracks))
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
	case ApiRequestTypeRoot:
		return &ApiResponseRoot{PlaybackReady: p.playbackReady()}, nil
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
	msgRecv := p.sess.Dealer().ReceiveMessage("hm://pusher/v1/connections/", "hm://connect-state/v1/", "hm://playlist/v2/playlist/")
	reqRecv := p.sess.Dealer().ReceiveRequest("hm://connect-state/v1/player/command")
	// Also receive playlist pushes that arrive via the Mercury AP event channel
	// (PacketTypeMercuryEvent). These are the vibe-section playlists the server
	// sends during an active DJ session — they outnumber the dealer pushes ~10:1.
	mercuryPlaylistRecv := p.sess.Mercury().SubscribeEvent("hm://playlist/v2/playlist/")
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
		case evMsg := <-mercuryPlaylistRecv:
			// Playlist push via the Mercury AP event channel — same handling as dealer.
			if err := p.handleDealerMessage(ctx, dealer.Message{Uri: evMsg.Uri, Payload: evMsg.Payload}); err != nil {
				p.app.log.WithError(err).Warn("failed handling mercury playlist event")
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
		case <-p.djPollTimer.C:
			p.djPollContextResolve(ctx)
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
