package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

const (
	mediaTypeSong       = "song"
	mediaTypeAlbum      = "album"
	mediaTypePlaylist   = "playlist"
	mediaTypeStation    = "station"
	mediaTypeMusicVideo = "music-video"
	mediaTypeArtist     = "artist"
)

type ArtworkTarget struct {
	MediaType  string
	ID         string
	Storefront string
}

type ArtworkInfo struct {
	DisplayName string
	CoverURL    string
	MotionURL   string
}

type artistProfileResponse struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Name    string `json:"name"`
			Artwork struct {
				URL string `json:"url"`
			} `json:"artwork"`
		} `json:"attributes"`
	} `json:"data"`
}

func (s *Service) FetchArtistProfile(storefront string, artistID string) (string, string, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://amp-api.music.apple.com/v1/catalog/%s/artists/%s", storefront, artistID), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.AppleToken))
	req.Header.Set("User-Agent", s.userAgent())
	req.Header.Set("Origin", "https://music.apple.com")
	query := req.URL.Query()
	if strings.TrimSpace(s.Language) != "" {
		query.Set("l", s.Language)
	}
	req.URL.RawQuery = query.Encode()
	resp, err := s.client().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("artist request failed: %s", resp.Status)
	}
	data := artistProfileResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", err
	}
	item, err := safe.FirstRef(s.op("fetchArtistProfile"), "artist.data", data.Data)
	if err != nil {
		return "", "", err
	}
	name := strings.TrimSpace(item.Attributes.Name)
	coverURL := strings.TrimSpace(item.Attributes.Artwork.URL)
	if coverURL == "" {
		return name, "", fmt.Errorf("artist profile photo unavailable")
	}
	return name, coverURL, nil
}

func (s *Service) FetchArtwork(target ArtworkTarget) (ArtworkInfo, error) {
	if strings.TrimSpace(target.MediaType) == "" || strings.TrimSpace(target.ID) == "" {
		return ArtworkInfo{}, fmt.Errorf("invalid target")
	}
	storefront := strings.TrimSpace(target.Storefront)
	switch target.MediaType {
	case mediaTypeSong:
		resp, err := s.getSongResp(storefront, target.ID)
		if err != nil {
			return ArtworkInfo{}, err
		}
		item, err := firstSongData(s.op("fetchArtwork.song"), resp)
		if err != nil {
			return ArtworkInfo{}, err
		}
		result := ArtworkInfo{
			DisplayName: composeArtistTitle(item.Attributes.ArtistName, item.Attributes.Name),
			CoverURL:    strings.TrimSpace(item.Attributes.Artwork.URL),
		}
		if albumRef, albumErr := safe.FirstRef(s.op("fetchArtwork.song"), "song.relationships.albums.data", item.Relationships.Albums.Data); albumErr == nil {
			albumID := strings.TrimSpace(albumRef.ID)
			if albumID != "" {
				if albumResp, err := s.getAlbumResp(storefront, albumID); err == nil {
					albumItem, dataErr := firstAlbumData(s.op("fetchArtwork.song.album"), albumResp)
					if dataErr == nil {
						result.MotionURL = firstNonEmpty(
							albumItem.Attributes.EditorialVideo.MotionSquare.Video,
							albumItem.Attributes.EditorialVideo.MotionTall.Video,
						)
					}
				}
			}
		}
		if result.DisplayName == "" {
			result.DisplayName = "song-" + target.ID
		}
		if result.CoverURL == "" {
			return ArtworkInfo{}, fmt.Errorf("song cover unavailable")
		}
		return result, nil
	case mediaTypeAlbum:
		resp, err := s.getAlbumResp(storefront, target.ID)
		if err != nil {
			return ArtworkInfo{}, err
		}
		item, err := firstAlbumData(s.op("fetchArtwork.album"), resp)
		if err != nil {
			return ArtworkInfo{}, err
		}
		result := ArtworkInfo{
			DisplayName: composeArtistTitle(item.Attributes.ArtistName, item.Attributes.Name),
			CoverURL:    strings.TrimSpace(item.Attributes.Artwork.URL),
			MotionURL: firstNonEmpty(
				item.Attributes.EditorialVideo.MotionSquare.Video,
				item.Attributes.EditorialVideo.MotionTall.Video,
			),
		}
		if result.DisplayName == "" {
			result.DisplayName = "album-" + target.ID
		}
		if result.CoverURL == "" {
			return ArtworkInfo{}, fmt.Errorf("album cover unavailable")
		}
		return result, nil
	case mediaTypePlaylist:
		resp, err := s.getPlaylistResp(storefront, target.ID)
		if err != nil {
			return ArtworkInfo{}, err
		}
		item, err := firstPlaylistData(s.op("fetchArtwork.playlist"), resp)
		if err != nil {
			return ArtworkInfo{}, err
		}
		result := ArtworkInfo{
			DisplayName: strings.TrimSpace(item.Attributes.Name),
			CoverURL:    strings.TrimSpace(item.Attributes.Artwork.URL),
			MotionURL: firstNonEmpty(
				item.Attributes.EditorialVideo.MotionSquare.Video,
				item.Attributes.EditorialVideo.MotionTall.Video,
			),
		}
		if result.DisplayName == "" {
			result.DisplayName = "playlist-" + target.ID
		}
		if result.CoverURL == "" {
			return ArtworkInfo{}, fmt.Errorf("playlist cover unavailable")
		}
		return result, nil
	case mediaTypeStation:
		resp, err := s.getStationResp(storefront, target.ID)
		if err != nil {
			return ArtworkInfo{}, err
		}
		item, err := firstStationData(s.op("fetchArtwork.station"), resp)
		if err != nil {
			return ArtworkInfo{}, err
		}
		result := ArtworkInfo{
			DisplayName: strings.TrimSpace(item.Attributes.Name),
			CoverURL:    strings.TrimSpace(item.Attributes.Artwork.URL),
			MotionURL: firstNonEmpty(
				item.Attributes.EditorialVideo.MotionSquare.Video,
				item.Attributes.EditorialVideo.MotionTall.Video,
			),
		}
		if result.DisplayName == "" {
			result.DisplayName = "station-" + target.ID
		}
		if result.CoverURL == "" {
			return ArtworkInfo{}, fmt.Errorf("station cover unavailable")
		}
		return result, nil
	case mediaTypeMusicVideo:
		resp, err := s.getMusicVideoResp(storefront, target.ID)
		if err != nil {
			return ArtworkInfo{}, err
		}
		item, err := firstMusicVideoData(s.op("fetchArtwork.musicVideo"), resp)
		if err != nil {
			return ArtworkInfo{}, err
		}
		result := ArtworkInfo{
			DisplayName: composeArtistTitle(item.Attributes.ArtistName, item.Attributes.Name),
			CoverURL:    strings.TrimSpace(item.Attributes.Artwork.URL),
		}
		if result.DisplayName == "" {
			result.DisplayName = "music-video-" + target.ID
		}
		if result.CoverURL == "" {
			return ArtworkInfo{}, fmt.Errorf("music video cover unavailable")
		}
		return result, nil
	case mediaTypeArtist:
		name, coverURL, err := s.FetchArtistProfile(storefront, target.ID)
		if err != nil {
			return ArtworkInfo{}, err
		}
		if name == "" {
			name = "artist-" + target.ID
		}
		return ArtworkInfo{
			DisplayName: name,
			CoverURL:    coverURL,
		}, nil
	default:
		return ArtworkInfo{}, fmt.Errorf("unsupported media type: %s", target.MediaType)
	}
}

func firstSongData(op string, resp *ampapi.SongResp) (*ampapi.SongRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "song.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "song.data", resp.Data)
}

func firstAlbumData(op string, resp *ampapi.AlbumResp) (*ampapi.AlbumRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "album.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "album.data", resp.Data)
}

func firstPlaylistData(op string, resp *ampapi.PlaylistResp) (*ampapi.PlaylistRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "playlist.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "playlist.data", resp.Data)
}

func firstStationData(op string, resp *ampapi.StationResp) (*ampapi.StationRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "station.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "station.data", resp.Data)
}

func firstMusicVideoData(op string, resp *ampapi.MusicVideoResp) (*ampapi.MusicVideoRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "music_video.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "music_video.data", resp.Data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func composeArtistTitle(artistName string, title string) string {
	artistName = strings.TrimSpace(artistName)
	title = strings.TrimSpace(title)
	if artistName != "" && title != "" {
		return artistName + " - " + title
	}
	if title != "" {
		return title
	}
	return artistName
}
