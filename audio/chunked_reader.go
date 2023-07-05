package audio

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

const DefaultChunkSize = 512 * 1024

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

type HttpChunkedReader struct {
	// TODO: this url will expire at some point
	url *url.URL

	chunks [][]byte

	len int64
	pos int64
}

func NewHttpChunkedReader(audioUrl string) (_ *HttpChunkedReader, err error) {
	r := &HttpChunkedReader{}

	r.url, err = url.Parse(audioUrl)
	if err != nil {
		return nil, fmt.Errorf("failed parsing resource url: %w", err)
	}

	// request the first chunk, needed for the complete content length
	resp, err := r.requestChunk(0)
	if err != nil {
		return nil, fmt.Errorf("failed requesting first chunk: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("invalid first chunk response status: %s", resp.Status)
	}

	// parse the Content-Range header with the complete content length
	_, _, r.len, err = parseContentRange(resp)
	if err != nil {
		return nil, fmt.Errorf("invalid first chunk content range response: %w", err)
	}

	// create the necessary amount of chunks
	if r.len%DefaultChunkSize == 0 {
		r.chunks = make([][]byte, r.len/DefaultChunkSize)
	} else {
		r.chunks = make([][]byte, r.len/DefaultChunkSize+1)
	}

	r.chunks[0], err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading first chunk: %w", err)
	}

	log.Debugf("fetched first chunk of %d, total size is %d bytes", len(r.chunks), r.len)
	return r, nil
}

func (r *HttpChunkedReader) requestChunk(idx int) (*http.Response, error) {
	return http.DefaultClient.Do(&http.Request{
		Method: "GET",
		URL:    r.url,
		Header: http.Header{
			"User-Agent": []string{librespot.UserAgent()},
			"Range":      []string{fmt.Sprintf("bytes=%d-%d", idx*DefaultChunkSize, (idx+1)*DefaultChunkSize-1)},
		},
	})
}

func (r *HttpChunkedReader) fetchChunk(idx int) error {
	if r.chunks[idx] != nil {
		return nil
	}

	resp, err := r.requestChunk(idx)
	if err != nil {
		return fmt.Errorf("failed requesting chunk %d: %w", idx, err)
	}

	defer func() { _ = resp.Body.Close() }()
	r.chunks[idx], err = io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed reading chunk %d: %w", idx, err)
	}

	log.Debugf("fetched chunk %d/%d, size: %d", idx, len(r.chunks)-1, len(r.chunks[idx]))
	return nil
}

func (r *HttpChunkedReader) Read(p []byte) (n int, err error) {
	n, err = r.ReadAt(p, r.pos)
	r.pos += int64(n)
	return n, err
}

func (r *HttpChunkedReader) ReadAt(p []byte, pos int64) (n int, err error) {
	chunk, off := int(pos/DefaultChunkSize), int(pos%DefaultChunkSize)

	n = 0
	for len(p) > 0 {
		if chunk >= len(r.chunks) {
			return n, io.EOF
		}

		// fetch the chunk in case we don't have it yet
		if err = r.fetchChunk(chunk); err != nil {
			return n, err
		}

		c := r.chunks[chunk][off:]
		if len(c) > len(p) {
			// the chunk is bigger than our output buffer, just copy everything and return
			n += copy(p, c[:len(p)])
			return n, nil
		}

		// the chunk is smaller than the available output buffer space, copy the chunk and advance
		n += copy(p, c)
		p = p[len(c):]

		// try to advance to next chunk
		chunk++
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
