package player

import (
	"encoding/binary"
	"errors"
	"github.com/jfreymuth/oggvorbis"
	log "github.com/sirupsen/logrus"
	"go-librespot/audio"
	"io"
	"math"
	"sync"
)

type sampleDecoder struct {
	reader *oggvorbis.Reader
	once   sync.Once
	ch     chan [Channels * 4]byte
	norm   float32
	seeked bool
	done   bool
	stop   bool
}

const bufferSizeSamples = 65536

func newSampleDecoder(reader *oggvorbis.Reader, norm *audio.ReplayGain) *sampleDecoder {
	return &sampleDecoder{
		reader: reader,
		norm:   norm.GetTrackFactor(1),
		ch:     make(chan [Channels * 4]byte, bufferSizeSamples),
	}
}

func (s *sampleDecoder) decodeLoop() {
	samples := make([]float32, Channels)

	for !s.stop {
		samplesN, err := s.reader.Read(samples)
		if errors.Is(err, io.EOF) {
			// exit loop cleanly
			break
		} else if err != nil {
			log.WithError(err).Error("exiting decoder loop")
			break
		}

		buf := [Channels * 4]byte{}
		for i := 0; i < samplesN; i++ {
			sample := samples[i]
			sample *= s.norm
			binary.LittleEndian.PutUint32(buf[i*4:(i+1)*4], math.Float32bits(sample))
		}

		// if we seeked, throw away the channel and make a new one
		if s.seeked {
			close(s.ch)
			s.ch = make(chan [Channels * 4]byte, bufferSizeSamples)
			s.seeked = false
		}

		s.ch <- buf
	}

	s.done = true
	close(s.ch)
}

func (s *sampleDecoder) Read(p []byte) (n int, err error) {
	s.once.Do(func() { go s.decodeLoop() })

	n = 0
	for n < len(p) {
		if n+Channels*4 > len(p) {
			return n, nil
		}

		frame, ok := <-s.ch
		if !ok {
			// return EOF only if we are done, otherwise we might just be seeking
			if s.done {
				return n, io.EOF
			} else {
				return n, nil
			}
		}

		copy(p[n:], frame[:])
		n += Channels * 4
	}

	return n, nil
}

func (s *sampleDecoder) Close() error {
	s.stop = true
	return nil
}

// Seek will seek the stream to the offset position in milliseconds.
func (s *sampleDecoder) Seek(offset int64, whence int) (int64, error) {
	if whence != io.SeekStart {
		panic("unsupported seek whence") // TODO
	}

	// signal that the channel should be cleared
	s.seeked = true

	pos := offset * SampleRate / 1000
	if err := s.reader.SetPosition(pos); err != nil {
		return 0, err
	}

	return offset, nil
}

func (s *sampleDecoder) Position() int64 {
	return (s.reader.Position() - int64(bufferSizeSamples*Channels)) * 1000 / SampleRate
}
