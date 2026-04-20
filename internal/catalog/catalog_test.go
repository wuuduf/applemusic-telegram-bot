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

func TestFetchArtworkAlbumPrefersMotionDetailSquare(t *testing.T) {
	service := &Service{
		AppleToken: "token",
		GetAlbumResp: func(storefront string, id string, language string, token string) (*ampapi.AlbumResp, error) {
			return mustAlbumResp(t, `{"data":[{"id":"album-1","type":"albums","attributes":{"artistName":"Artist","name":"Album","artwork":{"url":"https://example.com/album.jpg"},"editorialVideo":{"motionDetailSquare":{"video":"https://example.com/detail-square.m3u8"},"motionSquareVideo1x1":{"video":"https://example.com/square.m3u8"},"motionDetailTall":{"video":"https://example.com/detail-tall.m3u8"}}},"relationships":{"tracks":{"data":[]}}}]}`), nil
		},
	}

	info, err := service.FetchArtwork(ArtworkTarget{MediaType: mediaTypeAlbum, ID: "album-1", Storefront: "us"})
	if err != nil {
		t.Fatalf("FetchArtwork failed: %v", err)
	}
	if info.MotionURL != "https://example.com/detail-square.m3u8" {
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

func TestFetchArtistRelationshipAllSongsUsesRelationshipLimitAndNextOffset(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests++
		offset := r.URL.Query().Get("offset")
		limit := r.URL.Query().Get("limit")
		switch requests {
		case 1:
			if offset != "0" {
				t.Fatalf("expected first songs offset=0, got %q", offset)
			}
			if limit != "20" {
				t.Fatalf("expected first songs limit=20, got %q", limit)
			}
			_, _ = io.WriteString(w, `{"next":"/v1/catalog/us/artists/artist-1/songs?offset=20","data":[{"id":"song-1","attributes":{"name":"Song One","url":"https://example.com/song-1","releaseDate":"2024-01-01","artistName":"Artist One","albumName":"Album One"}}]}`)
		case 2:
			if offset != "20" {
				t.Fatalf("expected second songs offset=20, got %q", offset)
			}
			if limit != "20" {
				t.Fatalf("expected second songs limit=20, got %q", limit)
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"song-2","attributes":{"name":"Song Two","url":"https://example.com/song-2","releaseDate":"2025-01-01","artistName":"Artist One","albumName":"Album Two"}}]}`)
		default:
			t.Fatalf("unexpected request count: %d", requests)
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

	items, err := service.FetchArtistRelationshipAll("us", "artist-1", "songs")
	if err != nil {
		t.Fatalf("FetchArtistRelationshipAll songs failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 song relationship items, got %d", len(items))
	}
	if items[0].ID != "song-1" || items[1].ID != "song-2" {
		t.Fatalf("unexpected song relationship items: %#v", items)
	}
}

func TestFetchArtistViewAllFullAlbumsUsesViewEndpointAndRelativeNext(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		requests++
		if want := "/v1/catalog/cn/artists/artist-1/view/full-albums"; r.URL.Path != want {
			t.Fatalf("unexpected path: got %q want %q", r.URL.Path, want)
		}
		offset := r.URL.Query().Get("offset")
		limit := r.URL.Query().Get("limit")
		lang := r.URL.Query().Get("l")
		switch requests {
		case 1:
			if offset != "0" {
				t.Fatalf("expected first full-albums offset=0, got %q", offset)
			}
			if limit != "100" {
				t.Fatalf("expected first full-albums limit=100, got %q", limit)
			}
			if lang != "zh-Hans" {
				t.Fatalf("expected first full-albums language=zh-Hans, got %q", lang)
			}
			_, _ = io.WriteString(w, `{"next":"full-albums?offset=100","data":[{"id":"album-1","attributes":{"name":"Album One","url":"https://example.com/album-1","releaseDate":"2024-01-01","artistName":"Artist One","albumName":"Album One"}}]}`)
		case 2:
			if offset != "100" {
				t.Fatalf("expected second full-albums offset=100, got %q", offset)
			}
			if limit != "100" {
				t.Fatalf("expected second full-albums limit=100, got %q", limit)
			}
			if lang != "zh-Hans" {
				t.Fatalf("expected second full-albums language=zh-Hans, got %q", lang)
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"album-2","attributes":{"name":"Album Two","url":"https://example.com/album-2","releaseDate":"2025-01-01","artistName":"Artist One","albumName":"Album Two"}}]}`)
		default:
			t.Fatalf("unexpected request count: %d", requests)
		}
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = rewriteTransport{base: server.URL, next: client.Transport}
	service := &Service{
		AppleToken: "token",
		Language:   "zh-Hans",
		HTTPClient: client,
		UserAgent:  defaultUserAgent,
	}

	items, err := service.FetchArtistViewAll("cn", "artist-1", "full-albums")
	if err != nil {
		t.Fatalf("FetchArtistViewAll full-albums failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 full-albums items, got %d", len(items))
	}
	if items[0].ID != "album-1" || items[1].ID != "album-2" {
		t.Fatalf("unexpected full-albums items: %#v", items)
	}
}

func TestFetchCuratorName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/v1/catalog/us"; r.URL.Path != want {
			t.Fatalf("unexpected path: got %q want %q", r.URL.Path, want)
		}
		if got := r.URL.Query().Get("ids[curators]"); got != "1702073195" {
			t.Fatalf("unexpected curator id query: %q", got)
		}
		if got := r.URL.Query().Get("ids[apple-curators]"); got != "1702073195" {
			t.Fatalf("unexpected apple-curator id query: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"1702073195","type":"apple-curators","attributes":{"name":"100 Best Albums","url":"https://example.com/curator/1702073195"}}]}`)
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = rewriteTransport{base: server.URL, next: client.Transport}
	service := &Service{
		AppleToken: "token",
		HTTPClient: client,
		UserAgent:  defaultUserAgent,
	}

	name, err := service.FetchCuratorName("us", "1702073195")
	if err != nil {
		t.Fatalf("FetchCuratorName failed: %v", err)
	}
	if name != "100 Best Albums" {
		t.Fatalf("unexpected curator name: %q", name)
	}
}

func TestFetchCuratorAlbumsCollectsInlineAndRoomAlbums(t *testing.T) {
	roomTwoRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/catalog/us":
			if got := r.URL.Query().Get("l"); got != "zh-Hans" {
				t.Fatalf("expected curator language=zh-Hans, got %q", got)
			}
			_, _ = io.WriteString(w, `{
				"data":[
					{
						"id":"1702073195",
						"type":"apple-curators",
						"attributes":{"name":"100 Best Albums"},
						"relationships":{
							"grouping":{
								"data":[
									{
										"id":"group-1",
										"type":"groupings",
										"relationships":{
											"tabs":{
												"data":[
													{
														"id":"default",
														"type":"editorial-elements",
														"relationships":{
															"children":{
																"data":[
																	{
																		"id":"albums-inline",
																		"type":"editorial-elements",
																		"attributes":{"name":"Albums Inline","resourceTypes":["albums"]},
																		"relationships":{
																			"contents":{"data":[
																				{"id":"album-inline-1","type":"albums","attributes":{"name":"Inline Album 1","artistName":"Artist 1","url":"https://example.com/albums/1","releaseDate":"2024-01-01"}},
																				{"id":"album-inline-2","type":"albums","attributes":{"name":"Inline Album 2","artistName":"Artist 2","url":"https://example.com/albums/2","releaseDate":"2024-02-02"}}
																			]},
																			"room":{"data":[{"id":"room-albums-1","type":"rooms"}]}
																		}
																	},
																	{
																		"id":"stations-only",
																		"type":"editorial-elements",
																		"attributes":{"name":"Stations Only","resourceTypes":["stations"]},
																		"relationships":{
																			"contents":{"data":[{"id":"station-1","type":"stations"}]},
																			"room":{"data":[{"id":"room-stations-1","type":"rooms"}]}
																		}
																	},
																	{
																		"id":"albums-room-only",
																		"type":"editorial-elements",
																		"attributes":{"name":"Albums Room Only"},
																		"relationships":{
																			"contents":{"data":[{"id":"album-inline-1","type":"albums","attributes":{"name":"Inline Album 1 Duplicate","artistName":"Artist 1"}}]},
																			"room":{"data":[{"id":"room-albums-2","type":"rooms"}]}
																		}
																	}
																]
															}
														}
													}
												]
											}
										}
									}
								]
							}
						}
					}
				]
			}`)
		case "/v1/editorial/us/rooms/room-albums-1/contents":
			if got := r.URL.Query().Get("limit"); got != "100" {
				t.Fatalf("expected room-albums-1 limit=100, got %q", got)
			}
			if got := r.URL.Query().Get("l"); got != "zh-Hans" {
				t.Fatalf("expected room-albums-1 language=zh-Hans, got %q", got)
			}
			_, _ = io.WriteString(w, `{
				"data":[
					{"id":"album-inline-2","type":"albums","attributes":{"name":"Inline Album 2 Duplicate","artistName":"Artist 2"}},
					{"id":"album-room-1","type":"albums","attributes":{"name":"Room Album 1","artistName":"Artist 3","url":"https://example.com/albums/3","releaseDate":"2024-03-03"}}
				]
			}`)
		case "/v1/editorial/us/rooms/room-albums-2/contents":
			if got := r.URL.Query().Get("l"); got != "zh-Hans" {
				t.Fatalf("expected room-albums-2 language=zh-Hans, got %q", got)
			}
			switch r.URL.Query().Get("offset") {
			case "":
				roomTwoRequests++
				_, _ = io.WriteString(w, `{
					"next":"contents?offset=100",
					"data":[
						{"id":"album-room-2a","type":"albums","attributes":{"name":"Room Album 2A","artistName":"Artist 4"}}
					]
				}`)
			case "100":
				roomTwoRequests++
				_, _ = io.WriteString(w, `{
					"data":[
						{"id":"album-room-2b","type":"albums","attributes":{"name":"Room Album 2B","artistName":"Artist 5"}}
					]
				}`)
			default:
				t.Fatalf("unexpected room-albums-2 offset: %q", r.URL.Query().Get("offset"))
			}
		case "/v1/editorial/us/rooms/room-stations-1/contents":
			t.Fatalf("stations room should not be fetched")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = rewriteTransport{base: server.URL, next: client.Transport}
	service := &Service{
		AppleToken: "token",
		Language:   "zh-Hans",
		HTTPClient: client,
		UserAgent:  defaultUserAgent,
	}

	albums, err := service.FetchCuratorAlbums("us", "1702073195")
	if err != nil {
		t.Fatalf("FetchCuratorAlbums failed: %v", err)
	}
	if roomTwoRequests != 2 {
		t.Fatalf("expected room-albums-2 to paginate twice, got %d requests", roomTwoRequests)
	}
	if len(albums) != 5 {
		t.Fatalf("expected 5 unique curator albums, got %d", len(albums))
	}
	gotIDs := make([]string, 0, len(albums))
	for _, album := range albums {
		gotIDs = append(gotIDs, album.ID)
	}
	wantIDs := []string{"album-inline-1", "album-inline-2", "album-room-1", "album-room-2a", "album-room-2b"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("unexpected curator album ids: got %v want %v", gotIDs, wantIDs)
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
