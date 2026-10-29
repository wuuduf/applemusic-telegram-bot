package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
)

func (b *TelegramBot) trySendCachedTrack(chatID int64, replyToID int, trackID string, settings ChatDownloadSettings) bool {
	normalized := normalizeChatSettings(settings)
	// 仅复用“歌词+封面均内嵌”的缓存音频，避免和关闭内嵌的设置混淆。
	if !normalized.EmbedLyrics || !normalized.EmbedCover {
		return false
	}
	entry, ok := b.getCachedAudio(trackID, b.maxFileBytes, normalized.Format)
	if !ok {
		return false
	}
	if err := b.sendAudioByFileID(chatID, entry, replyToID, trackID); err != nil {
		b.deleteCachedAudio(trackID, entry.Format, entry.Compressed)
		return false
	}
	return true
}

func (b *TelegramBot) trySendCachedBundleZip(chatID int64, mediaType string, mediaID string, replyToID int, settings ChatDownloadSettings) bool {
	if mediaID == "" || mediaType == "" {
		return false
	}
	key := b.bundleZipCacheKey(mediaType, mediaID, settings)
	entry, ok := b.getCachedDocument(key)
	if !ok {
		return false
	}
	if err := b.sendDocumentByFileID(chatID, entry, replyToID); err != nil {
		b.deleteCachedDocument(key)
		return false
	}
	return true
}

func (b *TelegramBot) trySendCachedMusicVideo(chatID int64, replyToID int, mvID string, settings ChatDownloadSettings, variantKey string) bool {
	if mvID == "" {
		return false
	}
	videoKey := b.mvCacheKey(mvID, settings, variantKey, "video")
	if entry, ok := b.getCachedVideo(videoKey); ok {
		if err := b.sendVideoByFileID(chatID, entry, replyToID); err == nil {
			return true
		}
		b.deleteCachedVideo(videoKey)
	}
	documentKey := b.mvCacheKey(mvID, settings, variantKey, "document")
	if entry, ok := b.getCachedDocument(documentKey); ok {
		if err := b.sendDocumentByFileID(chatID, entry, replyToID); err == nil {
			return true
		}
		b.deleteCachedDocument(documentKey)
	}
	return false
}

func shouldUseTelegramCollectionSequentialOneByOne(single bool, transferMode string, mediaType string) bool {
	if single || transferMode != transferModeOneByOne {
		return false
	}
	switch mediaType {
	case mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation, mediaTypeArtistSongs:
		return true
	default:
		return false
	}
}

func (b *TelegramBot) prepareTelegramDownloadSession(single bool, forceRefresh bool, settings ChatDownloadSettings, mediaType string, progress func(phase string, done, total int64)) (*DownloadSession, error) {
	return b.prepareTelegramDownloadSessionWithContext(b.operationContext(), single, forceRefresh, settings, mediaType, progress)
}

func (b *TelegramBot) prepareTelegramDownloadSessionWithContext(ctx context.Context, single bool, forceRefresh bool, settings ChatDownloadSettings, mediaType string, progress func(phase string, done, total int64)) (*DownloadSession, error) {
	session := b.newBotDownloadSession(Config)
	if ctx != nil {
		session.Context = ctx
	}
	session.resetState()
	session.DlSelect = false
	session.ForceRedownload = forceRefresh
	if single {
		session.DlSong = true
	}
	session.Config.AacType = settings.AACType
	session.Config.MVAudioType = settings.MVAudioType
	applyTelegramAudioEmbeddingPolicy(session, settings, mediaType)

	switch settings.Format {
	case telegramFormatAtmos:
		session.DlAtmos = true
	case telegramFormatAac:
		session.DlAac = true
	}

	session.Config.ConvertAfterDownload = false
	if settings.Format == telegramFormatFlac {
		session.Config.ConvertAfterDownload = true
		session.Config.ConvertFormat = telegramFormatFlac
		session.Config.ConvertKeepOriginal = false
		session.Config.ConvertSkipLossyToLossless = false
		if _, err := exec.LookPath(session.Config.FFmpegPath); err != nil {
			return nil, fmt.Errorf("ffmpeg not found at '%s'", session.Config.FFmpegPath)
		}
	} else {
		session.Config.ConvertFormat = ""
	}

	session.ActiveProgress = progress
	return session, nil
}

func (b *TelegramBot) resolveCollectionTracksForSequential(session *DownloadSession, mediaType string, mediaID string, storefront string) ([]task.Track, bool, error) {
	if session == nil {
		return nil, false, fmt.Errorf("download session is nil")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	switch mediaType {
	case mediaTypeAlbum:
		ctx, err := resolveAlbumMediaStage(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken, "")
		if err != nil {
			return nil, false, err
		}
		prepareAlbumWorkspaceStage(ctx)
		if ctx == nil || ctx.album == nil {
			return nil, false, nil
		}
		return ctx.album.Tracks, false, nil
	case mediaTypePlaylist:
		ctx, err := resolvePlaylistMediaStage(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		if err != nil {
			return nil, false, err
		}
		preparePlaylistWorkspaceStage(ctx)
		if ctx == nil || ctx.playlist == nil {
			return nil, false, nil
		}
		return ctx.playlist.Tracks, false, nil
	case mediaTypeStation:
		ctx, err := resolveStationMediaStage(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		if err != nil {
			return nil, false, err
		}
		if ctx != nil && ctx.station != nil && ctx.station.Type == "stream" {
			return nil, true, nil
		}
		prepareStationWorkspaceStage(ctx)
		if ctx == nil || ctx.station == nil {
			return nil, false, nil
		}
		return ctx.station.Tracks, false, nil
	case mediaTypeArtistSongs:
		trackIDs, err := b.fetchArtistSongIDs(storefront, mediaID)
		if err != nil {
			return nil, false, err
		}
		return buildArtistSongTrackStubs(trackIDs), false, nil
	default:
		return nil, false, fmt.Errorf("unsupported collection type for sequential handling: %s", mediaType)
	}
}

func shouldReportCollectionTrackProgress(index int, total int) bool {
	if total <= 0 || index < 0 {
		return false
	}
	completed := index + 1
	if completed <= 1 || completed >= total {
		return true
	}
	return completed%downloadStatusTrackStep == 0
}

func (b *TelegramBot) sendArtistSongsProgressUpdate(chatID int64, replyToID int, mediaType string, completed int, total int) {
	if b == nil || b.client == nil {
		return
	}
	if mediaType != mediaTypeArtistSongs || completed <= 0 || total <= 0 {
		return
	}
	if completed%artistSongsProgressNotifyStep != 0 {
		return
	}
	_ = b.sendMessageWithReply(chatID, fmt.Sprintf("%d/%d", completed, total), nil, replyToID)
}

func (b *TelegramBot) runCollectionOneByOneSequential(chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, mediaType string, mediaID string, storefront string, status *DownloadStatus, statusLabelPrefix string, progress func(phase string, done, total int64)) (bool, bool, error) {
	return b.runCollectionOneByOneSequentialWithContext(b.operationContext(), chatID, replyToID, forceRefresh, settings, mediaType, mediaID, storefront, status, statusLabelPrefix, progress)
}

func (b *TelegramBot) runCollectionOneByOneSequentialWithContext(ctx context.Context, chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, mediaType string, mediaID string, storefront string, status *DownloadStatus, statusLabelPrefix string, progress func(phase string, done, total int64)) (bool, bool, error) {
	if !shouldUseTelegramCollectionSequentialOneByOne(false, transferModeOneByOne, mediaType) {
		return false, false, nil
	}
	if storefront == "" {
		storefront = Config.Storefront
	}

	var session *DownloadSession
	var tracks []task.Track
	stationStream := false
	resolveErr := func() error {
		// Keep core serialized to avoid heavy external tool contention.
		b.downloadCoreMu.Lock()
		defer b.downloadCoreMu.Unlock()

		var err error
		session, err = b.prepareTelegramDownloadSessionWithContext(ctx, false, forceRefresh, settings, mediaType, progress)
		if err != nil {
			return err
		}
		tracks, stationStream, err = b.resolveCollectionTracksForSequential(session, mediaType, mediaID, storefront)
		return err
	}()
	if resolveErr != nil {
		return true, false, resolveErr
	}
	if stationStream {
		// Stream station should keep legacy behavior.
		return false, false, nil
	}
	if len(tracks) == 0 {
		return true, false, errNoFilesDownloaded
	}

	sentAny := false
	downloadedAny := false
	sentExtraPaths := make(map[string]struct{})
	totalTracks := len(tracks)

	for idx := range tracks {
		if err := session.downloadContext().Err(); err != nil {
			return true, sentAny, err
		}
		track := &tracks[idx]
		trackLabel := fmt.Sprintf("Track %d/%d", idx+1, totalTracks)
		if strings.TrimSpace(statusLabelPrefix) != "" {
			trackLabel = strings.TrimSpace(statusLabelPrefix) + " · " + trackLabel
		}
		reportTrackProgress := shouldReportCollectionTrackProgress(idx, totalTracks)
		if status != nil && reportTrackProgress {
			status.Update(trackLabel, int64(idx), int64(totalTracks))
		}

		trackID := strings.TrimSpace(track.ID)
		if !forceRefresh && trackID != "" && b.trySendCachedTrack(chatID, replyToID, trackID, settings) {
			sentAny = true
			if status != nil && reportTrackProgress {
				status.Update(trackLabel+" (cached)", int64(idx+1), int64(totalTracks))
			}
			b.sendArtistSongsProgressUpdate(chatID, replyToID, mediaType, idx+1, totalTracks)
			continue
		}

		downloadFailed := false
		// Keep core serialized to avoid heavy external tool contention.
		func() {
			b.downloadCoreMu.Lock()
			defer b.downloadCoreMu.Unlock()
			session.clearDownloadState()
			if mediaType == mediaTypeArtistSongs {
				if err := ripSong(session, trackID, b.appleToken, storefront, session.Config.MediaUserToken); err != nil {
					if ctxErr := session.downloadContext().Err(); ctxErr != nil {
						downloadFailed = true
						return
					}
					fmt.Printf("Failed to rip artist song %s: %v\n", trackID, err)
					session.Counter.Error++
				}
				return
			}
			ripTrack(session, track, b.appleToken, session.Config.MediaUserToken)
		}()
		if downloadFailed {
			return true, sentAny, session.downloadContext().Err()
		}

		downloadedPaths := append([]string{}, session.LastDownloadedPaths...)
		if len(downloadedPaths) == 0 {
			if status != nil && reportTrackProgress {
				status.Update(trackLabel+" (no output)", int64(idx+1), int64(totalTracks))
			}
			b.sendArtistSongsProgressUpdate(chatID, replyToID, mediaType, idx+1, totalTracks)
			continue
		}
		downloadedAny = true

		uploadPaths := downloadedPaths
		if mediaType == mediaTypeAlbum || mediaType == mediaTypeArtistSongs {
			uploadPaths = b.augmentDownloadedPaths(uploadPaths, settings)
		}

		filteredPaths := make([]string, 0, len(uploadPaths))
		primaryCount := len(downloadedPaths)
		filteredPrimaryCount := 0
		for pathIdx, path := range uploadPaths {
			trimmed := strings.TrimSpace(path)
			if trimmed == "" {
				continue
			}
			if pathIdx >= primaryCount {
				if _, exists := sentExtraPaths[trimmed]; exists {
					continue
				}
			} else {
				filteredPrimaryCount++
			}
			filteredPaths = append(filteredPaths, trimmed)
		}
		if len(filteredPaths) == 0 {
			if status != nil && reportTrackProgress {
				status.Update(trackLabel+" (no upload path)", int64(idx+1), int64(totalTracks))
			}
			b.sendArtistSongsProgressUpdate(chatID, replyToID, mediaType, idx+1, totalTracks)
			continue
		}
		if b.cleanupTracker != nil {
			b.cleanupTracker.RecordPaths(filteredPaths)
		}

		sentPrimary := false
		for pathIdx, path := range filteredPaths {
			if pathIdx >= filteredPrimaryCount && !sentPrimary {
				if status != nil {
					status.UpdateSync("Primary media upload failed. Skip extra files (lyrics/cover/animated).", 0, 0)
				}
				break
			}
			if err := b.sendDownloadedPathWithRetry(session, chatID, path, replyToID, status, settings); err != nil {
				sanitized := sanitizeTelegramError(err, b.token)
				fmt.Printf("send file error (%s): %s\n", path, sanitized)
				appendRuntimeErrorLogf("send file error (%s): %s", path, sanitized)
				if status != nil && reportTrackProgress {
					status.Update(fmt.Sprintf("Failed to send %s: %s", filepath.Base(path), b.sanitizeTelegramErr(err)), 0, 0)
				}
				continue
			}
			if pathIdx < filteredPrimaryCount {
				sentPrimary = true
				sentAny = true
			} else {
				sentExtraPaths[path] = struct{}{}
			}
		}
		if status != nil && reportTrackProgress {
			status.Update(trackLabel, int64(idx+1), int64(totalTracks))
		}
		b.sendArtistSongsProgressUpdate(chatID, replyToID, mediaType, idx+1, totalTracks)
	}

	if forceRefresh && downloadedAny && mediaType != "" && mediaID != "" {
		// Refresh should only drop stale Telegram file_id caches after fresh output exists.
		b.purgeTargetCaches(&AppleURLTarget{MediaType: mediaType, ID: mediaID})
	}
	if !sentAny && !downloadedAny {
		return true, false, errNoFilesDownloaded
	}
	return true, sentAny, nil
}

func (b *TelegramBot) runDownload(chatID int64, fn func(session *DownloadSession) error, single bool, forceRefresh bool, replyToID int, settings ChatDownloadSettings, transferMode string, mediaType string, mediaID string, storefront string) {
	b.runDownloadWithContext(b.operationContext(), chatID, fn, single, forceRefresh, replyToID, settings, transferMode, mediaType, mediaID, storefront)
}

func (b *TelegramBot) runDownloadWithContext(ctx context.Context, chatID int64, fn func(session *DownloadSession) error, single bool, forceRefresh bool, replyToID int, settings ChatDownloadSettings, transferMode string, mediaType string, mediaID string, storefront string) {
	_ = b.runDownloadWithContextResult(ctx, chatID, fn, single, forceRefresh, replyToID, settings, transferMode, mediaType, mediaID, storefront)
}

func (b *TelegramBot) runDownloadWithContextResult(ctx context.Context, chatID int64, fn func(session *DownloadSession) error, single bool, forceRefresh bool, replyToID int, settings ChatDownloadSettings, transferMode string, mediaType string, mediaID string, storefront string) bool {
	if ctx == nil {
		ctx = b.operationContext()
	}
	var status *DownloadStatus
	startedAt := time.Now()
	taskResult := "unknown"
	success := false
	fmt.Printf("telegram task start chat=%d media=%s:%s storefront=%s transfer=%s single=%t refresh=%t\n", chatID, mediaType, mediaID, storefront, transferMode, single, forceRefresh)
	defer func() {
		if rec := recover(); rec != nil {
			taskResult = "panic"
			scope := fmt.Sprintf("telegram runDownload chat=%d media=%s:%s", chatID, mediaType, mediaID)
			_ = logRecoveredPanic(scope, rec)
			if status != nil {
				status.UpdateSync("任务异常已记录，已自动跳过当前任务。", 0, 0)
				status.Stop()
			} else {
				_ = b.sendMessageWithReply(chatID, "任务异常已记录，已自动跳过当前任务。", nil, replyToID)
			}
		}
		elapsed := time.Since(startedAt)
		fmt.Printf("telegram task end chat=%d media=%s:%s result=%s elapsed=%s\n", chatID, mediaType, mediaID, taskResult, elapsed.Round(time.Millisecond))
		if b.history != nil {
			b.history.Record(chatID, downloadHistoryEntry{
				MediaType:  mediaType,
				MediaID:    mediaID,
				Result:     taskResult,
				Success:    success,
				DurationMS: elapsed.Milliseconds(),
				At:         time.Now(),
			})
		}
	}()

	settings = normalizeChatSettings(settings)
	transferMode = normalizeTransferModeForMedia(transferMode, mediaType, single)

	var err error
	status, err = newDownloadStatus(b, chatID, replyToID)
	if err != nil {
		taskResult = fmt.Sprintf("status-create-failed: %v", err)
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to create status message: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return false
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		status.finishFailure()
	}()

	progress := func(phase string, done, total int64) {
		status.Update(phase, done, total)
	}
	if mediaType == mediaTypeArtistLPs {
		result, lpErr := b.downloadArtistLPAlbumsCollectionWithContext(ctx, chatID, replyToID, forceRefresh, settings, mediaID, storefront, transferMode, status)
		if lpErr != nil {
			if isContextCancellationError(lpErr) {
				taskResult = "canceled(lp)"
				status.UpdateSync("Canceled.", 0, 0)
				return false
			}
			if errors.Is(lpErr, errNoFilesDownloaded) {
				taskResult = "no-files(lp)"
				status.UpdateSync("No files were downloaded.", 0, 0)
				return false
			}
			taskResult = fmt.Sprintf("failed(lp): %s", sanitizeTelegramError(lpErr, b.token))
			status.UpdateSync(fmt.Sprintf("Failed: %s", b.sanitizeTelegramErr(lpErr)), 0, 0)
			return false
		}
		if result.Failed > 0 {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("All LPs finished: success=%d/%d, failed=%d.", result.Succeeded, result.Total, result.Failed), nil, replyToID)
		}
		if result.SentAny {
			taskResult = "success(lp)"
			completed = true
			status.finishSuccess()
			success = true
		} else {
			taskResult = "no-upload-success(lp)"
			status.UpdateSync("No LP was uploaded successfully.", 0, 0)
		}
		return success
	}
	if mediaType == mediaTypeCuratorLPs {
		result, curatorErr := b.downloadCuratorAlbumsCollectionWithContext(ctx, chatID, replyToID, forceRefresh, settings, mediaID, storefront, transferMode, status)
		if curatorErr != nil {
			if isContextCancellationError(curatorErr) {
				taskResult = "canceled(curator)"
				status.UpdateSync("Canceled.", 0, 0)
				return false
			}
			if errors.Is(curatorErr, errNoFilesDownloaded) {
				taskResult = "no-files(curator)"
				status.UpdateSync("No files were downloaded.", 0, 0)
				return false
			}
			taskResult = fmt.Sprintf("failed(curator): %s", sanitizeTelegramError(curatorErr, b.token))
			status.UpdateSync(fmt.Sprintf("Failed: %s", b.sanitizeTelegramErr(curatorErr)), 0, 0)
			return false
		}
		if result.Failed > 0 {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Curator albums finished: success=%d/%d, failed=%d.", result.Succeeded, result.Total, result.Failed), nil, replyToID)
		}
		if result.SentAny {
			taskResult = "success(curator)"
			completed = true
			status.finishSuccess()
			success = true
		} else {
			taskResult = "no-upload-success(curator)"
			status.UpdateSync("No curator album was uploaded successfully.", 0, 0)
		}
		return success
	}
	if shouldUseTelegramCollectionSequentialOneByOne(single, transferMode, mediaType) {
		handled, sentAny, seqErr := b.runCollectionOneByOneSequentialWithContext(ctx, chatID, replyToID, forceRefresh, settings, mediaType, mediaID, storefront, status, "", progress)
		if handled {
			if seqErr != nil {
				if isContextCancellationError(seqErr) {
					taskResult = "canceled(seq)"
					status.UpdateSync("Canceled.", 0, 0)
					return false
				}
				if errors.Is(seqErr, errNoFilesDownloaded) {
					taskResult = "no-files(seq)"
					status.UpdateSync("No files were downloaded.", 0, 0)
					return false
				}
				taskResult = fmt.Sprintf("failed(seq): %s", sanitizeTelegramError(seqErr, b.token))
				status.UpdateSync(fmt.Sprintf("Failed: %s", b.sanitizeTelegramErr(seqErr)), 0, 0)
				return false
			}
			if sentAny {
				taskResult = "success(seq)"
				completed = true
				status.finishSuccess()
				success = true
			} else {
				taskResult = "no-upload-success(seq)"
			}
			return success
		}
	}

	paths := []string{}
	primaryCount := 0
	var session *DownloadSession
	coreErr := func() error {
		// Keep core serialized to avoid heavy external tool contention.
		b.downloadCoreMu.Lock()
		defer b.downloadCoreMu.Unlock()

		session, err = b.prepareTelegramDownloadSessionWithContext(ctx, single, forceRefresh, settings, mediaType, progress)
		if err != nil {
			return err
		}

		status.Update("Downloading", 0, 0)
		if err := fn(session); err != nil {
			return err
		}

		paths = append([]string{}, session.LastDownloadedPaths...)
		primaryCount = len(paths)
		if len(paths) == 0 {
			return errNoFilesDownloaded
		}
		if mediaType == mediaTypeSong || mediaType == mediaTypeAlbum || mediaType == mediaTypeArtistSongs {
			paths = b.augmentDownloadedPaths(paths, settings)
		}
		fmt.Printf("telegram task ready chat=%d media=%s:%s primary=%d total=%d transfer=%s\n", chatID, mediaType, mediaID, primaryCount, len(paths), transferMode)
		return nil
	}()
	if coreErr != nil {
		if isContextCancellationError(coreErr) {
			taskResult = "canceled"
			status.UpdateSync("Canceled.", 0, 0)
			return false
		}
		if errors.Is(coreErr, errNoFilesDownloaded) {
			taskResult = "no-files"
			status.UpdateSync("No files were downloaded.", 0, 0)
			return false
		}
		taskResult = fmt.Sprintf("failed: %s", sanitizeTelegramError(coreErr, b.token))
		status.UpdateSync(fmt.Sprintf("Failed: %s", b.sanitizeTelegramErr(coreErr)), 0, 0)
		return false
	}
	if forceRefresh && mediaType != "" && mediaID != "" {
		// Refresh should only drop stale Telegram file_id caches after fresh output exists.
		b.purgeTargetCaches(&AppleURLTarget{MediaType: mediaType, ID: mediaID})
	}
	if b.cleanupTracker != nil {
		b.cleanupTracker.RecordPaths(paths)
	}

	if transferMode == transferModeZip {
		if status != nil {
			status.Update("Zipping", 0, 0)
		}
		zipPath, displayName, err := createZipFromPaths(paths)
		if err != nil {
			taskResult = fmt.Sprintf("zip-create-failed: %v", err)
			status.UpdateSync(fmt.Sprintf("Failed to create ZIP: %v", err), 0, 0)
			return false
		}
		defer os.Remove(zipPath)
		cacheKey := ""
		if mediaID != "" && mediaType != "" {
			cacheKey = b.bundleZipCacheKey(mediaType, mediaID, settings)
		}
		if err := b.sendDocumentFile(chatID, zipPath, displayName, replyToID, status, cacheKey); err != nil {
			sanitized := sanitizeTelegramError(err, b.token)
			fmt.Println("send ZIP error:", sanitized)
			appendRuntimeErrorLogf("send ZIP error (%s:%s): %s", mediaType, mediaID, sanitized)
			if zipSendFailureNeedsFallback(err) {
				fmt.Printf("telegram zip fallback chat=%d media=%s:%s reason=%s\n", chatID, mediaType, mediaID, sanitized)
				status.UpdateSync("ZIP exceeds Telegram limit or upload stalled, fallback to one-by-one transfer.", 0, 0)
			} else {
				taskResult = fmt.Sprintf("zip-send-failed: %s", sanitized)
				status.UpdateSync(fmt.Sprintf("Failed to send ZIP: %s", b.sanitizeTelegramErr(err)), 0, 0)
				return false
			}
		} else {
			taskResult = "success(zip)"
			completed = true
			status.finishSuccess()
			return true
		}
	}
	sentAny := b.sendPreparedDownloadPaths(session, chatID, replyToID, status, settings, paths, primaryCount)
	if sentAny {
		taskResult = "success"
		completed = true
		status.finishSuccess()
		success = true
	} else if taskResult == "unknown" {
		taskResult = "no-upload-success"
	}
	return success
}
