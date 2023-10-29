package player

import (
	librespot "go-librespot"
	metadatapb "go-librespot/proto/spotify/metadata"
	"strings"
)

func isMediaRestricted(media *librespot.Media, country string) bool {
	if len(country) != 2 {
		return false
	}

	contains := func(list string) bool {
		for i := 0; i < len(list); i += 2 {
			if strings.EqualFold(list[i:i+2], country) {
				return true
			}
		}
		return false
	}

	for _, res := range media.Restriction() {
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
