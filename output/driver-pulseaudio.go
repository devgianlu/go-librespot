package output

import (
	"fmt"
	"io"
	"sync"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
)

type pulseAudioOutput struct {
	log librespot.Logger

	sampleRate           int
	reader               librespot.Float32Reader
	client               *pulse.Client
	stream               *pulse.PlaybackStream
	volume               proto.Volume
	volumeLock           sync.Mutex
	externalVolumeUpdate chan float32
	err                  chan error
}

func newPulseAudioOutput(opts *NewOutputOptions) (*pulseAudioOutput, error) {
	// Initialize the PulseAudio client.
	// The device name is shown by PulseAudio volume controls (usually built
	// into a desktop environment), so we might want to use device_name here.
	// We could also maybe change the application icon name by device_type.
	client, err := pulse.NewClient(pulse.ClientApplicationName("go-librespot"), pulse.ClientApplicationIconName("speaker"))
	if err != nil {
		return nil, err
	}
	out := &pulseAudioOutput{
		log:                  opts.Log,
		sampleRate:           opts.SampleRate,
		reader:               opts.Reader,
		client:               client,
		externalVolumeUpdate: opts.VolumeUpdate,
		err:                  make(chan error, 2),
	}

	// Create a new playback.
	var channelOpt pulse.PlaybackOption
	if opts.ChannelCount == 1 {
		channelOpt = pulse.PlaybackMono
	} else if opts.ChannelCount == 2 {
		channelOpt = pulse.PlaybackStereo
	} else {
		return nil, fmt.Errorf("cannot play %d channels, pulse only supports mono and stereo", opts.ChannelCount)
	}
	volumeUpdates := make(chan proto.ChannelVolumes, 1)
	lplaybackopts := []pulse.PlaybackOption{
		pulse.PlaybackSampleRate(out.sampleRate),
		pulse.PlaybackVolumeChanges(volumeUpdates),
		channelOpt,
	}

	if opts.Device != "" {
		var lsink *pulse.Sink
		if opts.Device == "default" {
			lsink, err = client.DefaultSink()
		} else {
			lsink, err = client.SinkByID(opts.Device)
		}

		if err != nil {
			client.Close()
			return nil, fmt.Errorf("cannot find pulseaudio sink %s: %w", opts.Device, err)
		}

		lplaybackopts = append(lplaybackopts, pulse.PlaybackSink(lsink))
	}

	out.stream, err = out.client.NewPlayback(pulse.Float32Reader(out.float32Reader), lplaybackopts...)
	if err != nil {
		return nil, err
	}

	// Read the initial volume from PulseAudio.
	// PulseAudio strongly recommends against setting a default volume at
	// startup (especially if it's 100%), so instead we just follow the
	// PulseAudio provided volume.
	cvol, _ := out.stream.Volume()
	out.volume = cvol.Avg()
	sendVolumeUpdate(opts.VolumeUpdate, float32(out.volume.Norm()))

	// Listen for volume changes (through the volume mixer application, usually
	// built into the desktop environment), and send them back to Spotify.
	go func() {
		for cvol := range volumeUpdates {
			volume := cvol.Avg()

			out.volumeLock.Lock()
			if volume != out.volume {
				sendVolumeUpdate(opts.VolumeUpdate, float32(volume.Norm()))
				out.volume = volume
			}
			out.volumeLock.Unlock()
		}
	}()

	return out, nil
}

func (out *pulseAudioOutput) float32Reader(buf []float32) (int, error) {
	n, err := out.reader.Read(buf)
	if err != nil {
		if err == io.EOF {
			// Might happen, so translate this error message.
			return n, pulse.EndOfData
		}

		// Encountered another error. This will result in a stopped player, so
		// send the error back to the player using a non-blocking send.
		select {
		case out.err <- err:
		default:
		}
		return n, err
	}
	return n, err
}

func (out *pulseAudioOutput) Pause() error {
	if out.stream.Running() {
		// Stop() will stop new samples from being requested, but will continue
		// to play whatever is in the buffer.
		out.stream.Stop()

		// To really stop playback *now*, we have to also flush everything
		// that's in the buffer.
		err := out.client.RawRequest(&proto.FlushPlaybackStream{
			StreamIndex: out.stream.StreamIndex(),
		}, nil)
		if err != nil {
			return fmt.Errorf("Pause: could not flush playback: %e", err)
		}
	} else {
		// Nothing to do: we're already paused.
	}

	return nil
}

func (out *pulseAudioOutput) Resume() error {
	// Start the stream. This will start reading samples from out.reader and
	// push it to PulseAudio. It will do nothing if the playback is already
	// started.
	out.stream.Start()
	return nil
}

func (out *pulseAudioOutput) Drop() error {
	if out.stream.Running() {
		// Drop all samples while running. This happens when seeking.
		// So we stop playback, flush the buffer, and restart it again to clear
		// what's in the buffer. Presumably, all new samples from this point on
		// are the new samples (isn't there a race condition here with
		// SwitchingAudioSource?).
		out.stream.Stop()
		err := out.client.RawRequest(&proto.FlushPlaybackStream{
			StreamIndex: out.stream.StreamIndex(),
		}, nil)
		if err != nil {
			return fmt.Errorf("Drop: could not flush playback: %e", err)
		}
		out.stream.Start()
	} else {
		// This sometimes happens. But we don't need to do anything: we already
		// flushed the buffer in Pause().
	}
	return nil
}

func (out *pulseAudioOutput) DelayMs() (int64, error) {
	samples := out.stream.BufferSize()
	delay := int64(samples) * 1000 / int64(out.sampleRate)
	return delay, nil
}

func (out *pulseAudioOutput) SetVolume(vol float32) {
	volume := proto.NormVolume(float64(vol))

	out.volumeLock.Lock()
	if volume == out.volume {
		out.volumeLock.Unlock()
		return
	}
	out.volume = volume
	sendVolumeUpdate(out.externalVolumeUpdate, vol)
	out.volumeLock.Unlock()

	cvol := proto.ChannelVolumes{volume}
	err := out.stream.SetVolume(cvol)
	if err != nil {
		out.log.WithError(err).Warn("failed to set volume")
	}
}

func (out *pulseAudioOutput) Error() <-chan error {
	return out.err
}

func (out *pulseAudioOutput) Close() error {
	out.stream.Close()
	out.client.Close()
	return nil
}
