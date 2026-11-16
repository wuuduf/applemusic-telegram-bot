package catalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/lyrics"
	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
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
	// http.DefaultClient has no timeout; a stalled Apple request would hang
	// the caller forever.
	return nethttp.Client()
}

const (
	catalogMaxAttempts = 4
	// catalogMaxPages stops a paginated listing whose "next" link never
	// ends; no artist or curator room comes anywhere near it.
	catalogMaxPages = 500
)

// do sends a body-less request, retrying rate limits and server errors so one
// 429 in a listing of a hundred pages does not fail the whole task.
func (s *Service) do(req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= catalogMaxAttempts; attempt++ {
		resp, err := s.client().Do(req.Clone(req.Context()))
		if err != nil {
			lastErr = err
			if req.Context().Err() != nil || attempt == catalogMaxAttempts {
				return nil, err
			}
		} else {
			retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
			if !retryable || attempt == catalogMaxAttempts {
				return resp, nil
			}
			lastErr = fmt.Errorf("status %s", resp.Status)
			delay := catalogRetryDelay(attempt, resp.Header.Get("Retry-After"))
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			if !sleepContext(req.Context(), delay) {
				return nil, req.Context().Err()
			}
			continue
		}
		if !sleepContext(req.Context(), catalogRetryDelay(attempt, "")) {
			return nil, req.Context().Err()
		}
	}
	return nil, lastErr
}

func catalogRetryDelay(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
		if seconds > 30 {
			seconds = 30
		}
		return time.Duration(seconds) * time.Second
	}
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(1<<(attempt-1)) * 300 * time.Millisecond
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
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
