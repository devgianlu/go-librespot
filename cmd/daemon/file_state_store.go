package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	librespot "github.com/devgianlu/go-librespot"
)

type FileStateStore struct {
	statePath       string
	credentialsPath string

	log librespot.Logger
}

func NewFileStateStore(statePath, credentialsPath string, log librespot.Logger) *FileStateStore {
	return &FileStateStore{
		statePath:       statePath,
		credentialsPath: credentialsPath,
		log:             log,
	}
}

func (s *FileStateStore) Load() (*librespot.AppState, error) {
	state := &librespot.AppState{}

	content, err := os.ReadFile(s.statePath)
	switch {
	case err == nil:
		if err := json.Unmarshal(content, state); err != nil {
			return nil, fmt.Errorf("failed unmarshalling state file: %w", err)
		}
		s.log.Debugf("app state loaded")
	case errors.Is(err, os.ErrNotExist):
		s.log.Debugf("no app state found")
	default:
		return nil, fmt.Errorf("failed reading state file: %w", err)
	}

	// Legacy credentials.json fallback for installations that predate the
	// merged state.json layout.
	if state.Credentials.Username == "" && s.credentialsPath != "" {
		content, err := os.ReadFile(s.credentialsPath)
		switch {
		case err == nil:
			if err := json.Unmarshal(content, &state.Credentials); err != nil {
				return nil, fmt.Errorf("failed unmarshalling stored credentials file: %w", err)
			}
			s.log.WithField("username", librespot.ObfuscateUsername(state.Credentials.Username)).
				Debugf("stored credentials found")
		case errors.Is(err, os.ErrNotExist):
			s.log.Debugf("stored credentials not found")
		default:
			return nil, fmt.Errorf("failed reading credentials file: %w", err)
		}
	}

	return state, nil
}

func (s *FileStateStore) Save(state *librespot.AppState) error {
	state.Lock()
	defer state.Unlock()

	tmpFile, err := os.CreateTemp(filepath.Dir(s.statePath), filepath.Base(s.statePath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed creating temporary file for app state: %w", err)
	}
	tmpPath := tmpFile.Name()

	if err := json.NewEncoder(tmpFile).Encode(state); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed writing marshalled app state: %w", err)
	}

	// Windows cannot rename a file that still has an open handle.
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed closing temporary app state file: %w", err)
	}

	if err := replaceFile(tmpPath, s.statePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed replacing app state file: %w", err)
	}

	return nil
}

// replaceFile moves src onto dest. On Windows, os.Rename cannot overwrite an
// existing dest, so dest is removed first. The daemon holds a lockfile, so
// nothing else should be rewriting this path.
func replaceFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(src, dest)
}
