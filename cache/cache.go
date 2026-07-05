// Package cache implements an on-disk cache for the encrypted audio files
// downloaded from the Spotify CDN.
//
// Only the raw, still-encrypted bytes are stored: a cached file is useless
// without a valid audio key retrieved per playback, mirroring the behaviour of
// the reference librespot implementation. Files are keyed by their Spotify file
// id and evicted least-recently-used once an optional size limit is reached.
package cache

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	librespot "github.com/devgianlu/go-librespot"
)

// Cache stores encrypted audio files on disk under <dir>/audio.
type Cache struct {
	log     librespot.Logger
	dir     string
	limiter *sizeLimiter
}

// New creates an audio file cache rooted at dir. When sizeLimit is greater than
// zero, least-recently-used files are evicted to keep the cache within the
// limit; a value of zero disables eviction (unbounded cache).
func New(log librespot.Logger, dir string, sizeLimit int64) (*Cache, error) {
	audioDir := filepath.Join(dir, "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed creating cache directory: %w", err)
	}

	c := &Cache{log: log, dir: audioDir}
	if sizeLimit > 0 {
		c.limiter = newSizeLimiter(sizeLimit)
		if err := c.limiter.init(audioDir); err != nil {
			return nil, fmt.Errorf("failed initializing cache size limiter: %w", err)
		}

		// The limit may have shrunk since the last run, prune eagerly.
		for _, evicted := range c.limiter.prune() {
			log.Debugf("evicted cached file %s", filepath.Base(evicted))
		}
	}

	log.Debugf("initialized audio cache at %s", audioDir)
	return c, nil
}

// filePath returns the on-disk path for the given file id. The first two hex
// characters form a subdirectory to avoid a single directory with a huge number
// of entries.
func (c *Cache) filePath(fileId []byte) string {
	name := hex.EncodeToString(fileId)
	return filepath.Join(c.dir, name[:2], name[2:])
}

// File returns a reader for the cached encrypted audio file, if present. The
// boolean reports whether the file was found. Accessing a file refreshes its
// recency for LRU eviction.
func (c *Cache) File(fileId []byte) (librespot.SizedReadAtSeeker, bool) {
	path := c.filePath(fileId)

	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			c.log.WithError(err).Warnf("failed opening cached file %s", filepath.Base(path))
		}
		return nil, false
	}

	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		c.log.WithError(err).Warnf("failed stating cached file %s", filepath.Base(path))
		return nil, false
	}

	if c.limiter != nil {
		c.limiter.touch(path)
	}

	return &fileReader{File: f, size: fi.Size()}, true
}

// SaveFile stores the encrypted audio file identified by fileId, reading its
// contents from r. Writing is atomic: the data is written to a temporary file
// and renamed into place, so a crash never leaves a truncated cache entry.
// After a successful write, least-recently-used files are evicted when a size
// limit is configured.
func (c *Cache) SaveFile(fileId []byte, r io.Reader) error {
	path := c.filePath(fileId)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed creating cache subdirectory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed creating temporary cache file: %w", err)
	}
	tmpPath := tmp.Name()

	size, err := io.Copy(tmp, r)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed writing cache file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed closing cache file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed renaming cache file: %w", err)
	}

	c.log.Debugf("cached audio file %s (%d bytes)", filepath.Base(path), size)

	if c.limiter != nil {
		c.limiter.add(path, size)
		for _, evicted := range c.limiter.prune() {
			c.log.Debugf("evicted cached file %s", filepath.Base(evicted))
		}
	}

	return nil
}

// fileReader adapts an *os.File to librespot.SizedReadAtSeeker.
type fileReader struct {
	*os.File
	size int64
}

func (r *fileReader) Size() int64 { return r.size }
