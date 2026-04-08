package task

import (
	"encoding/json"
	"testing"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
)

func TestBuildTrackAlbumData(t *testing.T) {
	album := &ampapi.AlbumRespData{}
	if err := json.Unmarshal([]byte(`{
		"attributes": {
			"name": "Album",
			"artistName": "Artist",
			"upc": "123",
			"releaseDate": "2024-01-01",
			"copyright": "Copyright",
			"recordLabel": "Label",
			"trackCount": 12,
			"contentRating": "explicit",
			"isAppleDigitalMaster": true,
			"isMasteredForItunes": true,
			"artwork": {"url": "https://example.com/cover.jpg"},
			"editorialVideo": {
				"motionDetailSquare": {"video": "https://example.com/square.mp4"},
				"motionDetailTall": {"video": "https://example.com/tall.mp4"}
			}
		},
		"relationships": {
			"artists": {
				"data": [{
					"id": "artist-1",
					"attributes": {"artwork": {"url": "https://example.com/artist.jpg"}}
				}]
			}
		}
	}`), album); err != nil {
		t.Fatalf("unmarshal album data failed: %v", err)
	}

	data := buildTrackAlbumData(album)
	if data.Name != "Album" || data.ArtistName != "Artist" || data.ArtistID != "artist-1" {
		t.Fatalf("unexpected album snapshot: %#v", data)
	}
	if data.ArtistArtworkURL != "https://example.com/artist.jpg" || data.ArtworkURL != "https://example.com/cover.jpg" {
		t.Fatalf("unexpected artwork snapshot: %#v", data)
	}
	if data.TrackCount != 12 || !data.IsAppleDigitalMaster || !data.IsMasteredForItunes {
		t.Fatalf("unexpected flags snapshot: %#v", data)
	}
}

func TestBuildTrackPlaylistData(t *testing.T) {
	playlist := &ampapi.PlaylistRespData{}
	playlist.Attributes.Name = "Playlist"
	playlist.Attributes.ArtistName = "Apple Music"

	data := buildTrackPlaylistData(playlist)
	if data.Name != "Playlist" || data.ArtistName != "Apple Music" {
		t.Fatalf("unexpected playlist snapshot: %#v", data)
	}
}
