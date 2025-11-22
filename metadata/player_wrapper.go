package metadata

import (
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
)

// PlayerMetadata wraps metadata functionality for a player
type PlayerMetadata struct {
	fifoManager *FIFOManager
	metadata    *TrackMetadata
	mutex       sync.RWMutex
	enabled     bool
}

// NewPlayerMetadata creates a new player metadata wrapper
func NewPlayerMetadata(log librespot.Logger, config MetadataPipeConfig) *PlayerMetadata {
	pm := &PlayerMetadata{
		enabled: config.Enabled,
	}

	if config.Enabled {
		pm.fifoManager = NewFIFOManager(log, config.Path, config.Format, config.BufferSize)
		pm.metadata = NewTrackMetadata()
	}

	return pm
}

// Start starts the metadata system
func (pm *PlayerMetadata) Start() error {
	if !pm.enabled || pm.fifoManager == nil {
		return nil
	}

	return pm.fifoManager.Start()
}

// Stop stops the metadata system
func (pm *PlayerMetadata) Stop() {
	if !pm.enabled || pm.fifoManager == nil {
		return
	}

	pm.fifoManager.Stop()
}

func (pm *PlayerMetadata) UpdateTrack(info player.TrackUpdateInfo) {
	if !pm.enabled {
		return
	}

	pm.mutex.Lock()
	pm.metadata.Update(info.Title, info.Artist, info.Album, info.TrackID, info.Duration.Milliseconds(), 0, pm.metadata.Volume, info.Playing, info.ArtworkURL, info.ArtworkData)
	pm.mutex.Unlock()

	pm.writeMetadata()
}

// UpdatePosition updates playback position
func (pm *PlayerMetadata) UpdatePosition(position time.Duration) {
	if !pm.enabled {
		return
	}

	pm.mutex.Lock()
	pm.metadata.UpdatePosition(position.Milliseconds())
	pm.mutex.Unlock()

	pm.writeMetadata()
}

// UpdateVolume updates volume
func (pm *PlayerMetadata) UpdateVolume(volume int) {
	if !pm.enabled {
		return
	}

	pm.mutex.Lock()
	pm.metadata.UpdateVolume(volume)
	pm.mutex.Unlock()

	pm.writeMetadata()
}

// UpdatePlayingState updates playing state
func (pm *PlayerMetadata) UpdatePlayingState(playing bool) {
	if !pm.enabled {
		return
	}

	pm.mutex.Lock()
	pm.metadata.UpdatePlayingState(playing)
	pm.mutex.Unlock()

	pm.writeMetadata()
}

// writeMetadata writes current metadata to FIFO
func (pm *PlayerMetadata) writeMetadata() {
	if pm.fifoManager == nil {
		return
	}

	pm.mutex.RLock()
	metadataCopy := *pm.metadata // Make a copy to avoid race conditions
	pm.mutex.RUnlock()

	pm.fifoManager.WriteMetadata(&metadataCopy)
}

// MetadataPipeConfig represents metadata pipe configuration
type MetadataPipeConfig struct {
	Enabled    bool
	Path       string
	Format     string
	BufferSize int
}
