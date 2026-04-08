package catalog

import (
	"net/http"
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/lyrics"
)

const defaultUserAgent = "Mozilla/5.0"

type Service struct {
	AppleToken     string
	MediaUserToken string
	Language       string
	HTTPClient     *http.Client
	UserAgent      string
	OpPrefix       string

	GetSongResp       func(storefront string, id string, language string, token string) (*ampapi.SongResp, error)
	GetAlbumResp      func(storefront string, id string, language string, token string) (*ampapi.AlbumResp, error)
	GetPlaylistResp   func(storefront string, id string, language string, token string) (*ampapi.PlaylistResp, error)
	GetStationResp    func(storefront string, id string, language string, token string) (*ampapi.StationResp, error)
	GetMusicVideoResp func(storefront string, id string, language string, token string) (*ampapi.MusicVideoResp, error)
	GetLyrics         func(storefront string, songID string, lyricType string, language string, outputFormat string, token string, mediaUserToken string) (string, error)
}

func (s *Service) client() *http.Client {
	if s != nil && s.HTTPClient != nil {
		return s.HTTPClient
	}
	return http.DefaultClient
}

func (s *Service) userAgent() string {
	if s == nil {
		return defaultUserAgent
	}
	if value := strings.TrimSpace(s.UserAgent); value != "" {
		return value
	}
	return defaultUserAgent
}

func (s *Service) op(suffix string) string {
	if s == nil || strings.TrimSpace(s.OpPrefix) == "" {
		if strings.TrimSpace(suffix) == "" {
			return "catalog"
		}
		return "catalog." + suffix
	}
	if strings.TrimSpace(suffix) == "" {
		return strings.TrimSpace(s.OpPrefix)
	}
	return strings.TrimSpace(s.OpPrefix) + "." + suffix
}

func (s *Service) getSongResp(storefront string, id string) (*ampapi.SongResp, error) {
	if s != nil && s.GetSongResp != nil {
		return s.GetSongResp(storefront, id, s.Language, s.AppleToken)
	}
	return ampapi.GetSongResp(storefront, id, s.Language, s.AppleToken)
}

func (s *Service) getAlbumResp(storefront string, id string) (*ampapi.AlbumResp, error) {
	if s != nil && s.GetAlbumResp != nil {
		return s.GetAlbumResp(storefront, id, s.Language, s.AppleToken)
	}
	return ampapi.GetAlbumResp(storefront, id, s.Language, s.AppleToken)
}

func (s *Service) getPlaylistResp(storefront string, id string) (*ampapi.PlaylistResp, error) {
	if s != nil && s.GetPlaylistResp != nil {
		return s.GetPlaylistResp(storefront, id, s.Language, s.AppleToken)
	}
	return ampapi.GetPlaylistResp(storefront, id, s.Language, s.AppleToken)
}

func (s *Service) getStationResp(storefront string, id string) (*ampapi.StationResp, error) {
	if s != nil && s.GetStationResp != nil {
		return s.GetStationResp(storefront, id, s.Language, s.AppleToken)
	}
	return ampapi.GetStationResp(storefront, id, s.Language, s.AppleToken)
}

func (s *Service) getMusicVideoResp(storefront string, id string) (*ampapi.MusicVideoResp, error) {
	if s != nil && s.GetMusicVideoResp != nil {
		return s.GetMusicVideoResp(storefront, id, s.Language, s.AppleToken)
	}
	return ampapi.GetMusicVideoResp(storefront, id, s.Language, s.AppleToken)
}

func (s *Service) getLyrics(storefront string, songID string, lyricType string, outputFormat string) (string, error) {
	if s != nil && s.GetLyrics != nil {
		return s.GetLyrics(storefront, songID, lyricType, s.Language, outputFormat, s.AppleToken, s.MediaUserToken)
	}
	return lyrics.Get(storefront, songID, lyricType, s.Language, outputFormat, s.AppleToken, s.MediaUserToken)
}
