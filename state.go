package go_librespot

import (
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"sync"
)

type AppState struct {
	sync.Mutex

	path string

	DeviceId     string          `json:"device_id"`
	EventManager json.RawMessage `json:"event_manager"`
	Credentials  struct {
		Username string `json:"username"`
		Data     []byte `json:"data"`
	} `json:"credentials"`
}

func (s *AppState) Read(configDir string) error {
	s.path = filepath.Join(configDir, "state.json")

	if content, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(content, &s); err != nil {
			return fmt.Errorf("failed unmarshalling state file: %w", err)
		}
		log.Debugf("app state loaded")
	} else {
		log.Debugf("no app state found")
	}

	// Read credentials (old configuration, in credentials.json).
	if s.Credentials.Username == "" {
		if content, err := os.ReadFile(filepath.Join(configDir, "credentials.json")); err == nil {
			if err := json.Unmarshal(content, &s.Credentials); err != nil {
				return fmt.Errorf("failed unmarshalling stored credentials file: %w", err)
			}

			log.Debugf("stored credentials found for %s", s.Credentials.Username)
		} else {
			log.Debugf("stored credentials not found")
		}
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

	if err := json.NewEncoder(tmpFile).Encode(&s); err != nil {
		return fmt.Errorf("failed writing marshalled app state: %w", err)
	}

	if err := os.Rename(tmpFile.Name(), s.path); err != nil {
		return fmt.Errorf("failed replacing app state file: %w", err)
	}

	return nil
}
