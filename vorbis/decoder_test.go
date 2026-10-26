//go:build test_unit

package vorbis

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/stretchr/testify/require"
)

// testdata/sine_stereo.ogg is three seconds of a 440 Hz sine, stereo at 44.1 kHz:
//
//	ffmpeg -f lavfi -i sine=frequency=440:duration=3:sample_rate=44100 -ac 2 \
//	  -c:a vorbis -strict -2 -b:a 160k -page_duration 200000 vorbis/testdata/sine_stereo.ogg
const sineStereo = "testdata/sine_stereo.ogg"

func openSine(t *testing.T) (*Decoder, []byte) {
	t.Helper()

	data, err := os.ReadFile(sineStereo)
	require.NoError(t, err)

	d, err := New(&librespot.NullLogger{}, bytes.NewReader(data), nil, 1)
	require.NoError(t, err)
	t.Cleanup(d.Close)

	return d, data
}

// finalGranulepos is how many frames the stream holds, which the last page
// declares in its header.
func finalGranulepos(t *testing.T, data []byte) int64 {
	t.Helper()

	i := bytes.LastIndex(data, []byte("OggS"))
	require.GreaterOrEqual(t, i, 0)

	return int64(binary.LittleEndian.Uint64(data[i+6 : i+14]))
}

// Everything up to the end of the stream must come out, whatever the size of
// the reads. The input is read in fixed chunks, and the last one is short: the
// pages in it used to be dropped, cutting off the tail of the track, which is
// where the next one begins in an album meant to play without a pause.
func TestDecoderDeliversTheWholeStream(t *testing.T) {
	for _, size := range []int{4096, 3000, 512, 2} {
		t.Run("", func(t *testing.T) {
			d, data := openSine(t)

			var samples int
			buf := make([]float32, size)
			for {
				n, err := d.Read(buf)
				samples += n
				if err != nil {
					require.True(t, errors.Is(err, io.EOF), "unexpected error: %v", err)
					break
				}
			}

			require.Equal(t, finalGranulepos(t, data), int64(samples)/int64(d.Channels))
		})
	}
}

func TestDecoderStaysAtTheEnd(t *testing.T) {
	d, _ := openSine(t)

	buf := make([]float32, 4096)
	for {
		if _, err := d.Read(buf); err != nil {
			break
		}
	}

	for range 3 {
		n, err := d.Read(buf)
		require.Zero(t, n)
		require.ErrorIs(t, err, io.EOF)
	}
}
