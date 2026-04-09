package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
)

const (
	telegramCacheDirPerm  = 0o700
	telegramCacheFilePerm = 0o600
)

func cacheProfileKey(settings ChatDownloadSettings) string {
	normalized := normalizeChatSettings(settings)
	return fmt.Sprintf("%s|aac:%s|mv:%s|lyr:%s|emb:%t-%t|auto:%t-%t-%t",
		normalized.Format,
		normalized.AACType,
		normalized.MVAudioType,
		normalized.LyricsFormat,
		normalized.EmbedLyrics,
		normalized.EmbedCover,
		normalized.AutoLyrics,
		normalized.AutoCover,
		normalized.AutoAnimated,
	)
}

func (b *TelegramBot) cacheKey(trackID, format string, compressed bool) string {
	normalized := normalizeTelegramFormat(format)
	if normalized == "" {
		normalized = defaultTelegramFormat
	}
	return fmt.Sprintf("%s|%s|%t", trackID, normalized, compressed)
}

func (b *TelegramBot) bundleZipCacheKey(mediaType, mediaID string, settings ChatDownloadSettings) string {
	return fmt.Sprintf("%s:%s|%s|zip", mediaType, mediaID, cacheProfileKey(settings))
}

func (b *TelegramBot) mvCacheKey(mvID string, settings ChatDownloadSettings, mode string) string {
	return fmt.Sprintf("%s:%s|%s|%s", mediaTypeMusicVideo, mvID, cacheProfileKey(settings), mode)
}

func (b *TelegramBot) loadCache() {
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	b.cache = make(map[string]CachedAudio)
	b.docCache = make(map[string]CachedDocument)
	b.videoCache = make(map[string]CachedVideo)
	target := filepath.Clean(strings.TrimSpace(b.cacheFile))
	if target == "" {
		return
	}
	if err := ensureNonSymlinkRegularFile(target); err != nil {
		return
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return
	}
	var payload telegramCacheFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}
	if payload.Documents != nil {
		b.docCache = payload.Documents
	}
	if payload.Videos != nil {
		b.videoCache = payload.Videos
	}
	if payload.Items == nil {
		if payload.Version > 0 && payload.Version < 4 {
			b.saveCacheLocked()
		}
		return
	}
	if payload.Version < 2 {
		migrated := make(map[string]CachedAudio)
		for key, entry := range payload.Items {
			parts := strings.Split(key, "|")
			if len(parts) == 2 {
				trackID := parts[0]
				compressed, err := strconv.ParseBool(parts[1])
				if err != nil {
					continue
				}
				entry.Compressed = compressed
				if entry.Format == "" {
					entry.Format = defaultTelegramFormat
				}
				migrated[b.cacheKey(trackID, entry.Format, entry.Compressed)] = entry
				continue
			}
			if len(parts) >= 3 {
				trackID := parts[0]
				format := normalizeTelegramFormat(parts[1])
				compressed, err := strconv.ParseBool(parts[2])
				if err != nil {
					continue
				}
				if format == "" {
					format = defaultTelegramFormat
				}
				entry.Compressed = compressed
				if entry.Format == "" {
					entry.Format = format
				}
				migrated[b.cacheKey(trackID, format, entry.Compressed)] = entry
			}
		}
		b.cache = migrated
		b.saveCacheLocked()
		return
	}
	b.cache = payload.Items
	for key, entry := range b.cache {
		if entry.Format == "" {
			parts := strings.Split(key, "|")
			if len(parts) >= 2 {
				entry.Format = normalizeTelegramFormat(parts[1])
			}
			if entry.Format == "" {
				entry.Format = defaultTelegramFormat
			}
			b.cache[key] = entry
		}
	}
	if payload.Version < 4 {
		b.saveCacheLocked()
	}
}

func (b *TelegramBot) saveCacheLocked() {
	target := filepath.Clean(strings.TrimSpace(b.cacheFile))
	if target == "" {
		return
	}
	payload := telegramCacheFile{
		Version:   4,
		Items:     b.cache,
		Documents: b.docCache,
		Videos:    b.videoCache,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Printf("telegram cache save failed (%s, marshal): %v\n", target, err)
		appendRuntimeErrorLogf("telegram cache save failed (%s, marshal): %v", target, err)
		return
	}
	if err := atomicWriteTelegramCacheFile(target, data); err != nil {
		fmt.Printf("telegram cache save failed (%s, atomic write): %v\n", target, err)
		appendRuntimeErrorLogf("telegram cache save failed (%s, atomic write): %v", target, err)
	}
}

func ensureNonSymlinkRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink path: %s", path)
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular cache file: %s", path)
	}
	return nil
}

func ensureCacheDirectory(path string) (string, error) {
	dir := filepath.Clean(filepath.Dir(path))
	if dir == "" {
		return "", fmt.Errorf("invalid cache directory")
	}
	if dir != "." {
		if info, err := os.Lstat(dir); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("refusing symlink cache directory: %s", dir)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("cache directory is not a directory: %s", dir)
			}
		} else if os.IsNotExist(err) {
			if err := os.MkdirAll(dir, telegramCacheDirPerm); err != nil {
				return "", err
			}
		} else {
			return "", err
		}
		if err := os.Chmod(dir, telegramCacheDirPerm); err != nil && !os.IsPermission(err) {
			return "", err
		}
	}
	return dir, nil
}

func atomicWriteTelegramCacheFile(target string, data []byte) error {
	if err := ensureNonSymlinkRegularFile(target); err != nil {
		return err
	}
	dir, err := ensureCacheDirectory(target)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".telegram-cache-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	fail := func(writeErr error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return writeErr
	}
	if err := tmp.Chmod(telegramCacheFilePerm); err != nil && !os.IsPermission(err) {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := ensureNonSymlinkRegularFile(target); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(target, telegramCacheFilePerm); err != nil && !os.IsPermission(err) {
		return err
	}
	return nil
}

func (b *TelegramBot) fetchTrackMeta(trackID string) (AudioMeta, error) {
	if trackID == "" {
		return AudioMeta{}, fmt.Errorf("empty track id")
	}
	resp, err := ampapi.GetSongResp(Config.Storefront, trackID, b.searchLanguage(), b.appleToken)
	if err != nil || resp == nil {
		if err != nil {
			return AudioMeta{}, err
		}
		return AudioMeta{}, fmt.Errorf("empty song response")
	}
	data, err := firstSongData("main.telegram.fetchTrackMeta", resp)
	if err != nil {
		return AudioMeta{}, err
	}
	return AudioMeta{
		TrackID:        trackID,
		Title:          strings.TrimSpace(data.Attributes.Name),
		Performer:      strings.TrimSpace(data.Attributes.ArtistName),
		DurationMillis: int64(data.Attributes.DurationInMillis),
	}, nil
}

func (b *TelegramBot) enrichCachedAudio(trackID string, entry CachedAudio) CachedAudio {
	updated := false
	sizeBytes := entry.SizeBytes
	if sizeBytes <= 0 {
		sizeBytes = entry.FileSize
		if sizeBytes > 0 {
			entry.SizeBytes = sizeBytes
			updated = true
		}
	}
	if trackID != "" && (entry.DurationMillis <= 0 || entry.Title == "" || entry.Performer == "") {
		if meta, err := b.fetchTrackMeta(trackID); err == nil {
			if entry.DurationMillis <= 0 && meta.DurationMillis > 0 {
				entry.DurationMillis = meta.DurationMillis
				updated = true
			}
			if entry.Title == "" && meta.Title != "" {
				entry.Title = meta.Title
				updated = true
			}
			if entry.Performer == "" && meta.Performer != "" {
				entry.Performer = meta.Performer
				updated = true
			}
		}
	}
	if entry.BitrateKbps <= 0 && sizeBytes > 0 && entry.DurationMillis > 0 {
		entry.BitrateKbps = calcBitrateKbps(sizeBytes, entry.DurationMillis)
		updated = true
	}
	if updated && trackID != "" {
		b.storeCachedAudio(trackID, entry)
	}
	return entry
}

func (b *TelegramBot) storeCachedAudio(trackID string, entry CachedAudio) {
	if trackID == "" || entry.FileID == "" {
		return
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.cache == nil {
		b.cache = make(map[string]CachedAudio)
	}
	entry.Format = normalizeTelegramFormat(entry.Format)
	if entry.Format == "" {
		entry.Format = defaultTelegramFormat
	}
	entry.UpdatedAt = time.Now()
	b.cache[b.cacheKey(trackID, entry.Format, entry.Compressed)] = entry
	b.saveCacheLocked()
}

func (b *TelegramBot) deleteCachedAudio(trackID, format string, compressed bool) {
	if trackID == "" {
		return
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.cache == nil {
		return
	}
	delete(b.cache, b.cacheKey(trackID, format, compressed))
	b.saveCacheLocked()
}

func (b *TelegramBot) storeCachedDocument(key string, entry CachedDocument) {
	if key == "" || entry.FileID == "" {
		return
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.docCache == nil {
		b.docCache = make(map[string]CachedDocument)
	}
	entry.UpdatedAt = time.Now()
	b.docCache[key] = entry
	b.saveCacheLocked()
}

func (b *TelegramBot) getCachedDocument(key string) (CachedDocument, bool) {
	if key == "" {
		return CachedDocument{}, false
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.docCache == nil {
		return CachedDocument{}, false
	}
	entry, ok := b.docCache[key]
	return entry, ok
}

func (b *TelegramBot) deleteCachedDocument(key string) {
	if key == "" {
		return
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.docCache == nil {
		return
	}
	delete(b.docCache, key)
	b.saveCacheLocked()
}

func (b *TelegramBot) storeCachedVideo(key string, entry CachedVideo) {
	if key == "" || entry.FileID == "" {
		return
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.videoCache == nil {
		b.videoCache = make(map[string]CachedVideo)
	}
	entry.UpdatedAt = time.Now()
	b.videoCache[key] = entry
	b.saveCacheLocked()
}

func (b *TelegramBot) getCachedVideo(key string) (CachedVideo, bool) {
	if key == "" {
		return CachedVideo{}, false
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.videoCache == nil {
		return CachedVideo{}, false
	}
	entry, ok := b.videoCache[key]
	return entry, ok
}

func (b *TelegramBot) deleteCachedVideo(key string) {
	if key == "" {
		return
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.videoCache == nil {
		return
	}
	delete(b.videoCache, key)
	b.saveCacheLocked()
}

func deleteCacheEntriesWithPrefix[T any](items map[string]T, prefix string) int {
	if len(items) == 0 || strings.TrimSpace(prefix) == "" {
		return 0
	}
	removed := 0
	for key := range items {
		if strings.HasPrefix(key, prefix) {
			delete(items, key)
			removed++
		}
	}
	return removed
}

func (b *TelegramBot) purgeTargetCaches(target *AppleURLTarget) int {
	if b == nil || target == nil {
		return 0
	}
	mediaType := strings.TrimSpace(target.MediaType)
	mediaID := strings.TrimSpace(target.ID)
	if mediaType == "" || mediaID == "" {
		return 0
	}

	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()

	removed := 0
	switch mediaType {
	case mediaTypeSong:
		removed += deleteCacheEntriesWithPrefix(b.cache, mediaID+"|")
		removed += deleteCacheEntriesWithPrefix(b.docCache, mediaTypeSong+":"+mediaID+"|")
	case mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation:
		removed += deleteCacheEntriesWithPrefix(b.docCache, mediaType+":"+mediaID+"|")
	case mediaTypeMusicVideo:
		prefix := mediaTypeMusicVideo + ":" + mediaID + "|"
		removed += deleteCacheEntriesWithPrefix(b.docCache, prefix)
		removed += deleteCacheEntriesWithPrefix(b.videoCache, prefix)
	}
	if removed > 0 {
		b.saveCacheLocked()
	}
	return removed
}

func (b *TelegramBot) getCachedAudio(trackID string, maxBytes int64, format string) (CachedAudio, bool) {
	if trackID == "" {
		return CachedAudio{}, false
	}
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	if b.cache == nil {
		return CachedAudio{}, false
	}
	var candidates []CachedAudio
	normalized := normalizeTelegramFormat(format)
	if normalized != "" {
		if entry, ok := b.cache[b.cacheKey(trackID, normalized, false)]; ok {
			if entry.Format == "" {
				entry.Format = normalized
			}
			candidates = append(candidates, entry)
		}
		if entry, ok := b.cache[b.cacheKey(trackID, normalized, true)]; ok {
			if entry.Format == "" {
				entry.Format = normalized
			}
			candidates = append(candidates, entry)
		}
	} else {
		prefix := trackID + "|"
		for key, entry := range b.cache {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			if entry.Format == "" {
				parts := strings.Split(key, "|")
				if len(parts) >= 3 {
					entry.Format = normalizeTelegramFormat(parts[1])
				}
				if entry.Format == "" {
					entry.Format = defaultTelegramFormat
				}
			}
			candidates = append(candidates, entry)
		}
	}
	var best *CachedAudio
	for _, entry := range candidates {
		entrySize := entry.SizeBytes
		if entrySize <= 0 {
			entrySize = entry.FileSize
		}
		if maxBytes > 0 && entrySize > 0 && entrySize > maxBytes {
			continue
		}
		if best == nil {
			copyEntry := entry
			best = &copyEntry
			continue
		}
		if best.Compressed && !entry.Compressed {
			copyEntry := entry
			best = &copyEntry
			continue
		}
		bestSize := best.SizeBytes
		if bestSize <= 0 {
			bestSize = best.FileSize
		}
		if best.Compressed == entry.Compressed && entrySize > bestSize {
			copyEntry := entry
			best = &copyEntry
		}
	}
	if best == nil {
		return CachedAudio{}, false
	}
	return *best, true
}
