package tracks

import (
	"context"
	"fmt"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"slices"
	"strconv"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/spclient"
	"golang.org/x/exp/rand"
)

type List struct {
	log librespot.Logger

	ctx *spclient.ContextResolver

	shuffled    bool
	shuffleSeed uint64
	shuffleLen  int
	shuffleKeep int
	tracks      *pagedList[*connectpb.ContextTrack]

	playingQueue bool
	queue        []*connectpb.ContextTrack
}

func NewTrackListFromContext(ctx context.Context, log_ librespot.Logger, sp *spclient.Spclient, spotCtx *connectpb.Context) (_ *List, err error) {
	tl := &List{}
	tl.ctx, err = spclient.NewContextResolver(ctx, log_, sp, spotCtx)
	if err != nil {
		return nil, fmt.Errorf("failed initializing context resolver: %w", err)
	}

	tl.log = log_.WithField("uri", tl.ctx.Uri())
	tl.log.Debugf("resolved context of %s", tl.ctx.Type())

	tl.tracks = newPagedList[*connectpb.ContextTrack](tl.log, tl.ctx)
	return tl, nil
}

func (tl *List) Metadata() map[string]string {
	return tl.ctx.Metadata()
}

func (tl *List) TrySeek(ctx context.Context, f func(track *connectpb.ContextTrack) bool) error {
	if err := tl.Seek(ctx, f); err != nil {
		tl.log.WithError(err).Warnf("failed seeking to track in context %s", tl.ctx.Uri())

		err = tl.tracks.moveStart(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

func (tl *List) Seek(ctx context.Context, f func(*connectpb.ContextTrack) bool) error {
	iter := tl.tracks.iterStart()
	for iter.next(ctx) {
		curr := iter.get()
		if f(curr.item) {
			tl.tracks.move(iter)
			return nil
		}
	}

	if err := iter.error(); err != nil {
		return fmt.Errorf("failed fetching tracks for seek: %w", err)
	}

	return fmt.Errorf("could not find track")
}

func (tl *List) AllTracks(ctx context.Context) []*connectpb.ProvidedTrack {
	tracks := make([]*connectpb.ProvidedTrack, 0, tl.tracks.len())

	iter := tl.tracks.iterStart()
	for iter.next(ctx) {
		curr := iter.get()
		tracks = append(tracks, librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), curr.item))
	}

	if err := iter.error(); err != nil {
		tl.log.WithError(err).Error("failed fetching all tracks")
	}

	return tracks
}

const MaxTracksInContext = 32

func (tl *List) PrevTracks() []*connectpb.ProvidedTrack {
	tracks := make([]*connectpb.ProvidedTrack, 0, MaxTracksInContext)

	iter := tl.tracks.iterHere()
	for len(tracks) < MaxTracksInContext && iter.prev() {
		curr := iter.get()
		tracks = append(tracks, librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), curr.item))
	}

	if err := iter.error(); err != nil {
		tl.log.WithError(err).Error("failed fetching prev tracks")
	}

	// Tracks were added in reverse order. Fix this by reversing them again.
	slices.Reverse(tracks)

	return tracks
}

func (tl *List) NextTracks(ctx context.Context) []*connectpb.ProvidedTrack {
	tracks := make([]*connectpb.ProvidedTrack, 0, MaxTracksInContext)

	if len(tl.queue) > 0 {
		queue := tl.queue
		if tl.playingQueue {
			queue = queue[1:]
		}

		for i := 0; i < len(queue) && len(tracks) < MaxTracksInContext; i++ {
			tracks = append(tracks, librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), queue[i]))
		}
	}

	iter := tl.tracks.iterHere()
	for len(tracks) < MaxTracksInContext && iter.next(ctx) {
		curr := iter.get()
		tracks = append(tracks, librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), curr.item))
	}

	if err := iter.error(); err != nil {
		tl.log.WithError(err).Error("failed fetching next tracks")
	}

	return tracks
}

func (tl *List) Index() *connectpb.ContextIndex {
	if tl.playingQueue {
		return &connectpb.ContextIndex{}
	}

	curr := tl.tracks.get()
	return &connectpb.ContextIndex{Page: uint32(curr.pageIdx), Track: uint32(curr.itemIdx)}
}

func (tl *List) current() *connectpb.ContextTrack {
	if tl.playingQueue {
		return tl.queue[0]
	}

	curr := tl.tracks.get()
	return curr.item
}

func (tl *List) CurrentTrack() *connectpb.ProvidedTrack {
	item := tl.current()
	return librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), item)
}

func (tl *List) GoStart(ctx context.Context) bool {
	if err := tl.tracks.moveStart(ctx); err != nil {
		tl.log.WithError(err).Error("failed going to start")
		return false
	}

	return true
}

func (tl *List) PeekNext(ctx context.Context) *connectpb.ContextTrack {
	if tl.playingQueue && len(tl.queue) > 1 {
		return tl.queue[1]
	} else if !tl.playingQueue && len(tl.queue) > 0 {
		return tl.queue[0]
	}

	iter := tl.tracks.iterHere()
	if iter.next(ctx) {
		return iter.get().item
	}

	return nil
}

func (tl *List) GoNext(ctx context.Context) bool {
	if tl.playingQueue {
		tl.queue = tl.queue[1:]
	}

	if len(tl.queue) > 0 {
		tl.playingQueue = true
		return true
	}

	tl.playingQueue = false

	iter := tl.tracks.iterHere()
	if iter.next(ctx) {
		tl.tracks.move(iter)
		return true
	}

	if err := iter.error(); err != nil {
		tl.log.WithError(err).Error("failed going to next track")
	}

	return false
}

func (tl *List) GoPrev() bool {
	if tl.playingQueue {
		tl.playingQueue = false
	}

	iter := tl.tracks.iterHere()
	if iter.prev() {
		tl.tracks.move(iter)
		return true
	}

	if err := iter.error(); err != nil {
		tl.log.WithError(err).Error("failed going to previous track")
	}

	return false
}

func (tl *List) AddToQueue(track *connectpb.ContextTrack) {
	if track.Metadata == nil {
		track.Metadata = make(map[string]string)
	}

	track.Metadata["is_queued"] = "true"
	tl.queue = append(tl.queue, track)
}

func (tl *List) SetQueue(_ []*connectpb.ContextTrack, next []*connectpb.ContextTrack) {
	if tl.playingQueue {
		tl.queue = tl.queue[:1]
	} else {
		tl.queue = nil
	}

	// I don't know if this good enough, but it surely saves us a lot of complicated code
	for _, track := range next {
		if queued, ok := track.Metadata["is_queued"]; !ok || queued != "true" {
			continue
		}

		tl.queue = append(tl.queue, track)
	}
}

func (tl *List) SetPlayingQueue(val bool) {
	tl.playingQueue = len(tl.queue) > 0 && val
}

func (tl *List) ToggleShuffle(ctx context.Context, shuffle bool) error {
	if shuffle == tl.shuffled {
		return nil
	}

	if shuffle {
		// fetch all tracks
		iter := tl.tracks.iterStart()
		for iter.next(ctx) {
			// TODO: check that we do not seek forever
		}
		if err := iter.error(); err != nil {
			tl.log.WithError(err).Error("failed fetching all tracks")
		}

		// generate new seed and use it to shuffle
		tl.shuffleSeed = rand.Uint64() + 1
		tl.tracks.shuffle(rand.New(rand.NewSource(tl.shuffleSeed)))

		// move current track to first
		if tl.tracks.pos > 0 {
			tl.shuffleKeep = tl.tracks.pos
			tl.tracks.swap(0, tl.tracks.pos)
		} else {
			tl.shuffleKeep = -1
		}

		// save tracks list length
		tl.shuffleLen = tl.tracks.len()

		tl.shuffled = true
		tl.log.Debugf("shuffled context with seed %d (len: %d, keep: %d)", tl.shuffleSeed, tl.shuffleLen, tl.shuffleKeep)
		return nil
	} else {
		if tl.shuffleSeed != 0 && tl.tracks.len() == tl.shuffleLen {
			// restore track that was originally moved to first
			if tl.shuffleKeep > 0 {
				tl.tracks.swap(0, tl.shuffleKeep)
			}

			// we shuffled this, so we must be able to unshuffle it
			tl.tracks.unshuffle(rand.New(rand.NewSource(tl.shuffleSeed)))

			tl.shuffled = false
			tl.log.Debugf("unshuffled context with seed %d (len: %d, keep: %d)", tl.shuffleSeed, tl.shuffleLen, tl.shuffleKeep)
			return nil
		} else {
			// remember current track
			currentTrack := tl.current()

			// clear tracks and seek to the current track
			tl.tracks.clear()
			if err := tl.Seek(ctx, ContextTrackComparator(tl.ctx.Type(), currentTrack)); err != nil {
				return fmt.Errorf("failed seeking to current track: %w", err)
			}

			tl.shuffled = false
			tl.log.Debugf("unshuffled context by fetching pages (len: %d)", tl.tracks.len())
			return nil
		}
	}
}

func (tl *List) ApplySelectedListContent(content *playlist4pb.SelectedListContent) {
	for _, item := range content.Contents.Items {
		spotId, err := librespot.SpotifyIdFromUri(item.GetUri())
		if err != nil {
			log.WithError(err).Errorf("failed parsing uri %s", item.GetUri())
			continue
		}

		track := connectpb.ContextTrack{
			Uri: spotId.Uri(),
			Gid: spotId.Id(),
			Metadata: map[string]string{
				"added_at":          strconv.FormatInt(item.Attributes.GetTimestamp(), 10),
				"added_by_username": item.Attributes.GetAddedBy(),
			},
		}

		for _, attr := range item.Attributes.FormatAttributes {
			track.Metadata[*attr.Key] = *attr.Value
		}

		log.Infof("%v", &track) // TODO: no clue where these should be added
	}
}
