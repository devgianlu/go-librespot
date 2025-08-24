package player

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/devgianlu/go-librespot/output"
	downloadpb "github.com/devgianlu/go-librespot/proto/spotify/download"
	extmetadatapb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/devgianlu/go-librespot/spclient"
	"github.com/devgianlu/go-librespot/vorbis"
	"golang.org/x/exp/rand"
)

const (
	SampleRate = 44100
	Channels   = 2
)

const MaxStateVolume = 65535

const DisableCheckMediaRestricted = true

const CdnUrlQuarantineDuration = 15 * time.Minute

type Player struct {
	log librespot.Logger

	normalisationEnabled      bool
	normalisationUseAlbumGain bool
	normalisationPregain      float32
	countryCode               *string

	sp       *spclient.Spclient
	audioKey *audio.KeyProvider
	events   EventManager

	cdnQuarantine map[string]time.Time

	newOutput func(source librespot.Float32Reader, volume float32) (output.Output, error)

	cmd chan playerCmd
	ev  chan Event

	volumeSteps uint32

	startedPlaying time.Time
}

type playerCmdType int

const (
	playerCmdSet playerCmdType = iota
	playerCmdPlay
	playerCmdPause
	playerCmdStop
	playerCmdSeek
	playerCmdPosition
	playerCmdVolume
	playerCmdClose
)

type playerCmd struct {
	typ  playerCmdType
	data any
	resp chan any
}

type playerCmdDataSet struct {
	source  librespot.AudioSource
	primary bool
	paused  bool
	drop    bool
}

type MetadataCallback interface {
	UpdateTrack(title, artist, album, trackID string, duration time.Duration, playing bool)
	UpdatePosition(position time.Duration)
	UpdateVolume(volume int)
	UpdatePlayingState(playing bool)
}

type Options struct {
	Spclient *spclient.Spclient
	AudioKey *audio.KeyProvider
	Events   EventManager

	Log librespot.Logger

	// NormalisationEnabled specifies if the volume should be normalised according
	// to Spotify parameters. Only track normalization is supported.
	NormalisationEnabled bool
	// NormalisationUseAlbumGain specifies whether album gain instead of track gain
	// should be used for normalisation
	NormalisationUseAlbumGain bool
	// NormalisationPregain specifies the pre-gain to apply when normalising the volume
	// in dB. Use negative values to avoid clipping.
	NormalisationPregain float32

	// CountryCode specifies the country code to use for media restrictions.
	CountryCode *string

	// AudioBackend specifies the audio backend to use (alsa, pulseaudio, etc).
	AudioBackend string
	// AudioDevice specifies the audio device name.
	//
	// This feature is support only for the alsa backend.
	AudioDevice string
	// MixerDevice specifies the audio mixer name.
	//
	// This feature is support only for the alsa backend.
	MixerDevice string
	// MixerControlName specifies the mixer control name.
	//
	// This only works in combination with Mixer.
	MixerControlName string

	// AudioBufferTime is the buffer time in microseconds.
	//
	// This is only supported on the alsa backend.
	AudioBufferTime int
	// AudioPeriodCount is the number of periods to request.
	//
	// This is only supported on the alsa backend.
	AudioPeriodCount int

	// ExternalVolume specifies, if the volume is controlled outside the app.
	//
	// This is only supported on the alsa and pipe backends.
	// The PulseAudio backend always uses external volume.
	ExternalVolume bool

	// VolumeUpdate is a channel on which volume updates will be sent back to
	// Spotify. This must be a buffered channel.
	VolumeUpdate chan float32

	// AudioOutputPipe is the path to the output pipe.
	//
	// This is only supported on the pipe backend.
	AudioOutputPipe string

	// AudioOutputPipeFormat is the format of the output pipe.
	// Available formats are: "s16le", "s32le", "f32le". Default is "s16le".
	//
	// This is only supported on the pipe backend.
	AudioOutputPipeFormat string
	MetadataCallback      MetadataCallback
}

func NewPlayer(opts *Options) (*Player, error) {
	p := &Player{
		log:                       opts.Log,
		sp:                        opts.Spclient,
		audioKey:                  opts.AudioKey,
		events:                    opts.Events,
		cdnQuarantine:             make(map[string]time.Time),
		normalisationEnabled:      opts.NormalisationEnabled,
		normalisationUseAlbumGain: opts.NormalisationUseAlbumGain,
		normalisationPregain:      opts.NormalisationPregain,
		countryCode:               opts.CountryCode,
		newOutput: func(reader librespot.Float32Reader, volume float32) (output.Output, error) {
			return output.NewOutput(&output.NewOutputOptions{
				Log:              opts.Log,
				Backend:          opts.AudioBackend,
				Reader:           reader,
				SampleRate:       SampleRate,
				ChannelCount:     Channels,
				Device:           opts.AudioDevice,
				Mixer:            opts.MixerDevice,
				Control:          opts.MixerControlName,
				InitialVolume:    volume,
				BufferTimeMicro:  opts.AudioBufferTime,
				PeriodCount:      opts.AudioPeriodCount,
				ExternalVolume:   opts.ExternalVolume,
				VolumeUpdate:     opts.VolumeUpdate,
				OutputPipe:       opts.AudioOutputPipe,
				OutputPipeFormat: opts.AudioOutputPipeFormat,
			})
		},

		cmd: make(chan playerCmd),
		ev:  make(chan Event, 128),
	}

	go p.manageLoop()

	return p, nil
}

func (p *Player) manageLoop() {
	// currently available output device
	var out output.Output
	outErr := make(<-chan error)

	// initial volume is 1
	volume := float32(1)

	// init main source
	source := NewSwitchingAudioSource()

loop:
	for {
		select {
		case cmd := <-p.cmd:
			switch cmd.typ {
			case playerCmdSet:
				data := cmd.data.(playerCmdDataSet)
				if !data.primary {
					source.SetSecondary(data.source)
					cmd.resp <- nil
					break
				}

				// create a new output device if needed
				if out == nil {
					var err error
					out, err = p.newOutput(source, volume)
					if err != nil {
						cmd.resp <- err
						break
					}

					outErr = out.Error()
					p.log.Debugf("created new output device")
				}

				// set source
				source.SetPrimary(data.source)
				if data.paused {
					if err := out.Pause(); err != nil {
						cmd.resp <- err
						break
					}
				} else {
					if err := out.Resume(); err != nil {
						cmd.resp <- err
						break
					}
				}

				if data.drop {
					_ = out.Drop()
				}

				p.startedPlaying = time.Now()
				cmd.resp <- nil

				if data.paused {
					p.ev <- Event{Type: EventTypePause}
				} else {
					p.ev <- Event{Type: EventTypePlay}
				}
			case playerCmdPlay:
				if out != nil {
					if err := out.Resume(); err != nil {
						cmd.resp <- err
					} else {
						cmd.resp <- nil
						p.ev <- Event{Type: EventTypeResume}
					}
				} else {
					cmd.resp <- nil
				}
			case playerCmdPause:
				if out != nil {
					if err := out.Pause(); err != nil {
						cmd.resp <- err
					} else {
						cmd.resp <- nil
						p.ev <- Event{Type: EventTypePause}
					}
				} else {
					cmd.resp <- nil
				}
			case playerCmdStop:
				if out != nil {
					_ = out.Close()
					out = nil
					outErr = make(<-chan error)

					p.log.Tracef("closed output device because of stop command")
				}

				cmd.resp <- struct{}{}
				p.ev <- Event{Type: EventTypeStop}
			case playerCmdSeek:
				if out != nil {
					if err := source.SetPositionMs(cmd.data.(int64)); err != nil {
						cmd.resp <- err
					} else if err = out.Drop(); err != nil {
						cmd.resp <- err
					} else {
						cmd.resp <- nil
					}
				} else {
					cmd.resp <- nil
				}
			case playerCmdPosition:
				pos := source.PositionMs()
				if out != nil {
					delay, err := out.DelayMs()
					if err != nil {
						p.log.WithError(err).Warnf("failed getting output device delay")
						delay = 0
					}

					cmd.resp <- pos - delay
				} else {
					cmd.resp <- pos
				}
			case playerCmdVolume:
				volume = cmd.data.(float32)
				if out != nil {
					out.SetVolume(volume)
				}
			case playerCmdClose:
				break loop
			default:
				panic("unknown player command")
			}
		case err := <-outErr:
			if err != nil {
				p.log.WithError(err).Errorf("output device failed")
			}

			// the current output device has exited, clean it up
			_ = out.Close()
			out = nil
			outErr = make(<-chan error)

			p.log.Tracef("cleared closed output device")

			// FIXME: this is called even if not needed, like when autoplay starts
			p.ev <- Event{Type: EventTypeStop}
		case <-source.Done():
			p.ev <- Event{Type: EventTypeNotPlaying}
		}
	}

	close(p.cmd)

	_ = source.Close()

	if out != nil {
		_ = out.Close()
		out = nil
	}
}

func (p *Player) HasBeenPlayingFor() time.Duration {
	if p.startedPlaying.IsZero() {
		return 0
	}

	return time.Since(p.startedPlaying)
}

func (p *Player) Receive() <-chan Event {
	return p.ev
}

func (p *Player) Close() {
	p.cmd <- playerCmd{typ: playerCmdClose}
}

func (p *Player) SetVolume(val uint32) {
	vol := float32(val) / MaxStateVolume
	p.cmd <- playerCmd{typ: playerCmdVolume, data: vol}
}

func (p *Player) Play() error {
	resp := make(chan any, 1)
	p.cmd <- playerCmd{typ: playerCmdPlay, resp: resp}
	if err := <-resp; err != nil {
		return err.(error)
	}

	return nil
}

func (p *Player) Pause() error {
	resp := make(chan any, 1)
	p.cmd <- playerCmd{typ: playerCmdPause, resp: resp}
	if err := <-resp; err != nil {
		return err.(error)
	}

	return nil
}

func (p *Player) Stop() {
	resp := make(chan any, 1)
	p.cmd <- playerCmd{typ: playerCmdStop, resp: resp}
	<-resp
}

func (p *Player) SeekMs(pos int64) error {
	resp := make(chan any, 1)
	p.cmd <- playerCmd{typ: playerCmdSeek, data: pos, resp: resp}
	if err := <-resp; err != nil {
		return err.(error)
	}

	return nil
}

func (p *Player) PositionMs() int64 {
	resp := make(chan any, 1)
	p.cmd <- playerCmd{typ: playerCmdPosition, resp: resp}
	pos := <-resp
	return pos.(int64)
}

func (p *Player) SetPrimaryStream(source librespot.AudioSource, paused, drop bool) error {
	resp := make(chan any)
	p.cmd <- playerCmd{typ: playerCmdSet, data: playerCmdDataSet{source: source, primary: true, paused: paused, drop: drop}, resp: resp}
	if err := <-resp; err != nil {
		return err.(error)
	}

	return nil
}

func (p *Player) SetSecondaryStream(source librespot.AudioSource) {
	resp := make(chan any)
	p.cmd <- playerCmd{typ: playerCmdSet, data: playerCmdDataSet{source: source, primary: false}, resp: resp}
	<-resp
}

func (p *Player) httpChunkedReaderFromStorageResolve(log librespot.Logger, client *http.Client, storageResolve *downloadpb.StorageResolveResponse) (*audio.HttpChunkedReader, error) {
	if storageResolve.Result == downloadpb.StorageResolveResponse_STORAGE {
		return nil, fmt.Errorf("old storage not supported")
	} else if storageResolve.Result == downloadpb.StorageResolveResponse_RESTRICTED {
		return nil, fmt.Errorf("storage is restricted")
	} else if storageResolve.Result == downloadpb.StorageResolveResponse_CDN {
		if len(storageResolve.Cdnurl) == 0 {
			return nil, fmt.Errorf("no cdn urls")
		}

		log.Tracef("found %d cdn urls", len(storageResolve.Cdnurl))

		var err error
		for i := 0; i < len(storageResolve.Cdnurl); i++ {
			var cdnUrl *url.URL
			cdnUrl, err = url.Parse(storageResolve.Cdnurl[i])
			if err != nil {
				log.WithError(err).WithField("url", storageResolve.Cdnurl[i]).Warnf("failed parsing cdn url, trying next url")
				continue
			}

			if lastFailed, found := p.cdnQuarantine[cdnUrl.Host]; found {
				if i == len(storageResolve.Cdnurl)-1 {
					log.WithField("host", cdnUrl.Host).Warnf("cannot skip cdn url because it is the last one")
				} else if time.Since(lastFailed) < CdnUrlQuarantineDuration {
					log.WithField("host", cdnUrl.Host).Infof("skipping cdn url because it has failed recently")
					continue
				}
			}

			var rawStream *audio.HttpChunkedReader
			rawStream, err = audio.NewHttpChunkedReader(log, client, cdnUrl.String())
			if err != nil {
				log.WithError(err).WithField("host", cdnUrl.Host).Warnf("failed creating chunked reader, trying next url")
				p.cdnQuarantine[cdnUrl.Host] = time.Now()
				continue
			}

			delete(p.cdnQuarantine, cdnUrl.Host)
			return rawStream, nil
		}

		return nil, fmt.Errorf("failed creating chunked reader for any cdn url: %w", err)
	} else {
		return nil, fmt.Errorf("unknown storage resolve result: %s", storageResolve.Result)
	}
}

func (p *Player) NewStream(ctx context.Context, client *http.Client, spotId librespot.SpotifyId, bitrate int, mediaPosition int64) (*Stream, error) {
	log := p.log.WithField("uri", spotId.Uri())

	playbackId := make([]byte, 16)
	_, _ = rand.Read(playbackId)

	p.events.PreStreamLoadNew(playbackId, spotId, mediaPosition)

	var media *librespot.Media
	var file *metadatapb.AudioFile
	if spotId.Type() == librespot.SpotifyIdTypeTrack {
		var trackMeta metadatapb.Track
		err := p.sp.ExtendedMetadataSimple(ctx, spotId, extmetadatapb.ExtensionKind_TRACK_V4, &trackMeta)
		if err != nil {
			return nil, fmt.Errorf("failed getting track metadata: %w", err)
		}

		media = librespot.NewMediaFromTrack(&trackMeta)
		if !DisableCheckMediaRestricted && isMediaRestricted(media, *p.countryCode) {
			return nil, librespot.ErrMediaRestricted
		}

		if len(trackMeta.File) == 0 {
			for _, alt := range trackMeta.Alternative {
				if len(alt.File) == 0 {
					continue
				}

				trackMeta.File = append(trackMeta.File, alt.File...)
			}

			log.Warnf("original track has no formats, alternatives have a total of %d", len(trackMeta.File))
		}

		file = selectBestMediaFormat(trackMeta.File, bitrate)
		if file == nil {
			return nil, librespot.ErrNoSupportedFormats
		}
	} else if spotId.Type() == librespot.SpotifyIdTypeEpisode {
		var episodeMeta metadatapb.Episode
		err := p.sp.ExtendedMetadataSimple(ctx, spotId, extmetadatapb.ExtensionKind_EPISODE_V4, &episodeMeta)
		if err != nil {
			return nil, fmt.Errorf("failed getting episode metadata: %w", err)
		}

		media = librespot.NewMediaFromEpisode(&episodeMeta)
		if !DisableCheckMediaRestricted && isMediaRestricted(media, *p.countryCode) {
			return nil, librespot.ErrMediaRestricted
		}

		file = selectBestMediaFormat(episodeMeta.Audio, bitrate)
		if file == nil {
			return nil, librespot.ErrNoSupportedFormats
		}
	} else {
		return nil, fmt.Errorf("unsupported spotify type: %s", spotId.Type())
	}

	p.events.PostStreamResolveAudioFile(playbackId, int32(bitrate), media, file)

	log.Debugf("selected format %s (%x)", file.Format.String(), file.FileId)

	audioKey, err := p.audioKey.Request(ctx, spotId.Id(), file.FileId)
	if err != nil {
		return nil, fmt.Errorf("failed retrieving audio key: %w", err)
	}

	p.events.PostStreamRequestAudioKey(playbackId)

	storageResolve, err := p.sp.ResolveStorageInteractive(ctx, file.FileId, false)
	if err != nil {
		return nil, fmt.Errorf("failed resolving track storage: %w", err)
	}

	p.events.PostStreamResolveStorage(playbackId)

	rawStream, err := p.httpChunkedReaderFromStorageResolve(log, client, storageResolve)
	if err != nil {
		return nil, fmt.Errorf("failed creating chunked reader: %w", err)
	}

	p.events.PostStreamInitHttpChunkReader(playbackId, rawStream)

	decryptedStream, err := audio.NewAesAudioDecryptor(rawStream, audioKey)
	if err != nil {
		return nil, fmt.Errorf("failed intializing audio decryptor: %w", err)
	}

	audioStream, meta, err := vorbis.ExtractMetadataPage(p.log, decryptedStream, rawStream.Size())
	if err != nil {
		return nil, fmt.Errorf("failed reading metadata page: %w", err)
	}

	var normalisationFactor float32
	if p.normalisationEnabled {
		if p.normalisationUseAlbumGain {
			normalisationFactor = meta.GetAlbumFactor(p.normalisationPregain)
		} else {
			normalisationFactor = meta.GetTrackFactor(p.normalisationPregain)
		}
	} else {
		normalisationFactor = 1
	}

	stream, err := vorbis.New(log, audioStream, meta, normalisationFactor)
	if err != nil {
		return nil, fmt.Errorf("failed initializing ogg vorbis stream: %w", err)
	}

	if stream.SampleRate != SampleRate {
		return nil, fmt.Errorf("unsupported sample rate: %d", stream.SampleRate)
	} else if stream.Channels != Channels {
		return nil, fmt.Errorf("unsupported channels: %d", stream.Channels)
	}

	// Seek to the correct position if needed.
	if mediaPosition > 0 {
		if err := stream.SetPositionMs(max(0, min(mediaPosition, int64(media.Duration())))); err != nil {
			return nil, fmt.Errorf("failed seeking stream: %w", err)
		}
	}

	return &Stream{PlaybackId: playbackId, Source: stream, Media: media, File: file}, nil
}
