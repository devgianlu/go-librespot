package output

import (
	"fmt"
	"io"
	"sync"
	"time"

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
	clientopts := []pulse.ClientOption{
		pulse.ClientApplicationName("go-librespot"),
		pulse.ClientApplicationIconName("speaker"),
		// A drain can legitimately outlast the default one-second RPC timeout.
		pulse.ClientTimeout(10 * time.Second),
	}

	if opts.RuntimeSocket != "" {
		clientopts = append(clientopts, pulse.ClientServerString(opts.RuntimeSocket))
	}

	client, err := pulse.NewClient(clientopts...)
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
	switch opts.ChannelCount {
	case 1:
		channelOpt = pulse.PlaybackMono
	case 2:
		channelOpt = pulse.PlaybackStereo
	default:
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
	return out.Drop()
}

func (out *pulseAudioOutput) Resume() error {
	// Start the stream. This will start reading samples from out.reader and
	// push it to PulseAudio. It will do nothing if the playback is already
	// started.
	out.stream.Start()
	return nil
}

func (out *pulseAudioOutput) Drop() error {
	// The reader may already be idle at EOF with audio still queued on
	// the server. Explicit transport commands discard that audio immediately.
	out.stream.Stop()
	if err := out.client.RawRequest(&proto.FlushPlaybackStream{StreamIndex: out.stream.StreamIndex()}, nil); err != nil {
		return fmt.Errorf("flush PulseAudio playback: %w", err)
	}
	return nil
}

// Drain waits for the samples submitted before EOF to play. The pulse
// reader can already be idle here, in which case PlaybackStream.Drain skips
// the server request.
func (out *pulseAudioOutput) Drain() {
	err := out.client.RawRequest(&proto.DrainPlaybackStream{StreamIndex: out.stream.StreamIndex()}, nil)
	if err != nil {
		out.log.WithError(err).Warn("could not drain PulseAudio playback before advancing")
	}
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
