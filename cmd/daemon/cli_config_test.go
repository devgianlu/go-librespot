//go:build test_unit

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefaultAudioBackend(t *testing.T) {
	got := defaultAudioBackend()
	switch runtime.GOOS {
	case "windows":
		require.Equal(t, "wasapi", got)
	case "darwin":
		require.Equal(t, "audio-toolbox", got)
	default:
		require.Equal(t, "alsa", got)
	}
}

func TestLoadCLIConfigAudioBackend(t *testing.T) {
	defaultBackend := "alsa"
	switch runtime.GOOS {
	case "darwin":
		defaultBackend = "audio-toolbox"
	case "windows":
		defaultBackend = "wasapi"
	}
	for _, tc := range []struct {
		name, config, want string
	}{
		{"platform default", "initial_volume: 0\n", defaultBackend},
		{"explicit override", "audio_backend: pipe\ninitial_volume: 0\n", "pipe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yml"), []byte(tc.config), 0o600))
			oldArgs := os.Args
			t.Cleanup(func() { os.Args = oldArgs })
			os.Args = []string{"test", "--config_dir", dir}
			cfg := new(cliConfig)
			require.NoError(t, loadCLIConfig(cfg))
			t.Cleanup(func() {
				if cfg.configLock != nil {
					require.NoError(t, cfg.configLock.Unlock())
				}
			})
			require.Equal(t, tc.want, cfg.AudioBackend)
			require.Zero(t, cfg.InitialVolume, "explicit mute must survive default config merging")
		})
	}
}

func TestSkipDebounceMapping(t *testing.T) {
	var c cliConfig
	c.SkipDebounceMs = 400
	require.Equal(t, 400*time.Millisecond, c.toDaemonConfig().SkipDebounce)

	c.SkipDebounceMs = 0
	require.Zero(t, c.toDaemonConfig().SkipDebounce)
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"1GB", 1 << 30, false},
		{"1gb", 1 << 30, false},
		{"500MB", 500 << 20, false},
		{"512KB", 512 << 10, false},
		{"2TB", 2 << 40, false},
		{"1024", 1024, false},
		{"1024B", 1024, false},
		{"1.5GB", 1610612736, false},
		{" 256MB ", 256 << 20, false},
		{"abc", 0, true},
		{"-1GB", 0, true},
		{"GB", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSize(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestLoadCLIConfigWaitForReaderFlag(t *testing.T) {
	dir := t.TempDir()

	config := []byte("audio_backend: pipe\naudio_output_pipe: /tmp/fifo/go-spotify\naudio_output_pipe_wait_for_reader: true\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), config, 0o600))

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"test", "--config_dir", dir}

	cfg := &cliConfig{}
	require.NoError(t, loadCLIConfig(cfg))
	t.Cleanup(func() {
		if cfg.configLock != nil {
			_ = cfg.configLock.Unlock()
		}
	})
	require.True(t, cfg.AudioOutputPipeWaitForReader, "audio_output_pipe_wait_for_reader was not parsed from the config file")
}
