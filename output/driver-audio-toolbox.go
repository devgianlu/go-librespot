//go:build darwin

package output

// #cgo LDFLAGS: -framework AudioToolbox -framework CoreAudio
// #include <AudioToolbox/AudioToolbox.h>
// #include <CoreAudio/CoreAudio.h>
// #include <stdlib.h>
// #include <stdint.h>
// extern void audioCallback(void * inUserData, AudioQueueRef inAQ,	AudioQueueBufferRef inBuffer);
//
// typedef struct {
//     uintptr_t output;
// } AudioContext;
//
// static AudioContext* allocateAudioContext() {
//     return (AudioContext*)malloc(sizeof(AudioContext));
// }
//
// static void freeAudioContext(AudioContext *ctx) {
//     free(ctx);
// }
import "C"
import (
	"errors"
	"fmt"
	"runtime/cgo"
	"sync/atomic"
	"unsafe"

	librespot "github.com/devgianlu/go-librespot"
	log "github.com/sirupsen/logrus"
)

type toolboxOutput struct {
	channels   int
	sampleRate int
	reader     librespot.Float32Reader
	audioQueue C.AudioQueueRef
	bufferSize int
	context    *C.AudioContext
	paused     bool
	volume     float32
	closing    atomic.Bool
	err        chan error
}

func newAudioToolboxOutput(opts *NewOutputOptions) (*toolboxOutput, error) {
	out := &toolboxOutput{
		channels:   opts.ChannelCount,
		sampleRate: opts.SampleRate,
		reader:     opts.Reader,
		bufferSize: 2048,
		volume:     opts.InitialVolume,
		err:        make(chan error, 1),
	}

	// AudioQueue retains this C allocation until disposal. Store an integer
	// handle, never a Go pointer: the output contains Go-managed references.
	log.Tracef("allocating audio context")
	ctx := C.allocateAudioContext()
	if ctx == nil {
		allocErr := errors.New("failed to allocate AudioContext")
		out.err <- allocErr
		return nil, allocErr
	}
	ctx.output = C.uintptr_t(cgo.NewHandle(out))
	out.context = ctx
	ready := false
	defer func() {
		if !ready {
			_ = out.Close()
		}
	}()

	// Create a new Audio Toolbox output
	log.Tracef("configuring output")
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
		return nil, out.toolboxError("setupAudioQueue", err)
	}

	// Allocate Audio Toolbox buffers
	log.Tracef("allocating audio buffer")
	for i := 0; i < 3; i++ {
		var buffer C.AudioQueueBufferRef
		status := C.AudioQueueAllocateBuffer(out.audioQueue, C.UInt32(out.bufferSize*4), &buffer)
		if status != C.noErr {
			return nil, out.toolboxError("allocateAudioQueue", status)
		}

		// Init buffer with silence
		C.memset(unsafe.Pointer(buffer.mAudioData), 0, C.size_t(out.bufferSize*4))
		buffer.mAudioDataByteSize = C.UInt32(out.bufferSize * 4)
		status = C.AudioQueueEnqueueBuffer(out.audioQueue, buffer, 0, nil)
		if status != C.noErr {
			return nil, out.toolboxError("enqueueAudioQueue", status)
		}
	}

	// The queue defaults to full volume. Apply the requested level before any
	// samples can be played, including when startup is explicitly muted.
	if status := C.AudioQueueSetParameter(out.audioQueue, C.kAudioQueueParam_Volume, C.Float32(opts.InitialVolume)); status != C.noErr {
		return nil, out.toolboxError("setInitialVolume", status)
	}

	// Start the Audio Toolbox output
	log.Tracef("starting audio queue")
	if err := C.AudioQueueStart(out.audioQueue, nil); err != 0 {
		return nil, out.toolboxError("startAudioQueue", err)
	}

	log.Info("started audio-toolbox output")
	ready = true
	return out, nil
}

// Error handler - returns new error obj
func (out *toolboxOutput) toolboxError(name string, err C.int) error {
	result := fmt.Errorf("%s: %d", name, err)
	// Do not block the audio callback or dispose its own queue from inside it.
	select {
	case out.err <- result:
	default:
	}
	return result
}

// Gets samples from the reader and writes them to the output buffer
func (out *toolboxOutput) bufferSamples(buffer C.AudioQueueBufferRef) {
	if out.closing.Load() {
		return
	}
	data := make([]float32, out.bufferSize)
	n, err := out.reader.Read(data)
	if out.closing.Load() {
		return
	}
	if err != nil {
		select {
		case out.err <- fmt.Errorf("error reading samples: %w", err):
		default:
		}
		return
	}

	C.memcpy(unsafe.Pointer(buffer.mAudioData), unsafe.Pointer(&data[0]), C.size_t(n*4))
	buffer.mAudioDataByteSize = C.UInt32(n * 4)

	status := C.AudioQueueEnqueueBuffer(out.audioQueue, buffer, 0, nil)
	if status != C.noErr && !out.closing.Load() {
		log.Errorf("error queuing samples for output: %v", status)
	}
}

//export audioCallback
func audioCallback(inUserData unsafe.Pointer, inAQ C.AudioQueueRef, inBuffer C.AudioQueueBufferRef) {
	ctx := (*C.AudioContext)(inUserData)
	out := cgo.Handle(ctx.output).Value().(*toolboxOutput)
	out.bufferSamples(inBuffer)
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
	out.closing.Store(true)
	// Immediate disposal waits for callbacks to finish before their context
	// and handle can be released. Also covers partially constructed outputs.
	if out.audioQueue != nil {
		if status := C.AudioQueueDispose(out.audioQueue, C.Boolean(1)); status != C.noErr {
			return out.toolboxError("disposeAudioQueue", status)
		}
		out.audioQueue = nil
	}

	if out.context != nil {
		cgo.Handle(out.context.output).Delete()
		C.freeAudioContext(out.context)
		out.context = nil
	}

	return nil
}
