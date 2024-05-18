package tracks

import (
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"golang.org/x/exp/rand"
	"io"
)

type pagedListItem[T any] struct {
	item    T
	pageIdx int
	itemIdx int
}

type pagedListInterator[T any] struct {
	list *pagedList[T]
	pos  int
	err  error
}

func (i *pagedListInterator[T]) prev() bool {
	if i.pos < 0 {
		panic(fmt.Sprintf("invalid paged list iterator position: %d", i.pos))
	}

	if i.pos == 0 {
		i.err = nil
		return false
	}

	i.pos--
	i.err = nil
	return true
}

func (i *pagedListInterator[T]) next() bool {
	for i.pos+1 >= len(i.list.list) {
		if pageIdx, err := i.list.fetchNextPage(); errors.Is(err, io.EOF) {
			i.err = nil
			return false
		} else if err != nil {
			i.err = fmt.Errorf("failed moving to next index %d (page %d): %w", i.pos+1, pageIdx, err)
			return false
		}
	}

	i.pos++
	i.err = nil
	return true
}

func (i *pagedListInterator[T]) error() error {
	return i.err
}

func (i *pagedListInterator[T]) get() pagedListItem[T] {
	if i.pos < 0 {
		panic(fmt.Sprintf("invalid paged list iterator position: %d", i.pos))
	}

	return i.list.list[i.pos]
}

type pagedList[T any] struct {
	log *log.Entry

	pages librespot.PageResolver[T]
	list  []pagedListItem[T]
	pos   int
}

func newPagedList[T any](log *log.Entry, pages librespot.PageResolver[T]) *pagedList[T] {
	return &pagedList[T]{log: log, pages: pages, list: nil, pos: -1}
}

func (l *pagedList[T]) iterHere() *pagedListInterator[T] {
	if l.pos < 0 {
		panic(fmt.Sprintf("invalid paged list position: %d", l.pos))
	}

	return &pagedListInterator[T]{list: l, pos: l.pos, err: nil}
}

func (l *pagedList[T]) iterStart() *pagedListInterator[T] {
	return &pagedListInterator[T]{list: l, pos: -1, err: nil}
}

func (l *pagedList[T]) moveStart() error {
	if len(l.list) == 0 {
		if pageIdx, err := l.fetchNextPage(); errors.Is(err, io.EOF) {
			return fmt.Errorf("failed moving to start: no more pages as of %d", pageIdx)
		} else if err != nil {
			return fmt.Errorf("failed moving to start: %w", err)
		}
	}

	l.pos = 0
	return nil
}

func (l *pagedList[T]) move(iter *pagedListInterator[T]) {
	if iter.err != nil {
		panic("cannot move to errored paged list iterator")
	}

	l.pos = iter.pos
	return
}

func (l *pagedList[T]) get() pagedListItem[T] {
	if l.pos < 0 {
		panic(fmt.Sprintf("invalid paged list position: %d", l.pos))
	}

	return l.list[l.pos]
}

func (l *pagedList[T]) swap(i, j int) {
	if i < 0 || i >= len(l.list) || j < 0 || j >= len(l.list) {
		panic(fmt.Sprintf("invalid swap indices (i: %d, j: %d, len: %d)", i, j, len(l.list)))
	}

	l.list[i], l.list[j] = l.list[j], l.list[i]
	if i == l.pos {
		l.pos = j
	} else if j == l.pos {
		l.pos = i
	}
}

func (l *pagedList[T]) shuffle(rnd *rand.Rand) {
	if len(l.list) <= 1 {
		return
	}

	idx := l.pos
	for i := len(l.list) - 1; i > 0; i-- {
		j := rnd.Intn(i + 1)
		l.list[i], l.list[j] = l.list[j], l.list[i]
		if i == idx {
			idx = j
		} else if j == idx {
			idx = i
		}
	}
	l.pos = idx
}

func (l *pagedList[T]) unshuffle(rnd *rand.Rand) {
	if len(l.list) <= 1 {
		return
	}

	exchanges := make([]int, len(l.list)-1)
	for i := 0; i < len(l.list)-1; i++ {
		exchanges[i] = rnd.Intn(len(l.list) - i)
	}

	idx := l.pos
	for i := 1; i < len(l.list); i++ {
		j := exchanges[len(l.list)-i-1]
		l.list[i], l.list[j] = l.list[j], l.list[i]
		if i == idx {
			idx = j
		} else if j == idx {
			idx = i
		}
	}
	l.pos = idx
}

func (l *pagedList[T]) fetchNextPage() (int, error) {
	var pageIdx int
	if len(l.list) == 0 {
		pageIdx = 0
	} else {
		curr := l.list[len(l.list)-1]
		pageIdx = curr.pageIdx + 1
	}

	items, err := l.pages.Page(pageIdx)
	if err != nil {
		return pageIdx, err
	} else if len(items) == 0 {
		return pageIdx, fmt.Errorf("loaded an empty page for %d", pageIdx)
	}

	for trackIdx, item := range items {
		l.list = append(l.list, pagedListItem[T]{item, pageIdx, trackIdx})
	}

	l.log.Tracef("fetched new page %d with %d items (list: %d)", pageIdx, len(items), len(l.list))
	return pageIdx, nil
}

func (l *pagedList[T]) clear() {
	l.list = nil
	l.pos = -1
}

func (l *pagedList[T]) len() int {
	return len(l.list)
}
