package app

import (
	"fmt"
	"math"
	"strconv"

	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
	"github.com/zhaarey/go-mp4tag"
)

func writeMP4Tags(track *task.Track, lrc string, cfg *structs.ConfigSet) error {
	return writeMP4TagsWithMetadata(track, lrc, cfg, localizedTrackMetadata{})
}

type mp4TagTextFields struct {
	Title    string
	Artist   string
	Album    string
	Composer string
}

func mp4TagTextFieldsFor(title, artist, album, composer string, metadata localizedTrackMetadata) mp4TagTextFields {
	return mp4TagTextFields{
		Title:    localizedMetadataValue(metadata.Title, title),
		Artist:   localizedMetadataValue(metadata.Artist, artist),
		Album:    localizedMetadataValue(metadata.Album, album),
		Composer: localizedMetadataValue(metadata.Composer, composer),
	}
}

func writeMP4TagsWithMetadata(track *task.Track, lrc string, cfg *structs.ConfigSet, metadata localizedTrackMetadata) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	tagText := mp4TagTextFieldsFor(track.Resp.Attributes.Name, track.Resp.Attributes.ArtistName, track.Resp.Attributes.AlbumName, track.Resp.Attributes.ComposerName, metadata)
	genreNames := track.Resp.Attributes.GenreNames
	if len(metadata.GenreNames) > 0 && (len(genreNames) == 0 || !containsHanCharacters(genreNames[0])) {
		genreNames = metadata.GenreNames
	}
	genre, err := firstGenreName("main.writeMP4Tags", genreNames)
	if err != nil {
		return err
	}
	t := &mp4tag.MP4Tags{
		Title:      tagText.Title,
		TitleSort:  tagText.Title,
		Artist:     tagText.Artist,
		ArtistSort: tagText.Artist,
		Custom: map[string]string{
			"PERFORMER":   tagText.Artist,
			"RELEASETIME": track.Resp.Attributes.ReleaseDate,
			"ISRC":        track.Resp.Attributes.Isrc,
			"LABEL":       "",
			"UPC":         "",
		},
		Composer:     tagText.Composer,
		ComposerSort: tagText.Composer,
		CustomGenre:  genre,
		Lyrics:       lrc,
		TrackNumber:  int16(track.Resp.Attributes.TrackNumber),
		DiscNumber:   int16(track.Resp.Attributes.DiscNumber),
		Album:        tagText.Album,
		AlbumSort:    tagText.Album,
	}

	if track.PreType == "albums" {
		albumID, err := strconv.ParseUint(track.PreID, 10, 64)
		if err != nil {
			return err
		}
		if albumID <= math.MaxInt32 {
			t.ItunesAlbumID = int32(albumID)
		}
	}

	if artistRef, artistErr := safe.FirstRef("main.writeMP4Tags", "song.relationships.artists.data", track.Resp.Relationships.Artists.Data); artistErr == nil {
		artistID, err := strconv.ParseUint(artistRef.ID, 10, 64)
		if err != nil {
			return err
		}
		if artistID <= math.MaxInt32 {
			t.ItunesArtistID = int32(artistID)
		}
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
	if metadata.AlbumArtist != "" && ((track.PreType != "playlists" && track.PreType != "stations") || cfg.UseSongInfoForPlaylist) {
		t.AlbumArtist = localizedMetadataValue(metadata.AlbumArtist, t.AlbumArtist)
		t.AlbumArtistSort = t.AlbumArtist
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
