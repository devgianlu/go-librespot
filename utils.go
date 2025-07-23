package go_librespot

import "strings"

func ObfuscateUsername(username string) string {
	if strings.Contains(username, "@") {
		parts := strings.SplitN(username, "@", 2)
		if len(parts) == 2 {
			return ObfuscateUsername(parts[0]) + "@" + parts[1]
		}
	}

	if len(username) < 5 {
		return username
	}

	return username[:2] + strings.Repeat("*", len(username)-4) + username[len(username)-2:]
}
