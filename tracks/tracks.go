package tracks

import (
	"context"
	"fmt"
	"slices"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/spclient"
	"golang.org/x/exp/rand"
)

type ContextResolver interface {
	librespot.PageResolver[*connectpb.ContextTrack]

	Type() librespot.SpotifyIdType
	Uri() string
	Metadata() map[string]string
}

const (
	// seekMaxPages bounds how many pages a walk of the context may fetch.
	seekMaxPages = 256

	// seekWalkTimeout bounds a walk of the context in wall-clock time.
	seekWalkTimeout = 10 * time.Second
)

type List struct {
	log librespot.Logger

	ctx ContextResolver

	shuffled    bool
	shuffleSeed uint64
	shuffleLen  int
	shuffleKeep int
	tracks      *pagedList[*connectpb.ContextTrack]

	playingQueue bool
	queue        []*connectpb.ContextTrack
}

func NewTrackListFromContext(ctx context.Context, log_ librespot.Logger, sp *spclient.Spclient, spotCtx *connectpb.Context) (_ *List, err error) {
	resolver, err := spclient.NewContextResolver(ctx, log_, sp, spotCtx)
	if err != nil {
		return nil, fmt.Errorf("failed initializing context resolver: %w", err)
	}

	return newTrackList(log_, resolver), nil
}

func newTrackList(log_ librespot.Logger, resolver ContextResolver) *List {
	tl := &List{ctx: resolver}

	tl.log = log_.WithField("uri", tl.ctx.Uri())
	tl.log.Debugf("resolved context of %s", tl.ctx.Type())

	tl.tracks = newPagedList[*connectpb.ContextTrack](tl.log, tl.ctx)
	return tl
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

// seekQueue positions the list on track if the queue holds it, dropping the
// entries ahead of it, and reports whether it did.
func (tl *List) seekQueue(track *connectpb.ContextTrack) bool {
	// queue[0] is the track playing right now, so a jump can only target what
	// comes after it.
	start := 0
	if tl.playingQueue {
		start = 1
	}

	// Queue entries carry a uid, so when the caller names one an exact uid
	// match is the only evidence the queued copy was what got clicked.
	match := func(queued *connectpb.ContextTrack) bool { return queued.Uid == track.Uid }
	if len(track.Uid) == 0 {
		match = ContextTrackComparator(tl.ctx.Type(), track)
	}

	for i := start; i < len(tl.queue); i++ {
		if !match(tl.queue[i]) {
			continue
		}

		// Everything queued ahead of the chosen track is skipped, and the
		// chosen one becomes the queue entry now playing.
		tl.queue = tl.queue[i:]
		tl.playingQueue = true
		return true
	}

	return false
}

// TrySeekTo positions the list at track, whether it is queued or part of the
// context. When the bounded seek cannot find it in the context the track is
// played anyway, ahead of the context, and playback carries on into the context
// from its start afterwards.
func (tl *List) TrySeekTo(ctx context.Context, track *connectpb.ContextTrack) error {
	if tl.seekQueue(track) {
		return nil
	}

	// Not a queued track, so we are leaving the queue. Drop the entry that was
	// playing as well: it has been jumped away from. Whatever is queued behind
	// it still follows this track.
	if tl.playingQueue {
		tl.queue = tl.queue[1:]
		tl.playingQueue = false
	}

	if err := tl.Seek(ctx, ContextTrackComparator(tl.ctx.Type(), track)); err == nil {
		return nil
	} else {
		tl.log.WithError(err).Warnf("failed seeking to track in context %s, playing it ahead of the context", tl.ctx.Uri())
	}

	tl.tracks.moveInjected(track)
	return nil
}

func (tl *List) Seek(ctx context.Context, f func(*connectpb.ContextTrack) bool) error {
	ctx, cancel := context.WithTimeout(ctx, seekWalkTimeout)
	defer cancel()

	iter := tl.tracks.iterStart()
	for iter.next(ctx) {
		curr := iter.get()
		if f(curr.item) {
			tl.tracks.move(iter)
			return nil
		}

		if curr.pageIdx >= seekMaxPages {
			return fmt.Errorf("gave up seeking after %d pages", curr.pageIdx+1)
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

func (tl *List) NextTracks(ctx context.Context, nextHint []*connectpb.ContextTrack) []*connectpb.ProvidedTrack {
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

	// when set_queue commands are called, the order of the queue is given by the "next hint"
	if nextHint != nil {
		queueLength := len(tl.queue)
		if tl.playingQueue {
			queueLength -= 1
		}
		for idx, curr := range nextHint {
			// skip all the tracks that are already in the queue (green square icon inside spotify)
			if idx < queueLength {
				continue
			}
			if !(len(tracks) < MaxTracksInContext) {
				break
			}

			// if one moves one track out of the queue into the "coming next" tracks, it is unqueued, because queued items
			// are only the ones with the green symbol. if is_queued remains set, spotify will remove this track from the
			// coming up section entirely
			delete(curr.Metadata, "is_queued")
			tracks = append(tracks, librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), curr))
		}
	} else {
		// Do not waste too much time fetching next tracks. Even if we do not fetch everything in time,
		// the playback will continue anyway.
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		iter := tl.tracks.iterHere()
		for len(tracks) < MaxTracksInContext && iter.next(ctx) {
			curr := iter.get()
			tracks = append(tracks, librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), curr.item))
		}
		if err := iter.error(); err != nil {
			tl.log.WithError(err).Error("failed fetching next tracks")
		}
	}

	return tracks
}

func (tl *List) Index() *connectpb.ContextIndex {
	if tl.playingQueue {
		return &connectpb.ContextIndex{}
	}

	curr := tl.tracks.get()
	if curr.pageIdx < 0 {
		// An injected track (see TrySeekTo) sits outside the context's pages
		// and so has no index within it.
		return &connectpb.ContextIndex{}
	}

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
		// the queued tracks will always be the first tracks in the next list, so if we meet the first "non-queue",
		// 	the queue definitely ended
		if queued := track.Metadata["is_queued"]; queued != "true" {
			break
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
		// Fetch all tracks, under the same bounds as a seek: a generated
		// context hands out pages forever, so "all" has to stop somewhere or
		// the command that asked for the shuffle never returns.
		walkCtx, cancel := context.WithTimeout(ctx, seekWalkTimeout)

		iter := tl.tracks.iterStart()
		for iter.next(walkCtx) {
			if pageIdx := iter.get().pageIdx; pageIdx >= seekMaxPages {
				tl.log.Warnf("shuffling only the first %d pages of the context", pageIdx+1)
				break
			}
		}
		if err := iter.error(); err != nil {
			tl.log.WithError(err).Error("failed fetching all tracks")
		}

		cancel()

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

			// Clear tracks and re-fetch them in order, then seek to the current
			// track. Snapshot the list first: a failed re-fetch (e.g. a transient
			// error while paging the context) would otherwise leave the list
			// stranded at pos -1, and the next access would panic. On failure we
			// roll back to the previous (valid) state and report the error, so the
			// shuffle toggle simply does not take effect.
			savedList, savedPos := tl.tracks.list, tl.tracks.pos
			tl.tracks.clear()
			if err := tl.Seek(ctx, ContextTrackComparator(tl.ctx.Type(), currentTrack)); err != nil {
				tl.tracks.list, tl.tracks.pos = savedList, savedPos
				return fmt.Errorf("failed seeking to current track: %w", err)
			}

			tl.shuffled = false
			tl.log.Debugf("unshuffled context by fetching pages (len: %d)", tl.tracks.len())
			return nil
		}
	}
}
