package catalog

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
)

func TestFetchArtistProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"artist-1","attributes":{"name":"Artist One","artwork":{"url":"https://example.com/cover.jpg"}}}]}`)
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = rewriteTransport{base: server.URL, next: client.Transport}
	service := &Service{
		AppleToken: "token",
		HTTPClient: client,
		UserAgent:  defaultUserAgent,
	}

	name, coverURL, err := service.FetchArtistProfile("us", "artist-1")
	if err != nil {
		t.Fatalf("FetchArtistProfile failed: %v", err)
	}
	if name != "Artist One" {
		t.Fatalf("unexpected artist name: %q", name)
	}
	if coverURL != "https://example.com/cover.jpg" {
		t.Fatalf("unexpected cover url: %q", coverURL)
	}
}

func TestFetchArtworkSongUsesAlbumMotion(t *testing.T) {
	service := &Service{
		AppleToken: "token",
		GetSongResp: func(storefront string, id string, language string, token string) (*ampapi.SongResp, error) {
			return mustSongResp(t, `{"data":[{"id":"song-1","type":"songs","attributes":{"artistName":"Artist","name":"Song","artwork":{"url":"https://example.com/song.jpg"}},"relationships":{"albums":{"data":[{"id":"album-1"}]}}}]}`), nil
		},
		GetAlbumResp: func(storefront string, id string, language string, token string) (*ampapi.AlbumResp, error) {
			return mustAlbumResp(t, `{"data":[{"id":"album-1","type":"albums","attributes":{"artistName":"Artist","name":"Album","artwork":{"url":"https://example.com/album.jpg"},"editorialVideo":{"motionSquareVideo1x1":{"video":"https://example.com/motion.mp4"}}},"relationships":{"tracks":{"data":[]}}}]}`), nil
		},
	}

	info, err := service.FetchArtwork(ArtworkTarget{MediaType: mediaTypeSong, ID: "song-1", Storefront: "us"})
	if err != nil {
		t.Fatalf("FetchArtwork failed: %v", err)
	}
	if info.DisplayName != "Artist - Song" {
		t.Fatalf("unexpected display name: %q", info.DisplayName)
	}
	if info.CoverURL != "https://example.com/song.jpg" {
		t.Fatalf("unexpected cover url: %q", info.CoverURL)
	}
	if info.MotionURL != "https://example.com/motion.mp4" {
		t.Fatalf("unexpected motion url: %q", info.MotionURL)
	}
}

func TestFetchLyricsOnlyFallsBack(t *testing.T) {
	service := &Service{
		GetLyrics: func(storefront string, songID string, lyricType string, language string, outputFormat string, token string, mediaUserToken string) (string, error) {
			if lyricType == "syllable-lyrics" {
				return "", os.ErrNotExist
			}
			return "plain lyrics", nil
		},
	}

	content, lyricType, err := service.FetchLyricsOnly("song-1", "us", "lrc")
	if err != nil {
		t.Fatalf("FetchLyricsOnly failed: %v", err)
	}
	if content != "plain lyrics" || lyricType != "lyrics" {
		t.Fatalf("unexpected lyrics result: %q %q", content, lyricType)
	}
}

func TestFetchArtistRelationshipAll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("offset") {
		case "", "0":
			_, _ = io.WriteString(w, `{"next":"/v1/catalog/us/artists/artist-1/albums?offset=100","data":[{"id":"album-1","attributes":{"name":"Album One","url":"https://example.com/album-1","releaseDate":"2024-01-01","contentRating":"explicit","artistName":"Artist One","albumName":"Album One"}}]}`)
		case "100":
			_, _ = io.WriteString(w, `{"data":[{"id":"album-2","attributes":{"name":"Album Two","url":"https://example.com/album-2","releaseDate":"2025-01-01","artistName":"Artist One","albumName":"Album Two"}}]}`)
		default:
			t.Fatalf("unexpected offset query: %q", r.URL.Query().Get("offset"))
		}
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = rewriteTransport{base: server.URL, next: client.Transport}
	service := &Service{
		AppleToken: "token",
		HTTPClient: client,
		UserAgent:  defaultUserAgent,
	}

	items, err := service.FetchArtistRelationshipAll("us", "artist-1", "albums")
	if err != nil {
		t.Fatalf("FetchArtistRelationshipAll failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 relationship items, got %d", len(items))
	}
	if items[0].ID != "album-1" || items[1].ID != "album-2" {
		t.Fatalf("unexpected relationship items: %#v", items)
	}
}

func TestExportAlbumLyricsWritesFilesAndCountsFailures(t *testing.T) {
	tmpDir := t.TempDir()
	service := &Service{
		GetAlbumResp: func(storefront string, id string, language string, token string) (*ampapi.AlbumResp, error) {
			return mustAlbumResp(t, `{"data":[{"id":"album-1","type":"albums","attributes":{"name":"Album Name"},"relationships":{"tracks":{"data":[{"id":"song-1","type":"songs","attributes":{"artistName":"Artist","name":"First","trackNumber":1}},{"id":"song-2","type":"songs","attributes":{"artistName":"Artist","name":"Second","trackNumber":2}}]}}}]}`), nil
		},
		GetLyrics: func(storefront string, songID string, lyricType string, language string, outputFormat string, token string, mediaUserToken string) (string, error) {
			if songID == "song-2" {
				return "", os.ErrNotExist
			}
			return "lyrics for " + songID, nil
		},
	}

	result, err := service.ExportAlbumLyrics(tmpDir, "album-1", "us", "lrc")
	if err != nil {
		t.Fatalf("ExportAlbumLyrics failed: %v", err)
	}
	if result.AlbumName != "Album Name" {
		t.Fatalf("unexpected album name: %q", result.AlbumName)
	}
	if result.FailedCount != 1 {
		t.Fatalf("expected 1 failed track, got %d", result.FailedCount)
	}
	if len(result.Paths) != 1 {
		t.Fatalf("expected 1 exported file, got %d", len(result.Paths))
	}
	if filepath.Dir(result.Paths[0]) == tmpDir {
		t.Fatalf("expected lyrics file inside a temp subdir")
	}
	content, err := os.ReadFile(result.Paths[0])
	if err != nil {
		t.Fatalf("read exported lyrics failed: %v", err)
	}
	if !strings.Contains(string(content), "lyrics for song-1") {
		t.Fatalf("unexpected lyrics content: %q", string(content))
	}
}

func mustSongResp(t *testing.T, payload string) *ampapi.SongResp {
	t.Helper()
	resp := &ampapi.SongResp{}
	if err := json.Unmarshal([]byte(payload), resp); err != nil {
		t.Fatalf("unmarshal song resp failed: %v", err)
	}
	return resp
}

func mustAlbumResp(t *testing.T, payload string) *ampapi.AlbumResp {
	t.Helper()
	resp := &ampapi.AlbumResp{}
	if err := json.Unmarshal([]byte(payload), resp); err != nil {
		t.Fatalf("unmarshal album resp failed: %v", err)
	}
	return resp
}

type rewriteTransport struct {
	base string
	next http.RoundTripper
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(t.base, "http://")
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(clone)
}
