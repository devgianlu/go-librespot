//go:build test_unit

package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The first press of a burst loads at once, so a lone skip is as quick as it
// ever was; only a press that follows another within the window is held back.
func TestSkipDelay(t *testing.T) {
	now := time.Now()
	window := 400 * time.Millisecond

	cases := []struct {
		name     string
		debounce time.Duration
		lastSkip time.Time
		want     time.Duration
	}{
		{"disabled never delays", 0, now, 0},
		{"first skip is immediate", window, time.Time{}, 0},
		{"skip after quiet period is immediate", window, now.Add(-time.Second), 0},
		{"skip right after previous is held", window, now.Add(-100 * time.Millisecond), window},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, skipDelay(tc.debounce, now, tc.lastSkip))
		})
	}
}
