//go:build !android && !js && !windows && !nintendosdk && !linux && darwin

package output

// #cgo LDFLAGS: -framework AudioToolbox -framework CoreAudio
// #include <AudioToolbox/AudioToolbox.h>
// #include <CoreAudio/CoreAudio.h>
// extern void audioCallback(void * inUserData, AudioQueueRef inAQ,	AudioQueueBufferRef inBuffer);
//
// typedef struct {
//     void *output;
// } AudioContext;
//
// static void freeAudioContext(AudioContext *ctx) {
//     free(ctx);
// }
import "C"
import (
	"errors"
	"fmt"
	"io"
	"sync"
	"unsafe"

	librespot "github.com/devgianlu/go-librespot"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

type ringBuffer struct {
	data   []float32
	size   int
	head   int
	tail   int
	mu     sync.Mutex
	cond   *sync.Cond
	closed bool
}

func newRingBuffer(size int) *ringBuffer {
	rb := &ringBuffer{
		data: make([]float32, size),
		size: size,
	}
	rb.cond = sync.NewCond(&rb.mu)
	return rb
}

func (rb *ringBuffer) write(samples []float32) error {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.closed {
		return errors.New("ring buffer closed")
	}

	for _, sample := range samples {
		next := (rb.tail + 1) % rb.size
		if next == rb.head {
			rb.cond.Wait()
		}
		rb.data[rb.tail] = sample
		rb.tail = next
	}

	rb.cond.Signal()
	return nil
}

func (rb *ringBuffer) read(samples []float32) int {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.closed && rb.head == rb.tail {
		return 0
	}

	read := 0
	for read < len(samples) && rb.head != rb.tail {
		samples[read] = rb.data[rb.head]
		rb.head = (rb.head + 1) % rb.size
		read++
	}

	rb.cond.Signal()
	return read
}

func (rb *ringBuffer) close() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	rb.closed = true
	rb.cond.Broadcast()
}

type toolboxOutput struct {
	channels   int
	sampleRate int
	reader     librespot.Float32Reader
	audioQueue C.AudioQueueRef
	bufferSize int
	ringBuffer *ringBuffer
	context    *C.AudioContext
	paused     bool
	volume     float32
	err        chan error
	stopChan   chan struct{}
}

func newAudioToolboxOutput(reader librespot.Float32Reader, sampleRate, channels int, initialVolume float32) (*toolboxOutput, error) {
	out := &toolboxOutput{
		channels:   channels,
		sampleRate: sampleRate,
		reader:     reader,
		bufferSize: 2048,
		ringBuffer: newRingBuffer(8192),
		volume:     initialVolume,
		err:        make(chan error, 1),
		stopChan:   make(chan struct{}),
	}

	// We need the C.AudioContext to give the callback safe access to the output object
	log.Tracef("Allocating audio context")
	ctx := (*C.AudioContext)(C.malloc(C.size_t(unsafe.Sizeof(C.AudioContext{}))))
	if ctx == nil {
		return nil, errors.New("failed to allocate AudioContext")
	}
	ctx.output = unsafe.Pointer(out)

	// Create a new Audio Toolbox output
	log.Tracef("Configuring output")
	description := C.AudioStreamBasicDescription{
		mSampleRate:       C.double(out.sampleRate),
		mFormatID:         C.kAudioFormatLinearPCM,
		mFormatFlags:      C.kAudioFormatFlagIsFloat | C.kAudioFormatFlagIsPacked,
		mBytesPerPacket:   C.UInt32(4 * out.channels),
		mFramesPerPacket:  1,
		mBytesPerFrame:    C.UInt32(4 * out.channels),
		mChannelsPerFrame: C.UInt32(out.channels),
		mBitsPerChannel:   32,
	}
	err := C.AudioQueueNewOutput(
		&description,
		(C.AudioQueueOutputCallback)(C.audioCallback),
		unsafe.Pointer(ctx),
		0,
		0,
		0,
		&out.audioQueue,
	)
	if err != 0 {
		C.freeAudioContext(out.context)
		return nil, out.toolboxError("setupAudioQueue", err)
	}

	// Allocate Audio Toolbox buffers
	log.Tracef("Allocating audio buffer")
	for i := 0; i < 3; i++ {
		var buffer C.AudioQueueBufferRef
		status := C.AudioQueueAllocateBuffer(out.audioQueue, C.UInt32(out.bufferSize*4), &buffer)
		if status != C.noErr {
			return nil, out.toolboxError("allocateAudioQueue", err)
		}

		// Init buffer with silence
		C.memset(unsafe.Pointer(buffer.mAudioData), 0, C.size_t(out.bufferSize*4))
		buffer.mAudioDataByteSize = C.UInt32(out.bufferSize * 4)
		status = C.AudioQueueEnqueueBuffer(out.audioQueue, buffer, 0, nil)
		if status != C.noErr {
			return nil, out.toolboxError("enqueueAudioQueue", err)
		}
	}

	// Start the Audio Toolbox output
	log.Tracef("Starting audio queue")
	if err := C.AudioQueueStart(out.audioQueue, nil); err != 0 {
		return nil, out.toolboxError("startAudioQueue", err)
	}

	// Start subroutine which buffers audio samples to the ring buffer to play
	go func() {
		log.Tracef("start readLoop")
		out.err <- out.readLoop()
		log.Tracef("end readLoop")
		_ = out.Close()
	}()

	log.Info("Started audio-toolbox output")
	return out, nil
}

// Error handler - returns new error obj
func (out *toolboxOutput) toolboxError(name string, err C.int) error {
	if errors.Is(unix.Errno(-err), unix.EPIPE) {
		_ = out.Close()
	}
	return errors.New(fmt.Sprintf("%s: %d", name, err))
}

// Loop to fill the ring buffer with audio samples
func (out *toolboxOutput) readLoop() error {
	buffer := make([]float32, out.bufferSize)
	for {
		select {
		case <-out.stopChan:
			return nil
		default:
			n, err := out.reader.Read(buffer)
			if err == io.EOF {
				out.ringBuffer.close()
				return io.EOF
			} else if err != nil {
				out.err <- fmt.Errorf("error reading samples: %v", err)
				return err
			}
			if err := out.ringBuffer.write(buffer[:n]); err != nil {
				out.err <- err
				return err
			}
		}
	}
}

// Writes samples from the ring buffer into the output buffer
func (out *toolboxOutput) fillBuffer(buffer C.AudioQueueBufferRef) {
	data := make([]float32, out.bufferSize)
	n := out.ringBuffer.read(data)
	if n == 0 {
		// Underflow, fill with silence
		for i := 0; i < out.bufferSize; i++ {
			data[i] = 0
		}
	}

	C.memcpy(unsafe.Pointer(buffer.mAudioData), unsafe.Pointer(&data[0]), C.size_t(n*4))
	buffer.mAudioDataByteSize = C.UInt32(n * 4)

	status := C.AudioQueueEnqueueBuffer(out.audioQueue, buffer, 0, nil)
	if status != C.noErr {
		log.Errorf("error queuing samples for output: %v", status)
	}
}

//export audioCallback
func audioCallback(inUserData unsafe.Pointer, inAQ C.AudioQueueRef, inBuffer C.AudioQueueBufferRef) {
	ctx := (*C.AudioContext)(inUserData)
	out := (*toolboxOutput)(ctx.output)
	out.fillBuffer(inBuffer)
}

func (out *toolboxOutput) Pause() error {
	if out.paused {
		return nil
	}

	err := C.AudioQueuePause(out.audioQueue)
	if err != 0 {
		return out.toolboxError("pauseAudioQueue", err)
	}

	out.paused = true
	return nil
}

func (out *toolboxOutput) Resume() error {
	if !out.paused {
		return nil
	}

	err := C.AudioQueueStart(out.audioQueue, nil)
	if err != 0 {
		return out.toolboxError("resumeAudioQueue", err)
	}

	out.paused = false
	return nil
}

func (out *toolboxOutput) Drop() error {
	// Flush the audio queue to remove all pending buffers
	err := C.AudioQueueFlush(out.audioQueue)
	if err != 0 {
		return out.toolboxError("flushAudioQueue", err)
	}

	return nil
}

func (out *toolboxOutput) DelayMs() (int64, error) {
	// first get default audio output
	outputDeviceID := C.uint(C.kAudioObjectUnknown)
	size := C.uint(unsafe.Sizeof(outputDeviceID))

	propertyAddress := C.AudioObjectPropertyAddress{
		C.kAudioHardwarePropertyDefaultOutputDevice,
		C.kAudioObjectPropertyScopeGlobal,
		C.kAudioObjectPropertyElementMaster,
	}

	err := C.AudioObjectGetPropertyData(C.kAudioObjectSystemObject, &propertyAddress, 0, nil, &size, unsafe.Pointer(&outputDeviceID))
	if err != 0 {
		return 0, out.toolboxError("getDefaultOutput", err)
	}

	// after that, query the latency
	propertyAddress = C.AudioObjectPropertyAddress{
		C.kAudioDevicePropertyLatency,
		C.kAudioObjectPropertyScopeOutput,
		C.kAudioObjectPropertyElementMaster,
	}

	var latency uint32 = 0
	size = C.uint(unsafe.Sizeof(latency))
	err = C.AudioObjectGetPropertyData(outputDeviceID, &propertyAddress, 0, nil, &size, unsafe.Pointer(&latency))

	if err != 0 {
		return 0, out.toolboxError("getLatency", err)
	}

	return int64(latency*1000) / int64(out.sampleRate), nil
}

func (out *toolboxOutput) SetVolume(vol float32) {
	if vol < 0 || vol > 1 {
		panic(fmt.Sprintf("invalid volume value: %0.2f", vol))
	}
	C.AudioQueueSetParameter(out.audioQueue, C.kAudioQueueParam_Volume, C.Float32(vol))
	out.volume = vol
}

func (out *toolboxOutput) Error() <-chan error {
	return out.err
}

func (out *toolboxOutput) Close() error {

	// Stop the audio queue
	C.AudioQueueStop(out.audioQueue, C.Boolean(1))

	// Dispose of the audio queue
	if out.audioQueue != nil {
		C.AudioQueueDispose(out.audioQueue, C.Boolean(1))
	}

	if out.context != nil {
		C.freeAudioContext(out.context)
		out.context = nil
	}

	close(out.stopChan)
	out.ringBuffer.close()

	return nil
}
