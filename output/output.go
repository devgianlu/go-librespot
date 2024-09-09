package output

import (
	librespot "github.com/devgianlu/go-librespot"
)

type Output interface {
	// Pause pauses the output.
	Pause() error

	// Resume resumes the output.
	Resume() error

	// Drop empties the audio buffer without waiting.
	Drop() error

	// DelayMs returns the output device delay in milliseconds.
	DelayMs() (int64, error)

	// SetVolume sets the volume (0-1).
	SetVolume(vol float32)

	// Error returns the error that stopped the device (if any).
	Error() <-chan error

	// Close closes the output.
	Close() error
}

type NewOutputOptions struct {
	// Reader provides data for the output device.
	//
	// The format of data is as follows:
	//
	//	[data]      = [sample 1] [sample 2] [sample 3] ...
	//	[sample *]  = [channel 1] [channel 2] ...
	//	[channel *] = [byte 1] [byte 2] ...
	//
	// Byte ordering is little endian.
	Reader librespot.Float32Reader

	// SampleRate specifies the number of samples that should be played during one second.
	// Usual numbers are 44100 or 48000. One context has only one sample rate. You cannot play multiple audio
	// sources with different sample rates at the same time.
	SampleRate int

	// ChannelCount specifies the number of channels. One channel is mono playback. Two
	// channels are stereo playback. No other values are supported.
	ChannelCount int

	// Device specifies the audio device name.
	//
	// This feature is support only for the unix driver.
	Device string

	// Mixer specifies the audio mixer name.
	//
	// This feature is support only for the unix driver.
	Mixer string
	// Control specifies the mixer control name
	//
	// This only works in combination with Mixer
	Control string

	// InitialVolume specifies the initial output volume.
	InitialVolume float32

	// ExternalVolume specifies, if the volume is controlled outside of the app.
	ExternalVolume bool

	ExternalVolumeUpdate *RingBuffer[float32]
}

func NewOutput(options *NewOutputOptions) (Output, error) {
	out, err := newAlsaOutput(options.Reader, options.SampleRate, options.ChannelCount, options.Device, options.Mixer, options.Control, options.InitialVolume, options.ExternalVolume, options.ExternalVolumeUpdate)
	if err != nil {
		return nil, err
	}

	return out, nil
}
