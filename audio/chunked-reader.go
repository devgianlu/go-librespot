package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	librespot "github.com/devgianlu/go-librespot"
)

const (
	DefaultChunkSize = 512 * 1024
	PrefetchCount    = 3
)

var contentRangeRegexp = regexp.MustCompile("^bytes (\\d+)-(\\d+)/(\\d+)$")

func parseContentRange(resp *http.Response) (start int64, end int64, size int64, err error) {
	header := resp.Header.Get("Content-Range")
	if len(header) == 0 {
		return 0, 0, 0, fmt.Errorf("invalid first chunk response status: no Content-Range header")
	}

	match := contentRangeRegexp.FindStringSubmatch(header)
	if len(match) == 0 {
		return 0, 0, 0, fmt.Errorf("invalid content range header: %s", header)
	} else if start, err = strconv.ParseInt(match[1], 10, 0); err != nil {
		return 0, 0, 0, fmt.Errorf("invalid content range start: %w", err)
	} else if end, err = strconv.ParseInt(match[2], 10, 0); err != nil {
		return 0, 0, 0, fmt.Errorf("invalid content range end: %w", err)
	} else if size, err = strconv.ParseInt(match[3], 10, 0); err != nil {
		return 0, 0, 0, fmt.Errorf("invalid content range size: %w", err)
	}

	return start, end, size, nil
}

type chunkItem struct {
	*sync.Cond
	data     []byte
	fetching bool
}

func newChunkItem() *chunkItem {
	return &chunkItem{Cond: sync.NewCond(&sync.Mutex{})}
}

type HttpChunkedReader struct {
	log    librespot.Logger
	client *http.Client

	// TODO: this url will expire at some point
	url *url.URL

	chunks []*chunkItem

	len int64
	pos int64

	prefetchMu sync.Mutex
	prefetchWg sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc

	latMu     sync.Mutex
	latencies []time.Duration
}

func NewHttpChunkedReader(log librespot.Logger, client *http.Client, audioUrl string) (_ *HttpChunkedReader, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &HttpChunkedReader{
		log:    log,
		client: client,
		ctx:    ctx,
		cancel: cancel,
	}

	r.url, err = url.Parse(audioUrl)
	if err != nil {
		return nil, fmt.Errorf("failed parsing resource url: %w", err)
	}

	defer func() {
		if err != nil {
			r.cancel()
		}
	}()

	// request the first chunk, needed for the complete content length
	resp, err := r.downloadChunk(0)
	if err != nil {
		return nil, fmt.Errorf("failed requesting first chunk: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	// parse the Content-Range header with the complete content length
	_, _, r.len, err = parseContentRange(resp)
	if err != nil {
		return nil, fmt.Errorf("invalid first chunk content range response: %w", err)
	}

	// create the necessary amount of chunks
	totalChunks := (r.len + DefaultChunkSize - 1) / DefaultChunkSize

	r.chunks = make([]*chunkItem, totalChunks)
	for i := int64(0); i < totalChunks; i++ {
		r.chunks[i] = newChunkItem()
	}

	r.chunks[0].data, err = io.ReadAll(r.measureLatency(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("failed reading first chunk: %w", err)
	}

	log.Debugf("fetched first chunk of %d, total size is %d bytes", len(r.chunks), r.len)
	return r, nil
}

func (r *HttpChunkedReader) closeErr(err error) error {
	if err != nil && r.isClosed() {
		return net.ErrClosed
	}

	return err
}

func (r *HttpChunkedReader) isClosed() bool {
	return r.ctx.Err() != nil
}

func (r *HttpChunkedReader) downloadChunk(idx int) (*http.Response, error) {
	retryBackoff := backoff.WithContext(
		backoff.WithMaxRetries(backoff.NewConstantBackOff(1*time.Second), 3),
		r.ctx,
	)

	return backoff.RetryWithData(func() (*http.Response, error) {
		resp, err := r.client.Do((&http.Request{
			Method: "GET",
			URL:    r.url,
			Header: http.Header{
				"User-Agent": []string{librespot.UserAgent()},
				"Range": []string{fmt.Sprintf("bytes=%d-%d",
					idx*DefaultChunkSize,
					min(max(r.len, DefaultChunkSize), int64((idx+1)*DefaultChunkSize))-1,
				)},
			},
		}).WithContext(r.ctx))
		if err != nil {
			err = r.closeErr(err)
			if errors.Is(err, net.ErrClosed) {
				return nil, backoff.Permanent(err)
			}

			return nil, err
		}

		if resp.StatusCode != http.StatusPartialContent {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("invalid first chunk response status: %s", resp.Status)
		}

		return resp, nil
	}, retryBackoff)
}

func (r *HttpChunkedReader) downloadAndRead(idx int) ([]byte, error) {
	resp, err := r.downloadChunk(idx)
	if err != nil {
		return nil, fmt.Errorf("failed downloading chunk %d: %w", idx, r.closeErr(err))
	}

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(r.measureLatency(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("failed reading chunk %d: %w", idx, r.closeErr(err))
	}

	return data, nil
}

func (r *HttpChunkedReader) fetchChunk(idx int) ([]byte, error) {
	chunk := r.chunks[idx]

	chunk.L.Lock()
	for {
		if r.isClosed() {
			chunk.L.Unlock()
			return nil, net.ErrClosed
		}

		if chunk.data != nil {
			data := chunk.data
			chunk.L.Unlock()
			return data, nil
		}

		if !chunk.fetching {
			chunk.fetching = true
			chunk.L.Unlock()
			break
		}

		chunk.Wait()
	}

	data, err := r.downloadAndRead(idx)
	if err != nil {
		chunk.L.Lock()
		chunk.fetching = false
		chunk.Broadcast()
		chunk.L.Unlock()
		return nil, err
	}

	chunk.L.Lock()
	chunk.data = data
	chunk.fetching = false
	chunk.Broadcast()
	chunk.L.Unlock()

	r.log.Debugf("fetched chunk %d/%d, size: %d", idx, len(r.chunks)-1, len(data))
	if r.isClosed() {
		return nil, net.ErrClosed
	}

	return data, nil
}

func (r *HttpChunkedReader) prefetchChunks(curr int) {
	for i := curr + 1; i < curr+1+PrefetchCount; i++ {
		if i >= len(r.chunks) {
			break
		}

		if !r.startPrefetch(i) {
			return
		}
	}
}

func (r *HttpChunkedReader) startPrefetch(idx int) bool {
	r.prefetchMu.Lock()
	defer r.prefetchMu.Unlock()

	if r.isClosed() {
		return false
	}

	r.prefetchWg.Add(1)
	go func() {
		defer r.prefetchWg.Done()
		_, _ = r.fetchChunk(idx)
	}()

	return true
}

func (r *HttpChunkedReader) Read(p []byte) (n int, err error) {
	n, err = r.ReadAt(p, r.pos)
	r.pos += int64(n)
	return n, err
}

func (r *HttpChunkedReader) ReadAt(p []byte, pos int64) (n int, _ error) {
	if r.isClosed() {
		return 0, net.ErrClosed
	}

	chunkIdx, off := int(pos/DefaultChunkSize), int(pos%DefaultChunkSize)
	if chunkIdx >= len(r.chunks) {
		return 0, io.EOF
	}

	// start prefetching next chunks
	r.prefetchChunks(chunkIdx)

	n = 0
	for len(p) > 0 {
		if chunkIdx >= len(r.chunks) {
			return n, io.EOF
		}

		// get the chunk data
		chunk, err := r.fetchChunk(chunkIdx)
		if err != nil {
			return n, fmt.Errorf("failed reading chunk %d: %w", chunkIdx, err)
		}

		// read the chunk data
		c := chunk[min(off, len(chunk)):]
		if len(c) > len(p) {
			// the chunk is bigger than our output buffer, just copy everything and return
			n += copy(p, c[:len(p)])
			return n, nil
		}

		// the chunk is smaller than the available output buffer space, copy the chunk and advance
		n += copy(p, c)
		p = p[len(c):]

		// try to advance to next chunk
		chunkIdx++
		off = 0
	}

	return n, nil
}

func (r *HttpChunkedReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		if r.pos+offset < 0 || r.pos+offset >= r.len {
			return 0, fmt.Errorf("invalid seek position")
		}

		r.pos += offset
		return r.pos, nil
	case io.SeekStart:
		if offset < 0 || offset >= r.len {
			return 0, fmt.Errorf("invalid seek position")
		}

		r.pos = offset
		return r.pos, nil
	case io.SeekEnd:
		if r.len+offset < 0 || r.len+offset >= r.len {
			return 0, fmt.Errorf("invalid seek position")
		}

		r.pos = r.len + offset
		return r.pos, nil
	default:
		panic("unknown seek whence")
	}
}

func (r *HttpChunkedReader) measureLatency(rr io.Reader) io.Reader {
	return &LatencyReader{
		Reader:   rr,
		Callback: r.recordLatency,
	}
}

func (r *HttpChunkedReader) recordLatency(latency time.Duration) {
	r.latMu.Lock()
	defer r.latMu.Unlock()

	r.latencies = append(r.latencies, latency)
}

func (r *HttpChunkedReader) latencySnapshot() []time.Duration {
	r.latMu.Lock()
	defer r.latMu.Unlock()

	latencies := make([]time.Duration, len(r.latencies))
	copy(latencies, r.latencies)
	return latencies
}

func (r *HttpChunkedReader) Size() int64 {
	return r.len
}

func (r *HttpChunkedReader) Url() *url.URL {
	return r.url
}

func (r *HttpChunkedReader) InitialLatency() time.Duration {
	latencies := r.latencySnapshot()
	if len(latencies) == 0 {
		return 0
	}

	return latencies[0]
}

func (r *HttpChunkedReader) MaxLatency() time.Duration {
	latencies := r.latencySnapshot()
	if len(latencies) == 0 {
		return 0
	}

	maxLatency := latencies[0]
	for _, latency := range latencies {
		if latency > maxLatency {
			maxLatency = latency
		}
	}

	return maxLatency
}

func (r *HttpChunkedReader) MinLatency() time.Duration {
	latencies := r.latencySnapshot()
	if len(latencies) == 0 {
		return 0
	}

	minLatency := latencies[0]
	for _, latency := range latencies {
		if latency < minLatency {
			minLatency = latency
		}
	}

	return minLatency
}

func (r *HttpChunkedReader) AvgLatencyMs() float64 {
	latencies := r.latencySnapshot()
	if len(latencies) == 0 {
		return 0
	}

	var sum time.Duration
	for _, latency := range latencies {
		sum += latency
	}

	return float64(sum.Milliseconds()) / float64(len(latencies))
}

func (r *HttpChunkedReader) MedianLatency() time.Duration {
	latencies := r.latencySnapshot()
	if len(latencies) == 0 {
		return 0
	}

	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	mid := len(latencies) / 2
	if len(latencies)%2 == 0 {
		return (latencies[mid-1] + latencies[mid]) / 2
	}

	return latencies[mid]
}

func (r *HttpChunkedReader) TotalTime() time.Duration {
	latencies := r.latencySnapshot()

	var sum time.Duration
	for _, latency := range latencies {
		sum += latency
	}
	return sum
}

func (r *HttpChunkedReader) Close() error {
	r.prefetchMu.Lock()
	r.cancel()
	r.prefetchMu.Unlock()

	for _, chunk := range r.chunks {
		chunk.L.Lock()
		chunk.Broadcast()
		chunk.L.Unlock()
	}

	r.prefetchWg.Wait()
	return nil
}
