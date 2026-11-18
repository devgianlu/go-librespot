package main

import (
	"errors"
	"os"
	"runtime"
)

// UserStateDir is not included in the `os` package.
// It returns the default root directory to use for user-specific
// state data. Users should create their own application-specific subdirectory
// within this one and use that.
//
// On Unix systems, it returns $XDG_STATE_HOME as specified by
// https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html if
// non-empty, else $HOME/.local/state.
// On other systems it returns os.UserConfigDir
//
// If the location cannot be determined (for example, $HOME is not defined)
// then it will return an error.
func UserStateDir() (string, error) {
	var dir string

	switch runtime.GOOS {
	case "windows", "darwin", "ios", "plan9":
		return os.UserConfigDir()
	default: // Unix, as UserConfigDir
		dir = os.Getenv("XDG_STATE_HOME")
		if dir == "" {
			dir = os.Getenv("HOME")
			if dir == "" {
				return "", errors.New("neither $XDG_STATE_HOME nor $HOME are defined")
			}
			dir += "/.local/state"
		}
	}

	return dir, nil
}
