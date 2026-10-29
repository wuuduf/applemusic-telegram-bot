package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	sharedcatalog "github.com/wuuduf/applemusic-telegram-bot/internal/catalog"
	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

type artworkFetchResult struct {
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

func (b *TelegramBot) fetchArtwork(target *AppleURLTarget) (artworkFetchResult, error) {
	if target == nil {
		return artworkFetchResult{}, fmt.Errorf("invalid target")
	}
	info, err := b.catalogService().FetchArtwork(sharedcatalog.ArtworkTarget{
		MediaType:  target.MediaType,
		ID:         target.ID,
		Storefront: resolveStorefront(target),
	})
	if err != nil {
		return artworkFetchResult{}, err
	}
	return artworkFetchResult{
		DisplayName: info.DisplayName,
		CoverURL:    info.CoverURL,
		MotionURL:   info.MotionURL,
	}, nil
}

func renderCoverToTemp(coverURL string) (string, string, error) {
	tmpDir, err := os.MkdirTemp("", "amdl-cover-*")
	if err != nil {
		return "", "", err
	}
	coverPath, err := writeCover(tmpDir, "cover", coverURL)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", err
	}
	return coverPath, tmpDir, nil
}

func copyTempFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func (b *TelegramBot) saveAnimatedCover(motionURL string, savePath string) error {
	return b.saveAnimatedCoverWithContext(b.operationContext(), motionURL, savePath)
}

func (b *TelegramBot) saveAnimatedCoverWithContext(ctx context.Context, motionURL string, savePath string) error {
	if ctx == nil {
		ctx = b.operationContext()
	}
	if strings.TrimSpace(motionURL) == "" {
		return fmt.Errorf("motion url is empty")
	}
	videoURL, err := extractVideo(motionURL)
	if err != nil {
		return err
	}
	result, err := runExternalCommand(ctx, "ffmpeg", "-loglevel", "error", "-y", "-i", videoURL, "-c", "copy", savePath)
	if err != nil {
		outText := strings.TrimSpace(result.Combined)
		if outText == "" {
			outText = err.Error()
		}
		return fmt.Errorf("%s", outText)
	}
	return nil
}

func (b *TelegramBot) exportArtistAssets(chatID int64, replyToID int, artistID string, storefront string, transferMode string) {
	b.exportArtistAssetsWithContext(b.operationContext(), chatID, replyToID, artistID, storefront, transferMode)
}

func (b *TelegramBot) exportArtistAssetsWithContext(ctx context.Context, chatID int64, replyToID int, artistID string, storefront string, transferMode string) {
	if ctx == nil {
		ctx = b.operationContext()
	}
	if strings.TrimSpace(artistID) == "" {
		_ = b.sendMessageWithReply(chatID, "Artist ID is empty.", nil, replyToID)
		return
	}
	storefront = resolveStorefront(&AppleURLTarget{Storefront: storefront})
	status, err := newDownloadStatus(b, chatID, replyToID)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to create status message: %v", err), nil, replyToID)
		return
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		status.finishFailure()
	}()

	tmpDir, err := os.MkdirTemp("", "amdl-artist-assets-*")
	if err != nil {
		status.UpdateSync(fmt.Sprintf("Failed to create temp directory: %v", err), 0, 0)
		return
	}
	defer os.RemoveAll(tmpDir)

	usedNames := make(map[string]struct{})
	assetPaths := []string{}
	coverCount := 0
	motionCount := 0
	failedAlbums := 0
	ffmpegOK := true
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		ffmpegOK = false
	}

	status.Update("Loading artist profile", 0, 0)
	artistInfo, artistErr := b.fetchArtwork(&AppleURLTarget{
		MediaType:  mediaTypeArtist,
		Storefront: storefront,
		ID:         artistID,
	})
	artistName := sanitizeFileBaseName(firstNonEmpty(artistInfo.DisplayName, "artist-"+artistID))
	if artistErr == nil && strings.TrimSpace(artistInfo.CoverURL) != "" {
		profilePath, profileTmp, err := renderCoverToTemp(artistInfo.CoverURL)
		if err == nil {
			profileName := uniqueName(usedNames, artistName+"-artist-photo"+strings.ToLower(filepath.Ext(profilePath)))
			profileDst := filepath.Join(tmpDir, profileName)
			if err := copyTempFile(profilePath, profileDst); err == nil {
				assetPaths = append(assetPaths, profileDst)
				coverCount++
			}
		}
		if strings.TrimSpace(profileTmp) != "" {
			_ = os.RemoveAll(profileTmp)
		}
	}

	status.Update("Loading artist albums", 0, 0)
	albums, _, err := apputils.FetchArtistAlbums(storefront, artistID, b.appleToken, 0, 0, b.searchLanguage())
	if err != nil {
		status.UpdateSync(fmt.Sprintf("Failed to load artist albums: %v", err), 0, 0)
		return
	}
	if strings.HasPrefix(artistName, "artist-") && len(albums) > 0 {
		if resolvedName := sanitizeFileBaseName(albums[0].Artist); resolvedName != "" {
			artistName = resolvedName
		}
	}
	totalAlbums := len(albums)
	for i, album := range albums {
		if err := ctx.Err(); err != nil {
			status.UpdateSync("Canceled.", 0, 0)
			return
		}
		status.Update("Collecting artist assets", int64(i), int64(totalAlbums))
		if strings.TrimSpace(album.ID) == "" {
			continue
		}
		info, err := b.fetchArtwork(&AppleURLTarget{
			MediaType:  mediaTypeAlbum,
			Storefront: storefront,
			ID:         album.ID,
		})
		if err != nil {
			failedAlbums++
			continue
		}
		albumBase := sanitizeFileBaseName(firstNonEmpty(info.DisplayName, album.Name, "album-"+album.ID))

		if strings.TrimSpace(info.CoverURL) != "" {
			coverPath, coverTmp, err := renderCoverToTemp(info.CoverURL)
			if err == nil {
				coverName := uniqueName(usedNames, fmt.Sprintf("%03d-%s-cover%s", i+1, albumBase, strings.ToLower(filepath.Ext(coverPath))))
				coverDst := filepath.Join(tmpDir, coverName)
				if err := copyTempFile(coverPath, coverDst); err == nil {
					assetPaths = append(assetPaths, coverDst)
					coverCount++
				}
			}
			if strings.TrimSpace(coverTmp) != "" {
				_ = os.RemoveAll(coverTmp)
			}
		}

		if ffmpegOK && strings.TrimSpace(info.MotionURL) != "" {
			motionName := uniqueName(usedNames, fmt.Sprintf("%03d-%s-animated-cover.mp4", i+1, albumBase))
			motionDst := filepath.Join(tmpDir, motionName)
			if err := b.saveAnimatedCoverWithContext(ctx, info.MotionURL, motionDst); err == nil {
				assetPaths = append(assetPaths, motionDst)
				motionCount++
			} else if isContextCancellationError(err) {
				status.UpdateSync("Canceled.", 0, 0)
				return
			}
		}
	}

	if !ffmpegOK {
		_ = b.sendMessageWithReply(chatID, "ffmpeg 不可用，已跳过动态封面导出。", nil, replyToID)
	}
	if len(assetPaths) == 0 {
		status.UpdateSync("No artist assets exported.", 0, 0)
		return
	}

	if transferMode == transferModeZip {
		status.Update("Building ZIP", int64(len(assetPaths)), int64(len(assetPaths)))
		zipPath, displayName, err := createZipFromPaths(assetPaths)
		if err != nil {
			status.UpdateSync(fmt.Sprintf("Failed to build ZIP: %v", err), 0, 0)
			return
		}
		defer os.Remove(zipPath)
		if artistName != "" {
			displayName = artistName + ".artist-assets.zip"
		}
		if err := b.sendDocumentFileWithContext(ctx, chatID, zipPath, displayName, replyToID, status, ""); err == nil {
			completed = true
			status.finishSuccess()
			return
		} else if isContextCancellationError(err) {
			status.UpdateSync("Canceled.", 0, 0)
			return
		} else if zipSendFailureNeedsFallback(err) {
			status.UpdateSync("ZIP exceeds Telegram size limit or upload stalled, fallback to one-by-one.", 0, 0)
			transferMode = transferModeOneByOne
		} else {
			status.UpdateSync(fmt.Sprintf("Failed to send ZIP: %s", b.sanitizeTelegramErr(err)), 0, 0)
			return
		}
	}

	if transferMode == transferModeOneByOne {
		sentCount := 0
		for idx, path := range assetPaths {
			if err := ctx.Err(); err != nil {
				status.UpdateSync("Canceled.", 0, 0)
				return
			}
			status.Update("Sending artist assets", int64(idx), int64(len(assetPaths)))
			ext := strings.ToLower(filepath.Ext(path))
			var err error
			if ext == ".mp4" {
				err = b.sendVideoFileWithContext(ctx, chatID, path, replyToID, "", status, "")
				if err != nil {
					if isContextCancellationError(err) {
						status.UpdateSync("Canceled.", 0, 0)
						return
					}
					err = b.sendDocumentFileWithContext(ctx, chatID, path, filepath.Base(path), replyToID, status, "")
				}
			} else {
				err = b.sendDocumentFileWithContext(ctx, chatID, path, filepath.Base(path), replyToID, status, "")
			}
			if isContextCancellationError(err) {
				status.UpdateSync("Canceled.", 0, 0)
				return
			}
			if err == nil {
				sentCount++
			}
		}
		completed = true
		status.finishSuccess()
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Artist assets done: sent=%d, covers=%d, animated=%d, failed_albums=%d.", sentCount, coverCount, motionCount, failedAlbums), nil, replyToID)
		return
	}
}

func (b *TelegramBot) handleCoverOnly(chatID int64, replyToID int, target *AppleURLTarget, artistOnly bool) {
	b.handleCoverOnlyWithContext(b.operationContext(), chatID, replyToID, target, artistOnly)
}

func (b *TelegramBot) handleCoverOnlyWithContext(ctx context.Context, chatID int64, replyToID int, target *AppleURLTarget, artistOnly bool) {
	if ctx == nil {
		ctx = b.operationContext()
	}
	info, err := b.fetchArtwork(target)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to fetch cover info: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	coverPath, tmpDir, err := renderCoverToTemp(info.CoverURL)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to download cover: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	defer os.RemoveAll(tmpDir)
	base := sanitizeFileBaseName(info.DisplayName)
	suffix := "-cover"
	if artistOnly {
		suffix = "-artist-photo"
	}
	displayName := base + suffix + strings.ToLower(filepath.Ext(coverPath))
	if err := b.sendDocumentFileWithContext(ctx, chatID, coverPath, displayName, replyToID, nil, ""); err != nil {
		if isContextCancellationError(err) {
			return
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to send cover: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
}

func (b *TelegramBot) enqueueCoverTask(chatID int64, replyToID int, target *AppleURLTarget) {
	if target == nil || strings.TrimSpace(target.MediaType) == "" || strings.TrimSpace(target.ID) == "" {
		_ = b.sendMessageWithReply(chatID, "Invalid Apple Music URL.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       true,
		taskType:     telegramTaskCover,
		settings:     settings,
		transferMode: transferModeOneByOne,
		mediaType:    target.MediaType,
		mediaID:      target.ID,
		storefront:   resolveStorefront(target),
		requestID:    b.nextRequestID(),
	})
}

func (b *TelegramBot) handleAnimatedCoverOnly(chatID int64, replyToID int, target *AppleURLTarget) {
	b.handleAnimatedCoverOnlyWithContext(b.operationContext(), chatID, replyToID, target)
}

func (b *TelegramBot) handleAnimatedCoverOnlyWithContext(ctx context.Context, chatID int64, replyToID int, target *AppleURLTarget) {
	if ctx == nil {
		ctx = b.operationContext()
	}
	if target == nil {
		_ = b.sendMessageWithReply(chatID, "Invalid target.", nil, replyToID)
		return
	}
	switch target.MediaType {
	case mediaTypeSong, mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation:
	default:
		_ = b.sendMessageWithReply(chatID, "animatedcover only supports song/album/playlist/station.", nil, replyToID)
		return
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		_ = b.sendMessageWithReply(chatID, "animatedcover requires ffmpeg in PATH.", nil, replyToID)
		return
	}
	info, err := b.fetchArtwork(target)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to fetch artwork info: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	if strings.TrimSpace(info.MotionURL) == "" {
		_ = b.sendMessageWithReply(chatID, "No animated cover found for this item.", nil, replyToID)
		return
	}
	videoURL, err := extractVideo(info.MotionURL)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Animated cover not available: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	tmp, err := os.CreateTemp("", "amdl-animated-cover-*.mp4")
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to create temp file: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)
	result, err := runExternalCommand(ctx, "ffmpeg", "-loglevel", "error", "-y", "-i", videoURL, "-c", "copy", tmpPath)
	if err != nil {
		if isContextCancellationError(err) {
			return
		}
		errText := strings.TrimSpace(result.Combined)
		if errText == "" {
			errText = err.Error()
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to download animated cover: %s", errText), nil, replyToID)
		return
	}
	displayName := sanitizeFileBaseName(info.DisplayName) + "-animated-cover.mp4"
	if err := b.sendVideoFileWithContext(ctx, chatID, tmpPath, replyToID, "", nil, ""); err == nil {
		return
	} else {
		if isContextCancellationError(err) {
			return
		}
		if docErr := b.sendDocumentFileWithContext(ctx, chatID, tmpPath, displayName, replyToID, nil, ""); docErr != nil {
			if isContextCancellationError(docErr) {
				return
			}
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to send animated cover: %s; fallback failed: %s", b.sanitizeTelegramErr(err), b.sanitizeTelegramErr(docErr)), nil, replyToID)
			return
		}
	}
}

func (b *TelegramBot) enqueueAnimatedCoverTask(chatID int64, replyToID int, target *AppleURLTarget) {
	if target == nil || strings.TrimSpace(target.MediaType) == "" || strings.TrimSpace(target.ID) == "" {
		_ = b.sendMessageWithReply(chatID, "Invalid target.", nil, replyToID)
		return
	}
	switch target.MediaType {
	case mediaTypeSong, mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation:
	default:
		_ = b.sendMessageWithReply(chatID, "animatedcover only supports song/album/playlist/station.", nil, replyToID)
		return
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		_ = b.sendMessageWithReply(chatID, "animatedcover requires ffmpeg in PATH.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       true,
		taskType:     telegramTaskAnimatedCover,
		settings:     settings,
		transferMode: transferModeOneByOne,
		mediaType:    target.MediaType,
		mediaID:      target.ID,
		storefront:   resolveStorefront(target),
		requestID:    b.nextRequestID(),
	})
}

func (b *TelegramBot) fetchLyricsOnly(songID string, storefront string, outputFormat string) (string, string, error) {
	return b.catalogService().FetchLyricsOnly(songID, storefront, outputFormat)
}

func (b *TelegramBot) sendTextAsDocument(chatID int64, replyToID int, displayName string, ext string, content string) error {
	return b.sendTextAsDocumentWithContext(b.operationContext(), chatID, replyToID, displayName, ext, content)
}

func (b *TelegramBot) sendTextAsDocumentWithContext(ctx context.Context, chatID int64, replyToID int, displayName string, ext string, content string) error {
	ext = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")
	if ext == "" {
		ext = "txt"
	}
	tmp, err := os.CreateTemp("", "amdl-doc-*."+ext)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)
	if displayName == "" {
		displayName = "apple-music." + ext
	}
	return b.sendDocumentFileWithContext(ctx, chatID, tmpPath, displayName, replyToID, nil, "")
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

func (b *TelegramBot) exportAlbumLyrics(chatID int64, replyToID int, albumID string, storefront string, transferMode string) {
	b.exportAlbumLyricsWithSettings(chatID, replyToID, albumID, storefront, transferMode, b.getChatSettings(chatID))
}

func (b *TelegramBot) exportAlbumLyricsWithSettings(chatID int64, replyToID int, albumID string, storefront string, transferMode string, settings ChatDownloadSettings) {
	b.exportAlbumLyricsWithSettingsAndContext(b.operationContext(), chatID, replyToID, albumID, storefront, transferMode, settings)
}

func (b *TelegramBot) exportAlbumLyricsWithSettingsAndContext(ctx context.Context, chatID int64, replyToID int, albumID string, storefront string, transferMode string, settings ChatDownloadSettings) {
	if ctx == nil {
		ctx = b.operationContext()
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Lyrics export requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	if strings.TrimSpace(albumID) == "" {
		_ = b.sendMessageWithReply(chatID, "Album ID is empty.", nil, replyToID)
		return
	}
	storefront = resolveStorefront(&AppleURLTarget{Storefront: storefront})
	settings = normalizeChatSettings(settings)
	lyricsFormat := normalizeLyricsOutputFormat(settings.LyricsFormat)
	if lyricsFormat == "" {
		lyricsFormat = defaultTelegramLyricsFormat
	}
	status, err := newDownloadStatus(b, chatID, replyToID)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to create status message: %v", err), nil, replyToID)
		return
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		status.finishFailure()
	}()
	status.Update("Loading album metadata", 0, 0)
	exported, err := b.catalogService().ExportAlbumLyrics("", albumID, storefront, lyricsFormat)
	if err != nil {
		switch {
		case errors.Is(err, sharedcatalog.ErrAlbumNotFound):
			status.UpdateSync("Album not found.", 0, 0)
		case errors.Is(err, sharedcatalog.ErrNoLyricsExported):
			status.UpdateSync("No lyrics files could be exported for this album.", 0, 0)
		default:
			var accessErr *safe.AccessError
			if errors.As(err, &accessErr) {
				status.UpdateSync(fmt.Sprintf("Album metadata is invalid: %v", err), 0, 0)
			} else {
				status.UpdateSync(fmt.Sprintf("Failed to load album: %v", err), 0, 0)
			}
		}
		return
	}
	lyricPaths := exported.Paths
	failedCount := exported.FailedCount
	if len(lyricPaths) == 0 {
		status.UpdateSync("No lyrics files could be exported for this album.", 0, 0)
		return
	}
	status.Update("Preparing output", int64(len(lyricPaths)), int64(len(lyricPaths)+failedCount))
	if transferMode == transferModeZip {
		zipPath, displayName, err := createZipFromPaths(lyricPaths)
		if err != nil {
			status.UpdateSync(fmt.Sprintf("Failed to build ZIP: %v", err), 0, 0)
			return
		}
		defer os.Remove(zipPath)
		safeAlbumName := sanitizeFileBaseName(exported.AlbumName)
		if safeAlbumName != "" {
			displayName = safeAlbumName + ".lyrics.zip"
		}
		if err := b.sendDocumentFileWithContext(ctx, chatID, zipPath, displayName, replyToID, status, ""); err == nil {
			completed = true
			status.finishSuccess()
		} else if isContextCancellationError(err) {
			status.UpdateSync("Canceled.", 0, 0)
			return
		} else if zipSendFailureNeedsFallback(err) {
			status.UpdateSync("ZIP exceeds Telegram size limit or upload stalled, fallback to one-by-one.", 0, 0)
			transferMode = transferModeOneByOne
		} else {
			status.UpdateSync(fmt.Sprintf("Failed to send ZIP: %s", b.sanitizeTelegramErr(err)), 0, 0)
			return
		}
	}
	if transferMode == transferModeOneByOne {
		for idx, lyricPath := range lyricPaths {
			if err := ctx.Err(); err != nil {
				status.UpdateSync("Canceled.", 0, 0)
				return
			}
			status.Update("Sending lyrics files", int64(idx), int64(len(lyricPaths)))
			if err := b.sendDocumentFileWithContext(ctx, chatID, lyricPath, filepath.Base(lyricPath), replyToID, status, ""); err != nil {
				if isContextCancellationError(err) {
					status.UpdateSync("Canceled.", 0, 0)
					return
				}
				failedCount++
			}
		}
		completed = true
		status.finishSuccess()
	}
	if failedCount > 0 {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Lyrics export completed with %d failed tracks.", failedCount), nil, replyToID)
	}
}

func (b *TelegramBot) handleLyricsOnly(chatID int64, replyToID int, target *AppleURLTarget) {
	b.handleLyricsOnlyWithSettings(chatID, replyToID, target, b.getChatSettings(chatID))
}

func (b *TelegramBot) handleLyricsOnlyWithSettings(chatID int64, replyToID int, target *AppleURLTarget, settings ChatDownloadSettings) {
	b.handleLyricsOnlyWithSettingsAndContext(b.operationContext(), chatID, replyToID, target, settings)
}

func (b *TelegramBot) handleLyricsOnlyWithSettingsAndContext(ctx context.Context, chatID int64, replyToID int, target *AppleURLTarget, settings ChatDownloadSettings) {
	if ctx == nil {
		ctx = b.operationContext()
	}
	if target == nil || strings.TrimSpace(target.ID) == "" {
		_ = b.sendMessageWithReply(chatID, "Invalid song target.", nil, replyToID)
		return
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Lyrics export requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	settings = normalizeChatSettings(settings)
	outputFormat := normalizeLyricsOutputFormat(settings.LyricsFormat)
	if outputFormat == "" {
		outputFormat = defaultTelegramLyricsFormat
	}
	storefront := resolveStorefront(target)
	songID := strings.TrimSpace(target.ID)
	content, lyricType, err := b.fetchLyricsOnly(songID, storefront, outputFormat)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to fetch lyrics: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	baseName := "song-" + songID
	if songResp, err := ampapi.GetSongRespWithContext(ctx, storefront, songID, Config.Language, b.appleToken); err == nil {
		item, dataErr := firstSongData("main.telegram.handleLyricsOnly", songResp)
		if dataErr == nil {
			if title := composeArtistTitle(item.Attributes.ArtistName, item.Attributes.Name); strings.TrimSpace(title) != "" {
				baseName = title
			}
		}
	}
	displayName := sanitizeFileBaseName(baseName) + ".lyrics." + outputFormat
	if err := b.sendTextAsDocumentWithContext(ctx, chatID, replyToID, displayName, outputFormat, content); err != nil {
		if isContextCancellationError(err) {
			return
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to send lyrics file: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	if outputFormat == "lrc" {
		if lyricType == "syllable-lyrics" {
			_ = b.sendMessageWithReply(chatID, "LRC exported. Translation lines are included when Apple provides them.", nil, replyToID)
		} else {
			_ = b.sendMessageWithReply(chatID, "LRC exported from fallback source (translation may be unavailable).", nil, replyToID)
		}
		return
	}
	if lyricType == "syllable-lyrics" {
		_ = b.sendMessageWithReply(chatID, "TTML exported. Translation and transliteration are included when Apple provides them.", nil, replyToID)
	} else {
		_ = b.sendMessageWithReply(chatID, "TTML exported from fallback lyrics source (translation/transliteration may be unavailable).", nil, replyToID)
	}
}

func (b *TelegramBot) enqueueSongLyricsTask(chatID int64, replyToID int, target *AppleURLTarget) {
	if target == nil || strings.TrimSpace(target.ID) == "" || target.MediaType != mediaTypeSong {
		_ = b.sendMessageWithReply(chatID, "Invalid song target.", nil, replyToID)
		return
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Lyrics export requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       true,
		taskType:     telegramTaskSongLyrics,
		settings:     settings,
		transferMode: transferModeOneByOne,
		mediaType:    target.MediaType,
		mediaID:      target.ID,
		storefront:   resolveStorefront(target),
		requestID:    b.nextRequestID(),
	})
}

func (b *TelegramBot) enqueueAlbumLyricsTask(chatID int64, replyToID int, albumID string, storefront string, transferMode string) {
	if strings.TrimSpace(albumID) == "" {
		_ = b.sendMessageWithReply(chatID, "Album ID is empty.", nil, replyToID)
		return
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Lyrics export requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       false,
		taskType:     telegramTaskAlbumLyrics,
		settings:     settings,
		transferMode: transferMode,
		mediaType:    mediaTypeAlbum,
		mediaID:      albumID,
		storefront:   resolveStorefront(&AppleURLTarget{Storefront: storefront}),
		requestID:    b.nextRequestID(),
	})
}

func (b *TelegramBot) enqueueArtistAssetsTask(chatID int64, replyToID int, artistID string, storefront string, transferMode string) {
	if strings.TrimSpace(artistID) == "" {
		_ = b.sendMessageWithReply(chatID, "Artist ID is empty.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       false,
		taskType:     telegramTaskArtistAssets,
		settings:     settings,
		transferMode: transferMode,
		mediaType:    mediaTypeArtist,
		mediaID:      artistID,
		storefront:   resolveStorefront(&AppleURLTarget{Storefront: storefront}),
		requestID:    b.nextRequestID(),
	})
}
