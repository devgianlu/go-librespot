package player

import (
	"io"
	"time"
)

type silentSource struct {
	totalFrames    int64
	positionFrames int64
	nextReadAt     time.Time
}

func newSilentSource(durationMs int64) *silentSource {
	return &silentSource{totalFrames: durationMs * SampleRate / 1000}
}

func (s *silentSource) SetPositionMs(ms int64) error {
	s.positionFrames = min(max(ms, 0)*SampleRate/1000, s.totalFrames)
	return nil
}

func (s *silentSource) PositionMs() int64 {
	return s.positionFrames * 1000 / SampleRate
}

func (s *silentSource) Read(buf []float32) (int, error) {
	remainingFrames := s.totalFrames - s.positionFrames
	if remainingFrames <= 0 {
		return 0, io.EOF
	}

	if now := time.Now(); s.nextReadAt.Before(now) {
		s.nextReadAt = now
	}
	time.Sleep(time.Until(s.nextReadAt))

	frames := min(int64(len(buf)/Channels), remainingFrames)
	n := int(frames) * Channels
	clear(buf[:n])
	s.positionFrames += frames
	s.nextReadAt = s.nextReadAt.Add(time.Duration(frames) * time.Second / SampleRate)
	return n, nil
}
