package output

import (
	librespot "go-librespot"
)

type Output struct {
	*output
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

	// InitiallyPaused specifies whether the output device should be paused from the start.
	InitiallyPaused bool
}

func NewOutput(options *NewOutputOptions) (*Output, error) {
	out, err := newOutput(options.Reader, options.SampleRate, options.ChannelCount, options.Device, options.InitiallyPaused)
	if err != nil {
		return nil, err
	}

	return &Output{out}, nil
}

// Pause pauses the output.
func (c *Output) Pause() error {
	return c.output.Pause()
}

// Resume resumes the output.
func (c *Output) Resume() error {
	return c.output.Resume()
}

// Drop empties the audio buffer without waiting.
func (c *Output) Drop() error {
	return c.output.Drop()
}

// DelayMs returns the output device delay in milliseconds.
func (c *Output) DelayMs() (int64, error) {
	return c.output.DelayMs()
}

// SetVolume sets the volume (0-1).
func (c *Output) SetVolume(vol float32) {
	c.output.SetVolume(vol)
}

// WaitDone waits for the playback loop to exit.
func (c *Output) WaitDone() <-chan error {
	return c.output.WaitDone()
}

// IsEOF returns whether the reader reached EOF.
func (c *Output) IsEOF() bool {
	return c.output.IsEOF()
}

// Close closes the output.
func (c *Output) Close() error {
	return c.output.Close()
}
