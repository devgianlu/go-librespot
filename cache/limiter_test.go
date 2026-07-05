package cache

import (
	"bytes"
	"os"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/stretchr/testify/require"
)

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	// Limit fits two 10-byte files but not three.
	c := newTestCacheWithLimit(t, 25)

	a := []byte{0x0a}
	b := []byte{0x0b}
	d := []byte{0x0c}
	payload := bytes.Repeat([]byte("x"), 10)

	require.NoError(t, c.SaveFile(a, bytes.NewReader(payload)))
	touchOlder(c, c.filePath(a), 3*time.Second)

	require.NoError(t, c.SaveFile(b, bytes.NewReader(payload)))
	touchOlder(c, c.filePath(b), 2*time.Second)

	// Access a so that b becomes the least recently used.
	r, ok := c.File(a)
	require.True(t, ok)
	_ = r.(interface{ Close() error }).Close()

	// Saving d pushes us over the limit; b (oldest untouched) must be evicted.
	require.NoError(t, c.SaveFile(d, bytes.NewReader(payload)))

	_, ok = c.File(b)
	require.False(t, ok, "least recently used file should have been evicted")

	_, ok = c.File(a)
	require.True(t, ok, "recently used file should be retained")
	_, ok = c.File(d)
	require.True(t, ok, "newly written file should be retained")

	require.LessOrEqual(t, c.limiter.inUse, int64(25))
}

func TestNoEvictionWithoutLimit(t *testing.T) {
	c := newTestCache(t, 0)
	require.Nil(t, c.limiter)

	payload := bytes.Repeat([]byte("y"), 1000)
	for i := 0; i < 20; i++ {
		require.NoError(t, c.SaveFile([]byte{byte(i), 0xff}, bytes.NewReader(payload)))
	}

	for i := 0; i < 20; i++ {
		_, ok := c.File([]byte{byte(i), 0xff})
		require.True(t, ok)
	}
}

func newTestCacheWithLimit(t *testing.T, limit int64) *Cache {
	t.Helper()
	c, err := New(&librespot.NullLogger{}, t.TempDir(), limit)
	require.NoError(t, err)
	return c
}

// touchOlder rewinds a tracked entry's access time to simulate an older last
// access, since the test writes files in quick succession.
func touchOlder(c *Cache, path string, d time.Duration) {
	c.limiter.mu.Lock()
	if e, ok := c.limiter.files[path]; ok {
		e.atime = time.Now().Add(-d)
	}
	c.limiter.mu.Unlock()
	// Keep the on-disk mtime consistent for good measure.
	old := time.Now().Add(-d)
	_ = os.Chtimes(path, old, old)
}
