package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
)

func TestLocalizedMetadataValuePreservesHanOriginals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		localized string
		original  string
		want      string
	}{
		{name: "genre preserves original Han", localized: "流行", original: "流行樂", want: "流行樂"},
		{name: "album artist preserves original Han", localized: "群星", original: "眾藝人", want: "眾藝人"},
		{name: "genre localizes Latin original", localized: "流行", original: "Pop", want: "流行"},
		{name: "album artist localizes Latin original", localized: "群星", original: "Various Artists", want: "群星"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := localizedMetadataValue(tc.localized, tc.original); got != tc.want {
				t.Errorf("localizedMetadataValue(%q, %q) = %q, want %q", tc.localized, tc.original, got, tc.want)
			}
		})
	}
}

func TestLocalizedTrackMetadataFromSongPreservesHanSourceGenre(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sourceGenres []string
		wantGenres   []string
	}{
		{name: "actual first source genre contains Han", sourceGenres: []string{"流行樂", "Pop"}},
		{name: "Latin source genre allows localization", sourceGenres: []string{"Pop"}, wantGenres: []string{"流行"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := metadataTestSong("source-song", "USAAA2600001", "Title", "Artist", "Album")
			source.Attributes.GenreNames = tc.sourceGenres
			candidate := metadataTestSong("localized-song", "USAAA2600001", "歌名", "歌手", "專輯")
			candidate.Attributes.GenreNames = []string{"流行"}

			got := localizedTrackMetadataFromSong(source, candidate)
			if len(tc.wantGenres) == 0 {
				if len(got.GenreNames) != 0 {
					t.Errorf("localized genres = %v, want empty overrides for existing Han source genre %q", got.GenreNames, source.Attributes.GenreNames[0])
				}
			} else if !reflect.DeepEqual(got.GenreNames, tc.wantGenres) {
				t.Errorf("localized genres = %v, want %v", got.GenreNames, tc.wantGenres)
			}
		})
	}
}

func TestLocalizedTrackMetadataFromSongRetainsAlbumArtistDespiteHanSongArtist(t *testing.T) {
	source := metadataTestSong("source-song", "USAAA2600001", "Title", "原唱歌手", "Album")
	if err := json.Unmarshal([]byte(`{"relationships":{"albums":{"data":[{"id":"source-album","attributes":{"artistName":"Various Artists"}}]}}}`), source); err != nil {
		t.Fatalf("decode source album context: %v", err)
	}
	candidate := metadataTestSong("localized-song", "USAAA2600001", "歌名", "原唱歌手", "專輯")
	if err := json.Unmarshal([]byte(`{"relationships":{"albums":{"data":[{"id":"localized-album","attributes":{"artistName":"群星"}}]}}}`), candidate); err != nil {
		t.Fatalf("decode candidate album relationship: %v", err)
	}

	// The song artist is already Han, but the actual album-artist tag is a
	// different, Latin value. Keep the candidate until that tag is available.
	originalAlbumArtist := source.Relationships.Albums.Data[0].Attributes.ArtistName
	got := localizedTrackMetadataFromSong(source, candidate)
	if got.AlbumArtist != "群星" {
		t.Errorf("localized album artist = %q, want 群星 retained independently of song artist %q", got.AlbumArtist, source.Attributes.ArtistName)
	}
	if tag := localizedMetadataValue(got.AlbumArtist, originalAlbumArtist); tag != "群星" {
		t.Errorf("album-artist tag = %q, want 群星 replacing actual Latin album artist %q", tag, originalAlbumArtist)
	}
	if tag := localizedMetadataValue("群星", "眾藝人"); tag != "眾藝人" {
		t.Errorf("album-artist tag = %q, want existing Han album artist 眾藝人 preserved", tag)
	}
}

func TestFetchLocalizedSongMetadataRejectsPaginatedISRCSearch(t *testing.T) {
	// Do not parallelize: nethttp.Client() is shared with the other app tests.
	for _, tc := range []struct {
		name    string
		next    string
		wantErr bool
	}{
		{name: "paginated exact match is not proven unique", next: "/v1/catalog/cn/search?offset=25", wantErr: true},
		{name: "complete exact match is accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := metadataTestSong("source-song", "USAAA2600001", "Title", "Artist", "Album")
			candidate := metadataTestSong("localized-song", "USAAA2600001", "歌名", "歌手", "專輯")
			searchBody, err := json.Marshal(ampapi.SearchResp{Results: ampapi.SearchResults{
				Songs: &ampapi.SongResults{Next: tc.next, Data: []ampapi.SongRespData{*candidate}},
			}})
			if err != nil {
				t.Fatalf("encode search response: %v", err)
			}

			var mu sync.Mutex
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/catalog/cn/songs/source-song":
					_, _ = io.WriteString(w, `{"data":[]}`)
				case "/v1/catalog/cn/search":
					query := r.URL.Query()
					if query.Get("term") != source.Attributes.Isrc || query.Get("types") != "songs" || query.Get("offset") != "0" {
						t.Errorf("unexpected ISRC search query: %s", r.URL.RawQuery)
					}
					_, _ = w.Write(searchBody)
				default:
					t.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.String())
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			client := nethttp.Client()
			originalTransport := client.Transport
			// This transport rewrites every host, including any unexpected
			// Apple/token requests, so none can escape to the real service.
			client.Transport = hostRewriteTransport{target: server.URL}
			t.Cleanup(func() { client.Transport = originalTransport })

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := fetchLocalizedSongMetadata(ctx, "cn", source, "zh-Hans-CN", "test-token")
			mu.Lock()
			gotPaths := append([]string(nil), paths...)
			mu.Unlock()
			wantPaths := []string{"/v1/catalog/cn/songs/source-song", "/v1/catalog/cn/search"}
			if !reflect.DeepEqual(gotPaths, wantPaths) {
				t.Errorf("HTTP request paths = %v, want %v", gotPaths, wantPaths)
			}
			if tc.wantErr {
				if err == nil || got != nil {
					t.Errorf("fetchLocalizedSongMetadata() = (%v, %v), want nil song and error because unseen pages may contain another exact match", got, err)
				}
			} else if err != nil || got == nil || got.ID != candidate.ID {
				t.Errorf("fetchLocalizedSongMetadata() = (%v, %v), want complete-page match %q", got, err, candidate.ID)
			}
		})
	}
}
