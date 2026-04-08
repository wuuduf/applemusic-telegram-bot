package task

import (
	"context"
	"testing"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
)

func TestStationAppendTrackUsesAppendPosition(t *testing.T) {
	station := &Station{
		ID:         "ra.1",
		Name:       "Station",
		Storefront: "us",
		Language:   "en-US",
	}
	trackData := ampapi.TrackRespData{
		ID:   "song-1",
		Type: "songs",
	}
	trackData.Attributes.Name = "Track"
	trackData.Attributes.ExtendedAssetUrls.EnhancedHls = "https://example.com/song.m3u8"

	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("appendTrack should not panic when task numbers skip: %v", rec)
		}
	}()

	station.appendTrack(context.Background(), trackData, 1, TrackAlbumData{Name: "Album"}, 2, 10)

	if len(station.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(station.Tracks))
	}
	if station.Tracks[0].TaskNum != 2 || station.Tracks[0].TaskTotal != 10 {
		t.Fatalf("unexpected task numbering: %+v", station.Tracks[0])
	}
	if station.Tracks[0].PlaylistData.Name != "Station" {
		t.Fatalf("expected playlist metadata to be written on appended track")
	}
}
