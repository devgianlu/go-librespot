package player

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/mp3"
)

// maxNarrationSize caps what will be pulled into memory for one clip. Narration
// runs a couple of seconds and weighs a few hundred kilobytes; anything wildly
// larger means something other than a narration clip is on the other end.
const maxNarrationSize = 8 << 20

// NarrationSource plays a DJ narration clip: MP3 fetched from a CDN with a plain
// unauthenticated GET, with no Spotify id, audio key or PlayPlay involved.
//
// The whole clip is buffered. It is small, and the decoder needs to seek.
type NarrationSource struct {
	*mp3.Decoder
}

// NewNarrationSource fetches a synthesized narration clip and decodes it.
//
// The url from Spclient.NarrationUrl is already signed, so it is fetched without
// the Authorization header the rest of the client sends. The levels come from
// the track's narration.*.loudness and narration.*.true_peak metadata.
func NewNarrationSource(ctx context.Context, log librespot.Logger, client *http.Client,
	url string, loudnessDb, truePeakDb, pregain float32) (*NarrationSource, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating narration request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed fetching narration audio: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invalid status code from narration audio: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNarrationSize+1))
	if err != nil {
		return nil, fmt.Errorf("failed reading narration audio: %w", err)
	} else if len(data) > maxNarrationSize {
		return nil, fmt.Errorf("narration audio is too large (over %d bytes)", maxNarrationSize)
	} else if len(data) == 0 {
		return nil, fmt.Errorf("narration audio is empty")
	}

	gain := normalisationFactorFor(loudnessDb, truePeakDb, pregain)

	dec, err := mp3.New(log, bytes.NewReader(data), gain)
	if err != nil {
		return nil, fmt.Errorf("failed initializing narration mp3 stream: %w", err)
	}

	if dec.SampleRate != SampleRate {
		_ = dec.Close()
		return nil, fmt.Errorf("unsupported sample rate: %d", dec.SampleRate)
	} else if dec.Channels != Channels {
		_ = dec.Close()
		return nil, fmt.Errorf("unsupported channels: %d", dec.Channels)
	}

	log.Debugf("narration clip ready: %d bytes, gain = %.3f", len(data), gain)

	return &NarrationSource{Decoder: dec}, nil
}

// NarratedSource plays a track with the DJ talking around it: an introduction
// before it and a closing remark after it, either of which may be absent.
//
// Playback is sequential rather than mixed, matching the ms_narration_overlapping
// of 0 the official client reports. Position stays the track's throughout, so it
// reads as not yet started while the DJ introduces it.
type NarratedSource struct {
	log librespot.Logger

	// parts are played in order; main is the index of the track among them.
	parts []librespot.AudioSource
	main  int
	cur   int
}

// NewNarratedSource wraps a track with its narration. Either clip may be nil.
func NewNarratedSource(log librespot.Logger, intro, main, outro librespot.AudioSource) *NarratedSource {
	s := &NarratedSource{log: log}

	if intro != nil {
		s.parts = append(s.parts, intro)
	}

	s.main = len(s.parts)
	s.parts = append(s.parts, main)

	if outro != nil {
		s.parts = append(s.parts, outro)
	}

	return s
}

func (s *NarratedSource) Read(p []float32) (int, error) {
	for s.cur < len(s.parts) {
		n, err := s.parts[s.cur].Read(p)

		switch {
		case err == nil:
			return n, nil

		case errors.Is(err, io.EOF):
			s.cur++
			if n > 0 {
				if s.cur >= len(s.parts) {
					return n, io.EOF
				}
				return n, nil
			}

		default:
			// A failing track is a real error, but a failing narration clip is
			// not: drop it and carry on rather than losing the music.
			if s.cur == s.main {
				return n, err
			}

			s.log.WithError(err).Warnf("narration playback failed, skipping it")
			s.cur++
			if n > 0 {
				return n, nil
			}
		}
	}

	return 0, io.EOF
}

// SetPositionMs seeks the track, abandoning an introduction still playing: a
// listener who scrubs wants the music. The closing remark still follows, since
// it belongs to the end of the track.
func (s *NarratedSource) SetPositionMs(pos int64) error {
	if s.cur < s.main {
		s.cur = s.main
	}

	return s.parts[s.main].SetPositionMs(pos)
}

// NoCrossfade keeps a closing remark out of the crossfade reserve, which would
// otherwise spend the whole of it fading out. A source with only an introduction
// ends in ordinary music and still crossfades.
func (s *NarratedSource) NoCrossfade() bool {
	return s.main < len(s.parts)-1
}

func (s *NarratedSource) PositionMs() int64 {
	return s.parts[s.main].PositionMs()
}

func (s *NarratedSource) Close() error {
	var err error
	for _, part := range s.parts {
		if closer, ok := part.(io.Closer); ok && closer != nil {
			if cerr := closer.Close(); cerr != nil {
				err = cerr
			}
		}
	}

	return err
}

// NarrationLoudness reads the loudness and true peak a track's narration
// metadata declares. Reporting absence matters: taking a missing value as zero
// would mean normalising against 0 LUFS and attenuating the clip to nothing.
func NarrationLoudness(metadata map[string]string, prefix string) (loudnessDb, truePeakDb float32, ok bool) {
	loudness, err := strconv.ParseFloat(metadata[prefix+".loudness"], 32)
	if err != nil {
		return 0, 0, false
	}

	truePeak, err := strconv.ParseFloat(metadata[prefix+".true_peak"], 32)
	if err != nil {
		return 0, 0, false
	}

	return float32(loudness), float32(truePeak), true
}
