package login5

import (
	"crypto/sha1"
	"go-librespot/proto/google"
	challengespb "go-librespot/proto/spotify/login5/v3/challenges"
	"math/bits"
	"time"
)

func checkHashcash(hash []byte, length int) bool {
	idx := len(hash) - 1
	for idx >= 0 {
		zeros := bits.TrailingZeros8(hash[idx])
		if zeros >= length {
			return true
		} else if zeros < 8 {
			return false
		}

		length -= 8
		idx--
	}

	return false
}

func incrementHashcash(data []byte, idx int) {
	data[idx]++
	if data[idx] == 0 && idx > 0 {
		incrementHashcash(data, idx-1)
	}
}

func solveHashcash(loginContext []byte, challenge *challengespb.HashcashChallenge) *challengespb.HashcashSolution {
	loginContextSum := sha1.Sum(loginContext)

	suffix := make([]byte, 16)
	copy(suffix[0:8], loginContextSum[12:20])

	hasher := sha1.New()
	start := time.Now()
	for {
		hasher.Write(challenge.Prefix)
		hasher.Write(suffix)
		sum := hasher.Sum(nil)
		if checkHashcash(sum, int(challenge.Length)) {
			duration := time.Since(start)
			return &challengespb.HashcashSolution{
				Suffix: suffix,
				Duration: &google.Duration{
					Seconds: int64(duration / time.Second),
					Nanos:   int32(duration % time.Second),
				},
			}
		}

		incrementHashcash(suffix[0:8], 7)
		incrementHashcash(suffix[8:16], 7)
	}
}
