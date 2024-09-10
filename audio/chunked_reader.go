package audio

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	librespot "github.com/devgianlu/go-librespot"
	log "github.com/sirupsen/logrus"
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
	err      error
}

type HttpChunkedReader struct {
	log    *log.Entry
	client *http.Client

	// TODO: this url will expire at some point
	url *url.URL

	chunks []*chunkItem

	len int64
	pos int64
}

func NewHttpChunkedReader(log *log.Entry, audioUrl string) (_ *HttpChunkedReader, err error) {
	r := &HttpChunkedReader{log: log, client: &http.Client{}}

	r.url, err = url.Parse(audioUrl)
	if err != nil {
		return nil, fmt.Errorf("failed parsing resource url: %w", err)
	}

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
	var totalChunks int64
	if r.len%DefaultChunkSize == 0 {
		totalChunks = r.len / DefaultChunkSize
	} else {
		totalChunks = r.len/DefaultChunkSize + 1
	}

	r.chunks = make([]*chunkItem, totalChunks)
	for i := int64(0); i < totalChunks; i++ {
		r.chunks[i] = &chunkItem{Cond: sync.NewCond(&sync.Mutex{}), data: nil, err: nil}
	}

	r.chunks[0].data, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading first chunk: %w", err)
	}

	log.Debugf("fetched first chunk of %d, total size is %d bytes", len(r.chunks), r.len)
	return r, nil
}

func (r *HttpChunkedReader) downloadChunk(idx int) (*http.Response, error) {
	return backoff.RetryWithData(func() (*http.Response, error) {
		resp, err := r.client.Do(&http.Request{
			Method: "GET",
			URL:    r.url,
			Header: http.Header{
				"User-Agent": []string{librespot.UserAgent()},
				"Range":      []string{fmt.Sprintf("bytes=%d-%d", idx*DefaultChunkSize, (idx+1)*DefaultChunkSize-1)},
			},
		})
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusPartialContent {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("invalid first chunk response status: %s", resp.Status)
		}

		return resp, nil
	}, backoff.NewConstantBackOff(1*time.Second))
}

func (r *HttpChunkedReader) fetchChunk(idx int) ([]byte, error) {
	chunk := r.chunks[idx]
	chunk.L.Lock()

	// if the chunk is already being fetched, wait until it is done
	for chunk.fetching {
		for !(chunk.data != nil || chunk.err != nil) {
			chunk.Wait()
		}
	}

	// chunk fetched, just return its data
	if chunk.data != nil {
		chunk.L.Unlock()
		return chunk.data, nil
	}

	chunk.fetching = true
	chunk.L.Unlock()

	// download chunk
	resp, err := r.downloadChunk(idx)
	if err != nil {
		// update chunk and signal not fetching
		chunk.L.Lock()
		chunk.err = err
		chunk.fetching = false
		chunk.Broadcast()
		chunk.L.Unlock()

		return nil, fmt.Errorf("failed downloading chunk %d: %w", idx, chunk.err)
	}

	// ensure body gets closed
	defer func() { _ = resp.Body.Close() }()

	// read the chunk data
	data, err := io.ReadAll(resp.Body)
	if chunk.err != nil {
		// update chunk and signal not fetching
		chunk.L.Lock()
		chunk.err = err
		chunk.fetching = false
		chunk.Broadcast()
		chunk.L.Unlock()

		return nil, fmt.Errorf("failed reading chunk %d: %w", idx, chunk.err)
	}

	// update chunk and signal not fetching
	chunk.L.Lock()
	chunk.data = data
	chunk.fetching = false
	chunk.Broadcast()
	chunk.L.Unlock()

	r.log.Debugf("fetched chunk %d/%d, size: %d", idx, len(r.chunks)-1, len(chunk.data))
	return data, nil
}

func (r *HttpChunkedReader) prefetchChunks(curr int) {
	for i := curr + 1; i < curr+1+PrefetchCount; i++ {
		if i >= len(r.chunks) {
			break
		}

		go func(i int) { _, _ = r.fetchChunk(i) }(i)
	}
}

func (r *HttpChunkedReader) Read(p []byte) (n int, err error) {
	n, err = r.ReadAt(p, r.pos)
	r.pos += int64(n)
	return n, err
}

func (r *HttpChunkedReader) ReadAt(p []byte, pos int64) (n int, _ error) {
	chunkIdx, off := int(pos/DefaultChunkSize), int(pos%DefaultChunkSize)
	if chunkIdx >= len(r.chunks) {
		return 0, io.EOF
	}

	// start prefetching next chunks
	r.prefetchChunks(chunkIdx)

	n = 0
	for len(p) > 0 {
		if chunkIdx >= len(r.chunks) {
			return n, nil
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

func (r *HttpChunkedReader) Size() int64 {
	return r.len
}
