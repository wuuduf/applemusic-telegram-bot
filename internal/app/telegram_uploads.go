package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func combineStreamingRequestError(reqErr error, writeErr error) error {
	if reqErr == nil {
		return writeErr
	}
	if writeErr == nil || isPipeClosedError(writeErr) {
		return reqErr
	}
	return fmt.Errorf("%v (body-writer error: %v)", reqErr, writeErr)
}

func closeHTTPIdleConnections(client *http.Client) {
	if client == nil {
		return
	}
	if tr, ok := client.Transport.(*http.Transport); ok && tr != nil {
		tr.CloseIdleConnections()
	}
}

func newUploadWatchdog(parent context.Context, timeout time.Duration) (context.Context, func(), func(), func() bool) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	var mu sync.Mutex
	lastProgress := time.Now()
	stalled := atomic.Bool{}
	doneCh := make(chan struct{})
	var doneOnce sync.Once

	touch := func() {
		mu.Lock()
		lastProgress = time.Now()
		mu.Unlock()
	}
	stop := func() {
		doneOnce.Do(func() {
			close(doneCh)
		})
	}

	go func() {
		runWithRecovery("telegram upload watchdog", nil, func() {
			ticker := time.NewTicker(uploadWatchdogInterval)
			defer ticker.Stop()
			for {
				select {
				case <-doneCh:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					mu.Lock()
					idle := time.Since(lastProgress)
					mu.Unlock()
					if idle > timeout {
						stalled.Store(true)
						cancel()
						return
					}
				}
			}
		})
	}()

	return ctx, touch, stop, stalled.Load
}

func copyWithUploadProgress(dst io.Writer, src io.Reader, total int64, status *DownloadStatus, phase string, onProgress func()) (int64, error) {
	buf := make([]byte, uploadProgressBufferSize)
	var written int64
	lastUpdate := time.Time{}
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
				if onProgress != nil {
					onProgress()
				}
				now := time.Now()
				if status != nil {
					if total > 0 {
						if written >= total || lastUpdate.IsZero() || now.Sub(lastUpdate) >= 800*time.Millisecond {
							status.Update(phase, written, total)
							lastUpdate = now
						}
					} else {
						if lastUpdate.IsZero() || now.Sub(lastUpdate) >= 800*time.Millisecond {
							status.Update(phase, written, 0)
							lastUpdate = now
						}
					}
				}
			}
			if ew != nil {
				return written, ew
			}
			if nw != nr {
				return written, io.ErrShortWrite
			}
		}
		if er == io.EOF {
			if status != nil {
				if total > 0 {
					status.Update(phase, total, total)
				} else {
					status.Update(phase, written, 0)
				}
			}
			return written, nil
		}
		if er != nil {
			return written, er
		}
	}
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (b *TelegramBot) sendWithRetry(ctx context.Context, status *DownloadStatus, label string, maxAttempts int, fn func() error) error {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if ctx == nil {
		ctx = b.operationContext()
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		retryAfter, hasRetryAfter := parseTelegramRetryAfter(lastErr)
		if hasRetryAfter {
			b.applyTelegramRetryAfter(retryAfter)
		}
		if attempt == maxAttempts || (!isRetryableUploadError(lastErr) && !hasRetryAfter) {
			b.noteTelegramRateLimit(lastErr)
			return lastErr
		}
		if status != nil {
			phase := fmt.Sprintf("Upload interrupted, retrying (%d/%d)", attempt+1, maxAttempts)
			if strings.TrimSpace(label) != "" {
				phase = fmt.Sprintf("%s interrupted, retrying (%d/%d)", label, attempt+1, maxAttempts)
			}
			if hasRetryAfter {
				phase = fmt.Sprintf("%s rate limited, retry after %ds (%d/%d)", strings.TrimSpace(label), int(retryAfter.Seconds()), attempt+1, maxAttempts)
			}
			status.Update(phase, 0, 0)
		}
		closeHTTPIdleConnections(b.client)
		closeHTTPIdleConnections(b.pollClient)
		if hasRetryAfter {
			if err := sleepWithContext(ctx, retryAfter); err != nil {
				return err
			}
		} else {
			if err := sleepWithContext(ctx, time.Duration(attempt)*time.Second); err != nil {
				return err
			}
		}
	}
	return lastErr
}

func (b *TelegramBot) sendDownloadedPathWithRetry(session *DownloadSession, chatID int64, filePath string, replyToID int, status *DownloadStatus, settings ChatDownloadSettings) error {
	var finalErr error
	uploadCtx := b.uploadContext(session)
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".m4a", ".flac", ".mp3", ".aac", ".wav", ".opus":
		audioErr := b.sendWithRetry(uploadCtx, status, "Audio upload", 2, func() error {
			return b.sendAudioFile(session, chatID, filePath, replyToID, status, settings.Format)
		})
		if audioErr == nil {
			finalErr = nil
			break
		}
		if status != nil {
			status.Update("Audio upload failed, trying document fallback", 0, 0)
		}
		docErr := b.sendWithRetry(uploadCtx, status, "Document upload", 1, func() error {
			return b.sendDocumentFileWithContext(uploadCtx, chatID, filePath, filepath.Base(filePath), replyToID, status, "")
		})
		if docErr == nil {
			finalErr = nil
			break
		}
		finalErr = fmt.Errorf("sendAudio failed: %v; sendDocument fallback failed: %v", audioErr, docErr)
	case ".mp4", ".m4v", ".mov":
		finalErr = b.sendWithRetry(uploadCtx, status, "Video upload", 2, func() error {
			return b.sendMusicVideoFile(session, chatID, filePath, replyToID, status, settings)
		})
	default:
		finalErr = b.sendWithRetry(uploadCtx, status, "Document upload", 2, func() error {
			return b.sendDocumentFileWithContext(uploadCtx, chatID, filePath, filepath.Base(filePath), replyToID, status, "")
		})
	}
	if finalErr != nil {
		appRuntimeMetrics.recordUploadFailure()
	} else {
		appRuntimeMetrics.recordUploadSuccess()
	}
	return finalErr
}

func formatMVCaption(meta AudioMeta, sizeBytes int64) string {
	sizeMB := float64(sizeBytes) / (1024.0 * 1024.0)
	title := strings.TrimSpace(meta.Title)
	performer := strings.TrimSpace(meta.Performer)
	if title == "" && performer == "" {
		return fmt.Sprintf("#AppleMusic #mv %.2fMB\nvia @musicdlam_bot", sizeMB)
	}
	if performer != "" && title != "" {
		return fmt.Sprintf("%s - %s\n#AppleMusic #mv %.2fMB\nvia @musicdlam_bot", performer, title, sizeMB)
	}
	if title != "" {
		return fmt.Sprintf("%s\n#AppleMusic #mv %.2fMB\nvia @musicdlam_bot", title, sizeMB)
	}
	return fmt.Sprintf("%s\n#AppleMusic #mv %.2fMB\nvia @musicdlam_bot", performer, sizeMB)
}

func (b *TelegramBot) sendMusicVideoFile(session *DownloadSession, chatID int64, filePath string, replyToID int, status *DownloadStatus, settings ChatDownloadSettings) error {
	if session == nil {
		session = b.newBotDownloadSession(Config)
	}
	uploadCtx := b.uploadContext(session)
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	if info.Size() > b.maxFileBytes {
		return fmt.Errorf("video exceeds Telegram limit (%dMB). Lower mv-max or use smaller source", b.maxFileBytes/1024/1024)
	}
	meta, _ := session.getDownloadedMeta(filePath)
	videoCacheKey := ""
	documentCacheKey := ""
	if meta.TrackID != "" {
		videoCacheKey = b.mvCacheKey(meta.TrackID, settings, "video")
		documentCacheKey = b.mvCacheKey(meta.TrackID, settings, "document")
	}
	if status != nil {
		status.Update("Uploading video", 0, 0)
	}
	caption := formatMVCaption(meta, info.Size())
	if err := b.sendVideoFileWithContext(uploadCtx, chatID, filePath, replyToID, caption, status, videoCacheKey); err == nil {
		return nil
	} else {
		if videoCacheKey != "" {
			b.deleteCachedVideo(videoCacheKey)
		}
		if status != nil {
			status.Update("Video upload failed, trying document fallback", 0, 0)
		}
		if docErr := b.sendDocumentFileWithContext(uploadCtx, chatID, filePath, filepath.Base(filePath), replyToID, status, documentCacheKey); docErr == nil {
			return nil
		} else {
			return fmt.Errorf("sendVideo failed: %v; sendDocument fallback failed: %v", err, docErr)
		}
	}
}

func (b *TelegramBot) sendAudioFile(session *DownloadSession, chatID int64, filePath string, replyToID int, status *DownloadStatus, format string) error {
	if session == nil {
		session = b.newBotDownloadSession(Config)
	}
	uploadCtx := b.uploadContext(session)
	if err := b.waitTelegramSend(uploadCtx, chatID); err != nil {
		return err
	}
	meta, hasMeta := session.getDownloadedMeta(filePath)
	format = normalizeTelegramFormat(format)
	if format == "" {
		format = defaultTelegramFormat
	}
	if hasMeta {
		if actual := normalizeTelegramFormat(meta.Format); actual != "" {
			format = actual
		}
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	switch format {
	case telegramFormatFlac:
		if ext != ".flac" {
			return fmt.Errorf("output is not FLAC: %s", filepath.Base(filePath))
		}
	case telegramFormatAlac, telegramFormatAac, telegramFormatAtmos:
		if ext != ".m4a" && ext != ".mp4" {
			return fmt.Errorf("output is not M4A/MP4: %s", filepath.Base(filePath))
		}
	}
	sendPath := filePath
	displayName := filepath.Base(filePath)
	thumbPath := ""
	compressedPath := ""
	compressed := false
	cleanup := func() {
		if thumbPath != "" {
			_ = os.Remove(thumbPath)
		}
		if compressedPath != "" {
			_ = os.Remove(compressedPath)
		}
	}
	defer cleanup()

	info, err := os.Stat(sendPath)
	if err != nil {
		return err
	}
	if info.Size() > b.maxFileBytes {
		if format != telegramFormatFlac {
			return fmt.Errorf("%s file exceeds Telegram limit (%dMB). Use /settings flac, lower quality, or raise telegram-max-file-mb.", strings.ToUpper(format), b.maxFileBytes/1024/1024)
		}
		if status != nil {
			status.Update("Compressing", 0, 0)
		}
		compressedPath, err = b.compressFlacToSize(uploadCtx, sendPath, b.maxFileBytes)
		if err != nil {
			return err
		}
		sendPath = compressedPath
		compressed = true
		info, err = os.Stat(sendPath)
		if err != nil {
			return err
		}
		if info.Size() > b.maxFileBytes {
			return fmt.Errorf("compressed file still too large: %s", filepath.Base(sendPath))
		}
	}
	file, err := os.Open(sendPath)
	if err != nil {
		return err
	}
	defer file.Close()

	sizeBytes := info.Size()
	durationMillis := int64(0)
	if hasMeta {
		durationMillis = meta.DurationMillis
	}
	bitrateKbps := calcBitrateKbps(sizeBytes, durationMillis)
	if bitrateKbps <= 0 {
		if seconds, err := getAudioDurationSeconds(uploadCtx, sendPath); err == nil && seconds > 0 {
			durationMillis = int64(seconds * 1000.0)
			bitrateKbps = calcBitrateKbps(sizeBytes, durationMillis)
		}
	}
	caption := formatTelegramCaption(sizeBytes, bitrateKbps, format)
	if status != nil {
		status.Update("Uploading audio", 0, sizeBytes)
	}
	coverPath := findCoverFile(filepath.Dir(filePath))
	if coverPath != "" {
		if path, err := makeTelegramThumb(uploadCtx, coverPath); err == nil {
			thumbPath = path
		}
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()
	writeErrCh := make(chan error, 1)
	ctx, touchProgress, stopWatchdog, watchdogStalled := newUploadWatchdog(uploadCtx, uploadNoProgressTimeout)
	defer stopWatchdog()

	req, err := http.NewRequestWithContext(ctx, "POST", b.apiURL("sendAudio"), pr)
	if err != nil {
		_ = pw.CloseWithError(err)
		return err
	}
	req.Header.Set("Content-Type", contentType)
	go func() {
		defer stopWatchdog()
		defer func() {
			if rec := recover(); rec != nil {
				panicErr := logRecoveredPanic("telegram sendAudio multipart writer", rec)
				_ = pw.CloseWithError(panicErr)
				writeErrCh <- panicErr
			}
		}()
		err := func() error {
			if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
				return err
			}
			if replyToID > 0 {
				if err := writer.WriteField("reply_to_message_id", strconv.Itoa(replyToID)); err != nil {
					return err
				}
			}
			if caption != "" {
				if err := writer.WriteField("caption", caption); err != nil {
					return err
				}
			}
			if hasMeta {
				if meta.Title != "" {
					if err := writer.WriteField("title", meta.Title); err != nil {
						return err
					}
				}
				if meta.Performer != "" {
					if err := writer.WriteField("performer", meta.Performer); err != nil {
						return err
					}
				}
			}
			part, err := writer.CreateFormFile("audio", displayName)
			if err != nil {
				return err
			}
			if _, err := copyWithUploadProgress(part, file, sizeBytes, status, "Uploading audio", touchProgress); err != nil {
				return err
			}
			if thumbPath != "" {
				thumbFile, err := os.Open(thumbPath)
				if err == nil {
					defer thumbFile.Close()
					thumbPart, err := writer.CreateFormFile("thumbnail", filepath.Base(thumbPath))
					if err == nil {
						if _, err := io.Copy(thumbPart, thumbFile); err != nil {
							return err
						}
					}
				}
			}
			return writer.Close()
		}()
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		writeErrCh <- err
	}()
	resp, err := b.client.Do(req)
	if err != nil {
		_ = pw.CloseWithError(err)
		writeErr := <-writeErrCh
		if watchdogStalled() {
			return fmt.Errorf("audio upload stalled: no progress for %s", uploadNoProgressTimeout)
		}
		return combineStreamingRequestError(err, writeErr)
	}
	defer resp.Body.Close()
	writeErr := <-writeErrCh
	if writeErr != nil {
		return writeErr
	}
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(responseBody))
		if msg == "" {
			msg = resp.Status
		}
		err = fmt.Errorf("telegram sendAudio failed: %s", msg)
		b.noteTelegramRateLimit(err)
		return err
	}
	apiResp := sendAudioResponse{}
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		err = fmt.Errorf("telegram sendAudio error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	shouldCacheAudio := true
	if session != nil {
		shouldCacheAudio = session.Config.EmbedLrc && session.Config.EmbedCover
	}
	if shouldCacheAudio && hasMeta && meta.TrackID != "" && apiResp.Result.Audio.FileID != "" {
		b.storeCachedAudio(meta.TrackID, CachedAudio{
			FileID:         apiResp.Result.Audio.FileID,
			FileSize:       apiResp.Result.Audio.FileSize,
			Compressed:     compressed,
			Format:         format,
			SizeBytes:      sizeBytes,
			BitrateKbps:    bitrateKbps,
			DurationMillis: durationMillis,
			Title:          meta.Title,
			Performer:      meta.Performer,
		})
	}
	return nil
}

func (b *TelegramBot) sendDocumentFile(chatID int64, filePath string, displayName string, replyToID int, status *DownloadStatus, cacheKey string) error {
	return b.sendDocumentFileWithContext(b.operationContext(), chatID, filePath, displayName, replyToID, status, cacheKey)
}

func (b *TelegramBot) sendDocumentFileWithContext(ctx context.Context, chatID int64, filePath string, displayName string, replyToID int, status *DownloadStatus, cacheKey string) error {
	if displayName == "" {
		displayName = filepath.Base(filePath)
	}
	if ctx == nil {
		ctx = b.operationContext()
	}
	if err := b.waitTelegramSend(ctx, chatID); err != nil {
		return err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	if info.Size() > b.maxFileBytes {
		if strings.HasSuffix(strings.ToLower(displayName), ".zip") {
			return fmt.Errorf("ZIP exceeds Telegram limit (%dMB)", b.maxFileBytes/1024/1024)
		}
		return fmt.Errorf("file exceeds Telegram limit (%dMB)", b.maxFileBytes/1024/1024)
	}
	uploadPhase := "Uploading document"
	if status != nil {
		if strings.HasSuffix(strings.ToLower(displayName), ".zip") {
			uploadPhase = "Uploading ZIP"
		}
		status.Update(uploadPhase, 0, info.Size())
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()
	writeErrCh := make(chan error, 1)
	reqCtx, touchProgress, stopWatchdog, watchdogStalled := newUploadWatchdog(ctx, uploadNoProgressTimeout)
	defer stopWatchdog()

	req, err := http.NewRequestWithContext(reqCtx, "POST", b.apiURL("sendDocument"), pr)
	if err != nil {
		_ = pw.CloseWithError(err)
		return err
	}
	req.Header.Set("Content-Type", contentType)
	go func() {
		defer stopWatchdog()
		defer func() {
			if rec := recover(); rec != nil {
				panicErr := logRecoveredPanic("telegram sendDocument multipart writer", rec)
				_ = pw.CloseWithError(panicErr)
				writeErrCh <- panicErr
			}
		}()
		err := func() error {
			if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
				return err
			}
			if replyToID > 0 {
				if err := writer.WriteField("reply_to_message_id", strconv.Itoa(replyToID)); err != nil {
					return err
				}
			}
			part, err := writer.CreateFormFile("document", displayName)
			if err != nil {
				return err
			}
			file, err := os.Open(filePath)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := copyWithUploadProgress(part, file, info.Size(), status, uploadPhase, touchProgress); err != nil {
				return err
			}
			return writer.Close()
		}()
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		writeErrCh <- err
	}()
	resp, err := b.client.Do(req)
	if err != nil {
		_ = pw.CloseWithError(err)
		writeErr := <-writeErrCh
		if watchdogStalled() {
			return fmt.Errorf("document upload stalled: no progress for %s", uploadNoProgressTimeout)
		}
		return combineStreamingRequestError(err, writeErr)
	}
	defer resp.Body.Close()
	writeErr := <-writeErrCh
	if writeErr != nil {
		return writeErr
	}
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		err = fmt.Errorf("telegram sendDocument failed: %s", strings.TrimSpace(string(responseBody)))
		b.noteTelegramRateLimit(err)
		return err
	}
	apiResp := sendDocumentResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		err = fmt.Errorf("telegram sendDocument error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	if cacheKey != "" && apiResp.Result.Document.FileID != "" {
		b.storeCachedDocument(cacheKey, CachedDocument{
			FileID:   apiResp.Result.Document.FileID,
			FileSize: apiResp.Result.Document.FileSize,
		})
	}
	return nil
}

func (b *TelegramBot) sendDocumentByFileID(chatID int64, entry CachedDocument, replyToID int) error {
	if entry.FileID == "" {
		return fmt.Errorf("document file_id is empty")
	}
	ctx := b.operationContext()
	if err := b.waitTelegramSend(ctx, chatID); err != nil {
		return err
	}
	payload := map[string]any{
		"chat_id":  chatID,
		"document": entry.FileID,
	}
	if replyToID > 0 {
		payload["reply_to_message_id"] = replyToID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.apiURL("sendDocument"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		err = fmt.Errorf("telegram sendDocument failed: %s", strings.TrimSpace(string(responseBody)))
		b.noteTelegramRateLimit(err)
		return err
	}
	apiResp := apiResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		err = fmt.Errorf("telegram sendDocument error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	return nil
}

func (b *TelegramBot) sendVideoFile(chatID int64, filePath string, replyToID int, caption string, status *DownloadStatus, cacheKey string) error {
	return b.sendVideoFileWithContext(b.operationContext(), chatID, filePath, replyToID, caption, status, cacheKey)
}

func (b *TelegramBot) sendVideoFileWithContext(ctx context.Context, chatID int64, filePath string, replyToID int, caption string, status *DownloadStatus, cacheKey string) error {
	if ctx == nil {
		ctx = b.operationContext()
	}
	if err := b.waitTelegramSend(ctx, chatID); err != nil {
		return err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	if info.Size() > b.maxFileBytes {
		return fmt.Errorf("video exceeds Telegram limit (%dMB)", b.maxFileBytes/1024/1024)
	}
	if status != nil {
		status.Update("Uploading video", 0, info.Size())
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	contentType := writer.FormDataContentType()
	writeErrCh := make(chan error, 1)
	reqCtx, touchProgress, stopWatchdog, watchdogStalled := newUploadWatchdog(ctx, uploadNoProgressTimeout)
	defer stopWatchdog()

	req, err := http.NewRequestWithContext(reqCtx, "POST", b.apiURL("sendVideo"), pr)
	if err != nil {
		_ = pw.CloseWithError(err)
		return err
	}
	req.Header.Set("Content-Type", contentType)
	go func() {
		defer stopWatchdog()
		defer func() {
			if rec := recover(); rec != nil {
				panicErr := logRecoveredPanic("telegram sendVideo multipart writer", rec)
				_ = pw.CloseWithError(panicErr)
				writeErrCh <- panicErr
			}
		}()
		err := func() error {
			if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
				return err
			}
			if replyToID > 0 {
				if err := writer.WriteField("reply_to_message_id", strconv.Itoa(replyToID)); err != nil {
					return err
				}
			}
			if caption != "" {
				if err := writer.WriteField("caption", caption); err != nil {
					return err
				}
			}
			if err := writer.WriteField("supports_streaming", "true"); err != nil {
				return err
			}
			part, err := writer.CreateFormFile("video", filepath.Base(filePath))
			if err != nil {
				return err
			}
			file, err := os.Open(filePath)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := copyWithUploadProgress(part, file, info.Size(), status, "Uploading video", touchProgress); err != nil {
				return err
			}
			return writer.Close()
		}()
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		writeErrCh <- err
	}()
	resp, err := b.client.Do(req)
	if err != nil {
		_ = pw.CloseWithError(err)
		writeErr := <-writeErrCh
		if watchdogStalled() {
			return fmt.Errorf("video upload stalled: no progress for %s", uploadNoProgressTimeout)
		}
		return combineStreamingRequestError(err, writeErr)
	}
	defer resp.Body.Close()
	writeErr := <-writeErrCh
	if writeErr != nil {
		return writeErr
	}
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("telegram sendVideo failed: %s", strings.TrimSpace(string(responseBody)))
		b.noteTelegramRateLimit(err)
		return err
	}
	apiResp := sendVideoResponse{}
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		err = fmt.Errorf("telegram sendVideo error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	if cacheKey != "" && apiResp.Result.Video.FileID != "" {
		b.storeCachedVideo(cacheKey, CachedVideo{
			FileID:   apiResp.Result.Video.FileID,
			FileSize: apiResp.Result.Video.FileSize,
		})
	}
	return nil
}

func (b *TelegramBot) sendVideoByFileID(chatID int64, entry CachedVideo, replyToID int) error {
	if entry.FileID == "" {
		return fmt.Errorf("video file_id is empty")
	}
	ctx := b.operationContext()
	if err := b.waitTelegramSend(ctx, chatID); err != nil {
		return err
	}
	payload := map[string]any{
		"chat_id": chatID,
		"video":   entry.FileID,
	}
	if replyToID > 0 {
		payload["reply_to_message_id"] = replyToID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.apiURL("sendVideo"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		err = fmt.Errorf("telegram sendVideo failed: %s", strings.TrimSpace(string(responseBody)))
		b.noteTelegramRateLimit(err)
		return err
	}
	apiResp := apiResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		err = fmt.Errorf("telegram sendVideo error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	return nil
}

type DownloadStatus struct {
	bot         *TelegramBot
	chatID      int64
	messageID   int
	lastPhase   string
	lastPercent int
	lastText    string
	lastUpdate  time.Time
	mu          sync.Mutex
	latestPhase string
	latestDone  int64
	latestTotal int64
	dirty       bool
	updateCh    chan struct{}
	stopCh      chan struct{}
	stopOnce    sync.Once
}

func newDownloadStatus(bot *TelegramBot, chatID int64, replyToID int) (*DownloadStatus, error) {
	messageID, err := bot.sendMessageWithReplyReturn(chatID, "Starting download...", nil, replyToID)
	if err != nil {
		return nil, err
	}
	// 进度消息在任务进行中不走自动删除计时。
	// 成功由调用方显式删除；失败再挂回 2 分钟自动删除。
	bot.clearAutoDeleteMessage(chatID, messageID)
	status := &DownloadStatus{
		bot:       bot,
		chatID:    chatID,
		messageID: messageID,
		updateCh:  make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
	}
	go func() {
		runWithRecovery("telegram download status loop", nil, func() {
			status.loop()
		})
	}()
	return status, nil
}

func (s *DownloadStatus) Stop() {
	if s == nil || s.bot == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

func (s *DownloadStatus) finishSuccess() {
	if s == nil || s.bot == nil {
		return
	}
	s.Stop()
	if err := s.bot.deleteMessage(s.chatID, s.messageID); err != nil {
		// 删除失败时兜底挂回自动删除，避免长期残留。
		s.bot.scheduleAutoDeleteMessage(s.chatID, s.messageID, false)
	}
}

func (s *DownloadStatus) finishFailure() {
	if s == nil || s.bot == nil {
		return
	}
	s.Stop()
	s.bot.scheduleAutoDeleteMessage(s.chatID, s.messageID, false)
}

func (s *DownloadStatus) Update(phase string, done, total int64) {
	if s == nil || s.bot == nil {
		return
	}
	s.mu.Lock()
	s.setLatestLocked(phase, done, total)
	s.mu.Unlock()
	select {
	case s.updateCh <- struct{}{}:
	default:
	}
}

func (s *DownloadStatus) UpdateSync(phase string, done, total int64) {
	if s == nil || s.bot == nil {
		return
	}
	s.mu.Lock()
	s.setLatestLocked(phase, done, total)
	s.mu.Unlock()
	s.flush(true)
}

func (s *DownloadStatus) setLatestLocked(phase string, done, total int64) {
	normalizedPhase := strings.TrimSpace(phase)
	if normalizedPhase == "" {
		normalizedPhase = "Working"
	}
	s.latestPhase = normalizedPhase
	s.latestDone = done
	s.latestTotal = total
	s.dirty = true
}

func (s *DownloadStatus) loop() {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.updateCh:
			s.flush(false)
		case <-ticker.C:
			s.flush(false)
		case <-s.stopCh:
			return
		}
	}
}

func (s *DownloadStatus) flush(force bool) {
	if s == nil || s.bot == nil {
		return
	}
	s.mu.Lock()
	if !s.dirty && !force {
		s.mu.Unlock()
		return
	}
	phase := s.latestPhase
	done := s.latestDone
	total := s.latestTotal
	s.dirty = false
	lastPhase := s.lastPhase
	lastPercent := s.lastPercent
	lastText := s.lastText
	lastUpdate := s.lastUpdate
	s.mu.Unlock()

	percent := -1
	if total > 0 {
		percent = int(float64(done) / float64(total) * 100)
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
	}

	text := formatProgressText(phase, done, total, percent)
	now := time.Now()
	phaseChanged := phase != lastPhase
	percentChanged := percent != lastPercent && percent >= 0
	if !force {
		if text == lastText {
			return
		}
		if !phaseChanged && !percentChanged && now.Sub(lastUpdate) < 2*time.Second {
			return
		}
	}

	if err := s.bot.editMessageText(s.chatID, s.messageID, text, nil); err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.lastPhase = phase
	s.lastPercent = percent
	s.lastText = text
	s.lastUpdate = now
	s.mu.Unlock()
}

func formatProgressText(phase string, done, total int64, percent int) string {
	if total > 0 {
		if percent < 0 {
			percent = 0
		}
		return fmt.Sprintf("%s: %s / %s (%d%%)", phase, formatBytes(done), formatBytes(total), percent)
	}
	if done > 0 {
		return fmt.Sprintf("%s: %s", phase, formatBytes(done))
	}
	return phase
}

func formatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	size := float64(value)
	unitIndex := 0
	for size >= 1024 && unitIndex < len(units)-1 {
		size /= 1024
		unitIndex++
	}
	precision := 1
	if unitIndex >= 2 {
		precision = 2
	}
	return fmt.Sprintf("%.*f%s", precision, size, units[unitIndex])
}

func calcBitrateKbps(sizeBytes int64, durationMillis int64) float64 {
	if sizeBytes <= 0 || durationMillis <= 0 {
		return 0
	}
	seconds := float64(durationMillis) / 1000.0
	if seconds <= 0 {
		return 0
	}
	return (float64(sizeBytes) * 8.0) / (seconds * 1000.0)
}

func formatTelegramCaption(sizeBytes int64, bitrateKbps float64, format string) string {
	sizeMB := float64(sizeBytes) / (1024.0 * 1024.0)
	if sizeMB < 0 {
		sizeMB = 0
	}
	if bitrateKbps < 0 {
		bitrateKbps = 0
	}
	tag := normalizeTelegramFormat(format)
	if tag == "" {
		tag = defaultTelegramFormat
	}
	return fmt.Sprintf("#AppleMusic #%s 文件大小%.2fMB %.2fkbps\nvia @musicdlam_bot", tag, sizeMB, bitrateKbps)
}

func extractInlineTrackID(query string) string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "/songid") {
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 {
			return strings.TrimSpace(fields[1])
		}
		return ""
	}
	if strings.HasPrefix(lower, "songid") {
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 {
			return strings.TrimSpace(fields[1])
		}
		return ""
	}
	if strings.HasPrefix(lower, "song:") {
		return strings.TrimSpace(trimmed[5:])
	}
	return strings.TrimSpace(trimmed)
}

func findCoverFile(dir string) string {
	candidates := []string{
		"cover.jpg",
		"cover.png",
		"folder.jpg",
		"folder.png",
	}
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func makeTelegramThumb(ctx context.Context, coverPath string) (string, error) {
	tmp, err := os.CreateTemp("", "amdl-thumb-*.jpg")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	args := []string{
		"-y", "-i", coverPath,
		"-vf", "scale=320:320:force_original_aspect_ratio=decrease",
		"-frames:v", "1",
		"-q:v", "5",
		tmpPath,
	}
	outputResult, err := runExternalCommand(ctx, Config.FFmpegPath, args...)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg thumb failed: %v: %s", err, strings.TrimSpace(outputResult.Combined))
	}
	if info, err := os.Stat(tmpPath); err == nil && info.Size() > 200*1024 {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("thumb too large")
	}
	return tmpPath, nil
}

func (b *TelegramBot) compressFlacToSize(ctx context.Context, srcPath string, maxBytes int64) (string, error) {
	outPath, err := makeTempFlacPath()
	if err != nil {
		return "", err
	}
	coverPath := findCoverFile(filepath.Dir(srcPath))
	if err := runFlacCompress(ctx, srcPath, outPath, 0, 0, false, coverPath); err != nil {
		_ = os.Remove(outPath)
		return "", err
	}
	info, err := os.Stat(outPath)
	if err != nil {
		_ = os.Remove(outPath)
		return "", err
	}
	if info.Size() <= maxBytes {
		return outPath, nil
	}

	duration, err := getAudioDurationSeconds(ctx, srcPath)
	if err != nil {
		_ = os.Remove(outPath)
		return "", err
	}
	if duration <= 0 {
		_ = os.Remove(outPath)
		return "", fmt.Errorf("invalid duration for %s", filepath.Base(srcPath))
	}

	targetBitsPerSec := (float64(maxBytes) * 8.0 / duration) * 0.95
	sampleRate, channels := chooseResamplePlan(targetBitsPerSec)
	if err := runFlacCompress(ctx, srcPath, outPath, sampleRate, channels, true, coverPath); err != nil {
		_ = os.Remove(outPath)
		return "", err
	}

	info, err = os.Stat(outPath)
	if err != nil {
		_ = os.Remove(outPath)
		return "", err
	}
	if info.Size() > maxBytes {
		return "", fmt.Errorf("cannot compress below %dMB", maxBytes/1024/1024)
	}
	return outPath, nil
}

func runFlacCompress(ctx context.Context, srcPath, outPath string, sampleRate int, channels int, force16 bool, coverPath string) error {
	args := []string{"-y", "-i", srcPath}
	if coverPath != "" {
		args = append(args, "-i", coverPath)
		args = append(args,
			"-map", "0:a",
			"-map", "1:v",
			"-c:v", "mjpeg",
			"-disposition:v", "attached_pic",
		)
	} else {
		args = append(args, "-map", "0:a", "-map", "0:v?")
	}
	args = append(args,
		"-c:a", "flac",
		"-compression_level", "12",
	)
	if force16 {
		args = append(args, "-sample_fmt", "s16")
	}
	if sampleRate > 0 {
		args = append(args, "-ar", strconv.Itoa(sampleRate))
	}
	if channels > 0 {
		args = append(args, "-ac", strconv.Itoa(channels))
	}
	args = append(args, outPath)
	outputResult, err := runExternalCommand(ctx, Config.FFmpegPath, args...)
	if err != nil {
		return fmt.Errorf("ffmpeg compress failed: %v: %s", err, strings.TrimSpace(outputResult.Combined))
	}
	return nil
}

func chooseResamplePlan(targetBitsPerSec float64) (int, int) {
	channels := 2
	targetRate := targetBitsPerSec / float64(16*channels)
	if targetRate < 12000 {
		channels = 1
		targetRate = targetBitsPerSec / float64(16*channels)
	}
	return pickSampleRate(targetRate), channels
}

func pickSampleRate(target float64) int {
	rates := []int{48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000}
	for _, rate := range rates {
		if float64(rate) <= target {
			return rate
		}
	}
	return rates[len(rates)-1]
}

func makeTempFlacPath() (string, error) {
	tmp, err := os.CreateTemp("", "amdl-*.flac")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func getAudioDurationSeconds(ctx context.Context, path string) (float64, error) {
	if _, err := exec.LookPath("ffprobe"); err == nil {
		result, err := runExternalCommand(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path)
		if err == nil {
			value := strings.TrimSpace(result.Stdout)
			if value != "" {
				if secs, err := strconv.ParseFloat(value, 64); err == nil && secs > 0 {
					return secs, nil
				}
			}
		}
	}

	outResult, _ := runExternalCommand(ctx, Config.FFmpegPath, "-i", path)
	re := regexp.MustCompile(`Duration:\s+(\d+):(\d+):(\d+(?:\.\d+)?)`)
	match := re.FindStringSubmatch(outResult.Combined)
	if len(match) != 4 {
		return 0, fmt.Errorf("failed to read duration from ffmpeg output")
	}
	hours, _ := strconv.ParseFloat(match[1], 64)
	minutes, _ := strconv.ParseFloat(match[2], 64)
	seconds, _ := strconv.ParseFloat(match[3], 64)
	return hours*3600 + minutes*60 + seconds, nil
}

func (b *TelegramBot) sendMessage(chatID int64, text string, markup any) error {
	return b.sendMessageWithReply(chatID, text, markup, 0)
}

func (b *TelegramBot) sendMessageWithReply(chatID int64, text string, markup any, replyToID int) error {
	_, err := b.sendMessageWithReplyReturn(chatID, text, markup, replyToID)
	return err
}

func (b *TelegramBot) sendMessageWithReplyReturn(chatID int64, text string, markup any, replyToID int) (int, error) {
	text = b.localizeOutgoingText(chatID, text)
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	if replyToID > 0 {
		payload["reply_to_message_id"] = replyToID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest("POST", b.apiURL("sendMessage"), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("telegram sendMessage failed: %s", resp.Status)
		b.noteTelegramRateLimit(err)
		return 0, err
	}
	apiResp := sendMessageResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return 0, err
	}
	if !apiResp.OK {
		err = fmt.Errorf("telegram sendMessage error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return 0, err
	}
	b.scheduleAutoDeleteMessage(chatID, apiResp.Result.MessageID, b.shouldMarkAutoDeleteSticky(markup))
	return apiResp.Result.MessageID, nil
}

func (b *TelegramBot) sendAudioByFileID(chatID int64, entry CachedAudio, replyToID int, trackID string) error {
	entry = b.enrichCachedAudio(trackID, entry)
	ctx := b.operationContext()
	if err := b.waitTelegramSend(ctx, chatID); err != nil {
		return err
	}
	sizeBytes := entry.SizeBytes
	if sizeBytes <= 0 {
		sizeBytes = entry.FileSize
	}
	bitrateKbps := entry.BitrateKbps
	format := normalizeTelegramFormat(entry.Format)
	if format == "" {
		format = defaultTelegramFormat
	}
	caption := formatTelegramCaption(sizeBytes, bitrateKbps, format)
	payload := map[string]any{
		"chat_id": chatID,
		"audio":   entry.FileID,
		"caption": caption,
	}
	if entry.Title != "" {
		payload["title"] = entry.Title
	}
	if entry.Performer != "" {
		payload["performer"] = entry.Performer
	}
	if replyToID > 0 {
		payload["reply_to_message_id"] = replyToID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.apiURL("sendAudio"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		err = fmt.Errorf("telegram sendAudio failed: %s", strings.TrimSpace(string(responseBody)))
		b.noteTelegramRateLimit(err)
		return err
	}
	apiResp := sendAudioResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		err = fmt.Errorf("telegram sendAudio error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	return nil
}
