package go_librespot

import (
	"fmt"
	"runtime"
	"strings"
)

var commit, version string

func VersionNumberString() string {
	if len(version) > 0 {
		return strings.TrimPrefix(version, "v")
	} else if len(commit) >= 8 {
		return commit[:8]
	} else {
		return "dev"
	}
}

func SpotifyLikeClientVersion() string {
	if len(version) > 0 {
		if len(commit) >= 8 {
			return fmt.Sprintf("%s.g%s", version, commit[:8])
		} else {
			return version
		}
	}

	return "0.0.0"
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
