package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func makeDownloadInflightKey(chatID int64, mediaType string, mediaID string, storefront string, transferMode string, settings ChatDownloadSettings) string {
	normalized := normalizeChatSettings(settings)
	normalizedTransfer := normalizeTransferModeForMedia(transferMode, mediaType, mediaType == mediaTypeSong || mediaType == mediaTypeMusicVideo)
	return strings.Join([]string{
		strconv.FormatInt(chatID, 10),
		strings.ToLower(strings.TrimSpace(mediaType)),
		strings.TrimSpace(mediaID),
		strings.TrimSpace(storefront),
		normalizedTransfer,
		normalized.Format,
		normalized.AACType,
		normalized.MVAudioType,
		strconv.FormatBool(normalized.SongZip),
		strconv.FormatBool(normalized.AutoLyrics),
		strconv.FormatBool(normalized.AutoCover),
		strconv.FormatBool(normalized.AutoAnimated),
		normalized.LyricsFormat,
	}, "|")
}

func (b *TelegramBot) acquireInflightDownload(key string) bool {
	if b == nil {
		return false
	}
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return true
	}
	b.inflightMu.Lock()
	defer b.inflightMu.Unlock()
	if _, exists := b.inflightDownloads[trimmed]; exists {
		return false
	}
	b.inflightDownloads[trimmed] = struct{}{}
	return true
}

func (b *TelegramBot) releaseInflightDownload(key string) {
	if b == nil {
		return
	}
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return
	}
	b.inflightMu.Lock()
	delete(b.inflightDownloads, trimmed)
	b.inflightMu.Unlock()
}

func (b *TelegramBot) waitTelegramSend(ctx context.Context, chatID int64) error {
	if b == nil || b.sendLimiter == nil {
		return nil
	}
	err := b.sendLimiter.wait(ctx, chatID)
	if err != nil {
		b.logTelegramPrintf("telegram send wait canceled chat=%d: %v\n", chatID, err)
	}
	return err
}

func (b *TelegramBot) tryTelegramSend(chatID int64) bool {
	if b == nil || b.sendLimiter == nil {
		return true
	}
	return b.sendLimiter.tryReserve(chatID)
}

func (b *TelegramBot) operationContext() context.Context {
	if b != nil && b.shutdownCtx != nil {
		return b.shutdownCtx
	}
	return context.Background()
}

func (b *TelegramBot) uploadContext(session *DownloadSession) context.Context {
	if session != nil && session.Context != nil {
		return session.downloadContext()
	}
	return b.operationContext()
}

func (b *TelegramBot) newBotDownloadSession(base structs.ConfigSet) *DownloadSession {
	session := newDownloadSession(base)
	session.Context = b.operationContext()
	return session
}

func (b *TelegramBot) applyTelegramRetryAfter(duration time.Duration) {
	if b == nil || duration <= 0 {
		return
	}
	if b.sendLimiter != nil {
		b.sendLimiter.blockFor(duration)
	}
	fmt.Printf("telegram retry_after applied: blocking sends for %s\n", duration.Round(time.Second))
	appRuntimeMetrics.recordTelegramRetryAfter()
}

func (b *TelegramBot) noteTelegramRateLimit(err error) {
	if err == nil {
		return
	}
	retryAfter, ok := parseTelegramRetryAfter(err)
	if !ok {
		return
	}
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	b.applyTelegramRetryAfter(retryAfter)
}

func (b *TelegramBot) getUpdatesBackoff(err error) (time.Duration, string) {
	if err == nil {
		return 0, ""
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "409") || strings.Contains(msg, "conflict") {
		return b.getUpdatesConflictSleep(), "conflict"
	}
	if retryAfter, ok := parseTelegramRetryAfter(err); ok {
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return retryAfter, "retry_after"
	}
	return b.getUpdatesErrorSleep(), "default"
}

func (b *TelegramBot) queueDownloadSong(chatID int64, songID string) {
	b.queueDownloadSongWithStorefrontOptions(chatID, songID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadSongWithReply(chatID int64, songID string, replyToID int) {
	b.queueDownloadSongWithStorefrontOptions(chatID, songID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadSongWithStorefront(chatID int64, songID string, storefront string, replyToID int) {
	b.queueDownloadSongWithStorefrontOptions(chatID, songID, storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadSongWithStorefrontOptions(chatID int64, songID string, storefront string, replyToID int, forceRefresh bool) {
	if songID == "" {
		_ = b.sendMessage(chatID, "Song ID is empty.", nil)
		return
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	settings := b.getChatSettings(chatID)
	mode := transferModeOneByOne
	if settings.SongZip {
		mode = transferModeZip
	}
	b.enqueueSongDownload(chatID, songID, storefront, replyToID, mode, forceRefresh)
}

func (b *TelegramBot) enqueueSongDownload(chatID int64, songID string, storefront string, replyToID int, transferMode string, forceRefresh bool) {
	if songID == "" {
		_ = b.sendMessage(chatID, "Song ID is empty.", nil)
		return
	}
	settings := b.getChatSettings(chatID)
	transferMode = normalizeTransferModeForMedia(transferMode, mediaTypeSong, true)
	if !forceRefresh && transferMode == transferModeZip && b.trySendCachedBundleZip(chatID, mediaTypeSong, songID, replyToID, settings) {
		return
	}
	if !forceRefresh && transferMode == transferModeOneByOne && b.trySendCachedTrack(chatID, replyToID, songID, settings) {
		b.sendCachedSongAutoExtras(chatID, replyToID, songID, storefront, settings)
		return
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	inflightKey := makeDownloadInflightKey(chatID, mediaTypeSong, songID, storefront, transferMode, settings)
	if !b.acquireInflightDownload(inflightKey) {
		_ = b.sendMessageWithReply(chatID, "Same song task is already running for this chat. Please wait.", nil, replyToID)
		return
	}
	if queued := b.enqueueDownload(chatID, replyToID, true, forceRefresh, settings, transferMode, mediaTypeSong, songID, storefront, inflightKey, func(session *DownloadSession) error {
		return ripSong(session, songID, b.appleToken, storefront, session.Config.MediaUserToken)
	}); !queued {
		b.releaseInflightDownload(inflightKey)
	}
}

func hasSongAutoExtras(settings ChatDownloadSettings) bool {
	normalized := normalizeChatSettings(settings)
	return normalized.AutoLyrics || normalized.AutoCover || normalized.AutoAnimated
}

func (b *TelegramBot) sendCachedSongAutoExtras(chatID int64, replyToID int, songID string, storefront string, settings ChatDownloadSettings) {
	normalized := normalizeChatSettings(settings)
	if !hasSongAutoExtras(normalized) {
		return
	}
	if storefront == "" {
		storefront = Config.Storefront
	}

	baseName := "song-" + songID
	if meta, err := b.fetchTrackMeta(songID); err == nil {
		if title := composeArtistTitle(meta.Performer, meta.Title); strings.TrimSpace(title) != "" {
			baseName = title
		}
	}

	if normalized.AutoLyrics {
		outputFormat := normalizeLyricsOutputFormat(normalized.LyricsFormat)
		if outputFormat == "" {
			outputFormat = defaultTelegramLyricsFormat
		}
		content, _, err := b.fetchLyricsOnly(songID, storefront, outputFormat)
		if err != nil {
			b.logTelegramPrintf("cached song extra lyrics skipped (%s): %v\n", songID, err)
		} else if strings.TrimSpace(content) != "" {
			displayName := sanitizeFileBaseName(baseName) + ".lyrics." + outputFormat
			if err := b.sendTextAsDocument(chatID, replyToID, displayName, outputFormat, content); err != nil {
				b.logTelegramPrintf("send cached lyrics error (%s): %v\n", songID, err)
			}
		}
	}

	needArtwork := normalized.AutoCover || normalized.AutoAnimated
	if !needArtwork {
		return
	}
	info, err := b.fetchArtwork(&AppleURLTarget{
		MediaType:  mediaTypeSong,
		ID:         songID,
		Storefront: storefront,
	})
	if err != nil {
		b.logTelegramPrintf("cached song extra artwork skipped (%s): %v\n", songID, err)
		return
	}
	displayBase := sanitizeFileBaseName(firstNonEmpty(info.DisplayName, baseName))

	if normalized.AutoCover && strings.TrimSpace(info.CoverURL) != "" {
		coverPath, tmpDir, err := renderCoverToTemp(info.CoverURL)
		if err != nil {
			b.logTelegramPrintf("cached song extra cover skipped (%s): %v\n", songID, err)
		} else {
			displayName := displayBase + "-cover" + strings.ToLower(filepath.Ext(coverPath))
			if err := b.sendDocumentFile(chatID, coverPath, displayName, replyToID, nil, ""); err != nil {
				b.logTelegramPrintf("send cached cover error (%s): %v\n", songID, err)
			}
			_ = os.RemoveAll(tmpDir)
		}
	}

	if normalized.AutoAnimated && strings.TrimSpace(info.MotionURL) != "" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			fmt.Printf("cached song extra animated skipped (%s): ffmpeg not found\n", songID)
			return
		}
		tmp, err := os.CreateTemp("", "amdl-cached-song-animated-*.mp4")
		if err != nil {
			b.logTelegramPrintf("cached song extra animated skipped (%s): %v\n", songID, err)
			return
		}
		tmpPath := tmp.Name()
		_ = tmp.Close()
		defer os.Remove(tmpPath)
		if err := b.saveAnimatedCover(info.MotionURL, tmpPath); err != nil {
			b.logTelegramPrintf("cached song extra animated skipped (%s): %v\n", songID, err)
			return
		}
		if err := b.sendVideoFile(chatID, tmpPath, replyToID, "", nil, ""); err != nil {
			displayName := displayBase + "-animated-cover.mp4"
			if docErr := b.sendDocumentFile(chatID, tmpPath, displayName, replyToID, nil, ""); docErr != nil {
				b.logTelegramPrintf("send cached animated cover error (%s): %v; fallback: %v\n", songID, err, docErr)
			}
		}
	}
}

func (b *TelegramBot) queueDownloadAlbum(chatID int64, albumID string) {
	b.queueDownloadAlbumWithStorefrontOptions(chatID, albumID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadAlbumWithReply(chatID int64, albumID string, replyToID int) {
	b.queueDownloadAlbumWithStorefrontOptions(chatID, albumID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadAlbumWithStorefront(chatID int64, albumID string, storefront string, replyToID int) {
	b.queueDownloadAlbumWithStorefrontOptions(chatID, albumID, storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadAlbumWithStorefrontOptions(chatID int64, albumID string, storefront string, replyToID int, forceRefresh bool) {
	if albumID == "" {
		_ = b.sendMessage(chatID, "Album ID is empty.", nil)
		return
	}
	b.promptMediaTransfer(chatID, mediaTypeAlbum, albumID, storefront, "", replyToID, forceRefresh)
}

func (b *TelegramBot) queueDownloadPlaylist(chatID int64, playlistID string) {
	b.queueDownloadPlaylistWithStorefrontOptions(chatID, playlistID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadPlaylistWithReply(chatID int64, playlistID string, replyToID int) {
	b.queueDownloadPlaylistWithStorefrontOptions(chatID, playlistID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadPlaylistWithStorefront(chatID int64, playlistID string, storefront string, replyToID int) {
	b.queueDownloadPlaylistWithStorefrontOptions(chatID, playlistID, storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadPlaylistWithStorefrontOptions(chatID int64, playlistID string, storefront string, replyToID int, forceRefresh bool) {
	if playlistID == "" {
		_ = b.sendMessage(chatID, "Playlist ID is empty.", nil)
		return
	}
	b.promptMediaTransfer(chatID, mediaTypePlaylist, playlistID, storefront, "", replyToID, forceRefresh)
}

func (b *TelegramBot) queueDownloadStation(chatID int64, stationID string) {
	b.queueDownloadStationWithStorefront(chatID, stationID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadStationWithReply(chatID int64, stationID string, replyToID int) {
	b.queueDownloadStationWithStorefront(chatID, stationID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadStationWithStorefront(chatID int64, stationID string, storefront string, replyToID int, forceRefresh bool) {
	if stationID == "" {
		_ = b.sendMessage(chatID, "Station ID is empty.", nil)
		return
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Station download requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	b.promptMediaTransfer(chatID, mediaTypeStation, stationID, storefront, "", replyToID, forceRefresh)
}

func (b *TelegramBot) queueDownloadMusicVideo(chatID int64, mvID string) {
	b.queueDownloadMusicVideoWithStorefront(chatID, mvID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadMusicVideoWithReply(chatID int64, mvID string, replyToID int) {
	b.queueDownloadMusicVideoWithStorefront(chatID, mvID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadMusicVideoWithStorefront(chatID int64, mvID string, storefront string, replyToID int, forceRefresh bool) {
	normalizedID, storefront, ok := b.validateMusicVideoRequest(chatID, mvID, storefront, replyToID)
	if !ok {
		return
	}
	b.promptMusicVideoStreamSelection(chatID, normalizedID, storefront, replyToID, forceRefresh)
}

func (b *TelegramBot) promptMediaTransfer(chatID int64, mediaType string, mediaID string, storefront string, mediaName string, replyToID int, forceRefresh bool) {
	if mediaID == "" {
		_ = b.sendMessage(chatID, "Media ID is empty.", nil)
		return
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	message := "Choose transfer method:"
	if mediaType == mediaTypeAlbumLyrics {
		message = "Choose lyrics export method:"
	} else if mediaType == mediaTypeArtistAsset {
		message = "Choose artist assets export method:"
	}
	messageID, err := b.sendMessageWithReplyReturn(chatID, message, buildTransferKeyboard(b.getChatLanguage(chatID)), replyToID)
	if err != nil {
		return
	}
	b.setPendingTransfer(chatID, mediaType, mediaID, mediaName, storefront, replyToID, messageID, forceRefresh)
}

func (b *TelegramBot) enqueueCollectionDownload(chatID int64, mediaType string, mediaID string, storefront string, replyToID int, transferMode string, forceRefresh bool) {
	if mediaID == "" {
		_ = b.sendMessage(chatID, "Media ID is empty.", nil)
		return
	}
	settings := b.getChatSettings(chatID)
	if storefront == "" {
		storefront = Config.Storefront
	}
	transferMode = normalizeTransferModeForMedia(transferMode, mediaType, false)
	inflightKey := makeDownloadInflightKey(chatID, mediaType, mediaID, storefront, transferMode, settings)
	if !b.acquireInflightDownload(inflightKey) {
		_ = b.sendMessageWithReply(chatID, "Same download task is already running for this chat. Please wait.", nil, replyToID)
		return
	}
	enqueueWithRollback := func(fn func(session *DownloadSession) error) {
		if queued := b.enqueueDownload(chatID, replyToID, false, forceRefresh, settings, transferMode, mediaType, mediaID, storefront, inflightKey, fn); !queued {
			b.releaseInflightDownload(inflightKey)
		}
	}
	switch mediaType {
	case mediaTypeAlbum:
		enqueueWithRollback(func(session *DownloadSession) error {
			return ripAlbum(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken, "")
		})
	case mediaTypePlaylist:
		enqueueWithRollback(func(session *DownloadSession) error {
			return ripPlaylist(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		})
	case mediaTypeStation:
		enqueueWithRollback(func(session *DownloadSession) error {
			return ripStation(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		})
	case mediaTypeArtistSongs:
		enqueueWithRollback(func(session *DownloadSession) error {
			return b.downloadArtistSongsCollection(session, mediaID, storefront)
		})
	case mediaTypeArtistLPs:
		enqueueWithRollback(func(session *DownloadSession) error {
			return b.stageArtistLPAlbumsCollection(session, mediaID, storefront)
		})
	case mediaTypeCuratorLPs:
		enqueueWithRollback(func(session *DownloadSession) error {
			return b.stageCuratorAlbumsCollection(session, mediaID, storefront)
		})
	default:
		b.releaseInflightDownload(inflightKey)
		_ = b.sendMessageWithReply(chatID, "Unsupported collection type for transfer.", nil, replyToID)
	}
}

func (b *TelegramBot) buildQueuedRequestRunner(req *downloadRequest) error {
	if b == nil || req == nil {
		return fmt.Errorf("invalid request")
	}
	req.taskType = normalizeTelegramTaskType(req.taskType)
	req.settings = normalizeChatSettings(req.settings)
	if strings.TrimSpace(req.storefront) == "" {
		req.storefront = Config.Storefront
	}
	if strings.TrimSpace(req.mediaType) == "" || strings.TrimSpace(req.mediaID) == "" {
		return fmt.Errorf("invalid request: missing media type/id")
	}
	if req.run != nil {
		return nil
	}
	switch req.taskType {
	case telegramTaskDownload:
		req.transferMode = normalizeTransferModeForMedia(req.transferMode, req.mediaType, req.single)
		if req.fn == nil {
			fn, err := b.buildDownloadWorkerFn(req.mediaType, req.mediaID, req.storefront, req.transferMode, req.mvVariantURL, req.mvVariantKey, req.mvVariantTag)
			if err != nil {
				return err
			}
			req.fn = fn
		}
		req.run = func(bot *TelegramBot, ctx context.Context) error {
			bot.runDownloadWithContext(ctx, req.chatID, req.fn, req.single, req.forceRefresh, req.replyToID, req.settings, req.transferMode, req.mediaType, req.mediaID, req.storefront)
			return nil
		}
	case telegramTaskCover:
		req.single = true
		req.transferMode = transferModeOneByOne
		target := &AppleURLTarget{MediaType: req.mediaType, ID: req.mediaID, Storefront: req.storefront}
		req.run = func(bot *TelegramBot, ctx context.Context) error {
			bot.handleCoverOnlyWithContext(ctx, req.chatID, req.replyToID, target, false)
			return nil
		}
	case telegramTaskAnimatedCover:
		req.single = true
		req.transferMode = transferModeOneByOne
		target := &AppleURLTarget{MediaType: req.mediaType, ID: req.mediaID, Storefront: req.storefront}
		req.run = func(bot *TelegramBot, ctx context.Context) error {
			bot.handleAnimatedCoverOnlyWithContext(ctx, req.chatID, req.replyToID, target)
			return nil
		}
	case telegramTaskSongLyrics:
		req.single = true
		req.transferMode = transferModeOneByOne
		target := &AppleURLTarget{MediaType: req.mediaType, ID: req.mediaID, Storefront: req.storefront}
		settings := req.settings
		req.run = func(bot *TelegramBot, ctx context.Context) error {
			bot.handleLyricsOnlyWithSettingsAndContext(ctx, req.chatID, req.replyToID, target, settings)
			return nil
		}
	case telegramTaskAlbumLyrics:
		req.single = false
		if req.transferMode != transferModeZip {
			req.transferMode = transferModeOneByOne
		}
		settings := req.settings
		req.run = func(bot *TelegramBot, ctx context.Context) error {
			bot.exportAlbumLyricsWithSettingsAndContext(ctx, req.chatID, req.replyToID, req.mediaID, req.storefront, req.transferMode, settings)
			return nil
		}
	case telegramTaskArtistAssets:
		req.single = false
		if req.transferMode != transferModeZip {
			req.transferMode = transferModeOneByOne
		}
		req.run = func(bot *TelegramBot, ctx context.Context) error {
			bot.exportArtistAssetsWithContext(ctx, req.chatID, req.replyToID, req.mediaID, req.storefront, req.transferMode)
			return nil
		}
	default:
		return fmt.Errorf("unsupported task type: %s", req.taskType)
	}
	return nil
}

func (b *TelegramBot) enqueueTaskRequest(req *downloadRequest, blockedMessage string, queueFullMessage string) bool {
	if req == nil {
		return false
	}
	if err := b.buildQueuedRequestRunner(req); err != nil {
		if req.chatID != 0 {
			_ = b.sendMessageWithReply(req.chatID, fmt.Sprintf("Failed: %s", b.sanitizeTelegramErr(err)), nil, req.replyToID)
		}
		return false
	}
	if b.resourceGuard != nil {
		if ok, reason := b.resourceGuard.allow(); !ok {
			msg := strings.TrimSpace(blockedMessage)
			if msg == "" {
				msg = "Resource pressure detected. New tasks are temporarily blocked."
			}
			if strings.TrimSpace(reason) != "" {
				msg = msg + " " + reason
			}
			b.noteActionQueued(req.chatID, false)
			_ = b.sendMessageWithReply(req.chatID, msg, nil, req.replyToID)
			return false
		}
	}
	b.queueMu.Lock()
	queueLen := len(b.downloadQueue)
	queueCap := cap(b.downloadQueue)
	activeWorkers := b.activeWorkers
	workerLimit := b.workerLimit
	queueFull := queueLen >= queueCap
	b.queueMu.Unlock()

	if queueFull {
		appRuntimeMetrics.recordQueueFullDrop()
		b.noteActionQueued(req.chatID, false)
		msg := strings.TrimSpace(queueFullMessage)
		if msg == "" {
			msg = "Task queue is full. Please try again later."
		}
		_ = b.sendMessageWithReply(req.chatID, msg, nil, req.replyToID)
		return false
	}
	b.trackQueuedRequest(req)
	if !b.trySendDownloadRequest(req) {
		appRuntimeMetrics.recordQueueFullDrop()
		b.untrackRequest(req.requestID)
		b.noteActionQueued(req.chatID, false)
		msg := strings.TrimSpace(queueFullMessage)
		if msg == "" {
			msg = "Task queue is full. Please try again later."
		}
		_ = b.sendMessageWithReply(req.chatID, msg, nil, req.replyToID)
		return false
	}
	b.noteActionQueued(req.chatID, true)
	waitingRequests := b.queuedRequestCount()
	if queueLen > 0 || activeWorkers >= workerLimit {
		if waitingRequests <= 0 {
			waitingRequests = 1
		}
		_ = b.sendMessageWithReply(req.chatID, fmt.Sprintf("Queued. Waiting: %d, running: %d/%d", waitingRequests, activeWorkers, workerLimit), nil, req.replyToID)
	}
	return true
}

// trySendDownloadRequest hands req to the workers without blocking. It
// returns false when the queue is full or already closed for shutdown; the
// close happens under queueMu, so this never sends on a closed channel.
func (b *TelegramBot) trySendDownloadRequest(req *downloadRequest) bool {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	if b.queueClosed || b.downloadQueue == nil {
		return false
	}
	select {
	case b.downloadQueue <- req:
		return true
	default:
		return false
	}
}
