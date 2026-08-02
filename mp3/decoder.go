package mp3

import (
	"errors"
	"fmt"
	"io"
	"runtime/cgo"
	"sync"
	"unsafe"

	librespot "github.com/devgianlu/go-librespot"
)

// #cgo pkg-config: libmpg123
// #include <mpg123.h>
// #include <stdint.h>
// #include <stdio.h>
// #include <stdlib.h>
//
// extern ssize_t mpg123CallbackRead(void *handle, void *buf, size_t count);
// extern off_t mpg123CallbackSeek(void *handle, off_t offset, int whence);
//
// // cgo cannot pass Go functions as C function pointers directly, so wrap the
// // exported ones here.
// static int replaceReader(mpg123_handle *mh) {
//     return mpg123_replace_reader_handle(mh, mpg123CallbackRead, mpg123CallbackSeek, NULL);
// }
//
// // The reader handle outlives this call, so it cannot be Go memory. Park the
// // cgo.Handle in a C allocation instead.
// static uintptr_t* allocHandle(uintptr_t h) {
//     uintptr_t *p = (uintptr_t*)malloc(sizeof(uintptr_t));
//     if (p != NULL) *p = h;
//     return p;
// }
import "C"

// mpg123_init is only required once per process. It is a no-op on recent
// versions but is still documented as mandatory before the first mpg123_new.
var initOnce sync.Once

// Decoder implements an MP3 decoder on top of libmpg123.
type Decoder struct {
	log librespot.Logger

	SampleRate int32
	Channels   int32

	// gain is the default track gain.
	gain float32

	input librespot.SizedReadAtSeeker

	mh        *C.mpg123_handle
	handle    cgo.Handle
	handlePtr *C.uintptr_t
}

func New(log librespot.Logger, r librespot.SizedReadAtSeeker, gain float32) (_ *Decoder, err error) {
	initOnce.Do(func() { C.mpg123_init() })

	d := &Decoder{log: log, input: r, gain: gain}

	var errC C.int
	d.mh = C.mpg123_new(nil, &errC)
	if d.mh == nil {
		return nil, fmt.Errorf("could not create mpg123 decoder: %s", C.GoString(C.mpg123_plain_strerror(errC)))
	}

	// Release everything if we bail out part way through setup.
	defer func() {
		if err != nil {
			d.free()
		}
	}()

	// Quieten libmpg123: it otherwise writes decoding complaints to stderr.
	if res := C.mpg123_param(d.mh, C.MPG123_ADD_FLAGS, C.MPG123_QUIET, 0); res != C.MPG123_OK {
		return nil, fmt.Errorf("could not set mpg123 quiet flag: %s", d.strerror())
	}

	if res := C.replaceReader(d.mh); res != C.MPG123_OK {
		return nil, fmt.Errorf("could not replace mpg123 reader: %s", d.strerror())
	}

	// The format table is consulted when the first frame is decoded, so it has to
	// be narrowed before opening the stream: setting it afterwards has no effect
	// until the next natural format change, which for a single clip never comes.
	C.mpg123_format_none(d.mh)
	if res := C.mpg123_format2(d.mh, 0, C.MPG123_STEREO, C.MPG123_ENC_FLOAT_32); res != C.MPG123_OK {
		return nil, fmt.Errorf("could not set mpg123 format: %s", d.strerror())
	}

	d.handle = cgo.NewHandle(d)
	d.handlePtr = C.allocHandle(C.uintptr_t(d.handle))
	if d.handlePtr == nil {
		return nil, fmt.Errorf("could not allocate mpg123 reader handle")
	}

	if res := C.mpg123_open_handle(d.mh, unsafe.Pointer(d.handlePtr)); res != C.MPG123_OK {
		return nil, fmt.Errorf("could not open mpg123 stream: %s", d.strerror())
	}

	// Read the negotiated format back rather than assuming the request was
	// honoured: some encodings can be missing from a given library build.
	var rate C.long
	var channels, encoding C.int
	if res := C.mpg123_getformat(d.mh, &rate, &channels, &encoding); res != C.MPG123_OK {
		return nil, fmt.Errorf("could not get mpg123 format: %s", d.strerror())
	}

	if encoding != C.MPG123_ENC_FLOAT_32 {
		return nil, fmt.Errorf("unexpected mpg123 encoding: %d", encoding)
	}

	d.SampleRate = int32(rate)
	d.Channels = int32(channels)

	log.Debugf("mp3 stream info: sample rate = %d, channels = %d", d.SampleRate, d.Channels)
	return d, nil
}

func (d *Decoder) strerror() string {
	return C.GoString(C.mpg123_strerror(d.mh))
}

func (d *Decoder) free() {
	if d.mh != nil {
		C.mpg123_close(d.mh)
		C.mpg123_delete(d.mh)
		d.mh = nil
	}
	if d.handlePtr != nil {
		C.free(unsafe.Pointer(d.handlePtr))
		d.handlePtr = nil
	}
	if d.handle != 0 {
		d.handle.Delete()
		d.handle = 0
	}
}

func decoderFromHandle(handle unsafe.Pointer) *Decoder {
	return cgo.Handle(*(*C.uintptr_t)(handle)).Value().(*Decoder)
}

//export mpg123CallbackRead
func mpg123CallbackRead(handle unsafe.Pointer, buf unsafe.Pointer, count C.size_t) C.ssize_t {
	d := decoderFromHandle(handle)

	b := unsafe.Slice((*byte)(buf), int(count))
	n, err := d.input.Read(b)
	if err != nil && !errors.Is(err, io.EOF) {
		d.log.WithError(err).Errorf("failed to read from input")
		return -1
	}

	// A zero length read is how libmpg123 is told the stream ended.
	return C.ssize_t(n)
}

//export mpg123CallbackSeek
func mpg123CallbackSeek(handle unsafe.Pointer, offset C.off_t, whence C.int) C.off_t {
	d := decoderFromHandle(handle)

	pos, err := d.input.Seek(int64(offset), int(whence))
	if err != nil {
		d.log.WithError(err).Errorf("failed to seek in input")
		return -1
	}

	return C.off_t(pos)
}

func (d *Decoder) Read(p []float32) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	var done C.size_t
	res := C.mpg123_read(d.mh, unsafe.Pointer(&p[0]), C.size_t(len(p))*C.sizeof_float, &done)
	n = int(done) / C.sizeof_float

	// Applied here rather than via mpg123_volume so that gain behaves exactly as
	// it does in the Vorbis and FLAC decoders.
	for i := 0; i < n; i++ {
		p[i] *= d.gain
	}

	switch res {
	case C.MPG123_OK:
		return n, nil
	case C.MPG123_DONE:
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	default:
		return n, fmt.Errorf("error while decoding mp3 frame: %s", d.strerror())
	}
}

func (d *Decoder) SetPositionMs(pos int64) error {
	// mpg123 seeks and tells in samples, not bytes.
	off := pos * int64(d.SampleRate) / 1000
	if res := C.mpg123_seek(d.mh, C.off_t(off), C.SEEK_SET); res < 0 {
		return fmt.Errorf("could not seek to position %dms: %s", pos, d.strerror())
	}

	return nil
}

func (d *Decoder) PositionMs() int64 {
	off := C.mpg123_tell(d.mh)
	if off < 0 {
		d.log.Errorf("could not get decode position: %s", d.strerror())
		return 0
	}

	return int64(off) * 1000 / int64(d.SampleRate)
}

func (d *Decoder) Close() error {
	d.free()
	return nil
}
