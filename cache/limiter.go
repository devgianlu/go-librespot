package cache

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// limiterEntry tracks a single cached file for eviction purposes.
type limiterEntry struct {
	size  int64
	atime time.Time
}

// sizeLimiter keeps the total size of the cached files within a configured
// limit by evicting the least-recently-used entries. It is safe for concurrent
// use.
//
// Eviction candidates are found with a linear scan over the tracked files. The
// number of cached audio files is small enough (a bounded cache of multi-megabyte
// files) that a heap-based priority queue, as used by librespot, is not worth
// the extra complexity here.
type sizeLimiter struct {
	mu    sync.Mutex
	limit int64
	inUse int64
	files map[string]*limiterEntry
}

func newSizeLimiter(limit int64) *sizeLimiter {
	return &sizeLimiter{limit: limit, files: make(map[string]*limiterEntry)}
}

// init walks the cache directory and registers every existing file so the
// limiter reflects the on-disk state after a restart. Recency is seeded from
// the file modification time, as access time is not portably available. Any
// leftover temporary files from an interrupted write are removed.
func (s *sizeLimiter) init(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmp") {
			_ = os.Remove(path)
			return nil
		}

		s.mu.Lock()
		s.files[path] = &limiterEntry{size: info.Size(), atime: info.ModTime()}
		s.inUse += info.Size()
		s.mu.Unlock()
		return nil
	})
}

// add registers a newly written file (or updates an existing one) with the
// current time as its access time.
func (s *sizeLimiter) add(path string, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.files[path]; ok {
		s.inUse -= e.size
	}
	s.files[path] = &limiterEntry{size: size, atime: time.Now()}
	s.inUse += size
}

// touch refreshes the access time of a file, marking it as recently used.
func (s *sizeLimiter) touch(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.files[path]; ok {
		e.atime = time.Now()
	}
}

// prune removes least-recently-used files from disk until the tracked size is
// within the configured limit. It returns the paths that were evicted.
func (s *sizeLimiter) prune() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var evicted []string
	for s.inUse > s.limit {
		var oldestPath string
		var oldest *limiterEntry
		for path, e := range s.files {
			if oldest == nil || e.atime.Before(oldest.atime) {
				oldest, oldestPath = e, path
			}
		}
		if oldest == nil {
			break
		}

		err := os.Remove(oldestPath)
		delete(s.files, oldestPath)
		s.inUse -= oldest.size
		if err != nil && !os.IsNotExist(err) {
			// Stop after an un-removable file to avoid a busy loop; the entry has
			// already been dropped from accounting.
			break
		}
		evicted = append(evicted, oldestPath)
	}
	return evicted
}
