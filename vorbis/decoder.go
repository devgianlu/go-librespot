package vorbis

import (
	"sync"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/jfreymuth/oggvorbis"
	log "github.com/sirupsen/logrus"
)

// Decoder implements an OggVorbis decoder.
type Decoder struct {
	sync.Mutex

	log *log.Entry

	SampleRate int32
	Channels   int32

	// meta is the associated metadata (for seeking)
	meta *audio.MetadataPage

	// gain is the default track gain.
	gain float32

	input librespot.SizedReadAtSeeker

	reader *oggvorbis.Reader
}

func New(log *log.Entry, r librespot.SizedReadAtSeeker, meta *audio.MetadataPage, gain float32) (*Decoder, error) {
	reader, err := oggvorbis.NewReader(r)
	if err != nil {
		return nil, err
	}

	d := &Decoder{
		log:        log,
		input:      r,
		SampleRate: int32(reader.SampleRate()),
		Channels:   int32(reader.Channels()),
		meta:       meta,
		gain:       gain,
		reader:     reader,
	}

	return d, nil
}

func (d *Decoder) Read(p []float32) (n int, err error) {
	d.Lock()
	defer d.Unlock()

	n, err = d.reader.Read(p)

	// Apply gain.
	for i := 0; i < n; i++ {
		p[i] *= d.gain
	}

	return
}

func (d *Decoder) SetPositionMs(pos int64) (err error) {
	d.Lock()
	defer d.Unlock()

	if pos == d.positionMsUnlocked() {
		d.log.Tracef("no need to seek (position: %dms)", pos)
		return nil
	}

	// get the seek position in bytes from the milliseconds
	posSamples := pos * int64(d.SampleRate) / 1000
	posBytes := d.meta.GetSeekPosition(posSamples)
	if posBytes > d.input.Size() {
		posBytes = d.input.Size()
	}

	// seek there
	err = d.reader.SetPositionAfter(posSamples, posBytes)
	if err != nil {
		return err
	}

	d.log.Tracef("seek to %dms (diff: %d samples, samples: %d, bytes: %d)", pos, posSamples-d.reader.Position(), posSamples, posBytes)
	return nil
}

func (d *Decoder) PositionMs() int64 {
	d.Lock()
	defer d.Unlock()

	return d.positionMsUnlocked()
}

func (d *Decoder) positionMsUnlocked() int64 {
	return d.reader.Position() * 1000 / int64(d.reader.SampleRate())
}
