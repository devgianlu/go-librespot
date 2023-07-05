package audio

import (
	"encoding/binary"
	"io"
	"math"
)

type ReplayGain struct {
	trackGainDb float32
	trackPeak   float32
	albumGainDb float32
	albumPeak   float32
}

func ExtractReplayGainMetadata(r io.ReaderAt, limit int64) (io.ReadSeeker, *ReplayGain, error) {
	payload := make([]byte, 16)
	if _, err := r.ReadAt(payload, 144); err != nil {
		return nil, nil, err
	}

	var replayGain ReplayGain
	replayGain.trackGainDb = math.Float32frombits(binary.LittleEndian.Uint32(payload[0:4]))
	replayGain.trackPeak = math.Float32frombits(binary.LittleEndian.Uint32(payload[4:8]))
	replayGain.albumGainDb = math.Float32frombits(binary.LittleEndian.Uint32(payload[8:12]))
	replayGain.albumPeak = math.Float32frombits(binary.LittleEndian.Uint32(payload[12:16]))
	return io.NewSectionReader(r, 167, limit-167), &replayGain, nil
}
