package player

import (
	"fmt"
	"github.com/hajimehoshi/oto/v2"
	"github.com/jfreymuth/oggvorbis"
	librespot "go-librespot"
	"go-librespot/audio"
	downloadpb "go-librespot/proto/spotify/download"
	metadatapb "go-librespot/proto/spotify/metadata"
	"go-librespot/spclient"
	"io"
	"time"
)

const SampleRate = 44100
const Channels = 2

const MaxPlayers = 4
const MaxStateVolume = 65535

type Player struct {
	sp       *spclient.Spclient
	audioKey *audio.KeyProvider

	oto *oto.Context
	cmd chan playerCmd
	ev  chan Event

	volumeSteps uint32

	startedPlaying time.Time
}

type playerCmdType int

const (
	playerCmdNew playerCmdType = iota
	playerCmdPlay
	playerCmdPause
	playerCmdStop
	playerCmdSeek
	playerCmdVolume
	playerCmdClose
)

type playerCmd struct {
	typ  playerCmdType
	data any
	resp chan any
}

func NewPlayer(sp *spclient.Spclient, audioKey *audio.KeyProvider, preferredDevice string, volumeSteps uint32) (*Player, error) {
	otoCtx, readyChan, err := oto.NewContextWithOptions(&oto.NewContextOptions{
		SampleRate:      SampleRate,
		ChannelCount:    Channels,
		Format:          oto.FormatFloat32LE,
		BufferSize:      100 * time.Millisecond,
		PreferredDevice: preferredDevice,
	})
	if err != nil {
		return nil, fmt.Errorf("failed initializing oto context: %w", err)
	}

	<-readyChan

	p := &Player{
		sp:          sp,
		audioKey:    audioKey,
		oto:         otoCtx,
		volumeSteps: volumeSteps,
		cmd:         make(chan playerCmd),
		ev:          make(chan Event, 1), // FIXME: is too messy?
	}
	go p.manageLoop()

	return p, nil
}

func (p *Player) manageLoop() {
	players := [MaxPlayers]oto.Player{}
	started := [MaxPlayers]bool{}

loop:
	for {
		select {
		case cmd := <-p.cmd:
			switch cmd.typ {
			case playerCmdNew:
				for i := 0; i < MaxPlayers; i++ {
					if players[i] == nil {
						players[i] = cmd.data.(oto.Player)

						if p.startedPlaying.IsZero() {
							p.startedPlaying = time.Now()
						}
						cmd.resp <- i
						break
					}
				}
			case playerCmdPlay:
				pp := players[cmd.data.(int)]
				pp.Play()
				started[cmd.data.(int)] = true
				cmd.resp <- struct{}{}
				p.ev <- Event{Type: EventTypePlaying}
			case playerCmdPause:
				pp := players[cmd.data.(int)]
				pp.Pause()
				started[cmd.data.(int)] = false
				cmd.resp <- struct{}{}
				p.ev <- Event{Type: EventTypePaused}
			case playerCmdStop:
				pp := players[cmd.data.(int)]
				_ = pp.Close()
				started[cmd.data.(int)] = false
				players[cmd.data.(int)] = nil
				cmd.resp <- struct{}{}
				p.ev <- Event{Type: EventTypeStopped}
			case playerCmdSeek:
				// seek directly with milliseconds
				pp := players[cmd.data.(playerCmdSeekData).idx]
				_, err := pp.(io.Seeker).Seek(cmd.data.(playerCmdSeekData).pos, io.SeekStart)
				cmd.resp <- err
			case playerCmdVolume:
				vol := cmd.data.(float64)
				for _, pp := range players {
					if pp == nil {
						continue
					}

					pp.SetVolume(vol)
				}
			case playerCmdClose:
				break loop
			default:
				panic("unknown player command")
			}
		default:
			// FIXME: this is all awful
			for i, pp := range players {
				if pp == nil || !started[i] {
					continue
				}

				if !pp.IsPlaying() {
					p.ev <- Event{Type: EventTypeNotPlaying}
				}
			}

			time.Sleep(10 * time.Millisecond)
		}
	}

	close(p.cmd)

	// teardown
	for _, pp := range players {
		_ = pp.Close()
	}
}

func (p *Player) StartedPlayingAt() time.Time {
	return p.startedPlaying
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
	vol := float64(val) / MaxStateVolume
	p.cmd <- playerCmd{typ: playerCmdVolume, data: vol}
}

func (p *Player) newStream(pp oto.Player) int {
	resp := make(chan any, 1)
	p.cmd <- playerCmd{typ: playerCmdNew, data: pp, resp: resp}
	return (<-resp).(int)
}

func (p *Player) NewStream(tid librespot.TrackId, bitrate int) (*Stream, error) {
	trackMeta, err := p.sp.MetadataForTrack(tid)
	if err != nil {
		return nil, fmt.Errorf("failed getting track metadata: %w", err)
	}

	if len(trackMeta.File) == 0 {
		for _, alt := range trackMeta.Alternative {
			if len(alt.File) == 0 {
				continue
			}

			trackMeta.File = append(trackMeta.File, alt.File...)
		}
	}

	var file *metadatapb.AudioFile
	for _, ff := range trackMeta.File {
		if formatBitrate(*ff.Format) == bitrate {
			file = ff
			break
		}
	}

	if file == nil {
		return nil, fmt.Errorf("failed fetching requested bitrate: %d", bitrate)
	}

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

	stream, err := oggvorbis.NewReader(audioStream)
	if err != nil {
		return nil, fmt.Errorf("failed initializing ogg vorbis stream: %w", err)
	}

	if stream.SampleRate() != SampleRate {
		return nil, fmt.Errorf("unsupported sample rate: %d", stream.SampleRate())
	} else if stream.Channels() != Channels {
		return nil, fmt.Errorf("unsupported channels: %d", stream.Channels())
	}

	idx := p.newStream(p.oto.NewPlayer(newSampleDecoder(stream, norm)))
	if idx == -1 {
		return nil, fmt.Errorf("too many players")
	}

	return &Stream{p: p, idx: idx, Track: trackMeta, File: file}, nil
}
