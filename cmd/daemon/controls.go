package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/player"
	connectpb "go-librespot/proto/spotify/connectstate"
	playerpb "go-librespot/proto/spotify/player"
	"go-librespot/tracks"
	"google.golang.org/protobuf/proto"
	"math"
	"strconv"
	"time"
)

func (p *AppPlayer) prefetchNext() {
	next := p.state.tracks.PeekNext()
	if next == nil {
		return
	}

	nextId := librespot.SpotifyIdFromUri(next.Uri)
	if p.secondaryStream != nil && p.secondaryStream.Is(nextId) {
		return
	}

	log.WithField("uri", nextId.Uri()).Debugf("prefetching next %s", nextId.Type())

	var err error
	p.secondaryStream, err = p.player.NewStream(nextId, *p.app.cfg.Bitrate, 0)
	if err != nil {
		log.WithError(err).WithField("uri", nextId.String()).Warnf("failed prefetching %s stream", nextId.Type())
		return
	}

	p.player.SetSecondaryStream(p.secondaryStream.Source)

	log.WithField("uri", nextId.Uri()).
		Infof("prefetched %s %s (duration: %dms)", nextId.Type(),
			strconv.QuoteToGraphic(p.secondaryStream.Media.Name()), p.secondaryStream.Media.Duration())
}

func (p *AppPlayer) schedulePrefetchNext() {
	if p.state.player.IsPaused || p.primaryStream == nil {
		p.prefetchTimer.Reset(time.Duration(math.MaxInt64))
		return
	}

	untilTrackEnd := time.Duration(p.primaryStream.Media.Duration()-int32(p.player.PositionMs())) * time.Millisecond
	untilTrackEnd -= 30 * time.Second
	if untilTrackEnd < 10*time.Second {
		p.prefetchTimer.Reset(time.Duration(math.MaxInt64))

		go p.prefetchNext()
	} else {
		p.prefetchTimer.Reset(untilTrackEnd)
		log.Tracef("scheduling prefetch in %.0fs", untilTrackEnd.Seconds())
	}
}

func (p *AppPlayer) handlePlayerEvent(ev *player.Event) {
	switch ev.Type {
	case player.EventTypePlaying:
		p.state.player.IsPlaying = true
		p.state.player.IsPaused = false
		p.state.player.IsBuffering = false
		p.updateState()

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePlaying,
			Data: ApiEventDataPlaying{
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypePaused:
		p.state.player.IsPlaying = true
		p.state.player.IsPaused = true
		p.state.player.IsBuffering = false
		p.updateState()

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePaused,
			Data: ApiEventDataPaused{
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypeNotPlaying:
		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeNotPlaying,
			Data: ApiEventDataNotPlaying{
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})

		hasNextTrack, err := p.advanceNext(false)
		if err != nil {
			log.WithError(err).Error("failed advancing to next track")
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
	case player.EventTypeStopped:
		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeStopped,
			Data: ApiEventDataStopped{
				PlayOrigin: p.state.playOrigin(),
			},
		})
	default:
		panic("unhandled player event")
	}
}

type skipToFunc func(*connectpb.ContextTrack) bool

func (p *AppPlayer) loadContext(ctx *connectpb.Context, skipTo skipToFunc, paused bool) error {
	ctxTracks, err := tracks.NewTrackListFromContext(p.sess.Spclient(), ctx)
	if err != nil {
		return fmt.Errorf("failed creating track list: %w", err)
	}

	p.state.player.IsPaused = paused

	p.state.player.ContextUri = ctx.Uri
	p.state.player.ContextUrl = ctx.Url
	p.state.player.Restrictions = ctx.Restrictions
	p.state.player.ContextRestrictions = ctx.Restrictions

	if p.state.player.ContextMetadata == nil {
		p.state.player.ContextMetadata = map[string]string{}
	}
	for k, v := range ctx.Metadata {
		p.state.player.ContextMetadata[k] = v
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	if skipTo == nil {
		// if shuffle is enabled, we'll start from a random track
		if err := ctxTracks.ToggleShuffle(p.state.player.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}

		// seek to the first track
		if err := ctxTracks.TrySeek(func(_ *connectpb.ContextTrack) bool { return true }); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}
	} else {
		// seek to the given track
		if err := ctxTracks.TrySeek(skipTo); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}

		// shuffle afterwards
		if err := ctxTracks.ToggleShuffle(p.state.player.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}
	}

	p.state.tracks = ctxTracks
	p.state.player.Track = ctxTracks.CurrentTrack()
	p.state.player.PrevTracks = ctxTracks.PrevTracks()
	p.state.player.NextTracks = ctxTracks.NextTracks()
	p.state.player.Index = ctxTracks.Index()

	// load current track into stream
	if err := p.loadCurrentTrack(paused); err != nil {
		return fmt.Errorf("failed loading current track (load context): %w", err)
	}

	return nil
}

func (p *AppPlayer) loadCurrentTrack(paused bool) error {
	p.primaryStream = nil

	spotId := librespot.SpotifyIdFromUri(p.state.player.Track.Uri)
	if spotId.Type() != librespot.SpotifyIdTypeTrack && spotId.Type() != librespot.SpotifyIdTypeEpisode {
		return fmt.Errorf("unsupported spotify type: %s", spotId.Type())
	}

	trackPosition := p.state.trackPosition()
	log.WithField("uri", spotId.Uri()).
		Debugf("loading %s (paused: %t, position: %dms)", spotId.Type(), paused, trackPosition)

	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = true
	p.state.player.IsPaused = paused
	p.updateState()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeWillPlay,
		Data: ApiEventDataWillPlay{
			Uri:        spotId.Uri(),
			PlayOrigin: p.state.playOrigin(),
		},
	})

	var prefetched bool
	if p.secondaryStream != nil && p.secondaryStream.Is(spotId) {
		p.primaryStream = p.secondaryStream
		p.secondaryStream = nil
		prefetched = true
	} else {
		p.secondaryStream = nil
		prefetched = false

		var err error
		p.primaryStream, err = p.player.NewStream(spotId, *p.app.cfg.Bitrate, trackPosition)
		if err != nil {
			return fmt.Errorf("failed creating stream for %s: %w", spotId, err)
		}
	}

	if err := p.player.SetPrimaryStream(p.primaryStream.Source, paused); err != nil {
		return fmt.Errorf("failed setting stream for %s: %w", spotId, err)
	}

	log.WithField("uri", spotId.Uri()).
		Infof("loaded %s %s (paused: %t, position: %dms, duration: %dms, prefetched: %t)", spotId.Type(),
			strconv.QuoteToGraphic(p.primaryStream.Media.Name()), paused, trackPosition, p.primaryStream.Media.Duration(),
			prefetched)

	p.state.player.Duration = int64(p.primaryStream.Media.Duration())
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = false
	p.updateState()
	p.schedulePrefetchNext()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeMetadata,
		Data: ApiEventDataMetadata(*NewApiResponseStatusTrack(p.primaryStream.Media, p.prodInfo, trackPosition)),
	})

	// Fetch Album Metadata if available
	if p.primaryStream.Media.IsTrack() {
		track := p.primaryStream.Media.Track()
		if track.Album != nil && track.Album.Gid != nil {
			albumId := librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeAlbum, track.Album.Gid)
			album, err := p.sess.Spclient().MetadataForAlbum(albumId)
			if err != nil {
				log.WithError(err).Warn("failed fetching album details")
			} else {
				if album == nil {
					log.Warn("Album metadata is nil")
					return nil
				}

				var highestResImageUrl string
				if album.CoverGroup != nil && len(album.CoverGroup.Image) > 0 {
					highestResImage := album.CoverGroup.Image[len(album.CoverGroup.Image)-1]
					if highestResImage != nil && highestResImage.FileId != nil {
						highestResImageUrl = fmt.Sprintf("https://i.scdn.co/image/%s", hex.EncodeToString(highestResImage.FileId))
					}
				}

				response := AlbumResponse{
					Gid:          hex.EncodeToString(album.Gid),
					Name:         "",
					Artist:       make([]ArtistResponse, len(album.Artist)),
					Type:         "",
					Label:        "",
					Date:         DateResponse{},
					Popularity:   0,
					ExternalID:   make([]ExternalIDResponse, len(album.ExternalId)),
					Disc:         make([]DiscResponse, len(album.Disc)),
					Copyright:    make([]CopyrightResponse, len(album.Copyright)),
					CoverGroup:   CoverGroupResponse{Image: make([]ImageResponse, len(album.CoverGroup.Image))},
					OriginalTitle: "",
					Tracks:       []TrackDetailResponse{},
				}

				if album.Name != nil {
					response.Name = *album.Name
				}
				if album.Type != nil {
					response.Type = album.Type.String()
				}
				if album.Label != nil {
					response.Label = *album.Label
				}
				if album.Date != nil {
					response.Date = DateResponse{
						Year:  0,
						Month: 0,
						Day:   0,
					}
					if album.Date.Year != nil {
						response.Date.Year = *album.Date.Year
					}
					if album.Date.Month != nil {
						response.Date.Month = *album.Date.Month
					}
					if album.Date.Day != nil {
						response.Date.Day = *album.Date.Day
					}
				}
				if album.Popularity != nil {
					response.Popularity = *album.Popularity
				}
				if album.OriginalTitle != nil {
					response.OriginalTitle = *album.OriginalTitle
				}

				for i, artist := range album.Artist {
					if artist != nil {
						response.Artist[i] = ArtistResponse{
							Gid:  hex.EncodeToString(artist.Gid),
							Name: "",
						}
						if artist.Name != nil {
							response.Artist[i].Name = *artist.Name
						}
					}
				}

				for i, externalID := range album.ExternalId {
					if externalID != nil {
						response.ExternalID[i] = ExternalIDResponse{
							Type: "",
							ID:   "",
						}
						if externalID.Type != nil {
							response.ExternalID[i].Type = *externalID.Type
						}
						if externalID.Id != nil {
							response.ExternalID[i].ID = *externalID.Id
						}
					}
				}

				for i, disc := range album.Disc {
					if disc != nil {
						response.Disc[i] = DiscResponse{
							Number: 0,
							Track:  make([]TrackResponse, len(disc.Track)),
						}
						if disc.Number != nil {
							response.Disc[i].Number = int(*disc.Number)
						}
						for j, track := range disc.Track {
							if track != nil {
								response.Disc[i].Track[j] = TrackResponse{
									Gid: hex.EncodeToString(track.Gid),
									Number: 0,
								}
								if track.Number != nil {
									response.Disc[i].Track[j].Number = int(*track.Number)
								}

								trackDetail, err := p.sess.Spclient().MetadataForTrack(librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeTrack, track.Gid))
								if err != nil {
									log.WithError(err).Warn("failed getting track metadata")
									return nil
								}

								if trackDetail != nil {
									var artists []string
									for _, artist := range trackDetail.Artist {
										if artist != nil && artist.Name != nil {
											artists = append(artists, *artist.Name)
										}
									}

									response.Tracks = append(response.Tracks, TrackDetailResponse{
										Name:     "",
										Duration: 0,
										URI:      librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeTrack, track.Gid).Uri(),
										Artists:  artists,
										ImageUrl: highestResImageUrl,
										TrackNumber: 0,
										HasLyrics: trackDetail.HasLyrics,

									})
									if trackDetail.Name != nil {
										response.Tracks[len(response.Tracks)-1].Name = *trackDetail.Name
									}
									if trackDetail.Duration != nil {
										response.Tracks[len(response.Tracks)-1].Duration = int(*trackDetail.Duration)
									}
									if trackDetail.Number != nil {
										response.Tracks[len(response.Tracks)-1].TrackNumber = int(*trackDetail.Number)
									}
								}
							}
						}
					}
				}

				for i, copyright := range album.Copyright {
					if copyright != nil {
						response.Copyright[i] = CopyrightResponse{
							Type: int32(copyright.Type.Number()),
							Text: "",
						}
						if copyright.Text != nil {
							response.Copyright[i].Text = *copyright.Text
						}
					}
				}

				for i, image := range album.CoverGroup.Image {
					if image != nil {
						response.CoverGroup.Image[i] = ImageResponse{
							FileID: hex.EncodeToString(image.FileId),
							Size:   int(image.Size.Number()),
							Width:  0,
							Height: 0,
						}
						if image.Width != nil {
							response.CoverGroup.Image[i].Width = int(*image.Width)
						}
						if image.Height != nil {
							response.CoverGroup.Image[i].Height = int(*image.Height)
						}
					}
				}
				p.app.server.Emit(&ApiEvent{
					Type: ApiEventTypeAlbumMetadata,
					Data: response,
				})
			}
		}
	}

	return nil
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

	if shufflingContext != nil && *shufflingContext != p.state.player.Options.ShufflingContext {
		if err := p.state.tracks.ToggleShuffle(*shufflingContext); err != nil {
			log.WithError(err).Errorf("failed toggling shuffle context (value: %t)", *shufflingContext)
			return
		}

		p.state.player.Options.ShufflingContext = *shufflingContext
		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks()
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
		p.updateState()
	}
}

func (p *AppPlayer) addToQueue(track *connectpb.ContextTrack) {
	p.state.tracks.AddToQueue(track)
	p.state.player.PrevTracks = p.state.tracks.PrevTracks()
	p.state.player.NextTracks = p.state.tracks.NextTracks()
	p.updateState()
	p.schedulePrefetchNext()
}

func (p *AppPlayer) setQueue(prev []*connectpb.ContextTrack, next []*connectpb.ContextTrack) {
	p.state.tracks.SetQueue(prev, next)
	p.state.player.PrevTracks = p.state.tracks.PrevTracks()
	p.state.player.NextTracks = p.state.tracks.NextTracks()
	p.updateState()
	p.schedulePrefetchNext()
}

func (p *AppPlayer) play() error {
	if p.primaryStream == nil {
		return fmt.Errorf("no primary stream")
	}

	// seek before play to ensure we are at the correct stream position
	seekPos := p.state.trackPosition()
	seekPos = max(0, min(seekPos, int64(p.primaryStream.Media.Duration())))
	if err := p.player.SeekMs(seekPos); err != nil {
		return fmt.Errorf("failed seeking before play: %w", err)
	}

	p.player.Play()

	streamPos := p.player.PositionMs()
	log.Debugf("resume track at %dms", streamPos)

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = streamPos
	p.state.player.IsPaused = false
	p.updateState()
	p.schedulePrefetchNext()

	return nil
}

func (p *AppPlayer) pause() error {
	if p.primaryStream == nil {
		return fmt.Errorf("no primary stream")
	}

	streamPos := p.player.PositionMs()
	log.Debugf("pause track at %dms", streamPos)

	p.player.Pause()

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = streamPos
	p.state.player.IsPaused = true
	p.updateState()
	p.schedulePrefetchNext()

	return nil
}

func (p *AppPlayer) seek(position int64) error {
	if p.primaryStream == nil {
		return fmt.Errorf("no primary stream")
	}

	position = max(0, min(position, int64(p.primaryStream.Media.Duration())))

	log.Debugf("seek track to %dms", position)
	if err := p.player.SeekMs(position); err != nil {
		return err
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = position
	p.updateState()
	p.schedulePrefetchNext()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeSeek,
		Data: ApiEventDataSeek{
			Uri:        p.state.player.Track.Uri,
			Position:   int(position),
			Duration:   int(p.primaryStream.Media.Duration()),
			PlayOrigin: p.state.playOrigin(),
		},
	})

	return nil
}

func (p *AppPlayer) skipPrev() error {
	if p.player.PositionMs() > 3000 {
		return p.seek(0)
	}

	if p.state.tracks != nil {
		log.Debug("skip previous track")
		p.state.tracks.GoPrev()

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks()
		p.state.player.Index = p.state.tracks.Index()
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	// load current track into stream
	if err := p.loadCurrentTrack(p.state.player.IsPaused); err != nil {
		return fmt.Errorf("failed loading current track (skip prev): %w", err)
	}

	return nil
}

func (p *AppPlayer) skipNext() error {
	hasNextTrack, err := p.advanceNext(true)
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

func (p *AppPlayer) advanceNext(forceNext bool) (bool, error) {
	var uri string
	var hasNextTrack bool
	if p.state.tracks != nil {
		if !forceNext && p.state.player.Options.RepeatingTrack {
			hasNextTrack = true
			p.state.player.IsPaused = false
		} else {
			// try to get the next track
			hasNextTrack = p.state.tracks.GoNext()

			// if we could not get the next track we probably ended the context
			if !hasNextTrack {
				hasNextTrack = p.state.tracks.GoStart()

				// if repeating is disabled move to the first track, but do not start it
				if !p.state.player.Options.RepeatingContext {
					hasNextTrack = false
				}
			}

			p.state.player.IsPaused = !hasNextTrack
		}

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks()
		p.state.player.Index = p.state.tracks.Index()

		uri = p.state.player.Track.Uri
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	if !hasNextTrack && p.prodInfo.AutoplayEnabled() {
		p.state.player.Suppressions = &connectpb.Suppressions{}

		var prevTrackUris []string
		for _, track := range p.state.tracks.PrevTracks() {
			prevTrackUris = append(prevTrackUris, track.Uri)
		}

		ctx, err := p.sess.Spclient().ContextResolveAutoplay(&playerpb.AutoplayContextRequest{
			ContextUri:     proto.String(p.state.player.ContextUri),
			RecentTrackUri: prevTrackUris,
		})
		if err != nil {
			log.WithError(err).Warnf("failed resolving station for %s", p.state.player.ContextUri)
			return false, nil
		}

		if err := p.loadContext(ctx, func(_ *connectpb.ContextTrack) bool { return true }, false); err != nil {
			log.WithError(err).Warnf("failed loading station for %s", p.state.player.ContextUri)
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
	if err := p.loadCurrentTrack(!hasNextTrack); errors.Is(err, librespot.ErrMediaRestricted) || errors.Is(err, librespot.ErrNoSupportedFormats) {
		log.WithError(err).Infof("skipping unplayable media: %s", uri)
		if forceNext {
			// we failed in finding another track to play, just stop
			return false, err
		}

		return p.advanceNext(true)
	} else if err != nil {
		return false, fmt.Errorf("failed loading current track (advance to %s): %w", uri, err)
	}

	return hasNextTrack, nil
}

func (p *AppPlayer) updateVolume(newVal uint32) {
	if newVal > player.MaxStateVolume {
		newVal = player.MaxStateVolume
	} else if newVal < 0 {
		newVal = 0
	}

	// skip volume update
	if newVal == p.state.device.Volume {
		return
	}

	log.Debugf("update volume to %d/%d", newVal, player.MaxStateVolume)
	p.player.SetVolume(newVal)
	p.state.device.Volume = newVal

	if err := p.putConnectState(connectpb.PutStateReason_VOLUME_CHANGED); err != nil {
		log.WithError(err).Error("failed put state after volume change")
	}

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeVolume,
		Data: ApiEventDataVolume{
			Value: uint32(math.Ceil(float64(newVal**p.app.cfg.VolumeSteps) / player.MaxStateVolume)),
			Max:   *p.app.cfg.VolumeSteps,
		},
	})
}
