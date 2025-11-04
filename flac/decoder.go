package flac

import (
	"errors"
	"fmt"
	"io"
	"unsafe"

	librespot "github.com/devgianlu/go-librespot"
)

// #cgo pkg-config: flac
// #include <FLAC/stream_decoder.h>
// #include <stdlib.h>
//
// typedef struct {
//     void* decoder;
// } ClientData;
//
// static ClientData* allocateClientData() {
//     return (ClientData*)malloc(sizeof(ClientData));
// }
//
// static void freeClientData(ClientData *data) {
//     free(data);
// }
//
// static const char* getStreamDecoderErrorStatusString(FLAC__StreamDecoderErrorStatus status) {
//     return FLAC__StreamDecoderErrorStatusString[status];
// }
//
// extern FLAC__StreamDecoderReadStatus callbackRead(
//     FLAC__StreamDecoder *decoder,
//     FLAC__byte *buffer,
//     size_t *bytes,
//     void *clientData
// );
// extern FLAC__StreamDecoderSeekStatus callbackSeek(
//     FLAC__StreamDecoder *decoder,
//     FLAC__uint64 absoluteByteOffset,
//     void *clientData
// );
// extern FLAC__StreamDecoderTellStatus callbackTell(
//     FLAC__StreamDecoder *decoder,
//     FLAC__uint64 *absoluteByteOffset,
//     void *clientData
// );
// extern FLAC__StreamDecoderLengthStatus callbackLength(
//     FLAC__StreamDecoder *decoder,
//     FLAC__uint64 *streamLength,
//     void *clientData
// );
// extern FLAC__bool callbackEof(
//     FLAC__StreamDecoder *decoder,
//     void *clientData
// );
// extern FLAC__StreamDecoderWriteStatus callbackWrite(
//     FLAC__StreamDecoder *decoder,
//     FLAC__Frame *frame,
//     FLAC__int32 *buffer[],
//     void *clientData
// );
// extern void callbackMetadata(
//     FLAC__StreamDecoder *decoder,
//     FLAC__StreamMetadata *metadata,
//     void *client
// );
// extern void callbackError(
//     FLAC__StreamDecoder *decoder,
//     FLAC__StreamDecoderErrorStatus status,
//     void *clientData
// );
import "C"

// Decoder implements an FLAC decoder.
type Decoder struct {
	log librespot.Logger

	SampleRate int32
	Channels   int32

	gain float32

	input      librespot.SizedReadAtSeeker
	decoder    *C.FLAC__StreamDecoder
	clientData *C.ClientData
	buffer     []float32
}

func New(log librespot.Logger, r librespot.SizedReadAtSeeker, gain float32) (*Decoder, error) {
	d := &Decoder{log: log, input: r, gain: gain}

	d.clientData = C.allocateClientData()
	if d.clientData == nil {
		return nil, fmt.Errorf("could not allocate FLAC client data")
	}
	d.clientData.decoder = unsafe.Pointer(d)

	d.decoder = C.FLAC__stream_decoder_new()
	if d.decoder == nil {
		C.freeClientData(d.clientData)
		return nil, fmt.Errorf("could not create FLAC decoder")
	}

	if res := C.FLAC__stream_decoder_init_stream(
		d.decoder,
		(C.FLAC__StreamDecoderReadCallback)(C.callbackRead),
		(C.FLAC__StreamDecoderSeekCallback)(C.callbackSeek),
		(C.FLAC__StreamDecoderTellCallback)(C.callbackTell),
		(C.FLAC__StreamDecoderLengthCallback)(C.callbackLength),
		(C.FLAC__StreamDecoderEofCallback)(C.callbackEof),
		(C.FLAC__StreamDecoderWriteCallback)(C.callbackWrite),
		(C.FLAC__StreamDecoderMetadataCallback)(C.callbackMetadata),
		(C.FLAC__StreamDecoderErrorCallback)(C.callbackError),
		unsafe.Pointer(d.clientData),
	); res != C.FLAC__STREAM_DECODER_INIT_STATUS_OK {
		C.FLAC__stream_decoder_delete(d.decoder)
		C.freeClientData(d.clientData)

		err := C.FLAC__StreamDecoderInitStatusString[res]
		return nil, fmt.Errorf("could not initialize FLAC decoder: %s", C.GoString(err))
	}

	if C.FLAC__stream_decoder_process_until_end_of_metadata(d.decoder) == 0 {
		C.FLAC__stream_decoder_delete(d.decoder)
		C.freeClientData(d.clientData)

		return nil, fmt.Errorf("could not process FLAC metadata")
	}

	return d, nil
}

func callbackGetDecoder(clientData *C.void) *Decoder {
	return (*Decoder)(unsafe.Pointer((*C.ClientData)(unsafe.Pointer(clientData)).decoder))
}

//export callbackRead
func callbackRead(
	_ *C.FLAC__StreamDecoder,
	buffer *C.FLAC__byte,
	bytes *C.size_t,
	clientData *C.void,
) C.FLAC__StreamDecoderReadStatus {
	d := callbackGetDecoder(clientData)

	b := unsafe.Slice((*byte)(buffer), *bytes)
	n, err := d.input.Read(b)
	*bytes = C.size_t(n)

	if errors.Is(err, io.EOF) {
		if n == 0 {
			return C.FLAC__STREAM_DECODER_READ_STATUS_END_OF_STREAM
		}

		return C.FLAC__STREAM_DECODER_READ_STATUS_CONTINUE
	} else if err != nil {
		d.log.WithError(err).Errorf("failed to read from input")
		return C.FLAC__STREAM_DECODER_READ_STATUS_ABORT
	}

	return C.FLAC__STREAM_DECODER_READ_STATUS_CONTINUE
}

//export callbackSeek
func callbackSeek(
	_ *C.FLAC__StreamDecoder,
	absoluteByteOffset C.FLAC__uint64,
	clientData *C.void,
) C.FLAC__StreamDecoderSeekStatus {
	d := callbackGetDecoder(clientData)
	_, err := d.input.Seek(int64(absoluteByteOffset), io.SeekStart)
	if err != nil {
		d.log.WithError(err).Errorf("failed to seek in input")
		return C.FLAC__STREAM_DECODER_SEEK_STATUS_ERROR
	}
	return C.FLAC__STREAM_DECODER_SEEK_STATUS_OK
}

//export callbackTell
func callbackTell(
	_ *C.FLAC__StreamDecoder,
	absoluteByteOffset *C.FLAC__uint64,
	clientData *C.void,
) C.FLAC__StreamDecoderTellStatus {
	d := callbackGetDecoder(clientData)
	pos, err := d.input.Seek(0, io.SeekCurrent)
	if err != nil {
		d.log.WithError(err).Errorf("failed to tell position in input")
		return C.FLAC__STREAM_DECODER_TELL_STATUS_ERROR
	}
	*absoluteByteOffset = C.FLAC__uint64(pos)
	return C.FLAC__STREAM_DECODER_TELL_STATUS_OK
}

//export callbackLength
func callbackLength(
	_ *C.FLAC__StreamDecoder,
	streamLength *C.FLAC__uint64,
	clientData *C.void,
) C.FLAC__StreamDecoderLengthStatus {
	d := callbackGetDecoder(clientData)
	size := d.input.Size()
	*streamLength = C.FLAC__uint64(size)
	return C.FLAC__STREAM_DECODER_LENGTH_STATUS_OK
}

//export callbackEof
func callbackEof(
	_ *C.FLAC__StreamDecoder,
	clientData *C.void,
) C.FLAC__bool {
	d := callbackGetDecoder(clientData)
	pos, err := d.input.Seek(0, io.SeekCurrent)
	if err != nil {
		d.log.WithError(err).Errorf("failed to tell position in input")
		return C.FLAC__bool(1)
	}
	size := d.input.Size()
	if pos >= size {
		return C.FLAC__bool(1)
	}
	return C.FLAC__bool(0)
}

//export callbackWrite
func callbackWrite(
	_ *C.FLAC__StreamDecoder,
	frame *C.FLAC__Frame,
	buffer **C.FLAC__int32,
	clientData *C.void,
) C.FLAC__StreamDecoderWriteStatus {
	d := callbackGetDecoder(clientData)

	s := unsafe.Slice(buffer, d.Channels)
	norm := float32(uint32(1) << uint32(frame.header.bits_per_sample))

	// Copy samples to the temporary buffer interleaving channels
	for i := 0; i < int(frame.header.blocksize); i++ {
		for ch := 0; ch < int(d.Channels); ch++ {
			ss := unsafe.Slice(s[ch], frame.header.blocksize)
			d.buffer = append(d.buffer, float32(ss[i])/norm*d.gain)
		}
	}

	return C.FLAC__STREAM_DECODER_WRITE_STATUS_CONTINUE
}

//export callbackMetadata
func callbackMetadata(
	_ *C.FLAC__StreamDecoder,
	metadata *C.FLAC__StreamMetadata,
	clientData *C.void,
) {
	d := callbackGetDecoder(clientData)

	if metadata._type == C.FLAC__METADATA_TYPE_STREAMINFO {
		streamInfo := (*C.FLAC__StreamMetadata_StreamInfo)(unsafe.Pointer(&metadata.data[0]))
		d.SampleRate = int32(streamInfo.sample_rate)
		d.Channels = int32(streamInfo.channels)
		d.log.Infof("FLAC stream info: sample rate = %d, channels = %d", d.SampleRate, d.Channels)
	} else {
		d.log.Debugf("seen FLAC metadata type: %d", metadata._type)
	}
}

//export callbackError
func callbackError(
	_ *C.FLAC__StreamDecoder,
	status C.FLAC__StreamDecoderErrorStatus,
	clientData *C.void,
) {
	d := callbackGetDecoder(clientData)

	err := C.getStreamDecoderErrorStatusString(status)
	d.log.Errorf("FLAC decoder error: %s", C.GoString(err))
}

func (d *Decoder) Read(p []float32) (n int, err error) {
	for {
		nn := copy(p, d.buffer)
		p = p[nn:]
		n += nn
		d.buffer = d.buffer[nn:]

		if len(p) == 0 {
			return n, nil
		}

		if C.FLAC__stream_decoder_process_single(d.decoder) == 0 {
			return 0, fmt.Errorf("error while decoding FLAC frame")
		}

		if C.FLAC__stream_decoder_get_state(d.decoder) == C.FLAC__STREAM_DECODER_END_OF_STREAM {
			if len(d.buffer) == 0 {
				return n, io.EOF
			}
			return n, nil
		}
	}
}

func (d *Decoder) SetPositionMs(pos int64) error {
	posSamples := pos * int64(d.SampleRate) / 1000
	if C.FLAC__stream_decoder_seek_absolute(d.decoder, C.FLAC__uint64(posSamples)) == 0 {
		return fmt.Errorf("could not seek to position")
	}

	return nil
}

func (d *Decoder) PositionMs() int64 {
	var samplePosition C.FLAC__uint64
	if C.FLAC__stream_decoder_get_decode_position(d.decoder, &samplePosition) == 0 {
		d.log.Errorf("could not get decode position")
		return 0
	}

	return int64(samplePosition) * 1000 / int64(d.SampleRate)
}

func (d *Decoder) Close() error {
	C.FLAC__stream_decoder_delete(d.decoder)
	C.freeClientData(d.clientData)
	return nil
}
