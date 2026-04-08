package app

import (
	"fmt"
	"strconv"

	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
	"github.com/zhaarey/go-mp4tag"
)

func writeMP4Tags(track *task.Track, lrc string, cfg *structs.ConfigSet) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	genre, err := firstGenreName("main.writeMP4Tags", track.Resp.Attributes.GenreNames)
	if err != nil {
		return err
	}
	t := &mp4tag.MP4Tags{
		Title:      track.Resp.Attributes.Name,
		TitleSort:  track.Resp.Attributes.Name,
		Artist:     track.Resp.Attributes.ArtistName,
		ArtistSort: track.Resp.Attributes.ArtistName,
		Custom: map[string]string{
			"PERFORMER":   track.Resp.Attributes.ArtistName,
			"RELEASETIME": track.Resp.Attributes.ReleaseDate,
			"ISRC":        track.Resp.Attributes.Isrc,
			"LABEL":       "",
			"UPC":         "",
		},
		Composer:     track.Resp.Attributes.ComposerName,
		ComposerSort: track.Resp.Attributes.ComposerName,
		CustomGenre:  genre,
		Lyrics:       lrc,
		TrackNumber:  int16(track.Resp.Attributes.TrackNumber),
		DiscNumber:   int16(track.Resp.Attributes.DiscNumber),
		Album:        track.Resp.Attributes.AlbumName,
		AlbumSort:    track.Resp.Attributes.AlbumName,
	}

	if track.PreType == "albums" {
		albumID, err := strconv.ParseUint(track.PreID, 10, 32)
		if err != nil {
			return err
		}
		t.ItunesAlbumID = int32(albumID)
	}

	if artistRef, artistErr := safe.FirstRef("main.writeMP4Tags", "song.relationships.artists.data", track.Resp.Relationships.Artists.Data); artistErr == nil {
		artistID, err := strconv.ParseUint(artistRef.ID, 10, 32)
		if err != nil {
			return err
		}
		t.ItunesArtistID = int32(artistID)
	}

	if (track.PreType == "playlists" || track.PreType == "stations") && !cfg.UseSongInfoForPlaylist {
		t.DiscNumber = 1
		t.DiscTotal = 1
		t.TrackNumber = int16(track.TaskNum)
		t.TrackTotal = int16(track.TaskTotal)
		t.Album = track.PlaylistData.Name
		t.AlbumSort = track.PlaylistData.Name
		t.AlbumArtist = track.PlaylistData.ArtistName
		t.AlbumArtistSort = track.PlaylistData.ArtistName
	} else if (track.PreType == "playlists" || track.PreType == "stations") && cfg.UseSongInfoForPlaylist {
		t.DiscTotal = int16(track.DiscTotal)
		t.TrackTotal = int16(track.AlbumData.TrackCount)
		t.AlbumArtist = track.AlbumData.ArtistName
		t.AlbumArtistSort = track.AlbumData.ArtistName
		t.Custom["UPC"] = track.AlbumData.Upc
		t.Custom["LABEL"] = track.AlbumData.RecordLabel
		t.Date = track.AlbumData.ReleaseDate
		t.Copyright = track.AlbumData.Copyright
		t.Publisher = track.AlbumData.RecordLabel
	} else {
		t.DiscTotal = int16(track.DiscTotal)
		t.TrackTotal = int16(track.AlbumData.TrackCount)
		t.AlbumArtist = track.AlbumData.ArtistName
		t.AlbumArtistSort = track.AlbumData.ArtistName
		t.Custom["UPC"] = track.AlbumData.Upc
		t.Date = track.AlbumData.ReleaseDate
		t.Copyright = track.AlbumData.Copyright
		t.Publisher = track.AlbumData.RecordLabel
	}

	if track.Resp.Attributes.ContentRating == "explicit" {
		t.ItunesAdvisory = mp4tag.ItunesAdvisoryExplicit
	} else if track.Resp.Attributes.ContentRating == "clean" {
		t.ItunesAdvisory = mp4tag.ItunesAdvisoryClean
	} else {
		t.ItunesAdvisory = mp4tag.ItunesAdvisoryNone
	}

	mp4, err := mp4tag.Open(track.SavePath)
	if err != nil {
		return err
	}
	defer mp4.Close()
	err = mp4.Write(t, []string{})
	if err != nil {
		return err
	}
	return nil
}
