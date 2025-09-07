package metadata

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	librespot "github.com/devgianlu/go-librespot"
)

// FIFOManager manages the metadata FIFO pipe
type FIFOManager struct {
	log        librespot.Logger
	path       string
	format     string
	bufferSize int

	pipe   *os.File
	mutex  sync.RWMutex
	closed bool
	buffer chan []byte
	stopCh chan struct{}

	// Metrics
	writeCount int64
	errorCount int64
	dropCount  int64
}

// NewFIFOManager creates a new FIFO manager
func NewFIFOManager(log librespot.Logger, path, format string, bufferSize int) *FIFOManager {
	return &FIFOManager{
		log:        log,
		path:       path,
		format:     format,
		bufferSize: bufferSize,
		buffer:     make(chan []byte, bufferSize),
		stopCh:     make(chan struct{}),
	}
}

// Start initializes and starts the FIFO manager
func (fm *FIFOManager) Start() error {
	// Create named pipe if it doesn't exist
	if err := fm.createFIFO(); err != nil {
		return fmt.Errorf("failed to create FIFO: %w", err)
	}

	fm.log.WithField("path", fm.path).WithField("format", fm.format).
		Infof("metadata FIFO started")

	// Start the writer goroutine
	go fm.writerLoop()

	return nil
}

// Stop stops the FIFO manager
func (fm *FIFOManager) Stop() {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()

	if fm.closed {
		return
	}

	fm.closed = true
	close(fm.stopCh)

	if fm.pipe != nil {
		fm.pipe.Close()
	}

	// Remove the FIFO file
	if fm.path != "" {
		os.Remove(fm.path)
	}

	fm.log.WithField("writes", fm.writeCount).
		WithField("errors", fm.errorCount).
		WithField("drops", fm.dropCount).
		Infof("metadata FIFO stopped")
}

// WriteMetadata writes metadata to the FIFO
func (fm *FIFOManager) WriteMetadata(metadata *TrackMetadata) {
	fm.mutex.RLock()
	closed := fm.closed
	fm.mutex.RUnlock()

	if closed {
		return
	}

	var data []byte
	switch fm.format {
	//	case "json":
	//		data = metadata.ToJSONFormat()
	case "xml": // ADD THIS CASE
		data = metadata.ToXMLFormat()
		//default: // "dacp"
		//	data = metadata.ToDACPFormat()
	}

	select {
	case fm.buffer <- data:
		// Successfully queued
	case <-time.After(50 * time.Millisecond):
		fm.dropCount++
		// Drop the message if buffer is full
	}
}

// createFIFO creates the named pipe
func (fm *FIFOManager) createFIFO() error {
	// Remove existing FIFO if it exists
	os.Remove(fm.path)

	// Create new FIFO
	err := syscall.Mkfifo(fm.path, 0666)
	if err != nil {
		return fmt.Errorf("mkfifo failed: %w", err)
	}

	return nil
}

// openFIFO opens the FIFO for writing (non-blocking)
func (fm *FIFOManager) openFIFO() error {
	pipe, err := os.OpenFile(fm.path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("failed to open FIFO: %w", err)
	}

	fm.pipe = pipe
	return nil
}

// writerLoop handles writing to the FIFO
func (fm *FIFOManager) writerLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case data := <-fm.buffer:
			if err := fm.writeToFIFO(data); err != nil {
				fm.errorCount++
				// Only log errors occasionally to avoid spam
				if fm.errorCount%50 == 1 {
					fm.log.WithError(err).Debugf("error writing to metadata FIFO")
				}
			} else {
				fm.writeCount++
			}

		case <-ticker.C:
			// Periodic check for reader presence
			fm.checkFIFOConnection()

		case <-fm.stopCh:
			return
		}
	}
}

// writeToFIFO writes data to the FIFO, handling reconnection
func (fm *FIFOManager) writeToFIFO(data []byte) error {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()

	if fm.pipe == nil {
		if err := fm.openFIFO(); err != nil {
			return err
		}
	}

	_, err := fm.pipe.Write(data)
	if err != nil {
		// Close and attempt to reopen on error
		fm.pipe.Close()
		fm.pipe = nil
		return err
	}

	return nil
}

// checkFIFOConnection checks if the FIFO is still connected
func (fm *FIFOManager) checkFIFOConnection() {
	fm.mutex.Lock()
	defer fm.mutex.Unlock()

	if fm.pipe == nil {
		return
	}

	// Try a zero-byte write to check connection
	_, err := fm.pipe.Write([]byte{})
	if err != nil {
		fm.pipe.Close()
		fm.pipe = nil
	}
}
