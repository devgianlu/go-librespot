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

func TestRecencySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	a := []byte{0x0a}
	b := []byte{0x0b}
	payload := bytes.Repeat([]byte("x"), 10)

	c1, err := New(&librespot.NullLogger{}, dir, 100)
	require.NoError(t, err)
	require.NoError(t, c1.SaveFile(a, bytes.NewReader(payload)))
	require.NoError(t, c1.SaveFile(b, bytes.NewReader(payload)))

	// Age both files on disk, then access b: reading must persist the access
	// time to disk, not just in memory.
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(c1.filePath(a), old, old))
	require.NoError(t, os.Chtimes(c1.filePath(b), old.Add(-time.Hour), old.Add(-time.Hour)))

	r, ok := c1.File(b)
	require.True(t, ok)
	_ = r.(interface{ Close() error }).Close()

	// A fresh instance over the same directory, with a limit fitting only one
	// file, must evict a (stale) and keep b (recently accessed), even though b
	// was written last... i.e. the LRU order reflects access, not write order.
	c2, err := New(&librespot.NullLogger{}, dir, 15)
	require.NoError(t, err)

	_, ok = c2.File(a)
	require.False(t, ok, "stale file should have been evicted on startup")
	_, ok = c2.File(b)
	require.True(t, ok, "recently accessed file should survive a restart")
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
