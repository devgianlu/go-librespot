//go:build !android && !js && !windows && !nintendosdk && !linux && darwin

package output

//
//#cgo LDFLAGS: -framework AudioToolbox -v
//
//#include <CoreAudio/CoreAudio.h>
//#include <AudioToolbox/AudioToolbox.h>
//
//extern void audioCallback(void * inUserData, AudioQueueRef inAQ,	AudioQueueBufferRef inBuffer);
//
import "C"
import (
	"errors"
	"fmt"
	librespot "github.com/devgianlu/go-librespot"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
	"io"
	"runtime"
	"sync"
	"unsafe"
)

type arr[T any] struct {
	ptr   *T
	ln    uint
	vlPtr *float32
}
type output struct {
	channels   int
	sampleRate int
	device     string
	reader     librespot.Float32Reader

	cond *sync.Cond

	canPause   bool
	periodSize int
	bufferSize int
	samples    *RingBuffer[arr[float32]]

	pin runtime.Pinner

	externalVolume bool

	volume    float32
	volumePtr *float32
	paused    bool
	closed    bool
	released  bool

	buffers    []C.AudioQueueBufferRef
	numBuffers int

	audioQueue C.AudioQueueRef

	externalVolumeUpdate *RingBuffer[float32]
	err                  chan error
}

func newOutput(reader librespot.Float32Reader, sampleRate int, channels int, device string, mixer string, control string, initialVolume float32, externalVolume bool, externalVolumeUpdate *RingBuffer[float32]) (*output, error) {
	out := &output{
		reader:               reader,
		channels:             channels,
		sampleRate:           sampleRate,
		device:               device,
		volume:               initialVolume,
		err:                  make(chan error, 2),
		cond:                 sync.NewCond(&sync.Mutex{}),
		externalVolume:       externalVolume,
		externalVolumeUpdate: externalVolumeUpdate,
	}

	out.numBuffers = 16
	out.volumePtr = &out.volume

	// hideous hack, if it is possible somehow differently, please change this
	var in = C.calloc(C.ulong(out.numBuffers), C.ulong(unsafe.Sizeof(arr[float32]{})))
	var f = NewRingBuffer[arr[float32]](uint64(out.numBuffers))

	// replace the inner, with c-memory
	f.inner = unsafe.Slice((*arr[float32])(in), out.numBuffers)
	out.samples = f

	out.pin.Pin(f.notFull)
	out.pin.Pin(f.notEmpty)
	out.pin.Pin(out.volumePtr)

	if err := out.setupPcm(); err != nil {
		return nil, err
	}

	go func() {
		out.err <- out.readLoop()
		_ = out.Close()
	}()

	return out, nil
}

func (out *output) alsaError(name string, err C.int) error {
	if errors.Is(unix.Errno(-err), unix.EPIPE) {
		_ = out.Close()
	}
	return errors.New(fmt.Sprintf("%s: %d", name, err))
}

func (out *output) setupPcm() error {
	description := C.AudioStreamBasicDescription{
		mSampleRate:       (C.double)(out.sampleRate),
		mFormatID:         C.kAudioFormatLinearPCM,
		mFormatFlags:      C.kAudioFormatFlagIsFloat,
		mBytesPerPacket:   8,
		mFramesPerPacket:  1,
		mBytesPerFrame:    8,
		mChannelsPerFrame: 2,
		mBitsPerChannel:   32,
		mReserved:         0,
	}

	var cb C.AudioQueueOutputCallback = (C.AudioQueueOutputCallback)(C.audioCallback)

	err := C.AudioQueueNewOutput(&description, cb, (unsafe.Pointer)(out.samples), 0, 0, 0, &out.audioQueue)
	if err != 0 {
		return out.alsaError("setupAudioQueue", err)
	}

	// todo: somehow get a more clever period size on the hardware here
	out.periodSize = 4096
	var bufferSize = out.periodSize * out.channels * 4
	out.bufferSize = bufferSize

	out.buffers = make([]C.AudioQueueBufferRef, 4)

	for i := 0; i < len(out.buffers); i++ {
		var allocResult = C.AudioQueueAllocateBuffer(out.audioQueue, C.uint(bufferSize), (*C.AudioQueueBufferRef)(&out.buffers[i]))

		log.Tracef("alloc result %d: %d", i, int(allocResult))

		var buf = out.buffers[i]

		buf.mAudioDataByteSize = buf.mAudioDataBytesCapacity

		var enqResult = C.AudioQueueEnqueueBuffer(out.audioQueue, out.buffers[i], 0, nil)
		log.Tracef("enqueue buffer %d", int(enqResult))
	}

	var queueStart = C.AudioQueueStart(out.audioQueue, nil)

	log.Tracef("pcm setup! %d", int(queueStart))
	return nil
}

func (out *output) logParams(params any) error {
	return nil
}

func (out *output) readLoop() error {
	for {
		var fts = unsafe.Pointer(C.calloc(C.ulong(out.channels*out.periodSize), C.ulong(unsafe.Sizeof(C.float(0)))))

		floats := unsafe.Slice((*float32)(fts), out.channels*out.periodSize)

		n, err := out.reader.Read(floats)
		if n > 0 {
			floats = floats[:n]
			var ar = arr[float32]{
				ptr:   (*float32)(fts),
				ln:    uint(n),
				vlPtr: out.volumePtr,
			}

			if err := out.samples.PutWait(ar); errors.Is(err, ErrBufferClosed) {
				return nil
			} else if err != nil {
				_ = out.samples.Close()
				return err
			}
		}

		if errors.Is(err, io.EOF) {
			_ = out.samples.Close()
			return nil
		} else if err != nil {
			_ = out.samples.Close()
			return err
		}
	}
}

//export audioCallback
func audioCallback(inUserData unsafe.Pointer, inAq C.AudioQueueRef, inBuffer C.AudioQueueBufferRef) {
	var out = (*RingBuffer[arr[float32]])(inUserData)

	samples, err := out.GetWait()
	if err != nil {
		log.Tracef("callback err")
		return
	}
	defer C.free(unsafe.Pointer(samples.ptr))

	if uint(inBuffer.mAudioDataBytesCapacity/4) < samples.ln {
		log.Warnf("buffer overrun")
	}

	samplesToPush := min(samples.ln, uint(inBuffer.mAudioDataBytesCapacity/4))
	inBuffer.mAudioDataByteSize = C.uint(samplesToPush * 4)

	var mem = inBuffer.mAudioData
	C.memcpy(mem, unsafe.Pointer(samples.ptr), C.ulong(samplesToPush*4))

	ptr := unsafe.Slice((*float32)(mem), samplesToPush)
	for i := 0; i < int(samplesToPush); i++ {
		ptr[i] *= *samples.vlPtr
	}

	if C.AudioQueueEnqueueBuffer(inAq, inBuffer, 0, nil) != C.OSStatus(0) {
		log.Tracef("error enqueue")
	}
}

func (out *output) Pause() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed || out.paused {
		return nil
	}

	C.AudioQueuePause(out.audioQueue)

	out.paused = true

	return nil
}

func (out *output) Resume() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed || !out.paused {
		return nil
	}

	C.AudioQueueStart(out.audioQueue, nil)

	out.paused = false
	out.cond.Signal()

	return nil
}

func (out *output) Drop() error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed || out.released {
		return nil
	}

	out.samples.Clear()
	C.AudioQueueFlush(out.audioQueue)
	out.pin.Unpin()

	return nil
}

func (out *output) DelayMs() (int64, error) {
	var queueTime C.AudioTimeStamp
	err := C.AudioQueueGetCurrentTime(out.audioQueue, nil, &queueTime, nil)
	if err != 0 {
		return 0, out.alsaError("AudioQueueGetCurrentTime", err)
	}

	// Convert to milliseconds
	delay := int64((queueTime.mSampleTime / C.Float64(out.sampleRate)) * 1000)

	return delay, nil
}

func (out *output) SetVolume(vol float32) {
	if vol < 0 || vol > 1 {
		panic(fmt.Sprintf("invalid volume value: %0.2f", vol))
	}

	out.volume = vol
}

func (out *output) Error() <-chan error {
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	return out.err
}

func (out *output) Close() error {
	log.Tracef("close")
	out.cond.L.Lock()
	defer out.cond.L.Unlock()

	if out.closed || out.released {
		out.closed = true
		return nil
	}

	C.AudioQueueFlush(out.audioQueue)
	C.AudioQueueDispose(out.audioQueue, 0)

	C.free(unsafe.Pointer(&out.samples.inner[0]))

	out.closed = true
	out.cond.Signal()

	return nil
}
