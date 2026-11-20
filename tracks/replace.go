package tracks

import (
	"context"
	"errors"
	"fmt"
	"slices"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"golang.org/x/exp/rand"
)

// ErrNotATrackList is returned when the items of a context with a uri of its
// own are to be replaced: only a list played without one is the client's.
var ErrNotATrackList = errors.New("not a list of items without a context")

// ReplaceItems swaps the items of a list played without a context of its own
// (see librespot.InferSpotifyIdTypeFromContext) for uris, in place, while it
// plays. resolve builds the resolver of a list of uris.
//
// The item playing stays where it is and keeps playing, even when uris lacks
// it. In order the list then continues with what uris holds after it.
// Shuffled, the order played so far and the order still to come are kept: the
// items gone from uris drop out, and those new to it are mixed into what is
// still to come. The queue is not touched.
func (tl *List) ReplaceItems(ctx context.Context, uris []string, resolve func(uris []string) (ContextResolver, error)) error {
	if tl.ctx.Uri() != "" {
		return ErrNotATrackList
	}

	old, oldPos := tl.tracks.list, tl.tracks.pos
	if oldPos < 0 || oldPos >= len(old) {
		return fmt.Errorf("no item playing in the list")
	}
	current := old[oldPos].item.Uri

	// Keep the playing item in the list: before the first item that comes
	// after it in the old order, else at the end.
	if !slices.Contains(uris, current) {
		later := map[string]bool{}
		for _, it := range old[oldPos+1:] {
			later[it.item.Uri] = true
		}
		at := len(uris)
		for i, uri := range uris {
			if later[uri] {
				at = i
				break
			}
		}
		uris = slices.Insert(slices.Clone(uris), at, current)
	}

	resolver, err := resolve(uris)
	if err != nil {
		return fmt.Errorf("failed resolving the new items: %w", err)
	}

	list := newPagedList(tl.log, resolver)
	iter := list.iterStart()
	for iter.next(ctx) {
	}
	if err := iter.error(); err != nil {
		return fmt.Errorf("failed loading the new items: %w", err)
	}

	newPos := -1
	if tl.shuffled {
		newPos = mergeShuffled(list, old, oldPos)
	}
	if newPos < 0 {
		// In order, or the shuffled order lost the playing item to an earlier
		// duplicate of it: the new order as given.
		newPos = slices.IndexFunc(list.list, func(it pagedListItem[*connectpb.ContextTrack]) bool { return it.item.Uri == current })
		if tl.shuffled {
			list.shuffleAfter(newPos, rand.New(rand.NewSource(rand.Uint64())))
		}
	}
	list.pos = newPos

	tl.ctx, tl.tracks = resolver, list
	if tl.shuffled {
		// The seed no longer describes the order: turning shuffle off
		// fetches the items in order again.
		tl.shuffleSeed, tl.shuffleKeep, tl.shuffleLen = 0, -1, list.len()
	}
	return nil
}

// mergeShuffled orders the loaded items of list like old, the shuffled list
// they replace, and mixes the items new to it into what comes after the one
// playing at oldPos. It returns where that one lands, or -1 when an earlier
// duplicate of it took its place and list is left in its own order.
func mergeShuffled(list *pagedList[*connectpb.ContextTrack], old []pagedListItem[*connectpb.ContextTrack], oldPos int) int {
	// Each new item is matched with an old one of the same uri, in order, so
	// duplicates stay as many as uris names.
	pool := map[string][]int{}
	for i, it := range list.list {
		pool[it.item.Uri] = append(pool[it.item.Uri], i)
	}
	take := func(uri string) (int, bool) {
		idx := pool[uri]
		if len(idx) == 0 {
			return 0, false
		}
		pool[uri] = idx[1:]
		return idx[0], true
	}

	merged := make([]pagedListItem[*connectpb.ContextTrack], 0, len(list.list))
	used := make([]bool, len(list.list))
	pos := -1
	for i, it := range old {
		j, ok := take(it.item.Uri)
		if !ok {
			continue
		}
		used[j] = true
		if i == oldPos {
			pos = len(merged)
		}
		merged = append(merged, list.list[j])
	}
	if pos < 0 {
		return -1
	}

	for j, it := range list.list {
		if used[j] {
			continue
		}
		at := pos + 1 + rand.Intn(len(merged)-pos)
		merged = slices.Insert(merged, at, it)
	}

	list.list = merged
	return pos
}

// shuffleAfter shuffles the loaded items after pos, leaving those up to it.
func (l *pagedList[T]) shuffleAfter(pos int, rnd *rand.Rand) {
	rest := l.list[pos+1:]
	rnd.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
}
