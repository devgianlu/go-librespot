package go_librespot

import (
	"fmt"
	"runtime"
)

var (
	commit  = ""
	version = ""
)

func VersionNumberString() string {
	if len(version) > 0 {
		return version
	} else if len(commit) > 0 {
		return commit
	} else {
		return "dev"
	}
}

func VersionString() string {
	return fmt.Sprintf("go-librespot %s", VersionNumberString())
}

func SystemInfoString() string {
	return fmt.Sprintf("%s; Go %s (%s %s)", VersionString(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func UserAgent() string {
	return fmt.Sprintf("go-librespot/%s Go/%s", VersionNumberString(), runtime.Version())
}
