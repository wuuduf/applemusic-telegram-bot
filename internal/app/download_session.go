package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
)

type AudioMeta struct {
	TrackID        string
	VariantKey     string
	Title          string
	Performer      string
	DurationMillis int64
	Format         string
	AlbumName      string
	ComposerName   string
	ReleaseDate    string
	GenreNames     []string
	AudioTraits    []string
	Storefront     string
	HasLyrics      bool
	ContentRating  string
}

type CachedAudio struct {
	FileID         string    `json:"file_id"`
	FileSize       int64     `json:"file_size"`
	Compressed     bool      `json:"compressed"`
	Format         string    `json:"format,omitempty"`
	SizeBytes      int64     `json:"size_bytes,omitempty"`
	BitrateKbps    float64   `json:"bitrate_kbps,omitempty"`
	DurationMillis int64     `json:"duration_millis,omitempty"`
	Title          string    `json:"title,omitempty"`
	Performer      string    `json:"performer,omitempty"`
	AlbumName      string    `json:"album_name,omitempty"`
	GenreNames     []string  `json:"genre_names,omitempty"`
	Storefront     string    `json:"storefront,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type CachedDocument struct {
	FileID    string    `json:"file_id"`
	FileSize  int64     `json:"file_size,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type CachedVideo struct {
	FileID    string    `json:"file_id"`
	FileSize  int64     `json:"file_size,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type songCommentCacheEntry struct {
	Text      string
	ExpiresAt time.Time
}

type DownloadSession struct {
	Context             context.Context
	Config              structs.ConfigSet
	DlAtmos             bool
	DlAac               bool
	DlSelect            bool
	DlSong              bool
	ForceRedownload     bool
	Counter             structs.Counter
	OkDict              map[string][]int
	LastDownloadedPaths []string
	DownloadedMeta      map[string]AudioMeta
	ActiveProgress      func(phase string, done, total int64)
	StaticCoverDownload bool

	downloadedMetaMu sync.Mutex
}

type JobContext struct {
	Session        *DownloadSession
	Token          string
	Storefront     string
	MediaUserToken string
}

func newDownloadSession(base structs.ConfigSet) *DownloadSession {
	return &DownloadSession{
		Context:             context.Background(),
		Config:              base,
		OkDict:              make(map[string][]int),
		DownloadedMeta:      make(map[string]AudioMeta),
		StaticCoverDownload: true,
	}
}

func (s *DownloadSession) resetState() {
	if s == nil {
		return
	}
	s.DlAtmos = false
	s.DlAac = false
	s.DlSelect = false
	s.DlSong = false
	s.Counter = structs.Counter{}
	s.OkDict = make(map[string][]int)
	s.clearDownloadState()
}

func (s *DownloadSession) downloadContext() context.Context {
	if s == nil || s.Context == nil {
		return context.Background()
	}
	return s.Context
}

func inferTelegramAudioFormatFromPath(path string, fallback string) string {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".flac":
		return telegramFormatFlac
	case ".aac":
		return telegramFormatAac
	case ".m4a", ".mp4":
		normalized := normalizeTelegramFormat(fallback)
		if normalized != "" {
			return normalized
		}
		return defaultTelegramFormat
	default:
		normalized := normalizeTelegramFormat(fallback)
		if normalized != "" {
			return normalized
		}
		return defaultTelegramFormat
	}
}

func (s *DownloadSession) recordDownloadedTrack(track *task.Track, format string) {
	if s == nil || track == nil || track.SavePath == "" {
		return
	}
	meta := AudioMeta{
		TrackID:        strings.TrimSpace(track.ID),
		Title:          strings.TrimSpace(track.Resp.Attributes.Name),
		Performer:      strings.TrimSpace(track.Resp.Attributes.ArtistName),
		DurationMillis: int64(track.Resp.Attributes.DurationInMillis),
		Format:         strings.TrimSpace(format),
		AlbumName:      strings.TrimSpace(track.Resp.Attributes.AlbumName),
		ComposerName:   strings.TrimSpace(track.Resp.Attributes.ComposerName),
		ReleaseDate:    strings.TrimSpace(track.Resp.Attributes.ReleaseDate),
		GenreNames:     append([]string(nil), track.Resp.Attributes.GenreNames...),
		AudioTraits:    append([]string(nil), track.Resp.Attributes.AudioTraits...),
		Storefront:     strings.TrimSpace(track.Storefront),
		HasLyrics:      track.Resp.Attributes.HasLyrics,
		ContentRating:  strings.TrimSpace(track.Resp.Attributes.ContentRating),
	}
	if meta.TrackID != "" {
		if override, ok := popSearchMeta(meta.TrackID); ok {
			if override.Title != "" {
				meta.Title = override.Title
			}
			if override.Performer != "" {
				meta.Performer = override.Performer
			}
		}
	}
	s.recordDownloadedFile(track.SavePath, meta)
}

func (s *DownloadSession) recordDownloadedFile(path string, meta AudioMeta) {
	if s == nil || strings.TrimSpace(path) == "" {
		return
	}
	if normalized := normalizeTelegramFormat(meta.Format); normalized != "" {
		meta.Format = normalized
	}
	s.LastDownloadedPaths = append(s.LastDownloadedPaths, path)
	s.downloadedMetaMu.Lock()
	s.DownloadedMeta[path] = meta
	s.downloadedMetaMu.Unlock()
}

func (s *DownloadSession) getDownloadedMeta(path string) (AudioMeta, bool) {
	if s == nil {
		return AudioMeta{}, false
	}
	s.downloadedMetaMu.Lock()
	defer s.downloadedMetaMu.Unlock()
	meta, ok := s.DownloadedMeta[path]
	return meta, ok
}

func (s *DownloadSession) clearDownloadState() {
	if s == nil {
		return
	}
	s.LastDownloadedPaths = nil
	s.downloadedMetaMu.Lock()
	s.DownloadedMeta = make(map[string]AudioMeta)
	s.downloadedMetaMu.Unlock()
}

func (s *DownloadSession) shouldDownloadStaticCover() bool {
	if s == nil {
		return true
	}
	return s.StaticCoverDownload
}

func (s *DownloadSession) shouldReuseExistingFiles() bool {
	if s == nil {
		return true
	}
	return !s.ForceRedownload
}

type telegramCacheFile struct {
	Version   int                       `json:"version"`
	Items     map[string]CachedAudio    `json:"items"`
	Documents map[string]CachedDocument `json:"documents,omitempty"`
	Videos    map[string]CachedVideo    `json:"videos,omitempty"`
}
