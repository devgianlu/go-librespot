//go:build test_unit

package audio_test

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/stretchr/testify/require"
)

func TestRequestFailsWhenAccesspointIsClosed(t *testing.T) {
	accesspoint := ap.NewAccesspoint(&librespot.NullLogger{}, nil, "")
	accesspoint.Close()

	provider := audio.NewAudioKeyProvider(&librespot.NullLogger{}, accesspoint)

	_, err := provider.Request(context.Background(), nil, nil)
	require.ErrorIs(t, err, ap.ErrAccesspointClosed)
}

// fakeAccesspoint answers key requests as Spotify's accesspoint does on a live
// connection, and not at all on one that went silent, until DropConn replaces
// that connection with a live one.
type fakeAccesspoint struct {
	done chan struct{}
	recv chan ap.Packet

	mu      sync.Mutex
	conn    ap.ConnID
	silent  bool
	sentOn  []ap.ConnID
	dropped []ap.ConnID
}

func newFakeAccesspoint(silent bool) *fakeAccesspoint {
	return &fakeAccesspoint{done: make(chan struct{}), recv: make(chan ap.Packet, 8), silent: silent}
}

func (f *fakeAccesspoint) Done() <-chan struct{} { return f.done }

func (f *fakeAccesspoint) Receive(...ap.PacketType) <-chan ap.Packet { return f.recv }

func (f *fakeAccesspoint) SendConn(_ context.Context, pktType ap.PacketType, payload []byte) (ap.ConnID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sentOn = append(f.sentOn, f.conn)
	if pktType == ap.PacketTypeRequestKey && !f.silent {
		// file id, gid, sequence number, 0
		fileId, seq := payload[:16], payload[32:36]
		f.recv <- ap.Packet{Type: ap.PacketTypeAesKey, Payload: append(slices.Clone(seq), keyFor(fileId)...)}
	}
	return f.conn, nil
}

func (f *fakeAccesspoint) DropConn(id ap.ConnID) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.dropped = append(f.dropped, id)
	if id == f.conn {
		f.conn++
		f.silent = false
	}
}

func (f *fakeAccesspoint) close() { close(f.done) }

func keyFor(fileId []byte) []byte {
	key := slices.Clone(fileId)
	slices.Reverse(key)
	return key
}

var (
	testGid    = bytes.Repeat([]byte{0x11}, 16)
	testFileId = []byte("0123456789abcdef")
)

// A connection that went silent answers no key request: the track load waiting
// on it failed, and every later one too until the accesspoint noticed, minutes
// later. The request must drop that connection and get its key on the next.
func TestKeyRequestIsRetriedOnANewConnectionWhenTheFirstWentSilent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		accesspoint := newFakeAccesspoint(true)
		defer accesspoint.close()
		provider := audio.NewAudioKeyProvider(&librespot.NullLogger{}, accesspoint)

		start := time.Now()
		key, err := provider.Request(context.Background(), testGid, testFileId)
		require.NoError(t, err)
		require.Equal(t, keyFor(testFileId), key)
		require.LessOrEqual(t, time.Since(start), 5*time.Second)

		accesspoint.mu.Lock()
		defer accesspoint.mu.Unlock()
		require.Equal(t, []ap.ConnID{0}, accesspoint.dropped, "the silent connection was not dropped")
		require.Equal(t, []ap.ConnID{0, 1}, accesspoint.sentOn, "the request was not sent again on the new connection")
	})
}

// A caller that gives up first, such as a load cancelled by a skip, says
// nothing about the connection: it must not be dropped, nor the request sent
// again past the caller's deadline.
func TestKeyRequestEndedByItsCallerKeepsTheConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		accesspoint := newFakeAccesspoint(true)
		defer accesspoint.close()
		provider := audio.NewAudioKeyProvider(&librespot.NullLogger{}, accesspoint)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := provider.Request(ctx, testGid, testFileId)
		require.ErrorIs(t, err, context.DeadlineExceeded)

		accesspoint.mu.Lock()
		defer accesspoint.mu.Unlock()
		require.Empty(t, accesspoint.dropped)
		require.Equal(t, []ap.ConnID{0}, accesspoint.sentOn)
	})
}
