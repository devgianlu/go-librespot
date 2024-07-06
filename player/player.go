package player

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/audio"
	"go-librespot/output"
	downloadpb "go-librespot/proto/spotify/download"
	"go-librespot/proto/spotify/metadata"
	"go-librespot/spclient"
	"go-librespot/vorbis"
	"time"
)

const SampleRate = 44100
const Channels = 2

const MaxStateVolume = 65535

type Player struct {
	normalisationEnabled bool
	normalisationPregain float32
	countryCode          *string

	sp       *spclient.Spclient
	audioKey *audio.KeyProvider

	newOutput func(source librespot.Float32Reader, volume float32) (*output.Output, error)

	cmd chan playerCmd
	ev  chan Event

	externalVolume bool
	volumeSteps    uint32

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
}

func NewPlayer(sp *spclient.Spclient, audioKey *audio.KeyProvider, normalisationEnabled bool, normalisationPregain float32, countryCode *string, device, mixer string, control string, volumeSteps uint32, externalVolume bool, externalVolumeUpdate output.RingBuffer[float32]) (*Player, error) {
	p := &Player{
		sp:                   sp,
		audioKey:             audioKey,
		normalisationEnabled: normalisationEnabled,
		normalisationPregain: normalisationPregain,
		countryCode:          countryCode,
		newOutput: func(reader librespot.Float32Reader, volume float32) (*output.Output, error) {
			return output.NewOutput(&output.NewOutputOptions{
				Reader:               reader,
				SampleRate:           SampleRate,
				ChannelCount:         Channels,
				Device:               device,
				Mixer:                mixer,
				Control:              control,
				InitialVolume:        volume,
				ExternalVolume:       externalVolume,
				ExternalVolumeUpdate: externalVolumeUpdate,
			})
		},
		externalVolume: externalVolume,
		volumeSteps:    volumeSteps,
		cmd:            make(chan playerCmd),
		ev:             make(chan Event, 128), // FIXME: is too messy?
	}

	go p.manageLoop()

	return p, nil
}

func (p *Player) manageLoop() {
	// currently available output device
	var out *output.Output
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
					log.Debugf("created new output device")
				}

				// set source
				source.SetPrimary(data.source)
				if data.paused {
					_ = out.Pause()
				} else {
					_ = out.Resume()
				}

				// when setting the primary stream just drop everything
				_ = out.Drop()

				p.startedPlaying = time.Now()
				cmd.resp <- nil

				if data.paused {
					p.ev <- Event{Type: EventTypePaused}
				} else {
					p.ev <- Event{Type: EventTypePlaying}
				}
			case playerCmdPlay:
				if out != nil {
					if err := out.Resume(); err != nil {
						log.WithError(err).Errorf("failed resuming playback")
					}
				}

				cmd.resp <- struct{}{}
				p.ev <- Event{Type: EventTypePlaying}
			case playerCmdPause:
				if out != nil {
					if err := out.Pause(); err != nil {
						log.WithError(err).Errorf("failed pausing playback")
					}
				}

				cmd.resp <- struct{}{}
				p.ev <- Event{Type: EventTypePaused}
			case playerCmdStop:
				if out != nil {
					_ = out.Close()
					out = nil
					outErr = make(<-chan error)

					log.Tracef("closed output device because of stop command")
				}

				cmd.resp <- struct{}{}
				p.ev <- Event{Type: EventTypeStopped}
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
				if out != nil {
					delay, err := out.DelayMs()
					if err != nil {
						log.WithError(err).Warnf("failed getting output device delay")
						delay = 0
					}

						cmd.resp <- source.PositionMs() - delay
				} else {
					cmd.resp <- int64(0)
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
				log.WithError(err).Errorf("output device failed")
			}

			// the current output device has exited, clean it up
			_ = out.Close()
			out = nil
			outErr = make(<-chan error)

			log.Tracef("cleared closed output device")

			p.ev <- Event{Type: EventTypeStopped}
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

func (p *Player) Play() {
	resp := make(chan any, 1)
	p.cmd <- playerCmd{typ: playerCmdPlay, resp: resp}
	<-resp
}

func (p *Player) Pause() {
	resp := make(chan any, 1)
	p.cmd <- playerCmd{typ: playerCmdPause, resp: resp}
	<-resp
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

func (p *Player) SetPrimaryStream(source librespot.AudioSource, paused bool) error {
	resp := make(chan any)
	p.cmd <- playerCmd{typ: playerCmdSet, data: playerCmdDataSet{source: source, primary: true, paused: paused}, resp: resp}
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

const DisableCheckMediaRestricted = true

func (p *Player) NewStream(spotId librespot.SpotifyId, bitrate int, mediaPosition int64) (*Stream, error) {
	log := log.WithField("uri", spotId.Uri())

	var media *librespot.Media
	var file *metadata.AudioFile
	if spotId.Type() == librespot.SpotifyIdTypeTrack {
		trackMeta, err := p.sp.MetadataForTrack(spotId)
		if err != nil {
			return nil, fmt.Errorf("failed getting track metadata: %w", err)
		}

		media = librespot.NewMediaFromTrack(trackMeta)
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
		}

		file = selectBestMediaFormat(trackMeta.File, bitrate)
		if file == nil {
			return nil, librespot.ErrNoSupportedFormats
		}
	} else if spotId.Type() == librespot.SpotifyIdTypeEpisode {
		episodeMeta, err := p.sp.MetadataForEpisode(spotId)
		if err != nil {
			return nil, fmt.Errorf("failed getting episode metadata: %w", err)
		}

		media = librespot.NewMediaFromEpisode(episodeMeta)
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

	log.Debugf("selected format %s (%x)", file.Format.String(), file.FileId)

	audioKey, err := p.audioKey.Request(spotId.Id(), file.FileId)
	if err != nil {
		return nil, fmt.Errorf("failed retrieving audio key: %w", err)
	}

	storageResolve, err := p.sp.ResolveStorageInteractive(file.FileId, false)
	if err != nil {
		return nil, fmt.Errorf("failed resolving track storage: %w", err)
	}

	var trackUrl string
	switch storageResolve.Result {
	case downloadpb.StorageResolveResponse_CDN:
		if len(storageResolve.Cdnurl) == 0 {
			return nil, fmt.Errorf("no cdn urls")
		}

		trackUrl = storageResolve.Cdnurl[0]
	case downloadpb.StorageResolveResponse_STORAGE:
		return nil, fmt.Errorf("old storage not supported")
	case downloadpb.StorageResolveResponse_RESTRICTED:
		return nil, fmt.Errorf("storage is restricted")
	default:
		return nil, fmt.Errorf("unknown storage resolve result: %s", storageResolve.Result)
	}

	rawStream, err := audio.NewHttpChunkedReader(log.WithField("uri", spotId.String()), trackUrl)
	if err != nil {
		return nil, fmt.Errorf("failed initializing chunked reader: %w", err)
	}

	decryptedStream, err := audio.NewAesAudioDecryptor(rawStream, audioKey)
	if err != nil {
		return nil, fmt.Errorf("failed intializing audio decryptor: %w", err)
	}

	audioStream, meta, err := audio.ExtractMetadataPage(decryptedStream, rawStream.Size())
	if err != nil {
		return nil, fmt.Errorf("failed reading metadata page: %w", err)
	}

	var normalisationFactor float32
	if p.normalisationEnabled {
		normalisationFactor = meta.GetTrackFactor(p.normalisationPregain)
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

	if err := stream.SetPositionMs(max(0, min(mediaPosition, int64(media.Duration())))); err != nil {
		return nil, fmt.Errorf("failed seeking stream: %w", err)
	}

	return &Stream{Source: stream, Media: media, File: file}, nil
}
