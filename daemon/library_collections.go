package daemon

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	collectionpb "github.com/devgianlu/go-librespot/proto/spotify/collection/v2"
	extmetadatapb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/devgianlu/go-librespot/spclient"
	"google.golang.org/protobuf/types/known/anypb"
)

// extendedMetadataBatch caps how many entities one extended metadata request
// asks for.
const extendedMetadataBatch = 100

// collectionPageSize is how many items are asked for per collection page.
const collectionPageSize = 500

// collectionMaxPages caps the pages read for one set, in case the service
// keeps handing out page tokens.
const collectionMaxPages = 100

// pageBounds clamps a page of limit items at offset to a list of n items.
func pageBounds(n, offset, limit int) (start, end int) {
	start = min(offset, n)
	return start, min(start+limit, n)
}

// fetchCollectionUris pages through a collection set and returns the URIs of
// the given type, most recently added first.
func fetchCollectionUris(ctx context.Context, spc *spclient.Spclient, username, set, prefix string) ([]string, error) {
	var items []*collectionpb.CollectionItem
	token := ""
	for page := 0; ; page++ {
		if page == collectionMaxPages {
			return nil, fmt.Errorf("collection set %s still paging after %d pages", set, collectionMaxPages)
		}

		resp, err := spc.CollectionPage(ctx, username, set, token, collectionPageSize)
		if err != nil {
			return nil, err
		}

		items = append(items, resp.GetItems()...)
		token = resp.GetNextPageToken()
		if token == "" {
			return newestUris(items, prefix), nil
		}
	}
}

// newestUris returns the URIs of the items with the given prefix that are not
// removed, most recently added first.
func newestUris(items []*collectionpb.CollectionItem, prefix string) []string {
	var kept []*collectionpb.CollectionItem
	for _, item := range items {
		if !item.GetIsRemoved() && strings.HasPrefix(item.GetUri(), prefix) {
			kept = append(kept, item)
		}
	}

	slices.SortStableFunc(kept, func(a, b *collectionpb.CollectionItem) int {
		return cmp.Compare(b.GetAddedAt(), a.GetAddedAt())
	})

	uris := make([]string, len(kept))
	for i, item := range kept {
		uris[i] = item.GetUri()
	}
	return uris
}

// fetchEntityMetadata reads extended metadata of one kind for uris in batches
// and hands every entity that resolved to decode.
func fetchEntityMetadata(ctx context.Context, spc *spclient.Spclient, uris []string, kind extmetadatapb.ExtensionKind, decode func(uri string, data *anypb.Any)) error {
	for start := 0; start < len(uris); start += extendedMetadataBatch {
		req := &extmetadatapb.BatchedEntityRequest{}
		for _, uri := range uris[start:min(start+extendedMetadataBatch, len(uris))] {
			req.EntityRequest = append(req.EntityRequest, &extmetadatapb.EntityRequest{
				EntityUri: uri,
				Query:     []*extmetadatapb.ExtensionQuery{{ExtensionKind: kind}},
			})
		}

		resp, err := spc.ExtendedMetadata(ctx, req)
		if err != nil {
			return err
		}

		for _, item := range resp.GetExtendedMetadata() {
			if item.GetExtensionKind() != kind {
				continue
			}
			for _, ext := range item.GetExtensionData() {
				if ext.GetHeader().GetStatusCode() == 200 && ext.GetExtensionData() != nil {
					decode(ext.GetEntityUri(), ext.GetExtensionData())
				}
			}
		}
	}
	return nil
}

// imageUrlFunc turns a list of image variants into the URL of the configured
// size, or nil.
type imageUrlFunc func(images []*metadatapb.Image) *string

// imageUrlFunc returns how this player turns image variants into a URL: the
// configured size, through the product info's image URL template.
func (p *AppPlayer) imageUrlFunc() imageUrlFunc {
	prodInfo, size := p.prodInfo, p.app.cfg.ImageSize
	return func(images []*metadatapb.Image) *string {
		if prodInfo == nil {
			return nil
		}
		return prodInfo.ImageUrl(getBestImageIdForSize(images, size))
	}
}

// fetchLibraryAlbums lists a page of the user's saved albums with their
// details, and how many albums there are. Only the page's albums are looked
// up; those whose metadata does not resolve are still listed, by URI.
func fetchLibraryAlbums(ctx context.Context, spc *spclient.Spclient, username string, imageUrl imageUrlFunc, offset, limit int) (*ApiLibraryAlbums, error) {
	all, err := fetchCollectionUris(ctx, spc, username, spclient.CollectionSetLikedSongs, "spotify:album:")
	if err != nil {
		return nil, err
	}
	start, end := pageBounds(len(all), offset, limit)
	uris := all[start:end]

	byUri := make(map[string]*metadatapb.Album, len(uris))
	err = fetchEntityMetadata(ctx, spc, uris, extmetadatapb.ExtensionKind_ALBUM_V4, func(uri string, data *anypb.Any) {
		var album metadatapb.Album
		if data.UnmarshalTo(&album) == nil {
			byUri[uri] = &album
		}
	})
	if err != nil {
		return nil, err
	}

	albums := make([]ApiLibraryAlbum, len(uris))
	for i, uri := range uris {
		albums[i] = libraryAlbum(uri, byUri[uri], imageUrl)
	}
	return &ApiLibraryAlbums{Total: len(all), Offset: offset, Limit: limit, Items: albums}, nil
}

func libraryAlbum(uri string, album *metadatapb.Album, imageUrl imageUrlFunc) ApiLibraryAlbum {
	out := ApiLibraryAlbum{Uri: uri, ArtistNames: []string{}}
	if album == nil {
		return out
	}

	out.Name = album.GetName()
	for _, artist := range album.GetArtist() {
		out.ArtistNames = append(out.ArtistNames, artist.GetName())
	}
	out.Year = int(album.GetDate().GetYear())
	images := album.GetCover()
	if len(images) == 0 {
		images = album.GetCoverGroup().GetImage()
	}
	out.ImageUrl = imageUrl(images)
	return out
}

// fetchLibraryArtists lists a page of the artists the user follows with their
// details, and how many artists there are. Only the page's artists are looked
// up; those whose metadata does not resolve are still listed, by URI.
func fetchLibraryArtists(ctx context.Context, spc *spclient.Spclient, username string, imageUrl imageUrlFunc, offset, limit int) (*ApiLibraryArtists, error) {
	all, err := fetchCollectionUris(ctx, spc, username, spclient.CollectionSetArtists, "spotify:artist:")
	if err != nil {
		return nil, err
	}
	start, end := pageBounds(len(all), offset, limit)
	uris := all[start:end]

	byUri := make(map[string]*metadatapb.Artist, len(uris))
	err = fetchEntityMetadata(ctx, spc, uris, extmetadatapb.ExtensionKind_ARTIST_V4, func(uri string, data *anypb.Any) {
		var artist metadatapb.Artist
		if data.UnmarshalTo(&artist) == nil {
			byUri[uri] = &artist
		}
	})
	if err != nil {
		return nil, err
	}

	artists := make([]ApiLibraryArtist, len(uris))
	for i, uri := range uris {
		artists[i] = libraryArtist(uri, byUri[uri], imageUrl)
	}
	return &ApiLibraryArtists{Total: len(all), Offset: offset, Limit: limit, Items: artists}, nil
}

func libraryArtist(uri string, artist *metadatapb.Artist, imageUrl imageUrlFunc) ApiLibraryArtist {
	out := ApiLibraryArtist{Uri: uri}
	if artist == nil {
		return out
	}

	out.Name = artist.GetName()
	images := artist.GetPortrait()
	if len(images) == 0 {
		images = artist.GetPortraitGroup().GetImage()
	}
	out.ImageUrl = imageUrl(images)
	return out
}
