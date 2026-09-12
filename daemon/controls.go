package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/devgianlu/go-librespot/mpris"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	playerpb "github.com/devgianlu/go-librespot/proto/spotify/player"
	"github.com/devgianlu/go-librespot/spclient"
	"github.com/devgianlu/go-librespot/tracks"
	"google.golang.org/protobuf/proto"
)

// prefetchNext queues the stream for whatever plays next, so that the
// transition into it is seamless. Only picking the track happens here; fetching
// it runs on the loader lane, because a prefetch is a full track load — audio
// key, storage resolve, first chunk, and any narration around it.
func (p *AppPlayer) prefetchNext() {
	repeatingTrack := p.state.player.Options.RepeatingTrack

	// With repeat-track enabled the next thing to play is this same track
	// again; prefetch it like any other upcoming track so the transition
	// (including a crossfade) is seamless.
	repeatUri := p.state.player.Track.GetUri()
	repeatMetadata := maps.Clone(p.state.player.Track.GetMetadata())

	if repeatingTrack {
		p.submitPrefetch(repeatUri, repeatMetadata)
		return
	}

	if p.state.tracks == nil {
		return
	}

	var nextUri string
	var nextMetadata map[string]string
	p.listJob("peek next", classPrefetch, nil,
		func(ctx context.Context, list *tracks.List) error {
			if next := list.PeekNext(ctx); next != nil {
				nextUri, nextMetadata = next.Uri, maps.Clone(next.Metadata)
			}
			return nil
		},
		func(p *AppPlayer, _ *tracks.Snapshot, err error) {
			if err != nil || nextUri == "" {
				return
			}

			p.submitPrefetch(nextUri, nextMetadata)
		})
}

// submitPrefetch queues the stream for uri unless it is already the one waiting.
// Runs on the player loop, so it can check what is already prefetched.
func (p *AppPlayer) submitPrefetch(uri string, metadata map[string]string) {
	if uri == "" {
		// It should be implemented some day (the ContextTrack has enough
		// information to infer the track Uri) but it's hard to reproduce this
		// issue.
		p.app.log.Warn("cannot prefetch next track because the uri field is empty")
		return
	}

	nextId, err := librespot.SpotifyIdFromUri(uri)
	if err != nil {
		p.app.log.WithError(err).WithField("uri", uri).Warn("failed parsing prefetch uri")
		return
	} else if p.secondaryStream != nil && p.secondaryStream.Is(*nextId) {
		return
	}

	p.app.log.WithField("uri", nextId.Uri()).Debugf("prefetching next %s", nextId.Type())

	p.loader.submit(loaderJob{
		name:  "prefetch " + nextId.Uri(),
		class: classPrefetch,
		gen:   p.prefetchGen,
		run: func(ctx context.Context) loaderResult {
			stream, err := p.player.NewStream(ctx, p.app.client, *nextId, p.app.cfg.Bitrate, 0)
			if err != nil {
				return loaderResult{err: fmt.Errorf("failed prefetching %s stream: %w", nextId.Type(), err)}
			}

			// Narration is prefetched with the track. The player promotes this
			// source the instant the current one ends, so if it were the bare
			// track the music would be heard for however long the synthesis
			// takes before the load replaces it. Reaching a prefetched track
			// always means arriving in turn, hence the introduction rather than
			// the jump line.
			source := p.narrate(ctx, metadata, nextId.Uri(), stream.Source, narrationIntroPrefix)

			return loaderResult{
				discard: func() { closeSource(source) },
				commit: func(p *AppPlayer, err error) {
					if err == nil {
						p.commitPrefetch(stream, source)
					}
				},
			}
		},
	})
}

// closeSource releases a source the daemon built but is not going to play.
// Decoders disagree on whether Close returns an error, so both shapes are tried.
func closeSource(source librespot.AudioSource) {
	switch c := source.(type) {
	case interface{ Close() error }:
		_ = c.Close()
	case interface{ Close() }:
		c.Close()
	}
}

func (p *AppPlayer) commitPrefetch(stream *player.Stream, source librespot.AudioSource) {
	p.secondaryStream = stream
	p.secondarySource = source
	p.player.SetSecondaryStream(source)

	p.app.log.WithField("uri", stream.RequestedId.Uri()).
		Infof("prefetched %s %s (duration: %dms)", stream.RequestedId.Type(),
			strconv.QuoteToGraphic(stream.Media.Name()), stream.Media.Duration())
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

func (p *AppPlayer) handlePlayerEvent(ev *player.Event) {
	switch ev.Type {
	case player.EventTypePlay:
		p.state.player.IsPlaying = true
		p.state.setPaused(false)
		p.state.player.IsBuffering = false
		p.updateState()

		p.sess.Events().OnPlayerPlay(
			p.primaryStream,
			p.state.player.ContextUri,
			p.state.player.Options.ShufflingContext,
			p.state.player.PlayOrigin,
			p.state.player.Track,
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
		p.updateState()

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
		p.updateState()

		p.sess.Events().OnPlayerPause(
			p.primaryStream,
			p.state.player.ContextUri,
			p.state.player.Options.ShufflingContext,
			p.state.player.PlayOrigin,
			p.state.player.Track,
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
		p.sess.Events().OnPlayerEnd(p.primaryStream, p.state.trackPosition())

		// Played to the end: clear the resume point before advancing, so that
		// playing this episode again (repeat, or picking it from a show later)
		// starts it over instead of resuming a second before the end.
		p.reportResumeFinished(p.primaryStream)

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeNotPlaying,
			Data: ApiEventDataNotPlaying{
				ContextUri: p.state.player.ContextUri,
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})

		// A set_sleep_timer("end_of_track") is exactly this moment: the
		// current track has finished. Actually pause here instead of
		// advancing - this player has gapless/crossfade behavior, so the
		// underlying output can keep right on producing audio into whatever
		// is queued next regardless of whether the daemon "advances";
		// merely reporting paused state without calling pause() leaves the
		// speaker still playing while the app shows it as stopped. Reuses
		// the same call the duration-based timer already uses, which also
		// emits the normal EventTypePause follow-up (see above) that
		// reports paused/MPRIS state - no need to duplicate that here.
		if p.sleepAtEndOfTrack {
			p.sleepAtEndOfTrack = false
			p.state.player.SleepTimer = nil
			if err := p.pause(); err != nil {
				p.app.log.WithError(err).Warn("failed pausing playback for sleep timer")
			}
			return
		}

		p.advanceNext(false, false, func(hasNextTrack bool, err error) {
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
		})
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

// loadContext starts playing a context. The state that describes it is claimed
// here, so controllers see the new context immediately; resolving the context
// and walking it to the starting track runs on the loader lane, and the track
// it lands on is loaded once it has.
func (p *AppPlayer) loadContext(spotCtx *connectpb.Context, skipTo skipToFunc, paused, drop bool, then func(error)) {
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

	// The previous context's surroundings do not describe this one, and the new
	// track is not known until the context resolves.
	p.state.player.Track = nil
	p.state.player.PrevTracks = nil
	p.state.player.NextTracks = nil
	p.state.player.Index = nil
	p.state.player.ContextMetadata = contextMetadata(spotCtx.Metadata, nil)

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = true
	p.state.player.PlaybackSpeed = 0
	p.updateState()

	shuffle := p.state.player.Options.ShufflingContext
	p.loadGen++

	p.loader.submit(loaderJob{
		name:  "resolve " + spotCtx.Uri,
		class: classLoad,
		gen:   p.loadGen,
		run: func(ctx context.Context) loaderResult {
			list, snap, err := resolveContext(ctx, p.app.log, p.sess.Spclient(), spotCtx, skipTo, shuffle)
			if err != nil {
				return loaderResult{
					err:    err,
					commit: func(_ *AppPlayer, err error) { then(err) },
				}
			}

			return loaderResult{
				commit: func(p *AppPlayer, err error) {
					p.state.tracks = list
					p.state.player.ContextMetadata = contextMetadata(spotCtx.Metadata, snap.Metadata)
					p.publishSnapshot(snap)

					// skip forward if the track it landed on (or a run of them) is unplayable.
					p.loadCurrentTrackOrSkip(paused, drop, true, func(err error) {
						if err != nil {
							err = fmt.Errorf("failed loading current track (load context): %w", err)
						}
						then(err)
					})
				},
			}
		},
	})
}

// transferContext takes over playback from another device. The transfer itself
// is claimed by the caller before this is reached; resolving the context and
// finding the track being handed over runs on the loader lane.
func (p *AppPlayer) transferContext(transferState *connectpb.TransferState, paused bool) {
	spotCtx := transferState.CurrentSession.Context
	shuffle := transferState.Options.GetShufflingContext()
	current := transferState.Playback.CurrentTrack
	queue := transferState.Queue

	p.loadGen++

	p.loader.submit(loaderJob{
		name:  "transfer " + spotCtx.Uri,
		class: classLoad,
		gen:   p.loadGen,
		run: func(ctx context.Context) loaderResult {
			list, err := tracks.NewTrackListFromContext(ctx, p.app.log, p.sess.Spclient(), spotCtx)
			if err != nil {
				return loaderResult{err: fmt.Errorf("failed creating track list: %w", err)}
			}

			// Seek to the transferred track, playing it ahead of the context if
			// it cannot be located.
			if err := list.TrySeekTo(ctx, current); err != nil {
				return loaderResult{err: fmt.Errorf("failed seeking to track: %w", err)}
			}

			if err := list.ToggleShuffle(ctx, shuffle); err != nil {
				return loaderResult{err: fmt.Errorf("failed shuffling context: %w", err)}
			}

			for _, track := range queue.GetTracks() {
				list.AddToQueue(track)
			}
			list.SetPlayingQueue(queue.GetIsPlayingQueue())

			snap := list.Snapshot(ctx, nil)

			return loaderResult{
				commit: func(p *AppPlayer, err error) {
					if err != nil {
						return
					}

					p.state.queueID = highestQueueID(queue.GetTracks())
					p.state.tracks = list
					p.state.player.ContextMetadata = contextMetadata(spotCtx.Metadata, snap.Metadata)
					p.publishSnapshot(snap)

					// skip forward if the transferred track is unplayable, so a
					// cast onto a refused track does not freeze the player.
					p.loadCurrentTrackOrSkip(paused, true, false, func(err error) {
						if err != nil {
							p.app.log.WithError(err).Warn("failed loading current track (transfer)")
						}
					})
				},
			}
		},
	})
}

// highestQueueID reports the largest "q<number>" uid in a transferred queue, so
// that ids handed out afterwards carry on from it rather than repeating. The
// official clients start again at 0, which duplicates ids across a transfer and
// makes reordering behave strangely.
func highestQueueID(queued []*connectpb.ContextTrack) uint64 {
	var highest uint64
	for _, track := range queued {
		if track.Uid == "" || track.Uid[0] != 'q' {
			continue
		}

		n, err := strconv.ParseUint(track.Uid[1:], 10, 64)
		if err != nil {
			continue
		}

		highest = max(highest, n)
	}
	return highest
}

// resolveContext resolves a context and walks it to the track playback should
// start from. Runs on the loader lane: the list it builds is not the daemon's
// until the result is applied.
func resolveContext(ctx context.Context, log librespot.Logger, sp *spclient.Spclient, spotCtx *connectpb.Context, skipTo skipToFunc, shuffle bool) (*tracks.List, *tracks.Snapshot, error) {
	list, err := tracks.NewTrackListFromContext(ctx, log, sp, spotCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed creating track list: %w", err)
	}

	// Shuffling picks where to start, so with no track asked for it comes
	// first; with one, it is applied around the track that was asked for.
	if skipTo == nil {
		if err := list.ToggleShuffle(ctx, shuffle); err != nil {
			return nil, nil, fmt.Errorf("failed shuffling context: %w", err)
		}
		skipTo = func(*connectpb.ContextTrack) bool { return true }
	}

	if err := list.TrySeek(ctx, skipTo); err != nil {
		return nil, nil, fmt.Errorf("failed seeking to track: %w", err)
	}

	if err := list.ToggleShuffle(ctx, shuffle); err != nil {
		return nil, nil, fmt.Errorf("failed shuffling context: %w", err)
	}

	return list, list.Snapshot(ctx, nil), nil
}

// isUnplayableMedia reports whether a load failed for a reason that skipping
// forward can get past: media the account may not play, media in no format the
// daemon supports, or a track whose audio key Spotify refused.
func isUnplayableMedia(err error) bool {
	var keyErr *audio.KeyProviderError
	return errors.Is(err, librespot.ErrMediaRestricted) ||
		errors.Is(err, librespot.ErrNoSupportedFormats) ||
		errors.As(err, &keyErr)
}

// loadCurrentTrackOrSkip loads the current track; if it is unplayable it
// advances forward to the first playable one instead of failing — so a
// transfer, cast or context load that lands on a refused track does not freeze
// the player. advanceNext walks a run of unplayable tracks, bounded.
func (p *AppPlayer) loadCurrentTrackOrSkip(paused, drop, resume bool, then func(error)) {
	uri := p.state.player.Track.GetUri()

	p.loadCurrentTrack(paused, drop, resume, func(err error) {
		if err == nil || !isUnplayableMedia(err) {
			then(err)
			return
		}

		p.app.log.WithError(err).Warnf("current track unplayable, skipping forward: %s", uri)
		p.advanceNext(true, drop, func(_ bool, err error) {
			if err != nil {
				then(fmt.Errorf("failed advancing past unplayable track: %w", err))
				return
			}
			then(nil)
		})
	})
}

// loadCurrentTrack starts playing whatever the state points at. Only the
// bookkeeping happens here: fetching the media runs on the loader lane, and then
// is called back on the player loop once it has, with whatever went wrong.
func (p *AppPlayer) loadCurrentTrack(paused, drop, resume bool, then func(error)) {
	if p.primaryStream != nil {
		unloadPosition := p.player.PositionMs()
		p.sess.Events().OnPrimaryStreamUnload(p.primaryStream, unloadPosition)

		// Whatever replaces this stream, the listener stopped here: remember
		// the spot before losing track of the outgoing episode.
		p.reportResumePosition(p.primaryStream, unloadPosition)

		p.primaryStream = nil
	}

	spotId, err := librespot.SpotifyIdFromUri(p.state.player.Track.GetUri())
	if err != nil {
		then(fmt.Errorf("failed parsing uri: %w", err))
		return
	} else if spotId.Type() != librespot.SpotifyIdTypeTrack && spotId.Type() != librespot.SpotifyIdTypeEpisode {
		then(fmt.Errorf("unsupported spotify type: %s", spotId.Type()))
		return
	}

	trackPosition := p.state.trackPosition()
	p.app.log.WithField("uri", spotId.Uri()).
		Debugf("loading %s (paused: %t, position: %dms)", spotId.Type(), paused, trackPosition)

	// Whether the track starts at its very beginning. Sampled before updateTimestamp,
	// which folds the time elapsed since the last update back into the declared position.
	fromStart := p.state.player.PositionAsOfTimestamp == 0

	p.state.updateTimestamp()
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = true
	p.state.player.IsPaused = paused
	p.state.player.PlaybackSpeed = 0 // not progressing while buffering
	p.updateState()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeWillPlay,
		Data: ApiEventDataWillPlay{
			ContextUri: p.state.player.ContextUri,
			Uri:        spotId.Uri(),
			PlayOrigin: p.state.playOrigin(),
		},
	})

	// This load supersedes any older one, and anything prefetched for it.
	p.loadGen++
	p.prefetchGen++

	var prefetchedStream *player.Stream
	var prefetchedSource librespot.AudioSource
	if p.secondaryStream != nil && p.secondaryStream.Is(*spotId) {
		prefetchedStream = p.secondaryStream
		// Whatever the player was handed as the secondary, including any
		// narration already wrapped around it. Re-wrapping would build a second
		// source for the same track, and the player would treat it as a new one
		// and restart the transition.
		prefetchedSource = p.secondarySource
		p.secondaryStream = nil
		p.secondarySource = nil
	} else {
		// The prefetched stream (if any) is not the track being loaded: clear
		// it from the player too, so an upcoming track change cannot switch
		// or fade into a stale stream.
		p.clearUpcoming()
	}

	// Reaching a track by jumping straight to it gets the jump line, which is
	// worded for having moved deliberately rather than arrived in turn.
	introPrefix := narrationIntroPrefix
	if p.narrationJumped {
		introPrefix = narrationJumpPrefix
	}
	p.narrationJumped = false

	metadata := maps.Clone(p.state.player.Track.GetMetadata())
	p.loadInFlight = true

	p.loader.submit(loaderJob{
		name:  "load " + spotId.Uri(),
		class: classLoad,
		gen:   p.loadGen,
		run: func(ctx context.Context) loaderResult {
			position, startsAtZero := trackPosition, fromStart
			if resume {
				if resumed, ok := p.lookupResumePosition(ctx, *spotId); ok {
					position, startsAtZero = resumed, false
				}
			}

			stream, err := p.fetchTrack(ctx, *spotId, fetchTrackOpts{
				position:         position,
				fromStart:        startsAtZero,
				paused:           paused,
				drop:             drop,
				introPrefix:      introPrefix,
				metadata:         metadata,
				prefetchedStream: prefetchedStream,
				prefetchedSource: prefetchedSource,
			})
			if err != nil {
				return loaderResult{
					err:    err,
					commit: func(_ *AppPlayer, err error) { then(err) },
				}
			}

			// No discard: fetchTrack hands the source to the player before
			// returning, and the player drops it when the next load replaces
			// it. Closing it here could close a stream still being read.
			return loaderResult{
				commit: func(p *AppPlayer, err error) {
					p.commitLoad(stream, spotId.Uri(), paused, position, prefetchedStream != nil)
					then(err)
				},
			}
		},
	})
}

type fetchTrackOpts struct {
	position         int64
	fromStart        bool
	paused           bool
	drop             bool
	introPrefix      string
	metadata         map[string]string
	prefetchedStream *player.Stream
	prefetchedSource librespot.AudioSource
}

// fetchTrack builds the stream for a track and hands it to the player. Runs on
// the loader lane, so it touches nothing the player loop owns.
func (p *AppPlayer) fetchTrack(ctx context.Context, spotId librespot.SpotifyId, opts fetchTrackOpts) (*player.Stream, error) {
	log := p.app.log.WithField("uri", spotId.Uri())

	stream := opts.prefetchedStream
	if stream == nil {
		var err error
		if stream, err = p.player.NewStream(ctx, p.app.client, spotId, p.app.cfg.Bitrate, opts.position); err != nil {
			return nil, fmt.Errorf("failed creating stream for %s: %w", spotId, err)
		}
	} else if !opts.fromStart {
		// A prefetched stream was created at position zero, so a non-zero start
		// position (an episode's resume point, or a transfer) has to be applied
		// here unless the stream is declared to start from zero: seeking a few
		// milliseconds in rewinds a stream the output is already playing.
		seekTo := max(0, min(opts.position, int64(stream.Media.Duration())))
		if err := stream.Source.SetPositionMs(seekTo); err != nil {
			return nil, fmt.Errorf("failed seeking prefetched stream for %s: %w", spotId, err)
		}
	}

	// A DJ context asks for the DJ to talk around some of its tracks. Only from
	// the start: resuming mid-track, or seeking, should not replay the lead-in.
	var skipped string
	if !opts.fromStart {
		skipped = ", skipped: starting mid-track"
	}

	if available := narrationKinds(opts.metadata); len(available) == 0 {
		log.Debugf("track has no narration")
	} else {
		log.Debugf("track has narration: %s (playing %s%s)", strings.Join(available, ", "),
			narrationPlan(opts.metadata, opts.introPrefix), skipped)
	}

	source := stream.Source
	switch {
	case !opts.fromStart:
		// Mid-track: the bare stream, even if a narrated one was prefetched.
	case opts.prefetchedSource != nil:
		// Already narrated while prefetching, and already playing: keep the very
		// same source so the player sees this load as acknowledging the
		// transition it has made rather than as a new track.
		source = opts.prefetchedSource
	default:
		source = p.narrate(ctx, opts.metadata, spotId.Uri(), source, opts.introPrefix)
	}

	if err := p.player.SetPrimaryStream(source, opts.paused, opts.drop); err != nil {
		return nil, fmt.Errorf("failed setting stream for %s: %w", spotId, err)
	}

	p.sess.Events().PostPrimaryStreamLoad(stream, opts.paused)

	return stream, nil
}

// commitLoad reports the track the player is now on. Runs on the player loop.
func (p *AppPlayer) commitLoad(stream *player.Stream, uri string, paused bool, trackPosition int64, prefetched bool) {
	p.primaryStream = stream

	// A play or pause that arrived while this was loading asked for a state the
	// stream was not loaded in, so apply it now.
	if p.state.player.IsPaused != paused {
		paused = p.state.player.IsPaused

		var err error
		if paused {
			err = p.player.Pause()
		} else {
			err = p.player.Play()
		}
		if err != nil {
			p.app.log.WithError(err).Warn("failed applying playback state requested during the load")
		}
	}

	p.app.log.WithField("uri", uri).
		Infof("loaded %s %s (paused: %t, position: %dms, duration: %dms, prefetched: %t)",
			stream.RequestedId.Type(), strconv.QuoteToGraphic(stream.Media.Name()), paused,
			trackPosition, stream.Media.Duration(), prefetched)

	// Now that the media is known, publish what controllers need to draw the
	// track. This has to happen after the assignments above, which replace
	// Track wholesale with a fresh ProvidedTrack from the track list.
	enrichTrackMetadata(p.state.player.Track, stream.Media)

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = trackPosition
	p.state.player.PlaybackId = hex.EncodeToString(stream.PlaybackId)
	p.state.player.Duration = int64(stream.Media.Duration())
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = false
	p.state.setPaused(paused) // update IsPaused and PlaybackSpeed
	p.updateState()
	p.schedulePrefetchNext()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeMetadata,
		Data: ApiEventDataMetadata(*p.newApiResponseStatusTrack(stream, trackPosition)),
	})
}

func (p *AppPlayer) setOptions(repeatingContext *bool, repeatingTrack *bool, shufflingContext *bool) {
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
		shuffle := *shufflingContext

		// Reported straight away: this is what the shuffle button binds to, and
		// reordering the context can take a walk of every page. Reverted below
		// if that walk fails.
		p.state.player.Options.ShufflingContext = shuffle
		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeShuffleContext,
			Data: ApiEventDataShuffleContext{
				Value: shuffle,
			},
		})

		p.listJob("shuffle context", classMutate, nil,
			func(ctx context.Context, list *tracks.List) error {
				return list.ToggleShuffle(ctx, shuffle)
			},
			func(p *AppPlayer, snap *tracks.Snapshot, err error) {
				if err != nil {
					p.app.log.WithError(err).Errorf("failed toggling shuffle context (value: %t)", shuffle)
					p.state.player.Options.ShufflingContext = !shuffle
					p.updateState()
					return
				}

				p.publishSnapshot(snap)
				p.invalidateUpcoming()
				p.schedulePrefetchNext()
				p.updateState()
			})

		requiresUpdate = true
	}

	if requiresUpdate {
		// Repeat/shuffle changes alter which track comes next; a stream
		// prefetched under the old plan must not be switched or faded into.
		p.invalidateUpcoming()
		p.schedulePrefetchNext()

		p.updateState()
	}
}

func (p *AppPlayer) addToQueue(track *connectpb.ContextTrack) {
	if p.state.tracks == nil {
		p.app.log.Warnf("cannot add to queue without a context")
		return
	}

	if track.Uid == "" {
		// The uid always seems unset, so we have to set one manually.
		p.state.queueID++
		track.Uid = fmt.Sprintf("q%d", p.state.queueID)
	}

	p.listJob("add to queue", classMutate, nil,
		func(_ context.Context, list *tracks.List) error {
			list.AddToQueue(track)
			return nil
		},
		func(p *AppPlayer, snap *tracks.Snapshot, err error) {
			if err != nil {
				p.app.log.WithError(err).Warn("failed adding to queue")
				return
			}

			p.publishUpcoming(snap)

			// The queued track plays next: a stream prefetched under the old
			// plan must not be switched or faded into.
			p.invalidateUpcoming()
			p.schedulePrefetchNext()
			p.updateState()
		})
}

func (p *AppPlayer) setQueue(prev []*connectpb.ContextTrack, next []*connectpb.ContextTrack) {
	if p.state.tracks == nil {
		p.app.log.Warnf("cannot set queue without a context")
		return
	}

	p.listJob("set queue", classMutate, next,
		func(_ context.Context, list *tracks.List) error {
			list.SetQueue(prev, next)
			return nil
		},
		func(p *AppPlayer, snap *tracks.Snapshot, err error) {
			if err != nil {
				p.app.log.WithError(err).Warn("failed setting queue")
				return
			}

			p.publishUpcoming(snap)

			// The upcoming track may have changed: a stream prefetched under
			// the old plan must not be switched or faded into.
			p.invalidateUpcoming()
			p.schedulePrefetchNext()
			p.updateState()
		})
}

func (p *AppPlayer) play() error {
	if p.primaryStream == nil {
		// Asked for during a load: record it so the track starts playing when
		// it lands, rather than losing the command.
		if p.loadInFlight {
			p.state.setPaused(false)
			p.updateState()
			return nil
		}

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
	p.updateState()
	p.schedulePrefetchNext()

	return nil
}

func (p *AppPlayer) pause() error {
	if p.primaryStream == nil {
		// See play: a pause during a load is applied when the load lands.
		if p.loadInFlight {
			p.state.setPaused(true)
			p.updateState()
			return nil
		}

		return fmt.Errorf("no primary stream")
	}

	streamPos := p.player.PositionMs()
	p.app.log.Debugf("pause track at %dms", streamPos)

	if err := p.player.Pause(); err != nil {
		return fmt.Errorf("failed pausing playback: %w", err)
	}

	// Pausing is the usual way of stopping mid-episode, so this is the report
	// that matters most for picking the episode back up elsewhere.
	p.reportResumePosition(p.primaryStream, streamPos)

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = streamPos
	p.state.setPaused(true)
	p.updateState()
	p.schedulePrefetchNext()

	return nil
}

func (p *AppPlayer) seek(position int64) error {
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
	p.updateState()
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

func (p *AppPlayer) skipPrev(allowSeeking bool) error {
	if allowSeeking && p.player.PositionMs() > 3000 {
		return p.seek(0)
	}

	p.sess.Events().OnPlayerSkipBackward(p.primaryStream, p.player.PositionMs())

	if p.state.tracks == nil {
		return nil
	}

	p.app.log.Debug("skip previous track")
	p.loadGen++

	p.listJob("skip previous", classLoad, nil,
		func(_ context.Context, list *tracks.List) error {
			list.GoPrev()
			return nil
		},
		func(p *AppPlayer, snap *tracks.Snapshot, err error) {
			if err != nil {
				p.app.log.WithError(err).Warn("failed skipping to previous track")
				return
			}

			p.publishSnapshot(snap)
			p.state.player.Timestamp = time.Now().UnixMilli()
			p.state.player.PositionAsOfTimestamp = 0

			p.loadCurrentTrack(p.state.player.IsPaused, true, true, func(err error) {
				if err != nil {
					p.app.log.WithError(err).Warn("failed loading current track (skip prev)")
				}
			})
		})

	return nil
}

func (p *AppPlayer) skipNext(track *connectpb.ContextTrack) error {
	p.sess.Events().OnPlayerSkipForward(p.primaryStream, p.player.PositionMs(), track != nil)

	if track == nil {
		p.advanceNext(true, true, func(hasNextTrack bool, err error) {
			if err != nil {
				p.app.log.WithError(err).Warn("failed skipping to next track")
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
		})

		return nil
	}

	// Skipping straight to a chosen track is a jump, so the DJ introduces it
	// with its jump line rather than the one for arriving in sequence.
	p.narrationJumped = true
	p.loadGen++

	p.listJob("skip to "+track.GetUri(), classLoad, nil,
		func(ctx context.Context, list *tracks.List) error {
			return list.TrySeekTo(ctx, track)
		},
		func(p *AppPlayer, snap *tracks.Snapshot, err error) {
			if err != nil {
				p.narrationJumped = false
				p.app.log.WithError(err).Warn("failed skipping to track")
				return
			}

			p.publishSnapshot(snap)
			p.state.player.Timestamp = time.Now().UnixMilli()
			p.state.player.PositionAsOfTimestamp = 0

			p.loadCurrentTrack(p.state.player.IsPaused, true, true, func(err error) {
				if err != nil {
					p.app.log.WithError(err).Warn("failed loading current track (skip next)")
				}
			})
		})

	return nil
}

// maxConsecutiveUnplayableSkips caps how many refused/restricted tracks advanceNext will skip
// past in a row before stopping, so a fully-gated context can't loop forever.
const maxConsecutiveUnplayableSkips = 50

// advanceNext moves to the next track and starts it, reporting through then
// whether there was one. A run of unplayable tracks is walked forward, bounded
// by maxConsecutiveUnplayableSkips so a fully gated context cannot loop forever.
func (p *AppPlayer) advanceNext(forceNext, drop bool, then func(hasNextTrack bool, err error)) {
	if p.state.tracks == nil {
		p.advanceTo(false, nil, drop, then)
		return
	}

	// Repeat-track means the next thing to play is this same track: nothing to
	// navigate, and so no walk of the context either.
	if !forceNext && p.state.player.Options.RepeatingTrack {
		p.state.player.IsPaused = false
		p.advanceTo(true, nil, drop, then)
		return
	}

	repeatingContext := p.state.player.Options.RepeatingContext
	p.loadGen++

	var hasNextTrack bool
	var seed []string
	p.listJob("advance", classLoad, nil,
		func(ctx context.Context, list *tracks.List) error {
			hasNextTrack = list.GoNext(ctx)

			// if we could not get the next track we probably ended the context
			if !hasNextTrack {
				// if repeating is disabled move to the first track, but do not start it
				hasNextTrack = list.GoStart(ctx) && repeatingContext
			}

			// Running out of context means autoplay is next, and it is seeded
			// with what was recently played. Gathered here because the list is
			// only reachable from this side.
			if !hasNextTrack {
				for _, track := range list.AllTracks(maxAutoplaySeedTracks) {
					seed = append(seed, track.Uri)
				}
			}

			return nil
		},
		func(p *AppPlayer, snap *tracks.Snapshot, err error) {
			if err != nil {
				then(false, err)
				return
			}

			p.state.player.IsPaused = !hasNextTrack
			p.publishSnapshot(snap)
			p.advanceTo(hasNextTrack, seed, drop, then)
		})
}

// advanceTo starts whatever advanceNext settled on, or hands over to autoplay
// when the context has run out.
func (p *AppPlayer) advanceTo(hasNextTrack bool, seed []string, drop bool, then func(hasNextTrack bool, err error)) {
	uri := p.state.player.Track.GetUri()

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	if !hasNextTrack && !p.app.cfg.DisableAutoplay && !strings.HasPrefix(p.state.player.ContextUri, "spotify:station:") {
		p.startAutoplay(seed, drop, then)
		return
	}

	if !hasNextTrack {
		p.state.player.IsPlaying = false
		p.state.player.IsPaused = false
		p.state.player.IsBuffering = false
	}

	// BAND-AID: Spotify makes a per-track, context-dependent decision on granting the legacy
	// AES audio key. License-gated tracks are refused (AesKeyError, e.g. code 1) in ordinary
	// playlist playback — even though they play on official clients, which establish a licensed
	// context. We cannot decrypt a refused track, so skip it instead of freezing the player.
	// Remove once proper key licensing (PlayPlay) is implemented — tracked separately.
	p.loadCurrentTrack(!hasNextTrack, drop, true, func(err error) {
		if err == nil {
			p.consecutiveUnplayableSkips = 0
			then(hasNextTrack, nil)
			return
		}

		if !isUnplayableMedia(err) {
			p.consecutiveUnplayableSkips = 0
			then(false, fmt.Errorf("failed loading current track (advance to %s): %w", uri, err))
			return
		}

		var keyErr *audio.KeyProviderError
		if errors.As(err, &keyErr) {
			p.app.log.WithError(err).Warnf("skipping track: Spotify refused the audio key (code %d) for this playback context: %s", keyErr.Code, uri)
		} else {
			p.app.log.WithError(err).Infof("skipping unplayable media: %s", uri)
		}

		// Walk forward through a run of unplayable tracks (a context whose first — or several —
		// tracks are refused), bounded so a fully gated or RepeatingContext context advances to
		// the first playable track instead of freezing, and can never recurse forever.
		p.consecutiveUnplayableSkips++
		if p.consecutiveUnplayableSkips > maxConsecutiveUnplayableSkips {
			p.app.log.WithError(err).Warnf("stopping after %d consecutive unplayable tracks", p.consecutiveUnplayableSkips)
			p.consecutiveUnplayableSkips = 0
			then(false, err)
			return
		}

		p.advanceNext(true, drop, then)
	})
}

// startAutoplay resolves a station to carry on with once the context has run
// out, and starts playing it.
func (p *AppPlayer) startAutoplay(prevTrackUris []string, drop bool, then func(bool, error)) {
	p.state.player.Suppressions = &connectpb.Suppressions{}

	if len(prevTrackUris) == 0 {
		p.app.log.Warnf("cannot resolve autoplay station because there are no previous tracks in context %s", p.state.player.ContextUri)
		then(false, nil)
		return
	}

	contextUri := p.state.player.ContextUri
	p.app.log.Debugf("resolving autoplay station for %d tracks", len(prevTrackUris))

	p.loadGen++
	p.loader.submit(loaderJob{
		name:  "resolve station for " + contextUri,
		class: classLoad,
		gen:   p.loadGen,
		run: func(ctx context.Context) loaderResult {
			spotCtx, err := p.sess.Spclient().ContextResolveAutoplay(ctx, &playerpb.AutoplayContextRequest{
				ContextUri:     proto.String(contextUri),
				RecentTrackUri: prevTrackUris,
			})
			if err != nil {
				return loaderResult{
					err: fmt.Errorf("failed resolving station for %s: %w", contextUri, err),
					// Running out of context is not a failure to report upwards:
					// there is simply nothing more to play.
					commit: func(_ *AppPlayer, _ error) { then(false, nil) },
				}
			}

			return loaderResult{commit: func(p *AppPlayer, err error) {
				if err != nil {
					then(false, nil)
					return
				}

				p.app.log.Debugf("resolved autoplay station: %s", spotCtx.Uri)
				p.loadContext(spotCtx, func(_ *connectpb.ContextTrack) bool { return true }, false, drop, func(err error) {
					if err != nil {
						p.app.log.WithError(err).Warnf("failed loading station for %s", contextUri)
						then(false, nil)
						return
					}
					then(true, nil)
				})
			}}
		},
	})
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
	p.app.stateMu.Lock()
	p.app.state.LastVolume = &newVal
	p.app.stateMu.Unlock()
	p.app.requestPersist()

	// Replace whatever is already queued. The mixer writes to this channel too,
	// so the send has to tolerate losing the race for the freed slot: this
	// player loop is the only reader, and blocking here would deadlock it.
	select {
	case <-p.volumeUpdate:
	default:
	}

	select {
	case p.volumeUpdate <- float32(newVal) / player.MaxStateVolume:
	default:
	}
}

// Send notification that the volume changed.
// The original change can come from anywhere: from Spotify Connect, from the
// REST API, or from a volume mixer.
func (p *AppPlayer) volumeUpdated() {
	p.pushState(connectpb.PutStateReason_VOLUME_CHANGED)

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeVolume,
		Data: ApiEventDataVolume{
			Value: p.apiVolume(),
			Max:   p.app.cfg.VolumeSteps,
		},
	})
}

func (p *AppPlayer) stopPlayback() {
	p.player.Stop()
	p.primaryStream = nil
	p.secondaryStream = nil

	p.state.reset()
	p.pushState(connectpb.PutStateReason_BECAME_INACTIVE)

	p.schedulePrefetchNext()

	p.requestLogout()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeInactive,
	})
}
