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
	r    *oggvorbis.Reader
	o    sync.Once
	c    chan [Channels * 4]byte
	stop bool
}

func newSampleDecoder(reader *oggvorbis.Reader, norm *audio.ReplayGain) *sampleDecoder {
	// TODO: use ReplayGain metadata for normalisation
	return &sampleDecoder{r: reader, c: make(chan [Channels * 4]byte, 65536)}
}

func (s *sampleDecoder) decodeLoop() {
	samples := make([]float32, Channels)

	for !s.stop {
		samplesN, err := s.r.Read(samples)
		if errors.Is(err, io.EOF) {
			// exit loop cleanly
			break
		} else if err != nil {
			log.WithError(err).Error("exiting decoder loop")
			break
		}

		buf := [Channels * 4]byte{}
		for i := 0; i < samplesN; i++ {
			binary.LittleEndian.PutUint32(buf[i*4:(i+1)*4], math.Float32bits(samples[i]))
		}

		s.c <- buf
	}

	close(s.c)
}

func (s *sampleDecoder) Read(p []byte) (n int, err error) {
	s.o.Do(func() { go s.decodeLoop() })

	n = 0
	for n < len(p) {
		if n+Channels*4 > len(p) {
			return n, nil
		}

		frame, ok := <-s.c
		if !ok {
			return n, io.EOF
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

	// TODO: clear the buffer?

	pos := offset * SampleRate / 1000
	if err := s.r.SetPosition(pos); err != nil {
		return 0, err
	}

	return offset, nil
}
