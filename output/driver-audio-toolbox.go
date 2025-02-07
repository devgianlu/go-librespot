//go:build darwin

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
	"unsafe"

	librespot "github.com/devgianlu/go-librespot"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
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
	err        chan error
	stopChan   chan struct{}
}

func newAudioToolboxOutput(reader librespot.Float32Reader, sampleRate, channels int, initialVolume float32) (*toolboxOutput, error) {
	out := &toolboxOutput{
		channels:   channels,
		sampleRate: sampleRate,
		reader:     reader,
		bufferSize: 2048,
		volume:     initialVolume,
		err:        make(chan error, 1),
		stopChan:   make(chan struct{}),
	}

	// We need the C.AudioContext to give the callback safe access to the output context
	log.Tracef("Allocating audio context")
	ctx := (*C.AudioContext)(C.malloc(C.size_t(unsafe.Sizeof(C.AudioContext{}))))
	if ctx == nil {
		out.err <- errors.New("failed to allocate AudioContext")
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

	log.Info("Started audio-toolbox output")
	return out, nil
}

// Error handler - returns new error obj
func (out *toolboxOutput) toolboxError(name string, err C.int) error {
	if errors.Is(unix.Errno(-err), unix.EPIPE) {
		_ = out.Close()
	}
	out.err <- fmt.Errorf("%s: %d", name, err)
	return fmt.Errorf("%s: %d", name, err)
}

// Gets samples from the reader and writes them to the output buffer
func (out *toolboxOutput) bufferSamples(buffer C.AudioQueueBufferRef) {
	data := make([]float32, out.bufferSize)
	n, err := out.reader.Read(data)
	if err != nil {
		out.err <- fmt.Errorf("error reading samples: %v", err)
		return
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

	return nil
}
