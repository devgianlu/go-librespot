package output

import (
	"encoding/binary"
	"errors"
	log "github.com/sirupsen/logrus"

	"math"
	"sync"

	"github.com/gen2brain/malgo"
	librespot "go-librespot"
)

var ErrDrainReader = errors.New("please drain output")

type Output struct {
	context      *malgo.AllocatedContext
	device       *malgo.Device
	reader       librespot.Float32Reader
	sampleRate   int
	channelCount int
	volume       float32
	mu           sync.Mutex
}

type NewOutputOptions struct {
	Reader               librespot.Float32Reader
	SampleRate           int
	ChannelCount         int
	Device               string
	Mixer                string
	Control              string
	InitialVolume        float32
	ExternalVolume       bool
	ExternalVolumeUpdate RingBuffer[float32]
}

func NewOutput(options *NewOutputOptions) (*Output, error) {
	log.Println("Initializing context...")
	context, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		log.Println(message)
	})
	if err != nil {
		return nil, err
	}
	log.Println("Context initialized successfully.")

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Playback.Channels = uint32(options.ChannelCount)
	deviceConfig.SampleRate = uint32(options.SampleRate)
	deviceConfig.Alsa.NoMMap = 1 // Fix for ALSA if applicable

	output := &Output{
		context:      context,
		reader:       options.Reader,
		sampleRate:   options.SampleRate,
		channelCount: options.ChannelCount,
		volume:       options.InitialVolume,
	}

	deviceCallbacks := malgo.DeviceCallbacks{
		Data: func(outputSamples, inputSamples []byte, frameCount uint32) {
			output.onSamples(outputSamples, frameCount)
		},
	}

	log.Println("Initializing device...")
	device, err := malgo.InitDevice(context.Context, deviceConfig, deviceCallbacks)
	if err != nil {
		return nil, err
	}
	log.Println("Device initialized successfully.")

	output.device = device

	log.Println("Starting device...")
	err = device.Start()
	if err != nil {
		return nil, err
	}
	log.Println("Device started successfully.")

	return output, nil
}

func (o *Output) onSamples(outputBuffer []byte, frameCount uint32) {
	log.Println("onSamples called")
	floatBuffer := make([]float32, frameCount*uint32(o.channelCount))
	n, err := o.reader.Read(floatBuffer)
	if err != nil && err != ErrDrainReader {
		log.Println("Error reading from reader:", err)
		// Handle error, e.g., by stopping playback
		o.device.Stop()
		return
	}

	log.Printf("Read %d float32 samples from reader\n", n)
	byteBuffer := make([]byte, n*4)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint32(byteBuffer[i*4:], math.Float32bits(floatBuffer[i]))
	}

	copy(outputBuffer, byteBuffer)

	// Log some of the audio data for verification
	if len(byteBuffer) >= 16 {
		log.Printf("Audio data sample: %v\n", byteBuffer[:16])
	} else {
		log.Printf("Audio data sample: %v\n", byteBuffer)
	}

	if err == ErrDrainReader {
		o.reader.Drained()
	}
}

func (o *Output) Start() error {
	log.Println("Starting output device...")
	return o.device.Start()
}

func (o *Output) Stop() error {
	log.Println("Stopping output device...")
	return o.device.Stop()
}

func (o *Output) Close() error {
	log.Println("Closing output device...")
	o.device.Uninit()
	o.context.Free()
	return nil
}

type RingBuffer[T any] struct {
	inner chan T
}

func NewRingBuffer[T any](capacity uint64) RingBuffer[T] {
	return RingBuffer[T]{
		inner: make(chan T, capacity),
	}
}

func (b RingBuffer[T]) Put(val T) {
	if len(b.inner) == cap(b.inner) {
		_ = <-b.inner
	}
	b.inner <- val
}

func (b RingBuffer[T]) Get() (T, bool) {
	v, ok := <-b.inner
	return v, ok
}

// Pause pauses the output.
func (o *Output) Pause() error {
	log.Println("Pausing output device...")
	return o.device.Stop()
}

// Resume resumes the output.
func (o *Output) Resume() error {
	log.Println("Resuming output device...")
	return o.device.Start()
}

// Drop empties the audio buffer without waiting.
func (o *Output) Drop() error {
	log.Println("Dropping output buffer...")
	// Implement drop functionality if needed
	return nil
}

// DelayMs returns the output device delay in milliseconds.
func (o *Output) DelayMs() (int64, error) {
	// Implement delay calculation if needed
	return 0, nil
}

// SetVolume sets the volume (0-1).
func (o *Output) SetVolume(vol float32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.volume = vol
	log.Printf("Volume set to %f\n", vol)
	// Implement volume setting if possible
}

// Error returns the error that stopped the device (if any).
func (o *Output) Error() <-chan error {
	errCh := make(chan error)
	go func() {
		// Send errors if any
		close(errCh)
	}()
	return errCh
}

