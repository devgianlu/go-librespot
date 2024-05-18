package tracks

import (
	log "github.com/sirupsen/logrus"
	"golang.org/x/exp/rand"
	"io"
	"testing"
)

type dummyPageResolver struct {
	page func(pageIdx int) ([]int, error)
}

func (d *dummyPageResolver) Page(idx int) ([]int, error) {
	return d.page(idx)
}

var dummyLog = log.NewEntry(log.StandardLogger())

func TestPagedListSimpleIteration(t *testing.T) {
	list := newPagedList[int](dummyLog, &dummyPageResolver{page: func(pageIdx int) ([]int, error) {
		if pageIdx >= 2 {
			return nil, io.EOF
		}

		l := make([]int, 5)
		for i := 0; i < len(l); i++ {
			l[i] = (i + pageIdx*5) * 100
		}
		return l, nil
	}})
	iter := list.iterStart()

	for i := 0; i < 10; i++ {
		if !iter.next() {
			t.Fatalf("iterator next returned false before end")
		}

		item := iter.get()
		t.Logf("item = %v", item)

		if item.pageIdx != i/5 {
			t.Fatalf("item.pageIdx = %d, wanted %d", item.pageIdx, i/5)
		} else if item.itemIdx != i%5 {
			t.Fatalf("item.itemIdx = %d, wanted %d", item.itemIdx, i%5)
		} else if item.item != i*100 {
			t.Fatalf("item.item = %d, wanted %d", item.item, i*100)
		}
	}

	if iter.next() {
		t.Fatalf("iterator next returned true after end")
	} else if iter.error() != nil {
		t.Fatalf("iterator error is not nil after end")
	}

	for i := 8; i >= 0; i-- {
		if !iter.prev() {
			t.Fatalf("iterator prev returned false before start")
		}

		item := iter.get()
		t.Logf("item = %v", item)

		if item.pageIdx != i/5 {
			t.Fatalf("item.pageIdx = %d, wanted %d", item.pageIdx, i/5)
		} else if item.itemIdx != i%5 {
			t.Fatalf("item.itemIdx = %d, wanted %d", item.itemIdx, i%5)
		} else if item.item != i*100 {
			t.Fatalf("item.item = %d, wanted %d", item.item, i*100)
		}
	}

	if iter.prev() {
		t.Fatalf("iterator prev returned true at start")
	} else if iter.error() != nil {
		t.Fatalf("iterator error is not nil at start")
	}
}

func TestPagedListMoving(t *testing.T) {
	list := newPagedList[int](dummyLog, &dummyPageResolver{page: func(pageIdx int) ([]int, error) {
		l := make([]int, 5)
		for i := 0; i < len(l); i++ {
			l[i] = (i + pageIdx*5) * 100
		}
		return l, nil
	}})
	iter := list.iterStart()

	for i := 0; i < 13; i++ {
		if !iter.next() {
			t.Fatalf("iterator next returned false before end")
		}
	}

	list.move(iter)

	item := list.get()
	t.Logf("item = %v", item)
	if item.pageIdx != 2 {
		t.Fatalf("item.pageIdx = %d, wanted %d", item.pageIdx, 2)
	} else if item.itemIdx != 2 {
		t.Fatalf("item.itemIdx = %d, wanted %d", item.itemIdx, 2)
	} else if item.item != 12*100 {
		t.Fatalf("item.item = %d, wanted %d", item.item, 12*100)
	}

	if list.moveStart() != nil {
		t.Fatalf("moveStart returned an error")
	}

	item = list.get()
	t.Logf("item = %v", item)
	if item.pageIdx != 0 {
		t.Fatalf("item.pageIdx = %d, wanted %d", item.pageIdx, 0)
	} else if item.itemIdx != 0 {
		t.Fatalf("item.itemIdx = %d, wanted %d", item.itemIdx, 0)
	} else if item.item != 0 {
		t.Fatalf("item.item = %d, wanted %d", item.item, 0)
	}
}

func TestPagedListShuffle(t *testing.T) {
	list := newPagedList[int](dummyLog, &dummyPageResolver{page: func(pageIdx int) ([]int, error) {
		if pageIdx >= 10 {
			return nil, io.EOF
		}

		l := make([]int, 5)
		for i := 0; i < len(l); i++ {
			l[i] = (i + pageIdx*5) * 100
		}
		return l, nil
	}})

	// load all items
	iter := list.iterStart()
	for i := 0; i < 50; i++ {
		if !iter.next() {
			t.Fatalf("iterator next returned false before end")
		}

		item := iter.get()
		t.Logf("item = %v", item)

		if item.pageIdx != i/5 {
			t.Fatalf("item.pageIdx = %d, wanted %d", item.pageIdx, i/5)
		} else if item.itemIdx != i%5 {
			t.Fatalf("item.itemIdx = %d, wanted %d", item.itemIdx, i%5)
		} else if item.item != i*100 {
			t.Fatalf("item.item = %d, wanted %d", item.item, i*100)
		}
	}
	if iter.error() != nil {
		t.Fatalf("iterator error is not nil after end")
	}

	// move to some position
	iter = list.iterStart()
	for i := 0; i < 36; i++ {
		if !iter.next() {
			t.Fatalf("iterator next returned false before end")
		}
	}
	list.move(iter)

	// shuffle
	seed := rand.Uint64()
	list.shuffle(rand.New(rand.NewSource(seed)))

	// check position is still correct
	item := list.get()
	t.Logf("item = %v", item)
	if item.item != 35*100 {
		t.Fatalf("item.item = %d, wanted %d", item.item, 35*100)
	}

	// unshuffle
	list.unshuffle(rand.New(rand.NewSource(seed)))

	// check order is back to the original
	iter = list.iterStart()
	for i := 0; i < 50; i++ {
		if !iter.next() {
			t.Fatalf("iterator next returned false before end")
		}

		item := iter.get()
		t.Logf("item = %v", item)

		if item.pageIdx != i/5 {
			t.Fatalf("item.pageIdx = %d, wanted %d", item.pageIdx, i/5)
		} else if item.itemIdx != i%5 {
			t.Fatalf("item.itemIdx = %d, wanted %d", item.itemIdx, i%5)
		} else if item.item != i*100 {
			t.Fatalf("item.item = %d, wanted %d", item.item, i*100)
		}
	}

	// check position is still correct
	item = list.get()
	t.Logf("item = %v", item)
	if item.item != 35*100 {
		t.Fatalf("item.item = %d, wanted %d", item.item, 35*100)
	}
}
