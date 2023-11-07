package tracks

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	"go-librespot/spclient"
	"golang.org/x/exp/rand"
)

type List struct {
	ctx *spclient.ContextResolver

	shuffled    bool
	shuffleSeed uint64
	shuffleLen  int
	shuffleKeep int
	tracks      *pagedList[*connectpb.ContextTrack]
}

func NewTrackListFromContext(sp *spclient.Spclient, ctx *connectpb.Context) (_ *List, err error) {
	tl := &List{}
	tl.ctx, err = spclient.NewContextResolver(sp, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed initializing context resolver: %w", err)
	}

	tl.tracks = newPagedList[*connectpb.ContextTrack](tl.ctx)
	return tl, nil
}

func (tl *List) Metadata() map[string]string {
	return tl.ctx.Metadata()
}

func (tl *List) TrySeek(f func(track *connectpb.ContextTrack) bool) error {
	if err := tl.Seek(f); err != nil {
		err = tl.tracks.moveStart()
		if err != nil {
			return err
		}
	}

	return nil
}

func (tl *List) Seek(f func(*connectpb.ContextTrack) bool) error {
	iter := tl.tracks.iterStart()
	for iter.next() {
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

const MaxTracksInContext = 32

func (tl *List) PrevTracks() []*connectpb.ProvidedTrack {
	tracks := make([]*connectpb.ProvidedTrack, 0, MaxTracksInContext)

	iter := tl.tracks.iterHere()
	for len(tracks) < MaxTracksInContext && iter.prev() {
		curr := iter.get()
		tracks = append(tracks, librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), curr.item))
	}

	if err := iter.error(); err != nil {
		log.WithError(err).Error("failed fetching prev tracks")
	}

	return tracks
}

func (tl *List) NextTracks() []*connectpb.ProvidedTrack {
	tracks := make([]*connectpb.ProvidedTrack, 0, MaxTracksInContext)

	iter := tl.tracks.iterHere()
	for len(tracks) < MaxTracksInContext && iter.next() {
		curr := iter.get()
		tracks = append(tracks, librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), curr.item))
	}

	if err := iter.error(); err != nil {
		log.WithError(err).Error("failed fetching next tracks")
	}

	return tracks
}

func (tl *List) Index() *connectpb.ContextIndex {
	curr := tl.tracks.get()
	return &connectpb.ContextIndex{Page: uint32(curr.pageIdx), Track: uint32(curr.itemIdx)}
}

func (tl *List) current() *connectpb.ContextTrack {
	curr := tl.tracks.get()
	return curr.item
}

func (tl *List) CurrentTrack() *connectpb.ProvidedTrack {
	return librespot.ContextTrackToProvidedTrack(tl.ctx.Type(), tl.current())
}

func (tl *List) GoStart() bool {
	if err := tl.tracks.moveStart(); err != nil {
		log.WithError(err).Error("failed going to start")
		return false
	}

	return true
}

func (tl *List) GoNext() bool {
	iter := tl.tracks.iterHere()
	if iter.next() {
		tl.tracks.move(iter)
		return true
	}

	if err := iter.error(); err != nil {
		log.WithError(err).Error("failed going to next track")
	}

	return false
}

func (tl *List) GoPrev() bool {
	iter := tl.tracks.iterHere()
	if iter.prev() {
		tl.tracks.move(iter)
		return true
	}

	if err := iter.error(); err != nil {
		log.WithError(err).Error("failed going to previous track")
	}

	return false
}

func (tl *List) ToggleShuffle(shuffle bool) error {
	if shuffle == tl.shuffled {
		return nil
	}

	if shuffle {
		// fetch all tracks
		iter := tl.tracks.iterStart()
		for iter.next() {
			// TODO: check that we do not seek forever
		}
		if err := iter.error(); err != nil {
			log.WithError(err).Error("failed fetching all tracks")
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
		log.Debugf("shuffled context with seed %d (len: %d, keep: %d)", tl.shuffleSeed, tl.shuffleLen, tl.shuffleKeep)
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
			log.Debugf("unshuffled context with seed %d (len: %d, keep: %d)", tl.shuffleSeed, tl.shuffleLen, tl.shuffleKeep)
			return nil
		} else {
			// remember current track
			currentTrack := tl.current()

			// clear tracks and seek to the current track
			tl.tracks.clear()
			if err := tl.Seek(ContextTrackComparator(tl.ctx.Type(), currentTrack)); err != nil {
				return fmt.Errorf("failed seeking to current track: %w", err)
			}

			tl.shuffled = false
			log.Debugf("unshuffled context by fetching pages (len: %d)", tl.tracks.len())
			return nil
		}
	}
}
