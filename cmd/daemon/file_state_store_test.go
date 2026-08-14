//go:build test_unit

package main

import (
	"path/filepath"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/stretchr/testify/require"
)

func TestFileStateStoreSaveReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStateStore(filepath.Join(dir, "state.json"), filepath.Join(dir, "credentials.json"), &librespot.NullLogger{})

	first := &librespot.AppState{}
	first.DeviceId = "device-1"
	require.NoError(t, store.Save(first))

	loaded, err := store.Load()
	require.NoError(t, err)
	require.Equal(t, "device-1", loaded.DeviceId)

	second := &librespot.AppState{}
	second.DeviceId = "device-2"
	require.NoError(t, store.Save(second))

	loaded, err = store.Load()
	require.NoError(t, err)
	require.Equal(t, "device-2", loaded.DeviceId)
}
