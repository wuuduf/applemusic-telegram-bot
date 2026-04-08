package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var forbiddenNames = regexp.MustCompile(`[/\\<>:"|?*]`)

var (
	ErrAlbumNotFound    = errors.New("album not found")
	ErrNoLyricsExported = errors.New("no lyrics exported")
)

type AlbumLyricsExportResult struct {
	AlbumName   string
	Paths       []string
	FailedCount int
}

func (s *Service) FetchLyricsOnly(songID string, storefront string, outputFormat string) (string, string, error) {
	var lastErr error
	lyricTypes := []string{"syllable-lyrics", "lyrics"}
	for _, lyricType := range lyricTypes {
		content, err := s.getLyrics(storefront, songID, lyricType, outputFormat)
		if err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(content) == "" {
			lastErr = fmt.Errorf("empty lyrics content")
			continue
		}
		return content, lyricType, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("lyrics unavailable")
	}
	return "", "", lastErr
}

func (s *Service) ExportAlbumLyrics(tempRoot string, albumID string, storefront string, format string) (*AlbumLyricsExportResult, error) {
	if strings.TrimSpace(albumID) == "" {
		return nil, fmt.Errorf("album id is empty")
	}
	resp, err := s.getAlbumResp(storefront, albumID)
	if err != nil {
		return nil, fmt.Errorf("failed to load album: %w", err)
	}
	if resp == nil {
		return nil, ErrAlbumNotFound
	}
	album, err := firstAlbumData(s.op("exportAlbumLyrics"), resp)
	if err != nil {
		return nil, err
	}
	albumDir, err := os.MkdirTemp(tempRoot, "lyrics-album-*")
	if err != nil {
		return nil, err
	}
	usedNames := make(map[string]struct{})
	paths := []string{}
	failed := 0
	for idx, track := range album.Relationships.Tracks.Data {
		if !strings.EqualFold(track.Type, "songs") || strings.TrimSpace(track.ID) == "" {
			continue
		}
		content, _, lerr := s.FetchLyricsOnly(track.ID, storefront, format)
		if lerr != nil || strings.TrimSpace(content) == "" {
			failed++
			continue
		}
		baseName := sanitizeFileBaseName(composeArtistTitle(track.Attributes.ArtistName, track.Attributes.Name))
		if baseName == "" {
			baseName = "track-" + track.ID
		}
		order := track.Attributes.TrackNumber
		if order <= 0 {
			order = idx + 1
		}
		fileName := fmt.Sprintf("%02d. %s.lyrics.%s", order, baseName, format)
		fileName = uniqueName(usedNames, fileName)
		fullPath := filepath.Join(albumDir, fileName)
		if werr := os.WriteFile(fullPath, []byte(content), 0644); werr != nil {
			failed++
			continue
		}
		paths = append(paths, fullPath)
	}
	if len(paths) == 0 {
		_ = os.RemoveAll(albumDir)
		return &AlbumLyricsExportResult{
			AlbumName:   strings.TrimSpace(album.Attributes.Name),
			FailedCount: failed,
		}, ErrNoLyricsExported
	}
	sort.Strings(paths)
	return &AlbumLyricsExportResult{
		AlbumName:   strings.TrimSpace(album.Attributes.Name),
		Paths:       paths,
		FailedCount: failed,
	}, nil
}

func sanitizeFileBaseName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "apple-music"
	}
	name = forbiddenNames.ReplaceAllString(name, "_")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.Join(strings.Fields(name), " ")
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "apple-music"
	}
	return name
}

func uniqueName(existing map[string]struct{}, candidate string) string {
	if _, ok := existing[candidate]; !ok {
		existing[candidate] = struct{}{}
		return candidate
	}
	ext := filepath.Ext(candidate)
	base := strings.TrimSuffix(candidate, ext)
	for i := 2; i <= 9999; i++ {
		name := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, ok := existing[name]; !ok {
			existing[name] = struct{}{}
			return name
		}
	}
	existing[candidate] = struct{}{}
	return candidate
}
