package daemon

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"net"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/dealer"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/devgianlu/go-librespot/tracks"
)

type State struct {
	active      bool
	activeSince time.Time

	device *connectpb.DeviceInfo
	player *connectpb.PlayerState

	tracks  *tracks.List
	queueID uint64

	lastCommand           *dealer.RequestPayload
	lastTransferTimestamp int64
}

// Set the IsPaused flag, and also the PlaybackSpeed as well.
// PlaybackSpeed must be 0 when paused, or Spotify Android will have subtle
// bugs.
func (s *State) setPaused(val bool) {
	s.player.IsPaused = val
	if val {
		s.player.PlaybackSpeed = 0
	} else {
		s.player.PlaybackSpeed = 1
	}
}

func (s *State) setActive(val bool) {
	if val {
		if s.active {
			return
		}

		s.active = true
		s.activeSince = time.Now()
	} else {
		s.active = false
		s.activeSince = time.Time{}
	}
}

func (s *State) reset() {
	s.active = false
	s.activeSince = time.Time{}
	s.player = &connectpb.PlayerState{
		IsSystemInitiated: true,
		PlaybackSpeed:     1,
		PlayOrigin:        &connectpb.PlayOrigin{},
		Suppressions:      &connectpb.Suppressions{},
		Options:           &connectpb.ContextPlayerOptions{},
	}
}

func (s *State) trackPosition() int64 {
	// If paused or not actually playing, use raw position value
	if s.player.IsPaused || !s.player.IsPlaying {
		return s.player.PositionAsOfTimestamp
	}

	// Calculate dynamic position only if playback is actually active
	now := time.Now().UnixMilli()
	elapsed := now - s.player.Timestamp

	// Validate timestamp freshness: if elapsed time exceeds 10 minutes (600000ms),
	// timestamp is likely stale (e.g., from a previous session), use raw position
	const maxReasonableElapsed = 10 * 60 * 1000 // 10 minutes in milliseconds
	if elapsed > maxReasonableElapsed || elapsed < 0 {
		return s.player.PositionAsOfTimestamp
	}

	calculated := s.player.PositionAsOfTimestamp + elapsed
	// Ensure position is non-negative (shouldn't happen, but defensive)
	if calculated < 0 {
		return s.player.PositionAsOfTimestamp
	}

	return calculated
}

// Update timestamp, and updating the player position timestamp according to how
// much time has passed since the last update.
func (s *State) updateTimestamp() {
	// Use single timestamp throughout, for consistency.
	now := time.Now()

	// How many milliseconds the playback has advanced since the last update to
	// PositionAsOfTimestamp.
	advancedTimeMillis := now.UnixMilli() - s.player.Timestamp

	// How far the playback position has advanced during that time.
	// (For example, PlaybackSpeed is 0 when paused so the position doesn't
	// change).
	advancedPositionMillis := int64(float64(advancedTimeMillis) * s.player.PlaybackSpeed)

	// Update the timestamps accordingly.
	s.player.PositionAsOfTimestamp += advancedPositionMillis
	s.player.Timestamp = now.UnixMilli()
}

func (s *State) playOrigin() string {
	return s.player.PlayOrigin.FeatureIdentifier
}

// deviceAddressMask reports this device's own address in CIDR form, which is
// what the official client puts in its device_address_mask metadata: the
// interface address and its prefix length, not the network address, so
// "192.168.1.20/24" rather than "192.168.1.0/24". Devices reporting the same
// subnet are the ones the backend can consider to be on a local network
// together.
//
// Only IPv4, matching the official client. Returns empty when nothing suitable
// is found, in which case the entry is omitted rather than sent blank.
func deviceAddressMask() string {
	// Which address the host would use to reach the outside. A UDP socket is
	// only bound, never sends anything, so the destination is a documentation
	// address that is never routed anywhere.
	var local net.IP
	if conn, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			local = addr.IP
		}
		_ = conn.Close()
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	// The prefix length only comes from the interface, so the address found
	// above still has to be located among them. Without a default route (or on
	// a host that failed the dial) fall back to the first candidate instead.
	var fallback string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}

			ones, _ := ipNet.Mask.Size()
			cidr := fmt.Sprintf("%s/%d", ipNet.IP.To4(), ones)
			if local != nil && ipNet.IP.Equal(local) {
				return cidr
			} else if fallback == "" {
				fallback = cidr
			}
		}
	}

	return fallback
}

func (p *AppPlayer) initState() {
	p.state = &State{
		lastCommand: nil,
		device: &connectpb.DeviceInfo{
			CanPlay:               true,
			Volume:                player.MaxStateVolume,
			Name:                  p.app.cfg.DeviceName,
			DeviceId:              p.app.deviceId,
			DeviceType:            p.app.deviceType,
			DeviceSoftwareVersion: librespot.VersionString(),
			ClientId:              librespot.ClientIdHex,
			SpircVersion:          "3.2.6",
			Brand:                 "spotify",
			Model:                 "go-librespot",
			License:               "premium",
			Capabilities: &connectpb.Capabilities{
				CanBePlayer:                true,
				RestrictToLocal:            false,
				GaiaEqConnectId:            true,
				SupportsLogout:             p.app.cfg.ZeroconfEnabled,
				IsObservable:               true,
				VolumeSteps:                int32(p.app.cfg.VolumeSteps),
				SupportedTypes:             []string{"audio/track", "audio/episode", "audio/media"},
				CommandAcks:                true,
				SupportsRename:             false,
				Hidden:                     false,
				DisableVolume:              false,
				ConnectDisabled:            false,
				SupportsPlaylistV2:         true,
				IsControllable:             true,
				SupportsExternalEpisodes:   false, // TODO: support external episodes
				SupportsSetBackendMetadata: true,
				SupportsTransferCommand:    true,
				SupportsCommandRequest:     true,
				IsVoiceEnabled:             false,
				NeedsFullPlayerState:       false,
				SupportsGzipPushes:         true,
				SupportsSetOptionsCommand:  true,
				SupportsHifi:               nil, // TODO: nice to have?
				ConnectCapabilities:        "",
				SupportsDj:                 true,
				SupportsRemoteSleepTimer:   true,
			},
		},
	}

	p.state.device.MetadataMap = map[string]string{"tier1_port": "0"}
	if mask := deviceAddressMask(); mask != "" {
		p.state.device.MetadataMap["device_address_mask"] = mask
	}

	p.state.reset()
}

// statePutMinInterval is the minimum spacing between connect-state PUTs.
const statePutMinInterval = 200 * time.Millisecond

// updateState PUTs the latest connect-state, at most one per statePutMinInterval: immediately
// and synchronously when the budget allows, else deferred to the timer so a burst coalesces.
func (p *AppPlayer) updateState(ctx context.Context) {
	p.stateDirty = true
	if p.statePutScheduled {
		return
	}
	if wait := statePutMinInterval - time.Since(p.lastStatePut); wait > 0 {
		p.statePutScheduled = true
		p.stateTimer.Reset(wait)
		return
	}
	p.flushState(ctx)
}

func contextMetadata(fromCommand, fromResolver map[string]string) map[string]string {
	metadata := make(map[string]string, len(fromCommand)+len(fromResolver))
	maps.Copy(metadata, fromCommand)
	maps.Copy(metadata, fromResolver)
	return metadata
}

func (p *AppPlayer) putConnectState(ctx context.Context, reason connectpb.PutStateReason) error {
	if reason == connectpb.PutStateReason_BECAME_INACTIVE {
		return p.sess.Spclient().PutConnectStateInactive(ctx, p.spotConnId, false)
	}

	putStateReq := &connectpb.PutStateRequest{
		ClientSideTimestamp: uint64(time.Now().UnixMilli()),
		MemberType:          connectpb.MemberType_CONNECT_STATE,
		PutStateReason:      reason,
	}

	if t := p.state.activeSince; !t.IsZero() {
		putStateReq.StartedPlayingAt = uint64(t.UnixMilli())
	}
	if t := p.player.HasBeenPlayingFor(); t > 0 {
		putStateReq.HasBeenPlayingForMs = uint64(t.Milliseconds())
	}

	putStateReq.IsActive = p.state.active
	putStateReq.Device = &connectpb.Device{
		DeviceInfo:  p.state.device,
		PlayerState: p.state.player,
	}

	if p.state.lastCommand != nil {
		putStateReq.LastCommandMessageId = p.state.lastCommand.MessageId
		putStateReq.LastCommandSentByDeviceId = p.state.lastCommand.SentByDeviceId
	}

	// finally send the state update
	cluster, err := p.sess.Spclient().PutConnectState(ctx, p.spotConnId, putStateReq)
	if err != nil {
		return err
	}

	if device := cluster.Device[p.app.deviceId]; device != nil && device.PublicIp != "" {
		p.state.device.PublicIp = device.PublicIp
	}

	return nil
}

// coverImageSizes maps the ProvidedTrack metadata keys Spotify's clients look
// for to the image size each should resolve to. Every key is filled from the
// closest size the media actually carries.
var coverImageSizes = map[string]string{
	"image_small_url":  "small",
	"image_url":        "default",
	"image_large_url":  "large",
	"image_xlarge_url": "xlarge",
}

// enrichTrackMetadata adds the metadata controllers use to draw the
// now-playing view: the title, album and artwork of the media that is actually
// loaded.
//
// Only the context resolver's own metadata reaches us through
// ContextTrackToProvidedTrack, and it never carries any of this: an album
// context arrives with nothing at all, a playlist context with bookkeeping like
// added_at. The values here come from the media fetched to play the audio.
func enrichTrackMetadata(provided *connectpb.ProvidedTrack, media *librespot.Media) {
	if provided == nil || media == nil {
		return
	}

	// ContextTrackToProvidedTrack hands over the ContextTrack's own map, so
	// copy before adding: writing in place would edit the track list too.
	metadata := make(map[string]string, len(provided.Metadata)+len(coverImageSizes)+4)
	maps.Copy(metadata, provided.Metadata)

	set := func(key, value string) {
		if len(value) > 0 {
			metadata[key] = value
		}
	}
	setUri := func(key string, typ librespot.SpotifyIdType, gid []byte) string {
		// SpotifyIdFromGid panics on a malformed gid, and metadata off the
		// wire is not worth trusting that far.
		if len(gid) != 16 {
			return ""
		}

		uri := librespot.SpotifyIdFromGid(typ, gid).Uri()
		set(key, uri)
		return uri
	}

	var covers []*metadatapb.Image
	if media.IsTrack() {
		track := media.Track()
		set("title", track.GetName())

		if album := track.GetAlbum(); album != nil {
			set("album_title", album.GetName())
			provided.AlbumUri = setUri("album_uri", librespot.SpotifyIdTypeAlbum, album.GetGid())

			covers = album.GetCover()
			if len(covers) == 0 {
				covers = album.GetCoverGroup().GetImage()
			}
		}

		if artists := track.GetArtist(); len(artists) > 0 {
			provided.ArtistUri = setUri("artist_uri", librespot.SpotifyIdTypeArtist, artists[0].GetGid())
		}
	} else {
		episode := media.Episode()
		set("title", episode.GetName())

		// An episode has no album; controllers show the show in its place,
		// which is also what the API response does.
		if show := episode.GetShow(); show != nil {
			set("album_title", show.GetName())
			provided.AlbumUri = setUri("album_uri", librespot.SpotifyIdTypeShow, show.GetGid())
		}

		covers = episode.GetCoverImage().GetImage()
	}

	for key, size := range coverImageSizes {
		if id := getBestImageIdForSize(covers, size); id != nil {
			set(key, "spotify:image:"+hex.EncodeToString(id))
		}
	}

	provided.Metadata = metadata
}
