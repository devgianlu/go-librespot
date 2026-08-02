//go:build test_unit

package spclient

import (
	"testing"
	"time"

	resumptionpb "github.com/devgianlu/go-librespot/proto/spotify/resumption/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const testEpisodeUri = "spotify:episode:0Jv8TUEkzMplSPfX3ynBXu"

func revision(updatedAt time.Time, point any) *resumptionpb.ResumePointRevision {
	snapshot := &resumptionpb.ResumePoint{ResumePointId: testEpisodeUri}

	switch p := point.(type) {
	case time.Duration:
		snapshot.ResumePoint = &resumptionpb.ResumePoint_Position{Position: durationpb.New(p)}
	case string:
		switch p {
		case "finished":
			snapshot.ResumePoint = &resumptionpb.ResumePoint_Finished{Finished: &emptypb.Empty{}}
		case "started":
			snapshot.ResumePoint = &resumptionpb.ResumePoint_Started{Started: &emptypb.Empty{}}
		case "marked_as_finished":
			snapshot.ResumePoint = &resumptionpb.ResumePoint_MarkedAsFinished{MarkedAsFinished: &emptypb.Empty{}}
		default:
			panic("unknown resume point " + p)
		}
	default:
		panic("unknown resume point type")
	}

	return &resumptionpb.ResumePointRevision{
		Snapshot:   snapshot,
		UpdateTime: timestamppb.New(updatedAt),
	}
}

func states(revisions ...*resumptionpb.ResumePointRevision) []*resumptionpb.CurrentState {
	return []*resumptionpb.CurrentState{{Uri: testEpisodeUri, ResumePointRevisions: revisions}}
}

func TestResumePositionFromStates(t *testing.T) {
	base := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		name   string
		states []*resumptionpb.CurrentState
		want   int64
	}{
		{
			name:   "no states at all",
			states: nil,
			want:   0,
		},
		{
			name:   "entity has no revisions",
			states: states(),
			want:   0,
		},
		{
			name:   "a position resumes there",
			states: states(revision(base, 5*time.Minute+25*time.Second)),
			want:   325_000,
		},
		{
			name:   "finished restarts from the beginning",
			states: states(revision(base, "finished")),
			want:   0,
		},
		{
			name:   "marked as finished restarts from the beginning",
			states: states(revision(base, "marked_as_finished")),
			want:   0,
		},
		{
			name:   "started carries no position",
			states: states(revision(base, "started")),
			want:   0,
		},
		{
			// The backend returns every kind of revision it holds, in no
			// particular order; only the newest describes the current state.
			name: "newest revision wins over an older position",
			states: states(
				revision(base, 5*time.Minute),
				revision(base.Add(time.Minute), 10*time.Minute),
			),
			want: 600_000,
		},
		{
			name: "a later finished supersedes an earlier position",
			states: states(
				revision(base, 30*time.Minute),
				revision(base.Add(time.Minute), "finished"),
			),
			want: 0,
		},
		{
			name: "an earlier finished does not supersede a later position",
			states: states(
				revision(base.Add(time.Minute), 30*time.Second),
				revision(base, "finished"),
			),
			want: 30_000,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, resumePositionFromStates(testEpisodeUri, tt.states))
		})
	}
}

func TestResumePositionFromStatesIgnoresOtherEntities(t *testing.T) {
	other := &resumptionpb.CurrentState{
		Uri:                  "spotify:episode:1111111111111111111111",
		ResumePointRevisions: []*resumptionpb.ResumePointRevision{revision(time.Now(), 9*time.Minute)},
	}

	require.Zero(t, resumePositionFromStates(testEpisodeUri, []*resumptionpb.CurrentState{other}))
}

func TestResumePositionFromStatesRelativePosition(t *testing.T) {
	rev := &resumptionpb.ResumePointRevision{
		Snapshot: &resumptionpb.ResumePoint{
			ResumePointId: testEpisodeUri,
			ResumePoint: &resumptionpb.ResumePoint_RelativePosition{
				RelativePosition: &resumptionpb.RelativePosition{
					StartOffset:     durationpb.New(90 * time.Second),
					ContentDuration: durationpb.New(30 * time.Minute),
				},
			},
		},
		UpdateTime: timestamppb.Now(),
	}

	require.EqualValues(t, 90_000, resumePositionFromStates(testEpisodeUri, states(rev)))
}
