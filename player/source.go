package player

import (
	"errors"
	librespot "go-librespot"
	"io"
	"sync"
)

type SwitchingAudioSource struct {
	source map[bool]librespot.AudioSource
	which  bool
	cond   *sync.Cond

	done chan struct{}
}

func NewSwitchingAudioSource() *SwitchingAudioSource {
	return &SwitchingAudioSource{
		source: map[bool]librespot.AudioSource{},
		cond:   sync.NewCond(&sync.Mutex{}),
		done:   make(chan struct{}, 1),
	}
}

func (s *SwitchingAudioSource) SetPrimary(source librespot.AudioSource) {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()
	s.source[s.which] = source
	s.cond.Broadcast()
}

func (s *SwitchingAudioSource) SetSecondary(source librespot.AudioSource) {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()
	s.source[!s.which] = source
	s.cond.Broadcast()
}

func (s *SwitchingAudioSource) Done() <-chan struct{} {
	return s.done
}

func (s *SwitchingAudioSource) Read(p []float32) (n int, err error) {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()

	for s.source[s.which] == nil {
		s.cond.Wait()
	}

	n, err = s.source[s.which].Read(p)
	if errors.Is(err, io.EOF) {
		// notify this source is done
		s.done <- struct{}{}

		// if there's no other source just let the EOF through
		if s.source[!s.which] == nil {
			return n, err
		}

		// delete current source and switch to the other one
		delete(s.source, s.which)
		s.which = !s.which

		// ignore the EOF, we have mode data
		return n, nil
	} else if err != nil {
		return n, err
	}

	return n, nil
}

func (s *SwitchingAudioSource) SetPositionMs(pos int64) error {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()

	for s.source[s.which] == nil {
		s.cond.Wait()
	}

	return s.source[s.which].SetPositionMs(pos)
}

func (s *SwitchingAudioSource) PositionMs() int64 {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()

	for s.source[s.which] == nil {
		s.cond.Wait()
	}

	return s.source[s.which].PositionMs()
}

func (s *SwitchingAudioSource) Close() error {
	var err error
	if source, ok := s.source[true].(io.Closer); ok && source != nil {
		err = errors.Join(err, source.Close())
	}
	if source, ok := s.source[false].(io.Closer); ok && source != nil {
		err = errors.Join(err, source.Close())
	}
	return err
}
