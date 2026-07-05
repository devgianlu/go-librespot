package cache

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/stretchr/testify/require"
)

func newTestCache(t *testing.T, sizeLimit int64) *Cache {
	t.Helper()
	c, err := New(&librespot.NullLogger{}, t.TempDir(), sizeLimit)
	require.NoError(t, err)
	return c
}

func TestFilePathLayout(t *testing.T) {
	c := newTestCache(t, 0)

	fileId := []byte{0xab, 0xcd, 0xef, 0x01, 0x23}
	path := c.filePath(fileId)

	require.Equal(t, "ab", filepath.Base(filepath.Dir(path)))
	require.Equal(t, "cdef0123", filepath.Base(path))
}

func TestSaveAndReadRoundTrip(t *testing.T) {
	c := newTestCache(t, 0)
	fileId := []byte{0x01, 0x02, 0x03, 0x04}
	data := []byte("some encrypted audio payload")

	// miss before save
	_, ok := c.File(fileId)
	require.False(t, ok)

	require.NoError(t, c.SaveFile(fileId, bytes.NewReader(data)))

	r, ok := c.File(fileId)
	require.True(t, ok)
	defer func() { _ = r.(io.Closer).Close() }()

	require.Equal(t, int64(len(data)), r.Size())

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, data, got)

	// no leftover temporary files
	entries, err := filepath.Glob(filepath.Join(c.dir, "*", "*.tmp"))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestReadAtSeek(t *testing.T) {
	c := newTestCache(t, 0)
	fileId := []byte{0x0a, 0x0b}
	data := []byte("0123456789")
	require.NoError(t, c.SaveFile(fileId, bytes.NewReader(data)))

	r, ok := c.File(fileId)
	require.True(t, ok)
	defer func() { _ = r.(io.Closer).Close() }()

	buf := make([]byte, 4)
	n, err := r.ReadAt(buf, 3)
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Equal(t, []byte("3456"), buf)

	pos, err := r.Seek(2, io.SeekStart)
	require.NoError(t, err)
	require.Equal(t, int64(2), pos)
}

func TestSavePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	fileId := []byte{0xde, 0xad, 0xbe, 0xef}
	data := []byte("persisted payload")

	c1, err := New(&librespot.NullLogger{}, dir, 1<<20)
	require.NoError(t, err)
	require.NoError(t, c1.SaveFile(fileId, bytes.NewReader(data)))

	// A fresh cache instance over the same directory must find the file and
	// account for its size.
	c2, err := New(&librespot.NullLogger{}, dir, 1<<20)
	require.NoError(t, err)

	r, ok := c2.File(fileId)
	require.True(t, ok)
	defer func() { _ = r.(io.Closer).Close() }()
	require.EqualValues(t, len(data), c2.limiter.inUse)
}

func TestNewCreatesAudioDir(t *testing.T) {
	dir := t.TempDir()
	_, err := New(&librespot.NullLogger{}, dir, 0)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dir, "audio"))
	require.NoError(t, err)
	require.True(t, info.IsDir())
}
