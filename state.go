package go_librespot

import (
	"encoding/json"
	"fmt"
	"github.com/zalando/go-keyring"
	"os"
	"path/filepath"
	"sync"
)

type AppState struct {
	sync.Mutex

	log  Logger
	path string

	DeviceId     string          `json:"device_id"`
	EventManager json.RawMessage `json:"event_manager"`
	Credentials  struct {
		Username string `json:"username"`
		Data     []byte `json:"data"`
	} `json:"credentials"`
	LastVolume *uint32 `json:"last_volume"`
}

func (s *AppState) SetLogger(log Logger) {
	s.log = log
}

func (s *AppState) Read(configDir string) error {
	s.path = filepath.Join(configDir, "state.json")

	if content, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(content, &s); err != nil {
			return fmt.Errorf("failed unmarshalling state file: %w", err)
		}
		s.log.Debugf("app state loaded")
	} else {
		s.log.Debugf("no app state found")
	}

	// Read credentials (old configuration, in credentials.json).
	if s.Credentials.Username == "" {
		if content, err := os.ReadFile(filepath.Join(configDir, "credentials.json")); err == nil {
			if err := json.Unmarshal(content, &s.Credentials); err != nil {
				return fmt.Errorf("failed unmarshalling stored credentials file: %w", err)
			}

			s.log.WithField("username", ObfuscateUsername(s.Credentials.Username)).
				Debugf("stored credentials found")
		} else {
			s.log.Debugf("stored credentials not found")
		}

		// Credentials might be in the OS keyring.
		s.getCredsFromKeyring()

	}

	return nil
}


func (s *AppState) Write() error {
	s.Lock()
	defer s.Unlock()

	// Create a temporary file, and overwrite the old file.
	// This is a way to atomically replace files.
	// The file is created with mode 0o600 so we don't need to change the mode.
	tmpFile, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("failed creating temporary file for app state: %w", err)
	}
	if err := s.writeCredsToKeyring(); err != nil {
		if err := json.NewEncoder(tmpFile).Encode(s); err != nil {
			return fmt.Errorf("failed writing marshalled app state: %w", err)
		}
	}
  if err := s.persistStateWithoutCredentials(tmpFile); err != nil {
    return err  
	}

	if err := os.Rename(tmpFile.Name(), s.path); err != nil {
		return fmt.Errorf("failed replacing app state file: %w", err)
	}

	return nil
}

func (s *AppState) persistStateWithoutCredentials(tmpFile *os.File) error {
	persistedState := struct {
		DeviceId     string          `json:"device_id"`
		EventManager json.RawMessage `json:"event_manager"`
		LastVolume   *uint32         `json:"last_volume"`
	}{
		DeviceId:     s.DeviceId,
		EventManager: s.EventManager,
		LastVolume:   s.LastVolume,
	}

	if err := json.NewEncoder(tmpFile).Encode(&persistedState); err != nil {
		return fmt.Errorf("failed writing marshalled app state: %w", err)
	}
	return nil
}

func (s *AppState) getCredsFromKeyring() {
	storedCredsRaw, err := keyring.Get("librespot", "credentials")
	if err != nil {
		return
	}
	if err := json.Unmarshal([]byte(storedCredsRaw), &s.Credentials); err != nil {
		return
	}
}

func (s *AppState) writeCredsToKeyring() error {
	creds, err := json.Marshal(s.Credentials)
	if err != nil {
		return err
	}
	err = keyring.Set("librespot", "credentials", string(creds))
	if err != nil {
		return err
	}
	return nil
}
