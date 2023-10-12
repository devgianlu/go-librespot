package player

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/audio"
	"go-librespot/output"
	downloadpb "go-librespot/proto/spotify/download"
	"go-librespot/spclient"
	"go-librespot/vorbis"
	"io"
	"time"
)

const SampleRate = 44100
const Channels = 2

const MaxStateVolume = 65535

type Player struct {
	countryCode *string

	sp       *spclient.Spclient
	audioKey *audio.KeyProvider

	newOutput func(source librespot.Float32Reader, paused bool) (*output.Output, error)

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
	source librespot.AudioSource
	paused bool
}

func NewPlayer(sp *spclient.Spclient, audioKey *audio.KeyProvider, countryCode *string, device string, volumeSteps uint32) (*Player, error) {
	p := &Player{
		sp:          sp,
		audioKey:    audioKey,
		countryCode: countryCode,
		newOutput: func(reader librespot.Float32Reader, paused bool) (*output.Output, error) {
			return output.NewOutput(&output.NewOutputOptions{
				Reader:          reader,
				SampleRate:      SampleRate,
				ChannelCount:    Channels,
				Device:          device,
				InitiallyPaused: paused,
			})
		},
		volumeSteps: volumeSteps,
		cmd:         make(chan playerCmd),
		ev:          make(chan Event, 128), // FIXME: is too messy?
	}

	go p.manageLoop()

	return p, nil
}

func (p *Player) manageLoop() {
	var out *output.Output
	var source librespot.AudioSource
	var done <-chan error

loop:
	for {
		select {
		case cmd := <-p.cmd:
			switch cmd.typ {
			case playerCmdSet:
				if out != nil {
					_ = out.Close()
				}

				data := cmd.data.(playerCmdDataSet)
				source = data.source

				var err error
				out, err = p.newOutput(source, data.paused)
				if err != nil {
					source = nil
					cmd.resp <- err
				} else {
					done = out.WaitDone()
					p.startedPlaying = time.Now()

					cmd.resp <- nil

					if data.paused {
						p.ev <- Event{Type: EventTypePaused}
					} else {
						p.ev <- Event{Type: EventTypePlaying}
					}
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
				}

				cmd.resp <- struct{}{}
				p.ev <- Event{Type: EventTypeStopped}
			case playerCmdSeek:
				if source != nil && out != nil {
					if err := source.SetPositionMs(cmd.data.(int64)); err != nil {
						cmd.resp <- err
					} else if err := out.Drop(); err != nil {
						cmd.resp <- err
					} else {
						cmd.resp <- nil
					}
				} else {
					cmd.resp <- nil
				}
			case playerCmdPosition:
				if source != nil && out != nil {
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
				if source != nil {
					vol := cmd.data.(float32)
					out.SetVolume(vol)
				}
			case playerCmdClose:
				break loop
			default:
				panic("unknown player command")
			}
		case err := <-done:
			if err != nil {
				log.WithError(err).Errorf("playback failed")
			}

			done = nil
			if err != nil || out.IsEOF() {
				p.ev <- Event{Type: EventTypeNotPlaying}
			}
		}
	}

	close(p.cmd)

	// teardown
	if s, ok := source.(io.Closer); ok {
		_ = s.Close()
	}

	_ = out.Close()
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

const DisableCheckTrackRestricted = true

func (p *Player) NewStream(tid librespot.TrackId, bitrate int, trackPosition int64, paused bool) (*Stream, error) {
	trackMeta, err := p.sp.MetadataForTrack(tid)
	if err != nil {
		return nil, fmt.Errorf("failed getting track metadata: %w", err)
	}

	if !DisableCheckTrackRestricted && isTrackRestricted(trackMeta, *p.countryCode) {
		return nil, librespot.ErrTrackRestricted
	}

	if len(trackMeta.File) == 0 {
		for _, alt := range trackMeta.Alternative {
			if len(alt.File) == 0 {
				continue
			}

			trackMeta.File = append(trackMeta.File, alt.File...)
		}
	}

	file := selectBestFormat(trackMeta.File, bitrate)
	if file == nil {
		return nil, fmt.Errorf("no playable formats for %s", tid.Uri())
	}

	log.Debugf("selected format %s for %s", file.Format.String(), tid.Uri())

	audioKey, err := p.audioKey.Request(tid, file.FileId)
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

	rawStream, err := audio.NewHttpChunkedReader(trackUrl)
	if err != nil {
		return nil, fmt.Errorf("failed initializing chunked reader: %w", err)
	}

	// TODO: consider decrypting in the HttpChunkedReader
	decryptedStream, err := audio.NewAesAudioDecryptor(rawStream, audioKey)
	if err != nil {
		return nil, fmt.Errorf("failed intializing audio decryptor: %w", err)
	}

	audioStream, norm, err := audio.ExtractReplayGainMetadata(decryptedStream, rawStream.Size())
	if err != nil {
		return nil, fmt.Errorf("failed reading ReplayGain metadata: %w", err)
	}

	stream, err := vorbis.New(audioStream, *trackMeta.Duration, norm.GetTrackFactor(1))
	if err != nil {
		return nil, fmt.Errorf("failed initializing ogg vorbis stream: %w", err)
	}

	if stream.Info().SampleRate != SampleRate {
		return nil, fmt.Errorf("unsupported sample rate: %d", stream.Info().SampleRate)
	} else if stream.Info().Channels != Channels {
		return nil, fmt.Errorf("unsupported channels: %d", stream.Info().Channels)
	}

	if err := stream.SetPositionMs(trackPosition); err != nil {
		return nil, fmt.Errorf("failed seeking stream: %w", err)
	}

	resp := make(chan any)
	p.cmd <- playerCmd{typ: playerCmdSet, data: playerCmdDataSet{source: stream, paused: paused}, resp: resp}
	if err := <-resp; err != nil {
		return nil, err.(error)
	}

	return &Stream{p: p, Track: trackMeta, File: file}, nil
}
