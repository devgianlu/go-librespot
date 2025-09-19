//go:build test_unit

package audio_test

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/devgianlu/go-librespot/audio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLatencyReader(t *testing.T) {
	testData := "Hello, World! This is test data for latency measurement."
	reader := &slowTestReader{
		data:  []byte(testData),
		delay: 50 * time.Millisecond,
	}

	var measuredLatency time.Duration
	latencyReader := &audio.LatencyReader{
		Reader: reader,
		Callback: func(latency time.Duration) {
			measuredLatency = latency
		},
	}

	// Read all data
	buf := make([]byte, len(testData))
	n, err := latencyReader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, len(testData), n)
	assert.Equal(t, testData, string(buf))

	// Ensure EOF is reached
	_, err = latencyReader.Read(buf)
	assert.ErrorIs(t, err, io.EOF)

	// Verify latency was measured
	assert.Greater(t, measuredLatency, time.Duration(0))
}

func TestLatencyReaderMultipleReads(t *testing.T) {
	testData := "Hello, World! This is test data for multiple reads."
	reader := &slowTestReader{
		data:  []byte(testData),
		delay: 50 * time.Millisecond,
	}

	var latencies []time.Duration
	latencyReader := &audio.LatencyReader{
		Reader: reader,
		Callback: func(latency time.Duration) {
			latencies = append(latencies, latency)
		},
	}

	// Read data in chunks
	buf := make([]byte, 10)
	var result bytes.Buffer

	for {
		n, err := latencyReader.Read(buf)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		result.Write(buf[:n])
	}

	assert.Equal(t, testData, result.String())
	assert.Greater(t, len(latencies), 0)

	// All latencies should be positive
	for _, latency := range latencies {
		assert.Greater(t, latency, time.Duration(0))
	}
}

// Helper for testing slow reads
type slowTestReader struct {
	data  []byte
	pos   int
	delay time.Duration
}

func (r *slowTestReader) Read(p []byte) (n int, err error) {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}

	if r.pos >= len(r.data) {
		return 0, io.EOF
	}

	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
