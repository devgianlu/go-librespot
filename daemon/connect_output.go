package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/devgianlu/go-librespot/spclient"
)

// The daemon can also act as a controller of the account's other Spotify
// Connect devices. It keeps the latest Cluster the connect-state service sent
// (as the reply to a state push, or in a ClusterUpdate), lists it, and asks the
// service to forward transfers and commands to another device.

// ErrRemote reports that the connect-state service refused a request aimed at
// another device.
var ErrRemote = errors.New("connect-state request refused")

// connectControlTimeout bounds a request made to the connect-state service for
// another device.
const connectControlTimeout = 15 * time.Second

// maxRemoteVolume is the top of the volume scale of the Connect protocol.
const maxRemoteVolume = 65535

// setCluster records a cluster from the service. Runs on the Run goroutine.
func (p *AppPlayer) setCluster(c *connectpb.Cluster) {
	if c == nil {
		return
	}
	p.cluster = c
	p.clusterAt = time.Now()
}

// connectDevices describes the latest cluster. It returns nil when none was
// received yet. Runs on the Run goroutine.
func (p *AppPlayer) connectDevices(now time.Time) *ApiConnectDevices {
	c := p.cluster
	if c == nil {
		return nil
	}

	resp := &ApiConnectDevices{ActiveDeviceId: c.GetActiveDeviceId(), Devices: []ApiConnectDevice{}}
	for id, d := range c.GetDevice() {
		if d == nil {
			continue
		}
		resp.Devices = append(resp.Devices, ApiConnectDevice{
			Id:          id,
			Name:        d.GetName(),
			Type:        strings.ToLower(d.GetDeviceType().String()),
			Volume:      int32(d.GetVolume()),
			IsSelf:      id == p.app.deviceId,
			IsActive:    id == c.GetActiveDeviceId(),
			IsOffline:   d.GetIsOffline(),
			CanTransfer: len(d.GetDisallowTransferReasons()) == 0,
		})
	}

	// A stable order keeps the list from jumping around between polls.
	sort.Slice(resp.Devices, func(i, j int) bool { return resp.Devices[i].Id < resp.Devices[j].Id })

	if ps := c.GetPlayerState(); ps != nil && ps.GetTrack().GetUri() != "" {
		meta := ps.GetTrack().GetMetadata()
		position := ps.GetPositionAsOfTimestamp()
		if ps.GetIsPlaying() && !ps.GetIsPaused() && ps.GetTimestamp() > 0 {
			speed := ps.GetPlaybackSpeed()
			if speed == 0 {
				speed = 1
			}
			if elapsed := now.UnixMilli() - ps.GetTimestamp(); elapsed > 0 {
				position += int64(float64(elapsed) * speed)
			}
		}
		if d := ps.GetDuration(); d > 0 && position > d {
			position = d
		}

		resp.ApiConnectNowPlaying = &ApiConnectNowPlaying{
			ContextUri: ps.GetContextUri(),
			TrackUri:   ps.GetTrack().GetUri(),
			TrackName:  meta["title"],
			ArtistName: meta["artist_name"],
			AlbumName:  meta["album_title"],
			Paused:     ps.GetIsPaused(),
			Playing:    ps.GetIsPlaying() && !ps.GetIsPaused(),
			Position:   position,
			Duration:   ps.GetDuration(),
		}
	}

	return resp
}

// remoteDevice checks that id names a device of the latest cluster, other than
// this one unless allowSelf: a transfer may bring the playback here.
func (p *AppPlayer) remoteDevice(id string, allowSelf bool) error {
	if id == "" || (id == p.app.deviceId && !allowSelf) {
		return ErrBadRequest
	}
	if p.cluster == nil || p.cluster.GetDevice()[id] == nil {
		return ErrNotFound
	}
	return nil
}

// connectControlRequest decides what to send for an API request: the request
// kind, the HTTP method and the body.
func connectControlRequest(req ApiRequest) (method, kind string, body any, err error) {
	switch data := req.Data.(type) {
	case ApiConnectTransfer:
		options := map[string]any{"restore_paused": "restore"}
		if data.Paused {
			options["restore_paused"] = "pause"
		}
		return http.MethodPost, "connect/transfer", map[string]any{"transfer_options": options}, nil
	case ApiConnectCommand:
		switch data.Endpoint {
		case "pause", "resume", "skip_next", "skip_prev":
			return http.MethodPost, "player/command", map[string]any{"command": map[string]any{"endpoint": string(data.Endpoint)}}, nil
		case "seek_to":
			if data.Position < 0 {
				return "", "", nil, ErrBadRequest
			}
			return http.MethodPost, "player/command", map[string]any{"command": map[string]any{"endpoint": "seek_to", "value": data.Position}}, nil
		case "set_volume":
			if data.Volume < 0 || data.Volume > maxRemoteVolume {
				return "", "", nil, ErrBadRequest
			}
			return http.MethodPut, "connect/volume", map[string]any{"volume": data.Volume}, nil
		case "play":
			if !strings.HasPrefix(data.Uri, "spotify:") {
				return "", "", nil, ErrBadRequest
			}
			skipTo := map[string]any{}
			if data.SkipToUri != "" {
				if !strings.HasPrefix(data.SkipToUri, "spotify:") {
					return "", "", nil, ErrBadRequest
				}
				skipTo["track_uri"] = data.SkipToUri
			}
			options := map[string]any{"license": "tft", "skip_to": skipTo, "player_options_override": map[string]any{}}
			if data.Paused {
				options["initially_paused"] = true
			}
			return http.MethodPost, "player/command", map[string]any{"command": map[string]any{
				"endpoint":    "play",
				"context":     map[string]any{"uri": data.Uri, "url": "context://" + data.Uri, "metadata": map[string]any{}},
				"play_origin": map[string]any{"feature_identifier": "harmony", "feature_version": "go-librespot"},
				"options":     options,
			}}, nil
		}
	}
	return "", "", nil, ErrBadRequest
}

// handleConnectControl answers an API request to transfer to, or command,
// another device. The service call runs off the player loop.
func (p *AppPlayer) handleConnectControl(req ApiRequest) (any, error) {
	var deviceId string
	_, isTransfer := req.Data.(ApiConnectTransfer)
	switch data := req.Data.(type) {
	case ApiConnectTransfer:
		deviceId = data.DeviceId
	case ApiConnectCommand:
		deviceId = data.DeviceId
	}

	method, kind, body, err := connectControlRequest(req)
	if err != nil {
		return nil, err
	}
	if err := p.remoteDevice(deviceId, isTransfer); err != nil {
		return nil, err
	}

	spc := p.sess.Spclient()
	spotConnId := p.spotConnId
	reply := apiReply(req)
	p.goDetached(connectControlTimeout, func(ctx context.Context) {
		if _, err := spc.ConnectControl(ctx, method, kind, spotConnId, deviceId, body); err != nil {
			var remote *spclient.RemoteError
			if errors.As(err, &remote) {
				p.app.log.Warnf("connect-state %s refused with status %d", kind, remote.Status)
				err = fmt.Errorf("%w: status %d", ErrRemote, remote.Status)
			}
			reply.done(nil, err)
			return
		}
		reply.done(nil, nil)
	})

	return nil, errReplyDeferred
}
