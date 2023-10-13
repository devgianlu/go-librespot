package audio

import (
	"encoding/binary"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"io"
	"math"
)

type ReplayGain struct {
	trackGainDb float32
	trackPeak   float32
	albumGainDb float32
	albumPeak   float32
}

func ExtractReplayGainMetadata(r io.ReaderAt, limit int64) (librespot.SizedReadAtSeeker, *ReplayGain, error) {
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

func (rg ReplayGain) GetTrackFactor(normalisationPregain float32) float32 {
	normalisationFactor := float32(math.Pow(10, float64((rg.trackGainDb+normalisationPregain)/20)))
	if normalisationFactor*rg.trackPeak > 1 {
		log.Warn("reducing track normalisation factor to prevent clipping, please add negative pregain to avoid")
		normalisationFactor = 1 / rg.trackPeak
	}

	return normalisationFactor
}

func (rg ReplayGain) GetAlbumFactor(normalisationPregain float32) float32 {
	normalisationFactor := float32(math.Pow(10, float64((rg.albumGainDb+normalisationPregain)/20)))
	if normalisationFactor*rg.albumPeak > 1 {
		log.Warn("reducing album normalisation factor to prevent clipping, please add negative pregain to avoid")
		normalisationFactor = 1 / rg.albumPeak
	}

	return normalisationFactor
}
