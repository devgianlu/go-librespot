//go:build test_unit

package daemon

import (
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
	"github.com/stretchr/testify/mock"
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

// newVolumeTestApp is an App with a saved volume of saved (nil for none) whose
// saves are reported on the returned channel.
func newVolumeTestApp(t *testing.T, cfg *Config, saved *uint32) (*App, <-chan uint32) {
	t.Helper()

	saves := make(chan uint32, 8)
	store := librespot.NewMockStateStore(t)
	store.EXPECT().Save(mock.Anything).RunAndReturn(func(s *librespot.AppState) error {
		saves <- *s.LastVolume
		return nil
	}).Maybe()

	return &App{
		cfg:        cfg,
		log:        &librespot.NullLogger{},
		stateStore: store,
		state:      &librespot.AppState{LastVolume: saved},
	}, saves
}

func requireSaved(t *testing.T, saves <-chan uint32, want uint32) {
	t.Helper()
	select {
	case got := <-saves:
		require.Equal(t, want, got)
	case <-time.After(time.Second):
		t.Fatal("the volume was not saved")
	}
}

func TestSessionVolume(t *testing.T) {
	saved := uint32(player.MaxStateVolume / 4)
	cfg := func(ignoreLast bool) *Config {
		return &Config{VolumeSteps: 100, InitialVolume: 50, IgnoreLastVolume: ignoreLast}
	}

	app, _ := newVolumeTestApp(t, cfg(false), nil)
	require.Equal(t, uint32(player.MaxStateVolume/2), app.sessionVolume(), "nothing saved: initial_volume")

	app, _ = newVolumeTestApp(t, cfg(false), &saved)
	require.Equal(t, saved, app.sessionVolume(), "the volume an earlier run saved")

	app, saves := newVolumeTestApp(t, cfg(true), &saved)
	require.Equal(t, uint32(player.MaxStateVolume/2), app.sessionVolume(), "ignore_last_volume skips the saved one")

	set := uint32(player.MaxStateVolume / 3)
	app.setVolume(set)
	requireSaved(t, saves, set)
	require.Equal(t, set, app.sessionVolume(), "a volume set while running wins, ignore_last_volume or not")

	app, _ = newVolumeTestApp(t, &Config{VolumeSteps: 100, InitialVolume: 150}, nil)
	require.Equal(t, uint32(player.MaxStateVolume), app.sessionVolume(), "initial_volume above the steps is capped")
}

func TestVolumeFromApi(t *testing.T) {
	app := &App{cfg: &Config{VolumeSteps: 100}}
	current := uint32(player.MaxStateVolume * 37 / 100)

	cases := []struct {
		name string
		data ApiSetVolume
		want uint32
	}{
		{"absolute", ApiSetVolume{Volume: 64}, player.MaxStateVolume * 64 / 100},
		{"relative up", ApiSetVolume{Volume: 5, Relative: true}, player.MaxStateVolume * 42 / 100},
		{"relative down past zero", ApiSetVolume{Volume: -50, Relative: true}, 0},
		{"absolute above the steps", ApiSetVolume{Volume: 150}, player.MaxStateVolume},
		{"absolute below zero", ApiSetVolume{Volume: -1}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, app.volumeFromApi(tc.data, current))
		})
	}
}
