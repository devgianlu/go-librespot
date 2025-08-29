package metadata

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// TrackMetadata represents the current track information
type TrackMetadata struct {
	Title       string    `json:"title"`
	Artist      string    `json:"artist"`
	Album       string    `json:"album"`
	Duration    int64     `json:"duration_ms"`
	Position    int64     `json:"position_ms"`
	TrackID     string    `json:"track_id"`
	Volume      int       `json:"volume"`
	Playing     bool      `json:"playing"`
	Timestamp   time.Time `json:"timestamp"`
	ArtworkURL  string    `json:"artwork_url,omitempty"`
	ArtworkData []byte    `json:"artwork_data,omitempty"`
	SampleRate  float32   `json:"sample_rate,omitempty"` // ADD THIS for progress tracking
}

// NewTrackMetadata creates a new TrackMetadata instance
func NewTrackMetadata() *TrackMetadata {
	return &TrackMetadata{
		Timestamp:  time.Now(),
		SampleRate: 44100, // Default sample rate
	}
}

// ToDACPFormat converts metadata to DACP pipe format compatible with forked-daapd
func (tm *TrackMetadata) ToDACPFormat() []byte {
	var result []byte

	// DACP format uses key-value pairs with specific encoding
	if tm.Title != "" {
		result = append(result, encodeDACPItem("minm", tm.Title)...)
	}
	if tm.Artist != "" {
		result = append(result, encodeDACPItem("asar", tm.Artist)...)
	}
	if tm.Album != "" {
		result = append(result, encodeDACPItem("asal", tm.Album)...)
	}

	// ADD ARTWORK URL:
	if tm.ArtworkURL != "" {
		result = append(result, encodeDACPItem("asul", tm.ArtworkURL)...)
	}

	// Volume (0-100)
	result = append(result, encodeDACPInt("cmvo", tm.Volume)...)

	// Playing state: 2 = paused, 3 = playing, 4 = stopped
	playState := 4 // stopped
	if tm.Playing {
		playState = 3 // playing
	} else if tm.Duration > 0 {
		playState = 2 // paused
	}
	result = append(result, encodeDACPInt("caps", playState)...)

	// Duration in milliseconds
	if tm.Duration > 0 {
		result = append(result, encodeDACPInt("astm", int(tm.Duration))...)
	}

	// Position in milliseconds
	result = append(result, encodeDACPInt("cant", int(tm.Position))...)

	return result
}

// ToJSONFormat converts metadata to JSON format
func (tm *TrackMetadata) ToJSONFormat() []byte {
	data, _ := json.Marshal(tm)
	return append(data, '\n')
}

// ToXMLFormat method with debugging for artwork:
func (tm *TrackMetadata) ToXMLFormat() []byte {
	var result []byte

	// Helper function to encode XML metadata item with hex-encoded type/code
	encodeItem := func(itemType, code, data string) []byte {
		typeHex := asciiToHex(itemType)
		codeHex := asciiToHex(code)

		// Get original data length BEFORE encoding
		originalLength := len(data)

		// Encode data as base64
		encodedData := base64.StdEncoding.EncodeToString([]byte(data))

		// Use hex format for length (matching the type/code format)
		lengthHex := fmt.Sprintf("%x", originalLength)

		// Create XML-style item with hex-encoded length
		item := fmt.Sprintf("<item><type>%s</type><code>%s</code><length>%s</length><data>%s</data></item>\n",
			typeHex, codeHex, lengthHex, encodedData)

		return []byte(item)
	}

	encodeRawData := func(itemType, code string, data []byte) []byte {
		typeHex := asciiToHex(itemType)
		codeHex := asciiToHex(code)

		// Encode binary data as base64
		encodedData := base64.StdEncoding.EncodeToString(data)

		// DEBUG: Log artwork info
		fmt.Printf("DEBUG Artwork: type=%s, code=%s, raw_len=%d, encoded_len=%d\n",
			itemType, code, len(data), len(encodedData))
		fmt.Printf("DEBUG First 100 chars of encoded: %.100s\n", encodedData)

		// Use the ORIGINAL BINARY LENGTH, with 8-digit hex format
		lengthHex := fmt.Sprintf("%08x", len(data))

		// Create XML-style item
		item := fmt.Sprintf("<item><type>%s</type><code>%s</code><length>%s</length><data>%s</data></item>\n",
			typeHex, codeHex, lengthHex, encodedData)

		return []byte(item)
	}

	// Track title
	if tm.Title != "" {
		result = append(result, encodeItem("core", "minm", tm.Title)...)
	}

	// Artist
	if tm.Artist != "" {
		result = append(result, encodeItem("core", "asar", tm.Artist)...)
	}

	// Album
	if tm.Album != "" {
		result = append(result, encodeItem("core", "asal", tm.Album)...)
	}

	// ARTWORK - try different approaches:
	if len(tm.ArtworkData) > 0 {
		// Try ssnc/PICT with encoded length
		result = append(result, encodeRawData("ssnc", "PICT", tm.ArtworkData)...)

		// ALSO try mper/PICT (another common type for artwork)
		// Uncomment if needed: result = append(result, encodeRawData("mper", "PICT", tm.ArtworkData)...)
	}

	// Progress tracking (like librespot-java)
	if tm.Duration > 0 && tm.SampleRate > 0 {
		currentTime := float64(tm.Position)
		duration := float64(tm.Duration)
		sampleRate := float64(tm.SampleRate)

		progressStr := fmt.Sprintf("1/%.0f/%.0f",
			currentTime*sampleRate/1000.0+1,
			duration*sampleRate/1000.0+1)

		result = append(result, encodeItem("ssnc", "prgr", progressStr)...)
	}

	// Playing state
	playState := "stop"
	if tm.Playing {
		playState = "play"
	} else if tm.Duration > 0 {
		playState = "pause"
	}
	result = append(result, encodeItem("ssnc", "pply", playState)...)

	// Volume (0-100)
	volumeStr := fmt.Sprintf("%d", tm.Volume)
	result = append(result, encodeItem("ssnc", "pvol", volumeStr)...)

	// Position (in seconds) - make sure we're using the actual position
	positionSec := tm.Position / 1000
	positionStr := fmt.Sprintf("%d", positionSec)
	result = append(result, encodeItem("ssnc", "ppos", positionStr)...)

	// DEBUG: Log position info
	fmt.Printf("DEBUG Position: tm.Position=%d ms, positionSec=%d s\n", tm.Position, positionSec)

	return result
}

// Helper function to convert 4-char ASCII string to hex
func asciiToHex(s string) string {
	if len(s) != 4 {
		panic("Input must be 4 characters")
	}
	return fmt.Sprintf("%02x%02x%02x%02x", s[0], s[1], s[2], s[3])
}

// Update updates the metadata with new values
func (tm *TrackMetadata) Update(title, artist, album, trackID string, duration, position int64, volume int, playing bool, artworkURL string, artworkData []byte) {
	tm.Title = title
	tm.Artist = artist
	tm.Album = album
	tm.TrackID = trackID
	tm.Duration = duration
	tm.Position = position
	tm.Volume = volume
	tm.Playing = playing
	tm.ArtworkURL = artworkURL
	tm.ArtworkData = artworkData
	tm.Timestamp = time.Now()
}

// UpdatePosition updates only the position and timestamp
func (tm *TrackMetadata) UpdatePosition(position int64) {
	tm.Position = position
	tm.Timestamp = time.Now()
}

// UpdateVolume updates only the volume and timestamp
func (tm *TrackMetadata) UpdateVolume(volume int) {
	tm.Volume = volume
	tm.Timestamp = time.Now()
}

// UpdatePlayingState updates only the playing state and timestamp
func (tm *TrackMetadata) UpdatePlayingState(playing bool) {
	tm.Playing = playing
	tm.Timestamp = time.Now()
}

// UpdateSampleRate updates the sample rate for progress tracking
func (tm *TrackMetadata) UpdateSampleRate(sampleRate float32) {
	tm.SampleRate = sampleRate
	tm.Timestamp = time.Now()
}

// encodeDACPItem encodes a string item in DACP format
func encodeDACPItem(tag, value string) []byte {
	valueBytes := []byte(value)
	length := len(valueBytes)

	result := make([]byte, 8+length)
	copy(result[0:4], tag)
	result[4] = byte(length >> 24)
	result[5] = byte(length >> 16)
	result[6] = byte(length >> 8)
	result[7] = byte(length)
	copy(result[8:], valueBytes)

	return result
}

// encodeDACPInt encodes an integer item in DACP format
func encodeDACPInt(tag string, value int) []byte {
	result := make([]byte, 12)
	copy(result[0:4], tag)
	result[4] = 0
	result[5] = 0
	result[6] = 0
	result[7] = 4
	result[8] = byte(value >> 24)
	result[9] = byte(value >> 16)
	result[10] = byte(value >> 8)
	result[11] = byte(value)

	return result
}
