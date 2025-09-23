//go:build test_unit

package tracks

import (
	"context"
	"errors"
	"io"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"golang.org/x/exp/rand"
)

type PagedListInternalSuite struct {
	suite.Suite

	resolver *librespot.MockPageResolver[int]
	list     *pagedList[int]
}

func (suite *PagedListInternalSuite) SetupTest() {
	suite.resolver = librespot.NewMockPageResolver[int](suite.T())
	suite.list = newPagedList[int](&librespot.NullLogger{}, suite.resolver)
}

func (suite *PagedListInternalSuite) TestSimpleIteration() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return([]int{0, 100, 200, 300, 400}, nil).Once()
	suite.resolver.EXPECT().Page(mock.Anything, 1).Return([]int{500, 600, 700, 800, 900}, nil).Once()
	suite.resolver.EXPECT().Page(mock.Anything, 2).Return(nil, io.EOF).Once()

	iter := suite.list.iterStart()

	for i := 0; i < 10; i++ {
		suite.True(iter.next(context.Background()), "iterator next returned false before end")

		item := iter.get()
		suite.Equal(i/5, item.pageIdx)
		suite.Equal(i%5, item.itemIdx)
		suite.Equal(i*100, item.item)
	}

	suite.False(iter.next(context.Background()), "iterator next returned true after end")
	suite.NoError(iter.error())

	for i := 8; i >= 0; i-- {
		suite.True(iter.prev(), "iterator prev returned false before start")

		item := iter.get()
		suite.Equal(i/5, item.pageIdx)
		suite.Equal(i%5, item.itemIdx)
		suite.Equal(i*100, item.item)
	}

	suite.False(iter.prev(), "iterator prev returned true at start")
	suite.NoError(iter.error())
}

func (suite *PagedListInternalSuite) TestMoving() {
	suite.resolver.EXPECT().Page(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, pageIdx int) ([]int, error) {
		l := make([]int, 5)
		for i := 0; i < len(l); i++ {
			l[i] = (i + pageIdx*5) * 100
		}
		return l, nil
	})

	iter := suite.list.iterStart()

	for i := 0; i < 13; i++ {
		suite.True(iter.next(context.Background()), "iterator next returned false before end")
	}

	suite.list.move(iter)

	item := suite.list.get()
	suite.Equal(2, item.pageIdx)
	suite.Equal(2, item.itemIdx)
	suite.Equal(1200, item.item)

	suite.NoError(suite.list.moveStart(context.Background()))

	item = suite.list.get()
	suite.Equal(0, item.pageIdx)
	suite.Equal(0, item.itemIdx)
	suite.Equal(0, item.item)
}

func (suite *PagedListInternalSuite) TestShuffle() {
	suite.resolver.EXPECT().Page(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, pageIdx int) ([]int, error) {
		if pageIdx >= 10 {
			return nil, io.EOF
		}

		l := make([]int, 5)
		for i := 0; i < len(l); i++ {
			l[i] = (i + pageIdx*5) * 100
		}
		return l, nil
	})

	iter := suite.list.iterStart()
	for i := 0; i < 50; i++ {
		suite.True(iter.next(context.Background()), "iterator next returned false before end")

		item := iter.get()
		suite.Equal(i/5, item.pageIdx)
		suite.Equal(i%5, item.itemIdx)
		suite.Equal(i*100, item.item)
	}
	suite.NoError(iter.error())

	iter = suite.list.iterStart()
	for i := 0; i < 36; i++ {
		suite.True(iter.next(context.Background()), "iterator next returned false before end")
	}
	suite.list.move(iter)

	seed := rand.Uint64()
	suite.list.shuffle(rand.New(rand.NewSource(seed)))

	item := suite.list.get()
	suite.Equal(7, item.pageIdx)
	suite.Equal(0, item.itemIdx)
	suite.Equal(35*100, item.item)

	suite.list.unshuffle(rand.New(rand.NewSource(seed)))

	iter = suite.list.iterStart()
	for i := 0; i < 50; i++ {
		suite.True(iter.next(context.Background()), "iterator next returned false before end")

		item := iter.get()
		suite.Equal(i/5, item.pageIdx)
		suite.Equal(i%5, item.itemIdx)
		suite.Equal(i*100, item.item)
	}

	item = suite.list.get()
	suite.Equal(7, item.pageIdx)
	suite.Equal(0, item.itemIdx)
	suite.Equal(35*100, item.item)
}

func (suite *PagedListInternalSuite) TestErrorHandling() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return(nil, assert.AnError).Once()

	iter := suite.list.iterStart()
	suite.False(iter.next(context.Background()), "iterator next should return false on error")
	suite.Error(iter.error(), "iterator should have error")
	suite.Contains(iter.error().Error(), "failed moving to next index")
}

func (suite *PagedListInternalSuite) TestMoveStartError() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return(nil, assert.AnError).Once()

	err := suite.list.moveStart(context.Background())
	suite.Error(err, "moveStart should return error when page fetch fails")
	suite.Contains(err.Error(), "failed moving to start")
}

func (suite *PagedListInternalSuite) TestMoveStartEmptyList() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return(nil, io.EOF).Once()

	err := suite.list.moveStart(context.Background())
	suite.Error(err, "moveStart should return error when no pages available")
	suite.Contains(err.Error(), "no more pages")
}

func (suite *PagedListInternalSuite) TestSingleItemPage() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return([]int{42}, nil).Once()
	suite.resolver.EXPECT().Page(mock.Anything, 1).Return(nil, io.EOF).Once()

	iter := suite.list.iterStart()
	suite.True(iter.next(context.Background()), "should be able to get first item")

	item := iter.get()
	suite.Equal(0, item.pageIdx)
	suite.Equal(0, item.itemIdx)
	suite.Equal(42, item.item)

	suite.False(iter.next(context.Background()), "should reach end after single item")
	suite.NoError(iter.error())
}

func (suite *PagedListInternalSuite) TestMultiplePagesWithDifferentSizes() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return([]int{1, 2, 3}, nil).Once()
	suite.resolver.EXPECT().Page(mock.Anything, 1).Return([]int{4}, nil).Once()
	suite.resolver.EXPECT().Page(mock.Anything, 2).Return([]int{5, 6}, nil).Once()
	suite.resolver.EXPECT().Page(mock.Anything, 3).Return(nil, io.EOF).Once()

	iter := suite.list.iterStart()
	expectedValues := []int{1, 2, 3, 4, 5, 6}

	for i, expected := range expectedValues {
		suite.True(iter.next(context.Background()), "should be able to iterate through all items")
		item := iter.get()
		suite.Equal(expected, item.item, "item value should match expected")

		// Verify page and item indices
		if i < 3 {
			suite.Equal(0, item.pageIdx)
			suite.Equal(i, item.itemIdx)
		} else if i == 3 {
			suite.Equal(1, item.pageIdx)
			suite.Equal(0, item.itemIdx)
		} else {
			suite.Equal(2, item.pageIdx)
			suite.Equal(i-4, item.itemIdx)
		}
	}

	suite.False(iter.next(context.Background()), "should reach end")
	suite.NoError(iter.error())
}

func (suite *PagedListInternalSuite) TestClearAndLen() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return([]int{1, 2, 3}, nil).Once()

	// Initially empty
	suite.Equal(0, suite.list.len())

	// Load some data
	iter := suite.list.iterStart()
	suite.True(iter.next(context.Background()))
	suite.Equal(3, suite.list.len())

	// Clear and verify
	suite.list.clear()
	suite.Equal(0, suite.list.len())
}

func (suite *PagedListInternalSuite) TestSwap() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return([]int{10, 20, 30, 40, 50}, nil).Once()

	// Load data and move to position 2
	iter := suite.list.iterStart()
	for i := 0; i < 3; i++ {
		suite.True(iter.next(context.Background()))
	}
	suite.list.move(iter)

	// Verify initial position
	item := suite.list.get()
	suite.Equal(30, item.item)

	// Swap positions 1 and 3
	suite.list.swap(1, 3)

	// Position should remain at index 2
	item = suite.list.get()
	suite.Equal(30, item.item)

	// Swap current position (2) with position 4
	suite.list.swap(2, 4)

	// Position should now be at index 4
	item = suite.list.get()
	suite.Equal(30, item.item)
}

func (suite *PagedListInternalSuite) TestSwapPanics() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return([]int{1, 2, 3}, nil).Once()

	iter := suite.list.iterStart()
	iter.next(context.Background())

	// Test invalid indices
	suite.Panics(func() { suite.list.swap(-1, 0) }, "should panic on negative index")
	suite.Panics(func() { suite.list.swap(0, 5) }, "should panic on out of bounds index")
}

func (suite *PagedListInternalSuite) TestShuffleEmptyList() {
	rnd := rand.New(rand.NewSource(12345))

	// Should not panic on empty list
	suite.NotPanics(func() { suite.list.shuffle(rnd) })
	suite.NotPanics(func() { suite.list.unshuffle(rnd) })
}

func (suite *PagedListInternalSuite) TestShuffleSingleItem() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return([]int{42}, nil).Once()

	iter := suite.list.iterStart()
	suite.True(iter.next(context.Background()))
	suite.list.move(iter)

	rnd := rand.New(rand.NewSource(12345))

	// Should not panic or change anything with single item
	suite.list.shuffle(rnd)
	item := suite.list.get()
	suite.Equal(42, item.item)

	suite.list.unshuffle(rnd)
	item = suite.list.get()
	suite.Equal(42, item.item)
}

func (suite *PagedListInternalSuite) TestIteratorPanics() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return([]int{1, 2, 3}, nil).Once()

	iter := suite.list.iterStart()

	// Should panic when trying to get from invalid position
	suite.Panics(func() { iter.get() }, "should panic when getting from invalid position")

	// Move to valid position
	iter.next(context.Background())
	suite.NotPanics(func() { iter.get() }, "should not panic when getting from valid position")
}

func (suite *PagedListInternalSuite) TestMovePanics() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return(nil, errors.New("test error")).Once()

	iter := suite.list.iterStart()
	iter.next(context.Background()) // This will cause an error

	// Should panic when trying to move to errored iterator
	suite.Panics(func() { suite.list.move(iter) }, "should panic when moving to errored iterator")
}

func (suite *PagedListInternalSuite) TestGetPanics() {
	// Should panic when trying to get from invalid position
	suite.Panics(func() { suite.list.get() }, "should panic when getting from invalid position")
}

func (suite *PagedListInternalSuite) TestIterHerePanics() {
	// Should panic when trying to create iterator from invalid position
	suite.Panics(func() { suite.list.iterHere() }, "should panic when creating iterator from invalid position")
}

func (suite *PagedListInternalSuite) TestIterHere() {
	suite.resolver.EXPECT().Page(mock.Anything, 0).Return([]int{10, 20, 30}, nil).Once()

	// Load data and move to position 1
	iter := suite.list.iterStart()
	for i := 0; i < 2; i++ {
		suite.True(iter.next(context.Background()))
	}
	suite.list.move(iter)

	// Create iterator from current position
	hereIter := suite.list.iterHere()
	item := hereIter.get()
	suite.Equal(20, item.item)
	suite.Equal(1, item.itemIdx)
}

func (suite *PagedListInternalSuite) TestConcurrentAccess() {
	suite.resolver.EXPECT().Page(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, pageIdx int) ([]int, error) {
		if pageIdx >= 3 {
			return nil, io.EOF
		}
		return []int{pageIdx * 10, pageIdx*10 + 1}, nil
	})

	// Create multiple iterators
	iter1 := suite.list.iterStart()
	iter2 := suite.list.iterStart()

	// Both should be able to iterate independently
	suite.True(iter1.next(context.Background()))
	suite.True(iter2.next(context.Background()))

	item1 := iter1.get()
	item2 := iter2.get()
	suite.Equal(item1.item, item2.item) // Both should see the same first item

	// Advance one iterator further
	suite.True(iter1.next(context.Background()))
	item1 = iter1.get()
	item2 = iter2.get()
	suite.NotEqual(item1.item, item2.item) // Now they should be different
}

func TestPagedListInternalSuite(t *testing.T) {
	suite.Run(t, new(PagedListInternalSuite))
}
