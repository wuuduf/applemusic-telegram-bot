package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sharedcatalog "github.com/wuuduf/applemusic-telegram-bot/internal/catalog"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
)

func (b *TelegramBot) catalogService() *sharedcatalog.Service {
	return &sharedcatalog.Service{
		AppleToken:     b.appleToken,
		MediaUserToken: Config.MediaUserToken,
		Language:       Config.Language,
		HTTPClient:     b.client,
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		OpPrefix:       "main.telegram",
	}
}

func (b *TelegramBot) fetchArtistProfile(storefront string, artistID string) (string, string, error) {
	return b.catalogService().FetchArtistProfile(storefront, artistID)
}

func collectUniqueArtistSongIDs(items []sharedcatalog.ArtistRelationshipItem) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		trackID := strings.TrimSpace(item.ID)
		if trackID == "" {
			continue
		}
		if _, ok := seen[trackID]; ok {
			continue
		}
		seen[trackID] = struct{}{}
		ids = append(ids, trackID)
	}
	return ids
}

func collectUniqueArtistCollectionItems(items []sharedcatalog.ArtistRelationshipItem) []sharedcatalog.ArtistRelationshipItem {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	filtered := make([]sharedcatalog.ArtistRelationshipItem, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, item)
	}
	return filtered
}

func (b *TelegramBot) fetchArtistSongIDs(storefront string, artistID string) ([]string, error) {
	artistID = strings.TrimSpace(artistID)
	if artistID == "" {
		return nil, fmt.Errorf("artist id is empty")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	related, err := b.catalogService().FetchArtistRelationshipAll(storefront, artistID, "songs")
	if err != nil {
		return nil, err
	}
	return collectUniqueArtistSongIDs(related), nil
}

func (b *TelegramBot) fetchArtistLPAlbums(storefront string, artistID string) ([]sharedcatalog.ArtistRelationshipItem, error) {
	artistID = strings.TrimSpace(artistID)
	if artistID == "" {
		return nil, fmt.Errorf("artist id is empty")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	service := b.catalogService()
	service.Language = b.searchLanguage()
	related, err := service.FetchArtistViewAll(storefront, artistID, "full-albums")
	if err != nil {
		return nil, err
	}
	return collectUniqueArtistCollectionItems(related), nil
}

func collectUniqueCuratorAlbumItems(items []sharedcatalog.CuratorAlbumItem) []sharedcatalog.CuratorAlbumItem {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	filtered := make([]sharedcatalog.CuratorAlbumItem, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, item)
	}
	return filtered
}

func (b *TelegramBot) fetchCuratorAlbums(storefront string, curatorID string) ([]sharedcatalog.CuratorAlbumItem, error) {
	curatorID = strings.TrimSpace(curatorID)
	if curatorID == "" {
		return nil, fmt.Errorf("curator id is empty")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	service := b.catalogService()
	service.Language = b.searchLanguage()
	albums, err := service.FetchCuratorAlbums(storefront, curatorID)
	if err != nil {
		return nil, err
	}
	return collectUniqueCuratorAlbumItems(albums), nil
}

func buildArtistSongTrackStubs(trackIDs []string) []task.Track {
	if len(trackIDs) == 0 {
		return nil
	}
	total := len(trackIDs)
	tracks := make([]task.Track, 0, total)
	for idx, trackID := range trackIDs {
		tracks = append(tracks, task.Track{
			ID:        trackID,
			TaskNum:   idx + 1,
			TaskTotal: total,
		})
	}
	return tracks
}

func (b *TelegramBot) stageArtistLPAlbumsCollection(session *DownloadSession, artistID string, storefront string) error {
	if session == nil {
		return fmt.Errorf("download session is nil")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	albums, err := b.fetchArtistLPAlbums(storefront, artistID)
	if err != nil {
		return err
	}
	for _, album := range albums {
		if err := session.downloadContext().Err(); err != nil {
			return err
		}
		albumID := strings.TrimSpace(album.ID)
		if albumID == "" {
			continue
		}
		if err := ripAlbum(session, albumID, b.appleToken, storefront, session.Config.MediaUserToken, ""); err != nil {
			if ctxErr := session.downloadContext().Err(); ctxErr != nil {
				return ctxErr
			}
			fmt.Printf("Failed to rip artist LP album %s: %v\n", albumID, err)
			session.Counter.Error++
		}
	}
	return nil
}

func (b *TelegramBot) stageCuratorAlbumsCollection(session *DownloadSession, curatorID string, storefront string) error {
	if session == nil {
		return fmt.Errorf("download session is nil")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	albums, err := b.fetchCuratorAlbums(storefront, curatorID)
	if err != nil {
		return err
	}
	for _, album := range albums {
		if err := session.downloadContext().Err(); err != nil {
			return err
		}
		albumID := strings.TrimSpace(album.ID)
		if albumID == "" {
			continue
		}
		if err := ripAlbum(session, albumID, b.appleToken, storefront, session.Config.MediaUserToken, ""); err != nil {
			if ctxErr := session.downloadContext().Err(); ctxErr != nil {
				return ctxErr
			}
			fmt.Printf("Failed to rip curator album %s: %v\n", albumID, err)
			session.Counter.Error++
		}
	}
	return nil
}

func (b *TelegramBot) downloadArtistSongsCollection(session *DownloadSession, artistID string, storefront string) error {
	if session == nil {
		return fmt.Errorf("download session is nil")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	trackIDs, err := b.fetchArtistSongIDs(storefront, artistID)
	if err != nil {
		return err
	}
	for _, trackID := range trackIDs {
		if err := session.downloadContext().Err(); err != nil {
			return err
		}
		if err := ripSong(session, trackID, b.appleToken, storefront, session.Config.MediaUserToken); err != nil {
			if ctxErr := session.downloadContext().Err(); ctxErr != nil {
				return ctxErr
			}
			fmt.Printf("Failed to rip artist song %s: %v\n", trackID, err)
			session.Counter.Error++
		}
	}
	return nil
}

type albumCollectionDownloadResult struct {
	Total     int
	Succeeded int
	Failed    int
	SentAny   bool
}

func formatArtistLPAlbumProgress(index int, total int, album sharedcatalog.ArtistRelationshipItem) string {
	albumName := strings.TrimSpace(album.Name)
	if albumName == "" {
		albumName = "album " + strings.TrimSpace(album.ID)
	}
	return fmt.Sprintf("Album %d/%d · %s", index+1, total, albumName)
}

func formatCuratorAlbumProgress(index int, total int, album sharedcatalog.CuratorAlbumItem) string {
	albumName := strings.TrimSpace(album.Name)
	if albumName == "" {
		albumName = "album " + strings.TrimSpace(album.ID)
	}
	return fmt.Sprintf("Album %d/%d · %s", index+1, total, albumName)
}

func isContextCancellationError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (b *TelegramBot) sendPreparedDownloadPaths(session *DownloadSession, chatID int64, replyToID int, status *DownloadStatus, settings ChatDownloadSettings, paths []string, primaryCount int) bool {
	if len(paths) == 0 {
		return false
	}
	sentAny := false
	for idx, path := range paths {
		if idx >= primaryCount && !sentAny {
			if status != nil {
				status.UpdateSync("Primary media upload failed. Skip extra files (lyrics/cover/animated).", 0, 0)
			}
			break
		}
		if err := b.sendDownloadedPathWithRetry(session, chatID, path, replyToID, status, settings); err != nil {
			sanitized := sanitizeTelegramError(err, b.token)
			fmt.Printf("send file error (%s): %s\n", path, sanitized)
			appendRuntimeErrorLogf("send file error (%s): %s", path, sanitized)
			if status != nil {
				status.Update(fmt.Sprintf("Failed to send %s: %s", filepath.Base(path), b.sanitizeTelegramErr(err)), 0, 0)
			}
			continue
		}
		sentAny = true
	}
	return sentAny
}

func (b *TelegramBot) runArtistLPAlbumZipTransfer(chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, albumID string, storefront string, status *DownloadStatus, progress func(phase string, done, total int64)) (bool, error) {
	return b.runArtistLPAlbumZipTransferWithContext(b.operationContext(), chatID, replyToID, forceRefresh, settings, albumID, storefront, status, progress)
}

func (b *TelegramBot) runArtistLPAlbumZipTransferWithContext(ctx context.Context, chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, albumID string, storefront string, status *DownloadStatus, progress func(phase string, done, total int64)) (bool, error) {
	if !forceRefresh && b.trySendCachedBundleZip(chatID, mediaTypeAlbum, albumID, replyToID, settings) {
		return true, nil
	}

	var (
		session      *DownloadSession
		paths        []string
		primaryCount int
	)
	coreErr := func() error {
		b.downloadCoreMu.Lock()
		defer b.downloadCoreMu.Unlock()

		var err error
		session, err = b.prepareTelegramDownloadSessionWithContext(ctx, false, forceRefresh, settings, mediaTypeAlbum, progress)
		if err != nil {
			return err
		}
		if status != nil {
			status.Update("Downloading", 0, 0)
		}
		if err := ripAlbum(session, albumID, b.appleToken, storefront, session.Config.MediaUserToken, ""); err != nil {
			return err
		}

		paths = append([]string{}, session.LastDownloadedPaths...)
		primaryCount = len(paths)
		if primaryCount == 0 {
			return errNoFilesDownloaded
		}
		paths = b.augmentDownloadedPaths(paths, settings)
		fmt.Printf("telegram artist LP album ready chat=%d album=%s primary=%d total=%d transfer=%s\n", chatID, albumID, primaryCount, len(paths), transferModeZip)
		return nil
	}()
	if coreErr != nil {
		return false, coreErr
	}
	if forceRefresh && primaryCount > 0 {
		b.purgeTargetCaches(&AppleURLTarget{MediaType: mediaTypeAlbum, ID: albumID})
	}
	if b.cleanupTracker != nil {
		b.cleanupTracker.RecordPaths(paths)
	}

	if status != nil {
		status.Update("Zipping", 0, 0)
	}
	zipPath, displayName, err := createZipFromPaths(paths)
	if err != nil {
		return false, err
	}
	defer os.Remove(zipPath)

	cacheKey := b.bundleZipCacheKey(mediaTypeAlbum, albumID, settings)
	if err := b.sendDocumentFileWithContext(session.downloadContext(), chatID, zipPath, displayName, replyToID, status, cacheKey); err != nil {
		sanitized := sanitizeTelegramError(err, b.token)
		fmt.Printf("send artist LP ZIP error (album:%s): %s\n", albumID, sanitized)
		appendRuntimeErrorLogf("send artist LP ZIP error (album:%s): %s", albumID, sanitized)
		if zipSendFailureNeedsFallback(err) {
			if status != nil {
				status.UpdateSync("ZIP exceeds Telegram limit or upload stalled, fallback to one-by-one transfer.", 0, 0)
			}
			return b.sendPreparedDownloadPaths(session, chatID, replyToID, status, settings, paths, primaryCount), nil
		}
		return false, err
	}
	return true, nil
}

func (b *TelegramBot) runArtistLPAlbumTransfer(chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, transferMode string, albumID string, storefront string, status *DownloadStatus, statusLabelPrefix string, progress func(phase string, done, total int64)) (bool, error) {
	return b.runArtistLPAlbumTransferWithContext(b.operationContext(), chatID, replyToID, forceRefresh, settings, transferMode, albumID, storefront, status, statusLabelPrefix, progress)
}

func (b *TelegramBot) runArtistLPAlbumTransferWithContext(ctx context.Context, chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, transferMode string, albumID string, storefront string, status *DownloadStatus, statusLabelPrefix string, progress func(phase string, done, total int64)) (bool, error) {
	switch transferMode {
	case transferModeZip:
		return b.runArtistLPAlbumZipTransferWithContext(ctx, chatID, replyToID, forceRefresh, settings, albumID, storefront, status, progress)
	default:
		handled, sentAny, err := b.runCollectionOneByOneSequentialWithContext(ctx, chatID, replyToID, forceRefresh, settings, mediaTypeAlbum, albumID, storefront, status, statusLabelPrefix, progress)
		if !handled {
			return false, fmt.Errorf("album one-by-one handler unavailable")
		}
		return sentAny, err
	}
}

func (b *TelegramBot) downloadArtistLPAlbumsCollection(chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, artistID string, storefront string, transferMode string, status *DownloadStatus) (albumCollectionDownloadResult, error) {
	return b.downloadArtistLPAlbumsCollectionWithContext(b.operationContext(), chatID, replyToID, forceRefresh, settings, artistID, storefront, transferMode, status)
}

func (b *TelegramBot) downloadArtistLPAlbumsCollectionWithContext(ctx context.Context, chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, artistID string, storefront string, transferMode string, status *DownloadStatus) (albumCollectionDownloadResult, error) {
	result := albumCollectionDownloadResult{}
	if storefront == "" {
		storefront = Config.Storefront
	}
	albums, err := b.fetchArtistLPAlbums(storefront, artistID)
	if err != nil {
		return result, err
	}
	result.Total = len(albums)
	if len(albums) == 0 {
		return result, errNoFilesDownloaded
	}

	for idx, album := range albums {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		albumID := strings.TrimSpace(album.ID)
		if albumID == "" {
			result.Failed++
			continue
		}
		albumProgressLabel := formatArtistLPAlbumProgress(idx, len(albums), album)
		if status != nil {
			status.UpdateSync(albumProgressLabel, int64(idx), int64(len(albums)))
		}
		albumProgress := func(phase string, done, total int64) {
			if status == nil {
				return
			}
			phase = strings.TrimSpace(phase)
			if phase == "" {
				phase = "Working"
			}
			status.Update(albumProgressLabel+" · "+phase, done, total)
		}
		sentAny, albumErr := b.runArtistLPAlbumTransferWithContext(ctx, chatID, replyToID, forceRefresh, settings, transferMode, albumID, storefront, status, albumProgressLabel, albumProgress)
		if albumErr != nil {
			if isContextCancellationError(albumErr) {
				return result, albumErr
			}
			result.Failed++
			sanitized := sanitizeTelegramError(albumErr, b.token)
			fmt.Printf("artist LP album failed artist=%s album=%s transfer=%s: %s\n", artistID, albumID, transferMode, sanitized)
			appendRuntimeErrorLogf("artist LP album failed artist=%s album=%s transfer=%s: %s", artistID, albumID, transferMode, sanitized)
			if status != nil {
				status.UpdateSync(fmt.Sprintf("%s failed: %v", albumProgressLabel, albumErr), int64(idx+1), int64(len(albums)))
			}
			continue
		}
		if sentAny {
			result.SentAny = true
			result.Succeeded++
		} else {
			result.Failed++
			if status != nil {
				status.UpdateSync(albumProgressLabel+" (no upload success)", int64(idx+1), int64(len(albums)))
			}
			continue
		}
		if status != nil {
			status.UpdateSync(albumProgressLabel, int64(idx+1), int64(len(albums)))
		}
	}
	return result, nil
}

func (b *TelegramBot) downloadCuratorAlbumsCollection(chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, curatorID string, storefront string, transferMode string, status *DownloadStatus) (albumCollectionDownloadResult, error) {
	return b.downloadCuratorAlbumsCollectionWithContext(b.operationContext(), chatID, replyToID, forceRefresh, settings, curatorID, storefront, transferMode, status)
}

func (b *TelegramBot) downloadCuratorAlbumsCollectionWithContext(ctx context.Context, chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, curatorID string, storefront string, transferMode string, status *DownloadStatus) (albumCollectionDownloadResult, error) {
	result := albumCollectionDownloadResult{}
	if storefront == "" {
		storefront = Config.Storefront
	}
	albums, err := b.fetchCuratorAlbums(storefront, curatorID)
	if err != nil {
		return result, err
	}
	result.Total = len(albums)
	if len(albums) == 0 {
		return result, errNoFilesDownloaded
	}

	for idx, album := range albums {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		albumID := strings.TrimSpace(album.ID)
		if albumID == "" {
			result.Failed++
			continue
		}
		albumProgressLabel := formatCuratorAlbumProgress(idx, len(albums), album)
		if status != nil {
			status.UpdateSync(albumProgressLabel, int64(idx), int64(len(albums)))
		}
		albumProgress := func(phase string, done, total int64) {
			if status == nil {
				return
			}
			phase = strings.TrimSpace(phase)
			if phase == "" {
				phase = "Working"
			}
			status.Update(albumProgressLabel+" · "+phase, done, total)
		}
		sentAny, albumErr := b.runArtistLPAlbumTransferWithContext(ctx, chatID, replyToID, forceRefresh, settings, transferMode, albumID, storefront, status, albumProgressLabel, albumProgress)
		if albumErr != nil {
			if isContextCancellationError(albumErr) {
				return result, albumErr
			}
			result.Failed++
			sanitized := sanitizeTelegramError(albumErr, b.token)
			fmt.Printf("curator album failed curator=%s album=%s transfer=%s: %s\n", curatorID, albumID, transferMode, sanitized)
			appendRuntimeErrorLogf("curator album failed curator=%s album=%s transfer=%s: %s", curatorID, albumID, transferMode, sanitized)
			if status != nil {
				status.UpdateSync(fmt.Sprintf("%s failed: %v", albumProgressLabel, albumErr), int64(idx+1), int64(len(albums)))
			}
			continue
		}
		if sentAny {
			result.SentAny = true
			result.Succeeded++
		} else {
			result.Failed++
			if status != nil {
				status.UpdateSync(albumProgressLabel+" (no upload success)", int64(idx+1), int64(len(albums)))
			}
			continue
		}
		if status != nil {
			status.UpdateSync(albumProgressLabel, int64(idx+1), int64(len(albums)))
		}
	}
	return result, nil
}
