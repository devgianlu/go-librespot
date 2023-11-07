package tracks

import (
	librespot "go-librespot"
	connectpb "go-librespot/proto/spotify/connectstate/model"
)

func ContextTrackComparator(typ librespot.SpotifyIdType, target *connectpb.ContextTrack) func(*connectpb.ContextTrack) bool {
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
