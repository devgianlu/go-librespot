package audio

import (
	"errors"
	"io"
	"time"
)

type LatencyReader struct {
	io.Reader
	Callback func(time.Duration)

	start   time.Time
	latency time.Duration
}

func (r *LatencyReader) Read(b []byte) (int, error) {
	if r.start.IsZero() {
		r.start = time.Now()
	}

	n, err := r.Reader.Read(b)
	if errors.Is(err, io.EOF) {
		r.latency = time.Since(r.start)
		if r.Callback != nil {
			r.Callback(r.latency)
		}
	}

	return n, err
}
