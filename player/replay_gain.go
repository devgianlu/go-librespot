package player

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"mccoy.space/g/ogg"
)

type ReplayGain struct {
	trackGainDb float32
	trackPeak   float32
	albumGainDb float32
	albumPeak   float32
}

func readReplayGainMetadata(r io.Reader) (*ReplayGain, error) {
	oggPage, err := ogg.NewDecoder(r).Decode()
	if err != nil {
		return nil, fmt.Errorf("failed decoding ogg metadata page: %w", err)
	} else if oggPage.Type != 0x06 || len(oggPage.Packets) != 1 {
		return nil, fmt.Errorf("invalid ogg metadata page, type: %d, len: %d", oggPage.Type, len(oggPage.Packets))
	}

	payload := oggPage.Packets[0]

	var replayGain ReplayGain
	replayGain.trackGainDb = math.Float32frombits(binary.LittleEndian.Uint32(payload[116:120]))
	replayGain.trackPeak = math.Float32frombits(binary.LittleEndian.Uint32(payload[120:124]))
	replayGain.albumGainDb = math.Float32frombits(binary.LittleEndian.Uint32(payload[124:128]))
	replayGain.albumPeak = math.Float32frombits(binary.LittleEndian.Uint32(payload[128:132]))
	return &replayGain, nil
}
