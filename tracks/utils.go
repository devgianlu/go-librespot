package tracks

import (
	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
)

func ContextTrackComparator(typ librespot.SpotifyIdType, target *connectpb.ContextTrack) func(*connectpb.ContextTrack) bool {
	targetUri := target.Uri
	if len(targetUri) == 0 && len(target.Gid) > 0 {
		targetUri = librespot.SpotifyIdFromGid(typ, target.Gid).Uri()
	}

	return func(track *connectpb.ContextTrack) bool {
		if len(track.Uid) > 0 && track.Uid == target.Uid {
			return true
		} else if len(track.Uri) > 0 && track.Uri == targetUri {
			return true
		} else if len(track.Gid) > 0 && librespot.SpotifyIdFromGid(typ, track.Gid).Uri() == targetUri {
			return true
		} else {
			return false
		}
	}
}

func ProvidedTrackComparator(typ librespot.SpotifyIdType, target *connectpb.ProvidedTrack) func(*connectpb.ContextTrack) bool {
	return func(track *connectpb.ContextTrack) bool {
		if len(track.Uid) > 0 && track.Uid == target.Uid {
			return true
		} else if len(track.Uri) > 0 && track.Uri == target.Uri {
			return true
		} else if len(track.Gid) > 0 && librespot.SpotifyIdFromGid(typ, track.Gid).Uri() == target.Uri {
			return true
		} else {
			return false
		}
	}
}
