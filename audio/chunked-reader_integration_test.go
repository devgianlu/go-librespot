//go:build test_integration

package audio_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/stretchr/testify/suite"
	"go.uber.org/goleak"
)

type HttpChunkedReaderIntegrationSuite struct {
	suite.Suite

	testData []byte
	logger   librespot.Logger
	server   *httptest.Server
}

func (suite *HttpChunkedReaderIntegrationSuite) SetupTest() {
	suite.logger = &librespot.NullLogger{}

	// Create test data larger than multiple chunks
	suite.testData = make([]byte, audio.DefaultChunkSize*5+1000)
	for i := range suite.testData {
		suite.testData[i] = byte(i % 256)
	}

	suite.server = httptest.NewServer(http.HandlerFunc(suite.handleHTTPRequest))
}

func (suite *HttpChunkedReaderIntegrationSuite) TearDownTest() {
	suite.server.Close()
}

func (suite *HttpChunkedReaderIntegrationSuite) handleHTTPRequest(w http.ResponseWriter, r *http.Request) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var start, end int64
	n, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end)
	if n != 2 || err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if start < 0 || end >= int64(len(suite.testData)) || start > end {
		println(start, end, len(suite.testData))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(suite.testData)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(suite.testData[start : end+1])
}

func (suite *HttpChunkedReaderIntegrationSuite) TestBasicReadOperations() {
	reader, err := audio.NewHttpChunkedReader(suite.logger, suite.server.Client(), suite.server.URL)
	suite.Require().NoError(err)
	suite.T().Cleanup(func() { _ = reader.Close() })

	// Test basic read
	buf := make([]byte, 1000)
	n, err := reader.Read(buf)
	suite.Require().NoError(err)
	suite.Equal(1000, n)
	suite.Equal(suite.testData[:1000], buf)

	// Test ReadAt
	buf = make([]byte, 500)
	n, err = reader.ReadAt(buf, 2000)
	suite.Require().NoError(err)
	suite.Equal(500, n)
	suite.Equal(suite.testData[2000:2500], buf)
}

func (suite *HttpChunkedReaderIntegrationSuite) TestLargeSequentialRead() {
	reader, err := audio.NewHttpChunkedReader(suite.logger, suite.server.Client(), suite.server.URL)
	suite.Require().NoError(err)
	suite.T().Cleanup(func() { _ = reader.Close() })

	// Read the entire file
	result, err := io.ReadAll(reader)
	suite.Require().NoError(err)

	suite.Equal(suite.testData, result)
}

func (suite *HttpChunkedReaderIntegrationSuite) TestRandomAccessPattern() {
	reader, err := audio.NewHttpChunkedReader(suite.logger, suite.server.Client(), suite.server.URL)
	suite.Require().NoError(err)
	suite.T().Cleanup(func() { _ = reader.Close() })

	positions := []int64{
		0,
		audio.DefaultChunkSize / 2,
		audio.DefaultChunkSize,
		audio.DefaultChunkSize * 2,
		audio.DefaultChunkSize*3 + 100,
		int64(len(suite.testData)) - 1000,
	}

	for _, pos := range positions {
		buf := make([]byte, 512)
		n, err := reader.ReadAt(buf, pos)
		suite.Require().NoError(err)

		expectedSize := 512
		if pos+512 > int64(len(suite.testData)) {
			expectedSize = int(int64(len(suite.testData)) - pos)
		}

		suite.Equal(expectedSize, n)
		suite.Equal(suite.testData[pos:pos+int64(n)], buf[:n])
	}
}

func (suite *HttpChunkedReaderIntegrationSuite) TestConcurrentReads() {
	reader, err := audio.NewHttpChunkedReader(suite.logger, suite.server.Client(), suite.server.URL)
	suite.Require().NoError(err)
	suite.T().Cleanup(func() { _ = reader.Close() })

	const numGoroutines = 10
	const readSize = 1024

	var wg sync.WaitGroup
	results := make([][]byte, numGoroutines)
	positions := make([]int64, numGoroutines)
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			pos := int64(idx * audio.DefaultChunkSize / 2)
			positions[idx] = pos

			buf := make([]byte, readSize)
			n, err := reader.ReadAt(buf, pos)
			errors[idx] = err

			if err == nil {
				results[idx] = buf[:n]
			}
		}(i)
	}

	wg.Wait()

	// Verify all reads succeeded and data is correct
	for i := 0; i < numGoroutines; i++ {
		if positions[i] < int64(len(suite.testData)) {
			suite.Require().NoError(errors[i], "goroutine %d failed", i)

			expectedSize := readSize
			if positions[i]+int64(readSize) > int64(len(suite.testData)) {
				expectedSize = int(int64(len(suite.testData)) - positions[i])
			}

			suite.Equal(expectedSize, len(results[i]))
			suite.Equal(suite.testData[positions[i]:positions[i]+int64(len(results[i]))], results[i])
		}
	}
}

func (suite *HttpChunkedReaderIntegrationSuite) TestSeekOperations() {
	reader, err := audio.NewHttpChunkedReader(suite.logger, suite.server.Client(), suite.server.URL)
	suite.Require().NoError(err)
	suite.T().Cleanup(func() { _ = reader.Close() })

	// Test various seek operations
	testCases := []struct {
		offset  int64
		whence  int
		want    int64
		finally int64
	}{
		{100, io.SeekStart, 100, 200},
		{50, io.SeekCurrent, 250, 350},
		{-200, io.SeekEnd, int64(len(suite.testData)) - 200, int64(len(suite.testData)) - 100},
		{0, io.SeekStart, 0, 100},
	}

	for _, tc := range testCases {
		pos, err := reader.Seek(tc.offset, tc.whence)
		suite.Require().NoError(err)
		suite.Equal(tc.want, pos)

		// Verify we can read from the new position
		buf := make([]byte, 100)
		n, err := reader.Read(buf)
		suite.Require().NoError(err)
		suite.Equal(100, n)
		suite.Equal(suite.testData[pos:pos+100], buf)

		pos, err = reader.Seek(0, io.SeekCurrent)
		suite.Require().NoError(err)
		suite.Equal(tc.finally, pos)
	}
}

func (suite *HttpChunkedReaderIntegrationSuite) TestPrefetching() {
	reader, err := audio.NewHttpChunkedReader(suite.logger, suite.server.Client(), suite.server.URL)
	suite.Require().NoError(err)
	suite.T().Cleanup(func() { _ = reader.Close() })

	// Read from the beginning to trigger prefetching
	buf := make([]byte, 1000)
	_, err = reader.ReadAt(buf, 0)
	suite.Require().NoError(err)

	// Give some time for prefetching to occur
	time.Sleep(100 * time.Millisecond)

	// Reading the next chunks should be faster due to prefetching
	start := time.Now()
	_, err = reader.ReadAt(buf, audio.DefaultChunkSize)
	duration1 := time.Since(start)
	suite.Require().NoError(err)

	start = time.Now()
	_, err = reader.ReadAt(buf, audio.DefaultChunkSize*2)
	duration2 := time.Since(start)
	suite.Require().NoError(err)

	// Both should be relatively fast due to prefetching
	suite.Less(duration1, 100*time.Millisecond)
	suite.Less(duration2, 100*time.Millisecond)
}

func (suite *HttpChunkedReaderIntegrationSuite) TestLatencyMetrics() {
	reader, err := audio.NewHttpChunkedReader(suite.logger, suite.server.Client(), suite.server.URL)
	suite.Require().NoError(err)
	suite.T().Cleanup(func() { _ = reader.Close() })

	// Trigger multiple reads to generate latency data
	buf := make([]byte, 1000)
	for i := 0; i < 5; i++ {
		_, err := reader.ReadAt(buf, int64(i*audio.DefaultChunkSize))
		suite.Require().NoError(err)
	}

	// Check that latency metrics are reasonable
	suite.Greater(reader.InitialLatency(), time.Duration(0))
	suite.Greater(reader.MaxLatency(), time.Duration(0))
	suite.Greater(reader.MinLatency(), time.Duration(0))
	suite.Greater(reader.AvgLatencyMs(), 0.0)
	suite.Greater(reader.MedianLatency(), time.Duration(0))
	suite.Greater(reader.TotalTime(), time.Duration(0))

	// Sanity checks
	suite.LessOrEqual(reader.MinLatency(), reader.MaxLatency())
	suite.LessOrEqual(reader.MinLatency(), reader.MedianLatency())
	suite.LessOrEqual(reader.MedianLatency(), reader.MaxLatency())
}

func (suite *HttpChunkedReaderIntegrationSuite) TestErrorRecovery() {
	// Create a server that fails occasionally
	failCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCount++
		if failCount%3 == 0 { // Fail every 3rd request
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		suite.handleHTTPRequest(w, r)
	}))
	defer server.Close()

	reader, err := audio.NewHttpChunkedReader(suite.logger, server.Client(), server.URL)
	suite.Require().NoError(err)
	suite.T().Cleanup(func() { _ = reader.Close() })

	// Try to read multiple chunks - some will fail and retry
	buf := make([]byte, 1000)
	for i := 0; i < 3; i++ {
		_, err := reader.ReadAt(buf, int64(i*audio.DefaultChunkSize))
		// Some reads might succeed due to retries
		if err != nil {
			suite.ErrorContains(err, "failed downloading chunk")
		}
	}
}

func (suite *HttpChunkedReaderIntegrationSuite) TestBoundaryConditions() {
	reader, err := audio.NewHttpChunkedReader(suite.logger, suite.server.Client(), suite.server.URL)
	suite.Require().NoError(err)
	suite.T().Cleanup(func() { _ = reader.Close() })

	// Test reading at exact chunk boundary
	buf := make([]byte, 100)
	_, err = reader.ReadAt(buf, audio.DefaultChunkSize)
	suite.Require().NoError(err)

	// Test reading across chunk boundary
	buf = make([]byte, 200)
	_, err = reader.ReadAt(buf, audio.DefaultChunkSize-100)
	suite.Require().NoError(err)

	// Test reading past EOF
	buf = make([]byte, 1000)
	n, err := reader.ReadAt(buf, int64(len(suite.testData))-500)
	suite.ErrorIs(err, io.EOF)
	suite.Equal(500, n)

	// Test reading completely past EOF
	_, err = reader.ReadAt(buf, int64(len(suite.testData))+100)
	suite.ErrorIs(err, io.EOF)
}

func TestHttpChunkedReaderIntegrationSuite(t *testing.T) {
	defer goleak.VerifyNone(t)
	suite.Run(t, new(HttpChunkedReaderIntegrationSuite))
}
