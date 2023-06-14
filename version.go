package go_librespot

import (
	"fmt"
	"runtime"
)

func VersionNumberString() string {
	// TODO: we probably want a commit hash for non-debug binaries
	return "dev"
}

func VersionString() string {
	return fmt.Sprintf("go-librespot %s", VersionNumberString())
}

func SystemInfoString() string {
	// TODO: add operating system?
	return fmt.Sprintf("%s; Go %s", VersionString(), runtime.Version())
}
