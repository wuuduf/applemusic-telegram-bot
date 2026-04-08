package task

import (
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

type TrackAlbumData struct {
	Name                    string
	ArtistName              string
	Upc                     string
	ReleaseDate             string
	Copyright               string
	RecordLabel             string
	TrackCount              int
	ContentRating           string
	IsAppleDigitalMaster    bool
	IsMasteredForItunes     bool
	ArtworkURL              string
	MotionDetailSquareVideo string
	MotionDetailTallVideo   string
	ArtistID                string
	ArtistArtworkURL        string
}

type TrackPlaylistData struct {
	Name       string
	ArtistName string
}

func buildTrackAlbumData(albumData *ampapi.AlbumRespData) TrackAlbumData {
	if albumData == nil {
		return TrackAlbumData{}
	}
	result := TrackAlbumData{
		Name:                    strings.TrimSpace(albumData.Attributes.Name),
		ArtistName:              strings.TrimSpace(albumData.Attributes.ArtistName),
		Upc:                     strings.TrimSpace(albumData.Attributes.Upc),
		ReleaseDate:             strings.TrimSpace(albumData.Attributes.ReleaseDate),
		Copyright:               strings.TrimSpace(albumData.Attributes.Copyright),
		RecordLabel:             strings.TrimSpace(albumData.Attributes.RecordLabel),
		TrackCount:              albumData.Attributes.TrackCount,
		ContentRating:           strings.TrimSpace(albumData.Attributes.ContentRating),
		IsAppleDigitalMaster:    albumData.Attributes.IsAppleDigitalMaster,
		IsMasteredForItunes:     albumData.Attributes.IsMasteredForItunes,
		ArtworkURL:              strings.TrimSpace(albumData.Attributes.Artwork.URL),
		MotionDetailSquareVideo: strings.TrimSpace(albumData.Attributes.EditorialVideo.MotionDetailSquare.Video),
		MotionDetailTallVideo:   strings.TrimSpace(albumData.Attributes.EditorialVideo.MotionDetailTall.Video),
	}
	if artistRef, err := safe.FirstRef("task.buildTrackAlbumData", "album.relationships.artists.data", albumData.Relationships.Artists.Data); err == nil {
		result.ArtistID = strings.TrimSpace(artistRef.ID)
		result.ArtistArtworkURL = strings.TrimSpace(artistRef.Attributes.Artwork.Url)
	}
	return result
}

func buildTrackPlaylistData(playlistData *ampapi.PlaylistRespData) TrackPlaylistData {
	if playlistData == nil {
		return TrackPlaylistData{}
	}
	return TrackPlaylistData{
		Name:       strings.TrimSpace(playlistData.Attributes.Name),
		ArtistName: strings.TrimSpace(playlistData.Attributes.ArtistName),
	}
}
