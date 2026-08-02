package spclient

import (
	"context"
	"fmt"
	"io"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	resumptionpb "github.com/devgianlu/go-librespot/proto/spotify/resumption/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The resumption services live behind the herodotus gRPC gateway. Bodies are
// bare protobuf rather than gRPC-framed, so they go through Request like any
// other spclient endpoint.
const (
	listCurrentStatesPath         = "/herodotus/spotify.resumption.v1.CurrentStateService/ListCurrentStates"
	createResumePointRevisionPath = "/herodotus/spotify.resumption.v1.ResumePointRevisionService/CreateResumePointRevision"
)

func (c *Spclient) ListCurrentStates(ctx context.Context, reqProto *resumptionpb.ListCurrentStatesRequest) (*resumptionpb.ListCurrentStatesResponse, error) {
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling ListCurrentStatesRequest: %w", err)
	}

	resp, err := c.Request(ctx, "POST", listCurrentStatesPath, nil, nil, reqBody)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from list current states: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var respProto resumptionpb.ListCurrentStatesResponse
	if err := proto.Unmarshal(respBytes, &respProto); err != nil {
		return nil, fmt.Errorf("failed unmarshalling ListCurrentStatesResponse: %w", err)
	}

	return &respProto, nil
}

func (c *Spclient) CreateResumePointRevision(ctx context.Context, reqProto *resumptionpb.CreateResumePointRevisionRequest) (*resumptionpb.CreateResumePointRevisionResponse, error) {
	reqBody, err := proto.Marshal(reqProto)
	if err != nil {
		return nil, fmt.Errorf("failed marshalling CreateResumePointRevisionRequest: %w", err)
	}

	resp, err := c.Request(ctx, "POST", createResumePointRevisionPath, nil, nil, reqBody)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid status code from create resume point revision: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response body: %w", err)
	}

	var respProto resumptionpb.CreateResumePointRevisionResponse
	if err := proto.Unmarshal(respBytes, &respProto); err != nil {
		return nil, fmt.Errorf("failed unmarshalling CreateResumePointRevisionResponse: %w", err)
	}

	return &respProto, nil
}

// ResumePositionMs returns where playback of the given entity should start,
// in milliseconds. Zero means "from the beginning": either the backend has
// never seen the entity, or it was listened to the end and the next play is
// meant to restart it.
//
// Only episodes and audiobook chapters have resume points; callers should not
// bother asking for tracks.
func (c *Spclient) ResumePositionMs(ctx context.Context, id librespot.SpotifyId) (int64, error) {
	// The filter is a CEL expression evaluated over the CurrentState, bound as
	// "cs". A SpotifyId always renders as spotify:<type>:<base62>, so it cannot
	// escape the string literal.
	resp, err := c.ListCurrentStates(ctx, &resumptionpb.ListCurrentStatesRequest{
		PageSize: 1,
		Filter:   fmt.Sprintf("cs.uri == %q", id.Uri()),
	})
	if err != nil {
		return 0, err
	}

	return resumePositionFromStates(id.Uri(), resp.CurrentStates), nil
}

// resumePositionFromStates picks the resume position for uri out of a
// ListCurrentStates response.
func resumePositionFromStates(uri string, states []*resumptionpb.CurrentState) int64 {
	for _, state := range states {
		if state.Uri != uri {
			continue
		}

		// The backend keeps several revisions per entity — one per kind of
		// resume point it has been told about — so the current state is the
		// most recently updated of them, not the first.
		var latest *resumptionpb.ResumePointRevision
		for _, rev := range state.ResumePointRevisions {
			if latest == nil || rev.UpdateTime.AsTime().After(latest.UpdateTime.AsTime()) {
				latest = rev
			}
		}

		if latest == nil {
			continue
		}

		switch point := latest.Snapshot.GetResumePoint().(type) {
		case *resumptionpb.ResumePoint_Position:
			return point.Position.AsDuration().Milliseconds()
		case *resumptionpb.ResumePoint_RelativePosition:
			return point.RelativePosition.StartOffset.AsDuration().Milliseconds()
		default:
			// started/finished/marked_as_finished and the entity-specific
			// variants carry no position to resume from.
			return 0
		}
	}

	return 0
}

// SetResumePositionMs stores how far into the given entity playback has got.
func (c *Spclient) SetResumePositionMs(ctx context.Context, id librespot.SpotifyId, positionMs int64) error {
	// The official client rounds to whole seconds; match it so revisions
	// written from here look the same as the ones it writes.
	position := durationpb.New(time.Duration(positionMs/1000) * time.Second)

	return c.createResumePoint(ctx, id, &resumptionpb.ResumePoint{
		ResumePoint: &resumptionpb.ResumePoint_Position{Position: position},
	})
}

// SetResumeFinished marks the given entity as listened to the end, so the next
// play starts it over rather than resuming a position near the end.
func (c *Spclient) SetResumeFinished(ctx context.Context, id librespot.SpotifyId) error {
	return c.createResumePoint(ctx, id, &resumptionpb.ResumePoint{
		ResumePoint: &resumptionpb.ResumePoint_Finished{Finished: &emptypb.Empty{}},
	})
}

func (c *Spclient) createResumePoint(ctx context.Context, id librespot.SpotifyId, point *resumptionpb.ResumePoint) error {
	// user_id, resume_point_revision_id and resume_point_id are all filled in
	// by the backend from the bearer token and the uri; the official client
	// leaves them empty too.
	_, err := c.CreateResumePointRevision(ctx, &resumptionpb.CreateResumePointRevisionRequest{
		Uri: id.Uri(),
		ResumePointRevision: &resumptionpb.ResumePointRevision{
			Snapshot:   point,
			CreateTime: timestamppb.Now(),
		},
	})
	return err
}
