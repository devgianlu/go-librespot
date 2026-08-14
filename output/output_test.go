//go:build test_unit

package output

import (
	"runtime"
	"strings"
	"testing"
)

func TestWasapiBackendUnsupportedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("wasapi is supported on Windows")
	}
	_, err := NewOutput(&NewOutputOptions{Backend: "wasapi"})
	if err == nil {
		t.Fatal("expected wasapi to be unsupported off Windows")
	}
	if !strings.Contains(err.Error(), "only supported on Windows") {
		t.Fatalf("wasapi error = %q, want only supported on Windows", err)
	}
}

func TestAlsaBackendUnsupportedOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("alsa is supported on this platform")
	}
	_, err := NewOutput(&NewOutputOptions{Backend: "alsa"})
	if err == nil {
		t.Fatal("expected alsa to be unsupported on Windows")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("alsa error = %q, want not supported", err)
	}
}

func TestPipeBackendUnsupportedOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("pipe is supported on this platform")
	}
	_, err := NewOutput(&NewOutputOptions{Backend: "pipe"})
	if err == nil {
		t.Fatal("expected pipe to be unsupported on Windows")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("pipe error = %q, want not supported", err)
	}
}
