package player

import (
	"fmt"
	metadatapb "go-librespot/proto/spotify/metadata"
	"strings"
)

func isTrackRestricted(track *metadatapb.Track, country string) bool {
	if len(country) != 2 {
		panic(fmt.Sprintf("invalid country code: %s", country))
	}

	contains := func(list string) bool {
		for i := 0; i < len(list); i += 2 {
			if strings.EqualFold(list[i:i+2], country) {
				return true
			}
		}
		return false
	}

	for _, res := range track.Restriction {
		switch ress := res.CountryRestriction.(type) {
		case *metadatapb.Restriction_CountriesAllowed:
			if len(ress.CountriesAllowed) == 0 {
				return true
			}

			return !contains(ress.CountriesAllowed)
		case *metadatapb.Restriction_CountriesForbidden:
			return contains(ress.CountriesForbidden)
		default:
			panic("unexpected country restriction")
		}
	}

	return false
}
