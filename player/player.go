package player

import (
	"fmt"
	"github.com/hajimehoshi/oto/v2"
	"github.com/jfreymuth/oggvorbis"
	librespot "go-librespot"
	audiokey "go-librespot/audio_key"
	downloadpb "go-librespot/proto/spotify/download"
	metadatapb "go-librespot/proto/spotify/metadata"
	"go-librespot/spclient"
	"net/http"
	"time"
)

const SampleRate = 44100
const Channels = 2

const MaxPlayers = 4
const MaxVolume = 65535

type Player struct {
	sp       *spclient.Spclient
	audioKey *audiokey.AudioKeyProvider

	oto *oto.Context
	cmd chan playerCmd
	ev  chan Event

	startedPlaying time.Time
}

type playerCmdType int

const (
	playerCmdNew playerCmdType = iota
	playerCmdPlay
	playerCmdStop
	playerCmdVolume
	playerCmdClose
)

type playerCmd struct {
	typ  playerCmdType
	data any
	resp chan any
}

func NewPlayer(sp *spclient.Spclient, audioKey *audiokey.AudioKeyProvider) (*Player, error) {
	otoCtx, readyChan, err := oto.NewContextWithOptions(&oto.NewContextOptions{
		SampleRate:   SampleRate,
		ChannelCount: Channels,
		Format:       oto.FormatFloat32LE,
		BufferSize:   100 * time.Millisecond,
	})
	if err != nil {
		return nil, fmt.Errorf("failed initializing oto context: %w", err)
	}

	<-readyChan

	p := &Player{
		sp:       sp,
		audioKey: audioKey,
		oto:      otoCtx,
		cmd:      make(chan playerCmd),
		ev:       make(chan Event),
	}
	go p.manageLoop()

	return p, nil
}

func (p *Player) manageLoop() {
	players := [MaxPlayers]oto.Player{}

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

				cmd.resp <- struct{}{}

				p.ev <- Event{Type: EventTypePlaying}
			case playerCmdStop:
				pp := players[cmd.data.(int)]
				_ = pp.Close()
				players[cmd.data.(int)] = nil

				p.startedPlaying = time.Time{}
				cmd.resp <- struct{}{}

				p.ev <- Event{Type: EventTypeStopped}
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
			for _, pp := range players {
				if pp == nil {
					continue
				}

				pp.IsPlaying() // TODO: do something with this
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
	vol := float64(val) / MaxVolume
	p.cmd <- playerCmd{typ: playerCmdVolume, data: vol}
}

func (p *Player) newStream(pp oto.Player) int {
	resp := make(chan any)
	p.cmd <- playerCmd{typ: playerCmdNew, data: pp, resp: resp}
	return (<-resp).(int)
}

func (p *Player) NewStream(tid librespot.TrackId) (*Stream, error) {
	trackMeta, err := p.sp.MetadataForTrack(tid)
	if err != nil {
		return nil, fmt.Errorf("failed getting track metadata: %w", err)
	}

	if len(trackMeta.File) == 0 {
		// TODO: possibly pick alternative track
		return nil, fmt.Errorf("no playable files found")
	}

	// TODO: better audio quality customization
	var file *metadatapb.AudioFile
	for _, ff := range trackMeta.File {
		if *ff.Format == metadatapb.AudioFile_OGG_VORBIS_96 || *ff.Format == metadatapb.AudioFile_OGG_VORBIS_160 || *ff.Format == metadatapb.AudioFile_OGG_VORBIS_320 {
			file = ff
			break
		}
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

	req, _ := http.NewRequest("GET", trackUrl, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed retreving stream: %w", err)
	}

	dec, err := audiokey.NewAesAudioDecryptor(resp.Body, audioKey)
	if err != nil {
		return nil, fmt.Errorf("failed intializing audio decryptor: %w", err)
	}

	norm, err := readReplayGainMetadata(dec)
	if err != nil {
		return nil, fmt.Errorf("failed reading ReplayGain metadata: %w", err)
	}

	stream, err := oggvorbis.NewReader(dec)
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

	// TODO: where do we close the response body?

	return &Stream{p: p, idx: idx, Track: trackMeta, File: file}, nil
}
