//go:build test_unit

package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"github.com/devgianlu/go-librespot/spclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// fakePlaylistService answers the playlist endpoints creating a playlist
// touches; conflicts says how many rootlist changes answer 409 first.
type fakePlaylistService struct {
	mu        sync.Mutex
	conflicts int
	calls     []string
	added     []*playlist4pb.ListChanges
}

func (f *fakePlaylistService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/playlist/v2/playlist":
		out, _ := proto.Marshal(&playlist4pb.CreateListReply{Uri: proto.String("spotify:playlist:new")})
		_, _ = w.Write(out)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/rootlist"):
		out, _ := proto.Marshal(&playlist4pb.SelectedListContent{Revision: []byte{byte(len(f.calls))}})
		_, _ = w.Write(out)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rootlist/changes"):
		if f.conflicts > 0 {
			f.conflicts--
			w.WriteHeader(http.StatusConflict)
			return
		}
		var changes playlist4pb.ListChanges
		_ = proto.Unmarshal(body, &changes)
		f.added = append(f.added, &changes)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newFakeSpclient(t *testing.T, handler http.Handler) *spclient.Spclient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "https://")
	spc, err := spclient.NewSpclient(t.Context(), &librespot.NullLogger{}, server.Client(),
		func(context.Context) string { return host },
		func(context.Context, bool) (string, error) { return "token", nil },
		"device-id", "client-token")
	require.NoError(t, err)
	return spc
}

func TestCreatePlaylistPutsItOnTopOfTheLibrary(t *testing.T) {
	service := &fakePlaylistService{}
	uri, err := createPlaylist(t.Context(), newFakeSpclient(t, service), "user", "Road Trip")
	require.NoError(t, err)
	require.Equal(t, "spotify:playlist:new", uri)

	require.Equal(t, []string{
		"POST /playlist/v2/playlist",
		"GET /playlist/v2/user/user/rootlist",
		"POST /playlist/v2/user/user/rootlist/changes",
	}, service.calls)
	require.Len(t, service.added, 1)
	require.Equal(t, []byte{2}, service.added[0].GetBaseRevision(), "based on the revision just read")
	add := service.added[0].GetDeltas()[0].GetOps()[0].GetAdd()
	require.Equal(t, "spotify:playlist:new", add.GetItems()[0].GetUri())
	require.True(t, add.GetAddFirst())
}

func TestCreatePlaylistRetriesALibraryThatChangedOnce(t *testing.T) {
	service := &fakePlaylistService{conflicts: 1}
	_, err := createPlaylist(t.Context(), newFakeSpclient(t, service), "user", "Road Trip")
	require.NoError(t, err)

	require.Equal(t, 1, strings.Count(strings.Join(service.calls, "\n"), "POST /playlist/v2/playlist\n"), "the playlist is created once")
	require.Len(t, service.added, 1)
	require.Equal(t, []byte{4}, service.added[0].GetBaseRevision(), "based on the revision read again")
}

func TestCreatePlaylistReportsALibraryThatKeepsChanging(t *testing.T) {
	service := &fakePlaylistService{conflicts: 2}
	_, err := createPlaylist(t.Context(), newFakeSpclient(t, service), "user", "Road Trip")
	require.ErrorIs(t, err, spclient.ErrPlaylistConflict)
	require.ErrorContains(t, err, "created spotify:playlist:new")
	require.Empty(t, service.added)
}
