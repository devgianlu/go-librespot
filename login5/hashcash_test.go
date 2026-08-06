//go:build test_unit

package login5

import (
	"crypto/sha1"
	"testing"

	challengespb "github.com/devgianlu/go-librespot/proto/spotify/login5/v3/challenges"
	"github.com/stretchr/testify/require"
)

// The challenge counts trailing zero *bits* from the end of the digest, so the
// check walks backwards a byte at a time.
func TestCheckHashcash(t *testing.T) {
	for _, tt := range []struct {
		name   string
		hash   []byte
		length int
		want   bool
	}{
		{"zero length always passes", []byte{0xff}, 0, true},
		{"one trailing zero bit", []byte{0xfe}, 1, true},
		{"not enough trailing zero bits", []byte{0xfe}, 2, false},
		{"a full zero byte is eight bits", []byte{0xff, 0x00}, 8, true},
		{"eight bits plus one spans two bytes", []byte{0xfe, 0x00}, 9, true},
		{"spanning bytes falls short", []byte{0xff, 0x00}, 9, false},
		{"two zero bytes", []byte{0xff, 0x00, 0x00}, 16, true},
		{"all zero bytes still run out", []byte{0x00}, 9, false},
		{"empty hash cannot satisfy", []byte{}, 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, checkHashcash(tt.hash, tt.length))
		})
	}
}

// The suffix is a pair of big-endian counters incremented from the last byte,
// so a carry has to ripple left.
func TestIncrementHashcash(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   []byte
		want []byte
	}{
		{"simple increment", []byte{0x00, 0x00}, []byte{0x00, 0x01}},
		{"carry into the next byte", []byte{0x00, 0xff}, []byte{0x01, 0x00}},
		{"carry ripples across bytes", []byte{0x00, 0xff, 0xff}, []byte{0x01, 0x00, 0x00}},
		{"wraps around at the top", []byte{0xff, 0xff}, []byte{0x00, 0x00}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := append([]byte(nil), tt.in...)
			incrementHashcash(data, len(data)-1)
			require.Equal(t, tt.want, data)
		})
	}
}

// The solution has to actually satisfy the challenge it was produced for; a
// short length keeps this fast while still exercising the search loop.
func TestSolveHashcashProducesAValidSolution(t *testing.T) {
	challenge := &challengespb.HashcashChallenge{
		Prefix: []byte("prefix"),
		Length: 8,
	}
	loginContext := []byte("some login context")

	solution := solveHashcash(loginContext, challenge)

	require.NotNil(t, solution)
	require.Len(t, solution.Suffix, 16)

	hasher := sha1.New()
	hasher.Write(challenge.Prefix)
	hasher.Write(solution.Suffix)
	require.True(t, checkHashcash(hasher.Sum(nil), int(challenge.Length)),
		"the returned suffix must satisfy the challenge")
}

// The first eight suffix bytes are seeded from the login context digest, which
// is what ties a solution to its login attempt. A zero length challenge is
// satisfied on the first check, so the seed is still untouched by the search's
// increments and can be compared directly.
func TestSolveHashcashSeedsSuffixFromLoginContext(t *testing.T) {
	challenge := &challengespb.HashcashChallenge{Prefix: []byte("prefix"), Length: 0}
	loginContext := []byte("some login context")

	solution := solveHashcash(loginContext, challenge)

	sum := sha1.Sum(loginContext)
	require.Equal(t, sum[12:20], solution.Suffix[0:8],
		"the first half of the suffix is the login context digest tail")
	require.Equal(t, make([]byte, 8), solution.Suffix[8:16],
		"the second counter starts at zero")
}

func TestSolveHashcashReportsDuration(t *testing.T) {
	challenge := &challengespb.HashcashChallenge{Prefix: []byte("prefix"), Length: 8}

	solution := solveHashcash([]byte("ctx"), challenge)

	require.NotNil(t, solution.Duration)
	require.GreaterOrEqual(t, solution.Duration.Seconds, int64(0))
	require.GreaterOrEqual(t, solution.Duration.Nanos, int32(0))
}
