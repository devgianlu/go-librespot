package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/mpris"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	playerpb "github.com/devgianlu/go-librespot/proto/spotify/player"
	"github.com/devgianlu/go-librespot/spclient"
	"github.com/devgianlu/go-librespot/tracks"
	"google.golang.org/protobuf/proto"
)

func (p *AppPlayer) prefetchNext(ctx context.Context) {
	// Limit ourselves to 30 seconds for prefetching
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	next := p.state.tracks.PeekNext(ctx)
	if next == nil {
		return
	}

	if next.Uri == "" {
		// It should be implemented some day (the ContextTrack has enough
		// information to infer the track Uri) but it's hard to reproduce this
		// issue.
		p.app.log.Warn("cannot prefetch next track because the uri field is empty")
		return
	}

	nextId, err := librespot.SpotifyIdFromUri(next.Uri)
	if err != nil {
		p.app.log.WithError(err).WithField("uri", next.Uri).Warn("failed parsing prefetch uri")
		return
	} else if p.secondaryStream != nil && p.secondaryStream.Is(*nextId) {
		return
	}

	p.app.log.WithField("uri", nextId.Uri()).Debugf("prefetching next %s", nextId.Type())

	p.secondaryStream, err = p.player.NewStream(ctx, p.app.client, *nextId, p.app.cfg.Bitrate, 0)
	if err != nil {
		p.app.log.WithError(err).WithField("uri", nextId.String()).Warnf("failed prefetching %s stream", nextId.Type())
		return
	}

	p.player.SetSecondaryStream(p.secondaryStream.Source)

	p.app.log.WithField("uri", nextId.Uri()).
		Infof("prefetched %s %s (duration: %dms)", nextId.Type(),
			strconv.QuoteToGraphic(p.secondaryStream.Media.Name()), p.secondaryStream.Media.Duration())
}

func (p *AppPlayer) schedulePrefetchNext() {
	if p.state.player.IsPaused || p.primaryStream == nil {
		p.prefetchTimer.Stop()
		return
	}

	untilTrackEnd := time.Duration(p.primaryStream.Media.Duration()-int32(p.player.PositionMs())) * time.Millisecond
	untilTrackEnd -= 30 * time.Second
	if untilTrackEnd < 10*time.Second {
		p.prefetchTimer.Reset(0)
		p.app.log.Tracef("prefetch as soon as possible")
	} else {
		p.prefetchTimer.Reset(untilTrackEnd)
		p.app.log.Tracef("scheduling prefetch in %.0fs", untilTrackEnd.Seconds())
	}
}

func (p *AppPlayer) emitMprisUpdate(playbackStatus mpris.PlaybackStatus) {
	// p.state, p.state.player, p.state.device, p.state.player.Options are assumed to always be non-nil here

	var trackUri *string
	var media *librespot.Media
	if p.state.player.Track != nil {
		trackUri = &p.state.player.Track.Uri
	}
	if p.primaryStream != nil {
		media = p.primaryStream.Media
	}

	p.app.mpris.EmitStateUpdate(
		mpris.MediaState{
			PlaybackStatus: playbackStatus,
			LoopStatus: mpris.GetLoopStatus(
				p.state.player.Options.RepeatingContext, p.state.player.Options.RepeatingTrack),
			Shuffle:    p.state.player.Options.ShufflingContext,
			Volume:     float64(p.state.device.Volume) / float64(player.MaxStateVolume),
			PositionMs: p.state.player.Position,
			Uri:        trackUri,
			Media:      media,
		},
	)
}

func (p *AppPlayer) handlePlayerEvent(ctx context.Context, ev *player.Event) {
	// Limit ourselves to 30 seconds for handling player events
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch ev.Type {
	case player.EventTypePlay:
		p.state.player.IsPlaying = true
		p.state.setPaused(false)
		p.state.player.IsBuffering = false
		p.updateState(ctx)

		p.sess.Events().OnPlayerPlay(
			p.primaryStream,
			p.state.player.ContextUri,
			p.state.player.Options.ShufflingContext,
			p.state.player.PlayOrigin,
			p.state.tracks.CurrentTrack(),
			p.state.trackPosition(),
		)

		p.emitMprisUpdate(mpris.Playing)

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePlaying,
			Data: ApiEventDataPlaying{
				ContextUri: p.state.player.ContextUri,
				Uri:        p.state.player.Track.Uri,
				Resume:     false,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypeResume:
		p.state.player.IsPlaying = true
		p.state.setPaused(false)
		p.state.player.IsBuffering = false
		p.updateState(ctx)

		p.sess.Events().OnPlayerResume(p.primaryStream, p.state.trackPosition())

		p.emitMprisUpdate(mpris.Playing)

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePlaying,
			Data: ApiEventDataPlaying{
				ContextUri: p.state.player.ContextUri,
				Uri:        p.state.player.Track.Uri,
				Resume:     true,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypePause:
		p.state.player.IsPlaying = true
		p.state.setPaused(true)
		p.state.player.IsBuffering = false
		p.updateState(ctx)

		p.sess.Events().OnPlayerPause(
			p.primaryStream,
			p.state.player.ContextUri,
			p.state.player.Options.ShufflingContext,
			p.state.player.PlayOrigin,
			p.state.tracks.CurrentTrack(),
			p.state.trackPosition(),
		)

		p.emitMprisUpdate(mpris.Paused)

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePaused,
			Data: ApiEventDataPaused{
				ContextUri: p.state.player.ContextUri,
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypeNotPlaying:
		// If a DJ narration just finished, load the music track immediately
		// instead of advancing the context queue.
		if p.djPendingMusicId != nil {
			pendingId := p.djPendingMusicId
			p.djPendingMusicId = nil
			if err := p.loadDJPendingMusic(ctx, pendingId); err != nil {
				p.app.log.WithError(err).Error("failed loading DJ music after narration")
			}
			return
		}

		p.sess.Events().OnPlayerEnd(p.primaryStream, p.state.trackPosition())

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeNotPlaying,
			Data: ApiEventDataNotPlaying{
				ContextUri: p.state.player.ContextUri,
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})

		hasNextTrack, err := p.advanceNext(context.TODO(), false, false)
		if err != nil {
			p.app.log.WithError(err).Error("failed advancing to next track")
		}

		// if no track to be played, just stop
		if !hasNextTrack {
			p.app.server.Emit(&ApiEvent{
				Type: ApiEventTypeStopped,
				Data: ApiEventDataStopped{
					PlayOrigin: p.state.playOrigin(),
				},
			})
			p.emitMprisUpdate(mpris.Stopped)
		}
	case player.EventTypeStop:
		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeStopped,
			Data: ApiEventDataStopped{
				PlayOrigin: p.state.playOrigin(),
			},
		})
		p.emitMprisUpdate(mpris.Stopped)
	default:
		panic("unhandled player event")
	}
}

type skipToFunc func(*connectpb.ContextTrack) bool

func (p *AppPlayer) loadContext(ctx context.Context, spotCtx *connectpb.Context, skipTo skipToFunc, paused, drop bool) error {
	ctxTracks, err := tracks.NewTrackListFromContext(ctx, p.app.log, p.sess.Spclient(), spotCtx)
	p.app.log.Debugf("loadContext %s: resolve err=%v (featureId=%s)", spotCtx.Uri, err, func() string {
		if p.state.player.PlayOrigin != nil {
			return p.state.player.PlayOrigin.FeatureIdentifier
		}
		return "<nil>"
	}())
	if err != nil {
		// Dynamic contexts (e.g. Spotify DJ) return empty pages from spclient.
		// Use whatever tracks Spotify sent in the play command's context pages.
		var staticTracks []*connectpb.ContextTrack
		for _, page := range spotCtx.Pages {
			staticTracks = append(staticTracks, page.Tracks...)
		}
		if len(staticTracks) == 0 {
			// DJ contexts send no tracks in the play command payload.
			p.app.log.WithError(err).Warnf("no tracks in play command payload for %s", spotCtx.Uri)
			p.app.log.Debugf("djAwaitingLoad: PlayOrigin.FeatureIdentifier=%q stateActive=%t prevTrack=%v prevContextUri=%q",
				func() string {
					if p.state.player.PlayOrigin != nil {
						return p.state.player.PlayOrigin.FeatureIdentifier
					}
					return "<nil>"
				}(),
				p.state.active,
				func() string {
					if p.state.player.Track != nil {
						return p.state.player.Track.Uri
					}
					return "<nil>"
				}(),
				p.state.player.ContextUri,
			)
			p.state.player.ContextUri = spotCtx.Uri
			p.state.player.ContextUrl = spotCtx.Url
			p.state.player.ContextRestrictions = spotCtx.Restrictions
			p.app.djCachedContextUri = spotCtx.Uri
			p.app.djSectionBuffer = nil // clear on new DJ context so stale sections aren't reused

			if p.state.player.ContextMetadata == nil {
				p.state.player.ContextMetadata = map[string]string{}
			}

			// Always use state_restore to get full session metadata (playlist_volatile_context_id,
			// lexicon_current_time, session_control_display, etc.) — the phone requires these
			// fields to activate "Switch it up".
			p.app.log.Debugf("lexicon: fresh DJ start reason=state_restore")

			lexCtx, lexErr := p.sess.Spclient().LexiconContextResolve(ctx, spotCtx.Uri, "state_restore")
			if lexErr == nil {
				for _, page := range lexCtx.GetPages() {
					for _, t := range page.GetTracks() {
						if t.Uri != "spotify:delimiter" && t.Uri != "" {
							staticTracks = append(staticTracks, t)
						}
					}
				}
				if len(staticTracks) > 0 {
					p.app.log.Infof("lexicon: pre-fetched %d DJ tracks for %s (volatile_id=%s lexicon_time=%s)",
						len(staticTracks), spotCtx.Uri,
						lexCtx.Metadata["playlist_volatile_context_id"],
						lexCtx.Metadata["lexicon_current_time"])
					for k, v := range lexCtx.Metadata {
						p.state.player.ContextMetadata[k] = v
					}
					p.app.djCachedNextTracks = staticTracks
					p.app.djCacheIsOurs = true
				}
			} else {
				p.app.log.Debugf("lexicon: resolve failed (%v), will wait for cluster", lexErr)
			}
			p.state.player.ContextMetadata["dj.interactivity_enabled"] = "true"

			// Send IsPlaying=false + full metadata first.
			// This signals Spotify to register a fresh DJ session server-side,
			// which causes it to eventually broadcast a ClusterUpdate that enables
			// "Switch it up" on the phone.
			p.player.Stop()
			p.primaryStream = nil
			p.secondaryStream = nil
			p.state.player.NextTracks = nil
			p.state.player.PrevTracks = nil
			p.state.player.PositionAsOfTimestamp = 0
			p.state.player.IsPlaying = false
			p.state.player.IsBuffering = false
			p.updateState(ctx)

			if len(staticTracks) == 0 {
				// Lexicon failed — wait for poll to get tracks.
				p.djPollAttempts = 0
				p.djPollTimer.Reset(5 * time.Second)
				p.djAwaitingLoad = true
				return nil
			}
			// Lexicon succeeded — fall through to static resolver and start playing immediately.
		}
		p.app.log.WithError(err).Warnf("context resolution failed, building static track list for %s (%d tracks)", spotCtx.Uri, len(staticTracks))
		resolver := spclient.NewStaticContextResolver(p.app.log, spotCtx.Uri, staticTracks)
		ctxTracks = tracks.NewTrackListFromResolver(p.app.log, resolver)
	}

	p.state.setPaused(paused)

	sessionId := make([]byte, 16)
	_, _ = rand.Read(sessionId)
	p.state.player.SessionId = base64.StdEncoding.EncodeToString(sessionId)

	p.state.player.ContextUri = spotCtx.Uri
	p.state.player.ContextUrl = spotCtx.Url
	p.state.player.Restrictions = spotCtx.Restrictions
	p.state.player.ContextRestrictions = spotCtx.Restrictions

	if spotCtx.Restrictions != nil {
		if len(spotCtx.Restrictions.DisallowTogglingShuffleReasons) > 0 {
			p.state.player.Options.ShufflingContext = false
		}
		if len(spotCtx.Restrictions.DisallowTogglingRepeatTrackReasons) > 0 {
			p.state.player.Options.RepeatingTrack = false
		}
		if len(spotCtx.Restrictions.DisallowTogglingRepeatContextReasons) > 0 {
			p.state.player.Options.RepeatingContext = false
		}
	}

	if p.state.player.ContextMetadata == nil {
		p.state.player.ContextMetadata = map[string]string{}
	}
	for k, v := range spotCtx.Metadata {
		p.state.player.ContextMetadata[k] = v
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	if skipTo == nil {
		// if shuffle is enabled, we'll start from a random track
		if err := ctxTracks.ToggleShuffle(ctx, p.state.player.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}

		// seek to the first track
		if err := ctxTracks.TrySeek(ctx, func(_ *connectpb.ContextTrack) bool { return true }); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}
	} else {
		// seek to the given track
		if err := ctxTracks.TrySeek(ctx, skipTo); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}

		// shuffle afterwards
		if err := ctxTracks.ToggleShuffle(ctx, p.state.player.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}
	}

	p.state.tracks = ctxTracks
	p.state.player.Track = ctxTracks.CurrentTrack()
	p.state.player.PrevTracks = ctxTracks.PrevTracks()
	p.state.player.NextTracks = ctxTracks.NextTracks(ctx, nil)
	p.state.player.Index = ctxTracks.Index()

	// load current track into stream
	if err := p.loadCurrentTrack(ctx, paused, drop); err != nil {
		return fmt.Errorf("failed loading current track (load context): %w", err)
	}

	return nil
}

func (p *AppPlayer) loadCurrentTrack(ctx context.Context, paused, drop bool) error {
	if p.primaryStream != nil {
		p.sess.Events().OnPrimaryStreamUnload(p.primaryStream, p.player.PositionMs())

		p.primaryStream = nil
	}

	// Skip delimiter tracks (used as queue separators in DJ mode).
	if p.state.player.Track.Uri == "spotify:delimiter" {
		return librespot.ErrMediaRestricted
	}
	// Normalize spotify:media:<id> → spotify:track:<id> (used in some DJ queue pushes).
	trackUri := strings.ReplaceAll(p.state.player.Track.Uri, "spotify:media:", "spotify:track:")
	spotId, err := librespot.SpotifyIdFromUri(trackUri)
	if err != nil {
		return fmt.Errorf("failed parsing uri: %w", err)
	} else if spotId.Type() != librespot.SpotifyIdTypeTrack && spotId.Type() != librespot.SpotifyIdTypeEpisode {
		return fmt.Errorf("unsupported spotify type: %s", spotId.Type())
	}

	// Clear any stale DJ narration state.
	p.djPendingMusicId = nil

	// If this is a DJ track with narration, load the narration clip as the primary
	// stream and remember the music track to play after it.
	// Try intro (session start), then jump (between tracks), then outro.
	if player.IsDJTrack(p.state.player.Track) {
		var narrKeys []string
		for k := range p.state.player.Track.Metadata {
			if strings.HasPrefix(k, "narration.") {
				narrKeys = append(narrKeys, k)
			}
		}
		introId := p.state.player.Track.Metadata["narration.intro.commentary_id"]
		jumpId := p.state.player.Track.Metadata["narration.jump.commentary_id"]
		p.app.log.Debugf("DJ track narration: keys=%d intro_id=%q jump_id=%q", len(narrKeys), introId, jumpId)
		var narr *player.DJNarration
		for _, narrType := range []string{"intro", "jump", "outro"} {
			if n := player.NarrationForTrack(p.state.player.Track, narrType); n != nil {
				narr = n
				break
			}
		}
		if narr != nil {
			narrId, err := player.NarrationSpotifyId(narr.CommentaryId)
			if err != nil {
				p.app.log.WithError(err).Warn("failed parsing DJ narration id, skipping to music")
			} else {
				narrStream, err := p.player.NewNarrationStream(ctx, p.app.client, narrId, 160, 0)
				if err != nil {
					p.app.log.WithError(err).Warn("failed loading DJ narration stream, skipping to music")
				} else {
					p.app.log.WithField("commentary_id", narr.CommentaryId).
						Infof("playing DJ intro narration before %s", spotId.Uri())

					p.primaryStream = narrStream
					p.djPendingMusicId = spotId

					if err := p.player.SetPrimaryStream(narrStream.Source, paused, drop); err != nil {
						return fmt.Errorf("failed setting DJ narration stream: %w", err)
					}

					p.sess.Events().PostPrimaryStreamLoad(narrStream, paused)

					p.state.updateTimestamp()
					p.state.player.PlaybackId = hex.EncodeToString(narrStream.PlaybackId)
					p.state.player.Duration = int64(narrStream.Media.Duration())
					p.state.player.IsPlaying = true
					p.state.player.IsBuffering = false
					p.state.setPaused(paused)
					p.updateState(ctx)

					return nil
				}
			}
		}
	}

	trackPosition := p.state.trackPosition()
	p.app.log.WithField("uri", spotId.Uri()).
		Debugf("loading %s (paused: %t, position: %dms)", spotId.Type(), paused, trackPosition)

	p.state.updateTimestamp()
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = true
	p.state.player.IsPaused = paused
	p.state.player.PlaybackSpeed = 0 // not progressing while buffering
	p.updateState(ctx)

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeWillPlay,
		Data: ApiEventDataWillPlay{
			ContextUri: p.state.player.ContextUri,
			Uri:        spotId.Uri(),
			PlayOrigin: p.state.playOrigin(),
		},
	})

	var prefetched bool
	if p.secondaryStream != nil && p.secondaryStream.Is(*spotId) {
		p.primaryStream = p.secondaryStream
		p.secondaryStream = nil
		prefetched = true
	} else {
		p.secondaryStream = nil
		prefetched = false

		var err error
		p.primaryStream, err = p.player.NewStream(ctx, p.app.client, *spotId, p.app.cfg.Bitrate, trackPosition)
		if err != nil && trackPosition > 0 {
			p.app.log.WithError(err).Warnf("failed creating stream at %dms for %s, retrying from 0", trackPosition, spotId)
			p.state.player.PositionAsOfTimestamp = 0
			trackPosition = 0
			p.primaryStream, err = p.player.NewStream(ctx, p.app.client, *spotId, p.app.cfg.Bitrate, 0)
		}
		if err != nil {
			return fmt.Errorf("failed creating stream for %s: %w", spotId, err)
		}
	}

	if err := p.player.SetPrimaryStream(p.primaryStream.Source, paused, drop); err != nil {
		return fmt.Errorf("failed setting stream for %s: %w", spotId, err)
	}

	p.sess.Events().PostPrimaryStreamLoad(p.primaryStream, paused)

	p.app.log.WithField("uri", spotId.Uri()).
		Infof("loaded %s %s (paused: %t, position: %dms, duration: %dms, prefetched: %t)", spotId.Type(),
			strconv.QuoteToGraphic(p.primaryStream.Media.Name()), paused, trackPosition, p.primaryStream.Media.Duration(),
			prefetched)

	p.state.updateTimestamp()
	p.state.player.PlaybackId = hex.EncodeToString(p.primaryStream.PlaybackId)
	p.state.player.Duration = int64(p.primaryStream.Media.Duration())
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = false
	p.state.setPaused(paused) // update IsPaused and PlaybackSpeed
	p.updateState(ctx)
	p.schedulePrefetchNext()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeMetadata,
		Data: ApiEventDataMetadata(*p.newApiResponseStatusTrack(p.primaryStream.Media, trackPosition)),
	})
	return nil
}

// loadDJPendingMusic loads the music track that follows a DJ narration clip.
// Called from handlePlayerEvent when EventTypeNotPlaying fires while
// djPendingMusicId is set (i.e. the narration just finished).
func (p *AppPlayer) loadDJPendingMusic(ctx context.Context, spotId *librespot.SpotifyId) error {
	p.app.log.WithField("uri", spotId.Uri()).Info("narration finished, loading DJ music track")

	if p.primaryStream != nil {
		p.sess.Events().OnPrimaryStreamUnload(p.primaryStream, p.player.PositionMs())
		p.primaryStream = nil
	}
	p.secondaryStream = nil

	stream, err := p.player.NewStream(ctx, p.app.client, *spotId, p.app.cfg.Bitrate, 0)
	if err != nil {
		return fmt.Errorf("failed creating DJ music stream for %s: %w", spotId, err)
	}

	p.primaryStream = stream
	if err := p.player.SetPrimaryStream(stream.Source, false, true); err != nil {
		return fmt.Errorf("failed setting DJ music stream: %w", err)
	}

	p.sess.Events().PostPrimaryStreamLoad(stream, false)

	p.app.log.WithField("uri", spotId.Uri()).
		Infof("loaded DJ music %s (duration: %dms)", strconv.QuoteToGraphic(stream.Media.Name()), stream.Media.Duration())

	p.state.updateTimestamp()
	p.state.player.PlaybackId = hex.EncodeToString(stream.PlaybackId)
	p.state.player.Duration = int64(stream.Media.Duration())
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = false
	p.state.setPaused(false)
	p.updateState(ctx)
	p.schedulePrefetchNext()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeMetadata,
		Data: ApiEventDataMetadata(*p.newApiResponseStatusTrack(stream.Media, 0)),
	})

	return nil
}

func (p *AppPlayer) setOptions(ctx context.Context, repeatingContext *bool, repeatingTrack *bool, shufflingContext *bool) {
	var requiresUpdate bool
	if repeatingContext != nil && *repeatingContext != p.state.player.Options.RepeatingContext {
		p.state.player.Options.RepeatingContext = *repeatingContext

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeRepeatContext,
			Data: ApiEventDataRepeatContext{
				Value: *repeatingContext,
			},
		})

		requiresUpdate = true
	}

	if repeatingTrack != nil && *repeatingTrack != p.state.player.Options.RepeatingTrack {
		p.state.player.Options.RepeatingTrack = *repeatingTrack

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeRepeatTrack,
			Data: ApiEventDataRepeatTrack{
				Value: *repeatingTrack,
			},
		})

		requiresUpdate = true
	}

	if p.state.tracks != nil && shufflingContext != nil && *shufflingContext != p.state.player.Options.ShufflingContext {
		if err := p.state.tracks.ToggleShuffle(ctx, *shufflingContext); err != nil {
			p.app.log.WithError(err).Errorf("failed toggling shuffle context (value: %t)", *shufflingContext)
			return
		}

		p.state.player.Options.ShufflingContext = *shufflingContext
		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, nil)
		p.state.player.Index = p.state.tracks.Index()

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeShuffleContext,
			Data: ApiEventDataShuffleContext{
				Value: *shufflingContext,
			},
		})

		requiresUpdate = true
	}

	if requiresUpdate {
		p.updateState(ctx)
	}
}

func (p *AppPlayer) addToQueue(ctx context.Context, track *connectpb.ContextTrack) {
	if p.state.tracks == nil {
		p.app.log.Warnf("cannot add to queue without a context")
		return
	}

	if track.Uid == "" {
		// The uid always seems unset, so we have to set one manually.
		p.state.queueID++
		track.Uid = fmt.Sprintf("q%d", p.state.queueID)
	}

	p.state.tracks.AddToQueue(track)
	p.state.player.PrevTracks = p.state.tracks.PrevTracks()
	p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, nil)
	p.updateState(ctx)
	p.schedulePrefetchNext()
}

func (p *AppPlayer) setQueue(ctx context.Context, prev []*connectpb.ContextTrack, next []*connectpb.ContextTrack) {
	p.app.log.Debugf("set_queue received: prev=%d next=%d (djAwaitingLoad=%t)", len(prev), len(next), p.djAwaitingLoad)

	if p.state.tracks == nil {
		p.app.log.Warnf("cannot set queue without a context")
		return
	}

	// If Spotify delivers DJ tracks via set_queue (rather than cluster nextTracks),
	// cache them so the pendingDJ path can build a real track list from them.
	if len(next) > 0 && p.app.djCachedContextUri != "" && p.state.player.ContextUri == p.app.djCachedContextUri {
		p.app.log.Debugf("caching %d DJ tracks from set_queue for %s", len(next), p.app.djCachedContextUri)
		p.app.djCachedNextTracks = next
	}

	p.state.tracks.SetQueue(prev, next)
	p.state.player.PrevTracks = p.state.tracks.PrevTracks()
	p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, next)
	p.updateState(ctx)
	p.schedulePrefetchNext()
}

func (p *AppPlayer) play(ctx context.Context) error {
	if p.primaryStream == nil {
		return fmt.Errorf("no primary stream")
	}

	// seek before play to ensure we are at the correct stream position
	seekPos := p.state.trackPosition()
	seekPos = max(0, min(seekPos, int64(p.primaryStream.Media.Duration())))
	if err := p.player.SeekMs(seekPos); err != nil {
		return fmt.Errorf("failed seeking before play: %w", err)
	}

	if err := p.player.Play(); err != nil {
		return fmt.Errorf("failed starting playback: %w", err)
	}

	streamPos := p.player.PositionMs()
	p.app.log.Debugf("resume track at %dms", streamPos)

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = streamPos
	p.state.setPaused(false)
	p.updateState(ctx)
	p.schedulePrefetchNext()

	return nil
}

func (p *AppPlayer) pause(ctx context.Context) error {
	if p.primaryStream == nil {
		return fmt.Errorf("no primary stream")
	}

	streamPos := p.player.PositionMs()
	p.app.log.Debugf("pause track at %dms", streamPos)

	if err := p.player.Pause(); err != nil {
		return fmt.Errorf("failed pausing playback: %w", err)
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = streamPos
	p.state.setPaused(true)
	p.updateState(ctx)
	p.schedulePrefetchNext()

	return nil
}

func (p *AppPlayer) seek(ctx context.Context, position int64) error {
	if p.primaryStream == nil {
		return fmt.Errorf("no primary stream")
	}

	oldPosition := p.player.PositionMs()
	position = max(0, min(position, int64(p.primaryStream.Media.Duration())))

	p.app.log.Debugf("seek track to %dms", position)
	if err := p.player.SeekMs(position); err != nil {
		return err
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = position
	p.updateState(ctx)
	p.schedulePrefetchNext()

	p.sess.Events().OnPlayerSeek(p.primaryStream, oldPosition, position)

	p.app.mpris.EmitSeekUpdate(
		mpris.SeekState{
			PositionMs: position,
		},
	)

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeSeek,
		Data: ApiEventDataSeek{
			ContextUri: p.state.player.ContextUri,
			Uri:        p.state.player.Track.Uri,
			Position:   int(position),
			Duration:   int(p.primaryStream.Media.Duration()),
			PlayOrigin: p.state.playOrigin(),
		},
	})

	return nil
}

func (p *AppPlayer) skipPrev(ctx context.Context, allowSeeking bool) error {
	if allowSeeking && p.player.PositionMs() > 3000 {
		return p.seek(ctx, 0)
	}

	p.sess.Events().OnPlayerSkipBackward(p.primaryStream, p.player.PositionMs())

	if p.state.tracks != nil {
		p.app.log.Debug("skip previous track")
		p.state.tracks.GoPrev()

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, nil)
		p.state.player.Index = p.state.tracks.Index()
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	// load current track into stream
	if err := p.loadCurrentTrack(ctx, p.state.player.IsPaused, true); err != nil {
		return fmt.Errorf("failed loading current track (skip prev): %w", err)
	}

	return nil
}

func (p *AppPlayer) skipNext(ctx context.Context, track *connectpb.ContextTrack) error {
	p.sess.Events().OnPlayerSkipForward(p.primaryStream, p.player.PositionMs(), track != nil)

	if track != nil {
		contextSpotType := librespot.InferSpotifyIdTypeFromContextUri(p.state.player.ContextUri)
		if err := p.state.tracks.Seek(ctx, tracks.ContextTrackComparator(contextSpotType, track)); err != nil {
			// Track not found in our list. For DJ mode, load it directly from
			// the hint Spotify sent rather than silently restarting from track 0.
			if player.IsDJTrack(p.state.player.Track) {
				p.app.log.Warnf("DJ skip target %s not in track list, loading directly", track.Uri)
				p.state.player.Timestamp = time.Now().UnixMilli()
				p.state.player.PositionAsOfTimestamp = 0
				p.state.player.Track = librespot.ContextTrackToProvidedTrack(contextSpotType, track)
				return p.loadCurrentTrack(ctx, p.state.player.IsPaused, true)
			}
			return err
		}

		p.state.player.Timestamp = time.Now().UnixMilli()
		p.state.player.PositionAsOfTimestamp = 0

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, nil)
		p.state.player.Index = p.state.tracks.Index()

		if err := p.loadCurrentTrack(ctx, p.state.player.IsPaused, true); err != nil {
			// In DJ mode, narration/media clips appear in the queue as spotify:track: but
			// return 404 when fetched — skip past them automatically.
			isDJ := p.state.player.PlayOrigin != nil && p.state.player.PlayOrigin.FeatureIdentifier == "dynamic-sessions"
			if isDJ {
				p.app.log.WithError(err).Warnf("DJ track %s failed to load, auto-advancing to next", p.state.player.Track.GetUri())
				_, advErr := p.advanceNext(ctx, false, true)
				return advErr
			}
			return err
		}
		return nil
	} else {
		hasNextTrack, err := p.advanceNext(ctx, true, true)
		if err != nil {
			return fmt.Errorf("failed skipping to next track: %w", err)
		}

		// if no track to be played, just stop
		if !hasNextTrack {
			p.app.server.Emit(&ApiEvent{
				Type: ApiEventTypeStopped,
				Data: ApiEventDataStopped{
					PlayOrigin: p.state.playOrigin(),
				},
			})
		}

		return nil
	}
}

func (p *AppPlayer) advanceNext(ctx context.Context, forceNext, drop bool) (bool, error) {
	var uri string
	var hasNextTrack bool
	if p.state.tracks != nil {
		if !forceNext && p.state.player.Options.RepeatingTrack {
			hasNextTrack = true
			p.state.player.IsPaused = false
		} else {
			// If we are still waiting for the initial DJ cluster update, the track
			// list is the old (pre-DJ) context. Advancing it would play a playlist
			// song. Signal the server and wait for the cluster push instead.
			if p.djAwaitingLoad {
				if p.state.player.ContextUri == p.app.djCachedContextUri {
					p.app.log.Debugf("advanceNext: djAwaitingLoad=true, keeping stream alive (context=%s)", p.state.player.ContextUri)
					p.updateState(ctx)
					return false, nil
				}
				// Stale flag from a previous DJ session; clear it so normal
				// playlists are not blocked.
				p.app.log.Debugf("advanceNext: clearing stale djAwaitingLoad (context=%s, cachedDJ=%s)", p.state.player.ContextUri, p.app.djCachedContextUri)
				p.djAwaitingLoad = false
			}

			// try to get the next track
			hasNextTrack = p.state.tracks.GoNext(ctx)

			// if we could not get the next track we probably ended the context
			if !hasNextTrack {
				// DJ contexts manage their queue externally via ClusterUpdate/set_queue —
				// do not loop back to track 0 or attempt autoplay when exhausted.
				// Use PlayOrigin.FeatureIdentifier so this only fires when we are
				// actually in an active DJ session, not whenever any playlist whose
				// URI was previously used as a DJ seed reaches its end.
				isDJ := p.state.player.PlayOrigin != nil && p.state.player.PlayOrigin.FeatureIdentifier == "dynamic-sessions"
				if isDJ {
					p.app.log.Debugf("advanceNext: isDJ no next tracks, setting djAwaitingLoad and triggering poll")
					p.djAwaitingLoad = true
					p.djPollAttempts = 0
					p.djPollTimer.Reset(5 * time.Second)
					p.updateState(ctx)
					return false, nil
				}
				hasNextTrack = p.state.tracks.GoStart(ctx)
				if !p.state.player.Options.RepeatingContext {
					hasNextTrack = false
				}
			}

			p.state.player.IsPaused = !hasNextTrack
		}

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx, nil)
		p.state.player.Index = p.state.tracks.Index()

		// Proactive DJ queue refresh: when nextTracks drops below 8, schedule a
		// lexicon poll to fetch 15 fresh tracks with new jump points. We do NOT set
		// IsPlaying=false here — that disrupts the phone's DJ state and greys out
		// "Switch it up" even after the queue is refreshed.
		isDJActive := p.state.player.PlayOrigin != nil && p.state.player.PlayOrigin.FeatureIdentifier == "dynamic-sessions"
		if isDJActive && !p.djAwaitingLoad && len(p.state.player.NextTracks) < 8 {
			p.app.log.Infof("advanceNext: DJ queue low (%d tracks), scheduling lexicon refresh", len(p.state.player.NextTracks))
			p.djPollAttempts = 0
			if !p.djPollTimer.Stop() {
				select {
				case <-p.djPollTimer.C:
				default:
				}
			}
			p.djPollTimer.Reset(3 * time.Second)
		}

		uri = p.state.player.Track.Uri
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	if !hasNextTrack && !p.app.cfg.DisableAutoplay && !strings.HasPrefix(p.state.player.ContextUri, "spotify:station:") {
		p.state.player.Suppressions = &connectpb.Suppressions{}

		// Consider all tracks as recent because we got here by reaching the end of the context
		var prevTrackUris []string
		if p.state.tracks != nil {
			for _, track := range p.state.tracks.AllTracks(ctx) {
				prevTrackUris = append(prevTrackUris, track.Uri)
			}
		}

		if len(prevTrackUris) == 0 {
			p.app.log.Warnf("cannot resolve autoplay station because there are no previous tracks in context %s", p.state.player.ContextUri)
			return false, nil
		}

		p.app.log.Debugf("resolving autoplay station for %d tracks", len(prevTrackUris))
		spotCtx, err := p.sess.Spclient().ContextResolveAutoplay(ctx, &playerpb.AutoplayContextRequest{
			ContextUri:     proto.String(p.state.player.ContextUri),
			RecentTrackUri: prevTrackUris,
		})
		if err != nil {
			p.app.log.WithError(err).Warnf("failed resolving station for %s", p.state.player.ContextUri)
			return false, nil
		}

		p.app.log.Debugf("resolved autoplay station: %s", spotCtx.Uri)
		if err := p.loadContext(ctx, spotCtx, func(_ *connectpb.ContextTrack) bool { return true }, false, drop); err != nil {
			p.app.log.WithError(err).Warnf("failed loading station for %s", p.state.player.ContextUri)
			return false, nil
		}

		return true, nil
	}

	if !hasNextTrack {
		p.state.player.IsPlaying = false
		p.state.player.IsPaused = false
		p.state.player.IsBuffering = false
	}

	// load current track into stream
	isDJSession := p.state.player.PlayOrigin != nil && p.state.player.PlayOrigin.FeatureIdentifier == "dynamic-sessions"
	if err := p.loadCurrentTrack(ctx, !hasNextTrack, drop); errors.Is(err, librespot.ErrMediaRestricted) || errors.Is(err, librespot.ErrNoSupportedFormats) || (isDJSession && err != nil && !forceNext) {
		p.app.log.WithError(err).Infof("skipping unplayable media: %s", uri)
		if forceNext {
			if isDJSession {
				// Two consecutive unplayable DJ tracks (e.g. back-to-back narration clips).
				// Signal Spotify for a fresh queue rather than failing hard.
				p.app.log.WithError(err).Warnf("DJ: consecutive unplayable tracks, signaling Spotify for more")
				p.player.Stop()
				p.primaryStream = nil
				p.secondaryStream = nil
				p.state.player.IsPlaying = false
				p.state.player.IsBuffering = false
				p.djAwaitingLoad = true
				p.updateState(ctx)
				return false, nil
			}
			// we failed in finding another track to play, just stop
			return false, err
		}

		return p.advanceNext(ctx, true, drop)
	} else if err != nil {
		return false, fmt.Errorf("failed loading current track (advance to %s): %w", uri, err)
	}

	return hasNextTrack, nil
}

// Return the volume as an integer in the range 0..player.MaxStateVolume, as
// used in the API.
func (p *AppPlayer) apiVolume() uint32 {
	return uint32(math.Round(float64(p.state.device.Volume*p.app.cfg.VolumeSteps) / player.MaxStateVolume))
}

// Set the player volume to the new volume, also notifies about the change.
func (p *AppPlayer) updateVolume(newVal uint32) {
	if newVal > player.MaxStateVolume {
		newVal = player.MaxStateVolume
	} else if newVal < 0 {
		newVal = 0
	}

	p.app.log.Debugf("update volume requested to %d/%d", newVal, player.MaxStateVolume)
	p.player.SetVolume(newVal)

	// Save the volume to the state
	p.app.state.LastVolume = &newVal
	if err := p.app.state.Write(); err != nil {
		p.app.log.WithError(err).Error("failed writing state after volume change")
	}

	// If there is a value in the channel buffer, remove it.
	select {
	case <-p.volumeUpdate:
	default:
	}

	p.volumeUpdate <- float32(newVal) / player.MaxStateVolume
}

// Send notification that the volume changed.
// The original change can come from anywhere: from Spotify Connect, from the
// REST API, or from a volume mixer.
func (p *AppPlayer) volumeUpdated(ctx context.Context) {
	// Limit ourselves to 5 seconds for handling volume updates
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := p.putConnectState(ctx, connectpb.PutStateReason_VOLUME_CHANGED); err != nil {
		p.app.log.WithError(err).Error("failed put state after volume change")
	}

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeVolume,
		Data: ApiEventDataVolume{
			Value: p.apiVolume(),
			Max:   p.app.cfg.VolumeSteps,
		},
	})
}

func (p *AppPlayer) stopPlayback(ctx context.Context) error {
	p.player.Stop()
	p.primaryStream = nil
	p.secondaryStream = nil

	p.state.reset()
	if err := p.putConnectState(ctx, connectpb.PutStateReason_BECAME_INACTIVE); err != nil {
		return fmt.Errorf("failed inactive state put: %w", err)
	}

	p.schedulePrefetchNext()

	if p.app.cfg.ZeroconfEnabled {
		p.logout <- p
	}

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeInactive,
	})

	return nil
}
