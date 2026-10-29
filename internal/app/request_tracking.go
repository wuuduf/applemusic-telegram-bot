package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (b *TelegramBot) queuedRequestCount() int {
	if b == nil {
		return 0
	}
	b.requestStateMu.Lock()
	defer b.requestStateMu.Unlock()
	if len(b.activeRequests) == 0 {
		return 0
	}
	count := 0
	for _, request := range b.activeRequests {
		if strings.TrimSpace(request.State) != "running" {
			count++
		}
	}
	return count
}

func (b *TelegramBot) registerRunningRequestCancel(requestID string, cancel context.CancelFunc) {
	if b == nil || strings.TrimSpace(requestID) == "" || cancel == nil {
		return
	}
	b.requestCancelMu.Lock()
	defer b.requestCancelMu.Unlock()
	if b.runningRequestCtx == nil {
		b.runningRequestCtx = make(map[string]context.CancelFunc)
	}
	b.runningRequestCtx[requestID] = cancel
}

func (b *TelegramBot) clearRunningRequestCancel(requestID string) {
	if b == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	b.requestCancelMu.Lock()
	if b.runningRequestCtx != nil {
		delete(b.runningRequestCtx, requestID)
	}
	b.requestCancelMu.Unlock()
}

func (b *TelegramBot) markRequestCanceled(requestID string) {
	if b == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	b.requestCancelMu.Lock()
	defer b.requestCancelMu.Unlock()
	if b.canceledRequestIDs == nil {
		b.canceledRequestIDs = make(map[string]struct{})
	}
	b.canceledRequestIDs[requestID] = struct{}{}
}

func (b *TelegramBot) isRequestCanceled(requestID string) bool {
	if b == nil || strings.TrimSpace(requestID) == "" {
		return false
	}
	b.requestCancelMu.Lock()
	defer b.requestCancelMu.Unlock()
	_, ok := b.canceledRequestIDs[requestID]
	return ok
}

func (b *TelegramBot) consumeCanceledRequest(requestID string) bool {
	if b == nil || strings.TrimSpace(requestID) == "" {
		return false
	}
	b.requestCancelMu.Lock()
	defer b.requestCancelMu.Unlock()
	if b.canceledRequestIDs == nil {
		return false
	}
	_, ok := b.canceledRequestIDs[requestID]
	if ok {
		delete(b.canceledRequestIDs, requestID)
	}
	return ok
}

func (b *TelegramBot) chatTrackedRequests(chatID int64) []telegramPersistedRequest {
	if b == nil || chatID == 0 {
		return nil
	}
	b.requestStateMu.Lock()
	defer b.requestStateMu.Unlock()
	requests := make([]telegramPersistedRequest, 0, len(b.activeRequests))
	for _, request := range b.activeRequests {
		if request.ChatID != chatID {
			continue
		}
		requests = append(requests, request)
	}
	sort.Slice(requests, func(i, j int) bool {
		leftRunning := strings.TrimSpace(requests[i].State) == "running"
		rightRunning := strings.TrimSpace(requests[j].State) == "running"
		if leftRunning != rightRunning {
			return leftRunning
		}
		if !requests[i].UpdatedAt.Equal(requests[j].UpdatedAt) {
			return requests[i].UpdatedAt.Before(requests[j].UpdatedAt)
		}
		return requests[i].RequestID < requests[j].RequestID
	})
	return requests
}

func telegramTaskLabel(taskType string) string {
	switch normalizeTelegramTaskType(taskType) {
	case telegramTaskDownload:
		return "download"
	case telegramTaskCover:
		return "cover"
	case telegramTaskAnimatedCover:
		return "animated-cover"
	case telegramTaskSongLyrics:
		return "song-lyrics"
	case telegramTaskAlbumLyrics:
		return "album-lyrics"
	case telegramTaskArtistAssets:
		return "artist-assets"
	default:
		taskType = strings.TrimSpace(taskType)
		if taskType == "" {
			return telegramTaskDownload
		}
		return taskType
	}
}

func telegramRequestTargetLabel(request telegramPersistedRequest) string {
	mediaType := strings.TrimSpace(request.MediaType)
	mediaID := strings.TrimSpace(request.MediaID)
	if mediaType == "" && mediaID == "" {
		return telegramTaskLabel(request.TaskType)
	}
	if mediaType == "" {
		return mediaID
	}
	if mediaID == "" {
		return mediaType
	}
	return mediaType + ":" + mediaID
}

func formatTelegramRequestAge(updatedAt time.Time) string {
	if updatedAt.IsZero() {
		return "unknown"
	}
	age := time.Since(updatedAt)
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	}
}

func (b *TelegramBot) formatQueueText(chatID int64) string {
	requests := b.chatTrackedRequests(chatID)
	if len(requests) == 0 {
		return "No queued or running tasks for this chat."
	}
	lines := []string{"Tasks for this chat:"}
	limit := len(requests)
	if limit > 20 {
		limit = 20
	}
	for i := 0; i < limit; i++ {
		request := requests[i]
		state := strings.TrimSpace(request.State)
		if state == "" {
			state = "queued"
		}
		lines = append(lines, fmt.Sprintf("%d. %s · %s · %s · id=%s · age=%s", i+1, state, telegramTaskLabel(request.TaskType), telegramRequestTargetLabel(request), request.RequestID, formatTelegramRequestAge(request.UpdatedAt)))
	}
	if len(requests) > limit {
		lines = append(lines, fmt.Sprintf("... and %d more", len(requests)-limit))
	}
	lines = append(lines, "Use /cancel <request_id> to stop one task.")
	return strings.Join(lines, "\n")
}

func (b *TelegramBot) formatStatusText(chatID int64) string {
	queueLen, activeWorkers, workerLimit := b.queueStats()
	trackedTotal := b.trackedRequestCount()
	inflight := b.inflightCount()
	requests := b.chatTrackedRequests(chatID)
	chatQueued := 0
	chatRunning := 0
	for _, request := range requests {
		if strings.TrimSpace(request.State) == "running" {
			chatRunning++
		} else {
			chatQueued++
		}
	}
	metrics := appRuntimeMetrics.snapshot()
	lines := []string{
		"Bot status:",
		fmt.Sprintf("- queue=%d running=%d/%d tracked=%d inflight=%d", queueLen, activeWorkers, workerLimit, trackedTotal, inflight),
		fmt.Sprintf("- this chat: queued=%d running=%d", chatQueued, chatRunning),
		fmt.Sprintf("- uploads: ok=%d fail=%d retry_after=%d", metrics.UploadSuccesses, metrics.UploadFailures, metrics.TelegramRetryAfter),
		fmt.Sprintf("- external_cmd_timeouts=%d cleanup_files=%d cleanup_bytes=%d", metrics.ExternalCmdTimeouts, metrics.CleanupDeletedFiles, metrics.CleanupDeletedBytes),
	}
	if len(requests) > 0 {
		request := requests[0]
		lines = append(lines, fmt.Sprintf("- next task: %s · %s · id=%s", strings.TrimSpace(request.State), telegramRequestTargetLabel(request), request.RequestID))
	}
	if b.history != nil && b.history.enabled {
		total, success, failed := b.history.Stats(chatID)
		if total > 0 {
			lines = append(lines, fmt.Sprintf("- history (this chat): total=%d success=%d failed=%d", total, success, failed))
		}
	}
	lines = append(lines, "Use /queue for detailed task list, /history for recent downloads.")
	return strings.Join(lines, "\n")
}

func historyResultLabel(entry downloadHistoryEntry) string {
	result := strings.TrimSpace(entry.Result)
	if result == "" {
		if entry.Success {
			return "success"
		}
		return "failed"
	}
	return result
}

func (b *TelegramBot) formatHistoryText(chatID int64, args []string) string {
	if b == nil || b.history == nil || !b.history.enabled {
		return "Download history is disabled."
	}
	entries := b.history.Entries(chatID)
	if len(entries) == 0 {
		return "No download history for this chat yet."
	}
	limit := 10
	if len(args) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(args[0])); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > len(entries) {
		limit = len(entries)
	}
	total, success, failed := b.history.Stats(chatID)
	lines := []string{fmt.Sprintf("Recent downloads (total=%d success=%d failed=%d):", total, success, failed)}
	for i := 0; i < limit; i++ {
		e := entries[i]
		mark := "✓"
		if !e.Success {
			mark = "✗"
		}
		target := strings.TrimSpace(e.MediaType)
		if id := strings.TrimSpace(e.MediaID); id != "" {
			if target != "" {
				target += ":" + id
			} else {
				target = id
			}
		}
		if target == "" {
			target = "(task)"
		}
		age := formatTelegramRequestAge(e.At)
		if e.DurationMS > 0 {
			lines = append(lines, fmt.Sprintf("%d. %s %s · %s · %s ago · took %s", i+1, mark, target, historyResultLabel(e), age, (time.Duration(e.DurationMS)*time.Millisecond).Round(time.Millisecond)))
		} else {
			lines = append(lines, fmt.Sprintf("%d. %s %s · %s · %s ago", i+1, mark, target, historyResultLabel(e), age))
		}
	}
	if len(entries) > limit {
		lines = append(lines, fmt.Sprintf("... and %d more (use /history <n>)", len(entries)-limit))
	}
	return strings.Join(lines, "\n")
}

func (b *TelegramBot) cancelChatRequest(chatID int64, requestID string) (string, bool) {
	if b == nil {
		return "", false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "", false
	}
	b.requestStateMu.Lock()
	record, ok := b.activeRequests[requestID]
	if !ok || record.ChatID != chatID {
		b.requestStateMu.Unlock()
		return "", false
	}
	state := strings.TrimSpace(record.State)
	if state == "" {
		state = "queued"
	}
	if state == "running" {
		b.requestStateMu.Unlock()
		b.requestCancelMu.Lock()
		cancel := b.runningRequestCtx[requestID]
		b.requestCancelMu.Unlock()
		if cancel != nil {
			cancel()
		}
		return state, true
	}
	delete(b.activeRequests, requestID)
	b.requestStateMu.Unlock()
	b.markRequestCanceled(requestID)
	if b.queueCond != nil {
		b.queueMu.Lock()
		b.queueCond.Broadcast()
		b.queueMu.Unlock()
	}
	b.requestStateSave()
	return state, true
}

func (b *TelegramBot) enqueueDownload(chatID int64, replyToID int, single bool, forceRefresh bool, settings ChatDownloadSettings, transferMode string, mediaType string, mediaID string, storefront string, inflightKey string, fn func(session *DownloadSession) error) bool {
	req := &downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       single,
		forceRefresh: forceRefresh,
		taskType:     telegramTaskDownload,
		settings:     settings,
		transferMode: transferMode,
		mediaType:    mediaType,
		mediaID:      mediaID,
		storefront:   storefront,
		inflightKey:  inflightKey,
		requestID:    b.nextRequestID(),
		fn:           fn,
	}
	return b.enqueueTaskRequest(req, "Resource pressure detected. New download tasks are temporarily blocked.", "Download queue is full. Please try again later.")
}

func (b *TelegramBot) enqueueTelegramTask(req *downloadRequest) bool {
	return b.enqueueTaskRequest(req, "Resource pressure detected. New tasks are temporarily blocked.", "Task queue is full. Please try again later.")
}

func (b *TelegramBot) runQueuedRequest(req *downloadRequest) {
	b.runQueuedRequestWithContext(req, b.operationContext())
}

func (b *TelegramBot) runQueuedRequestWithContext(req *downloadRequest, ctx context.Context) {
	if req == nil {
		return
	}
	if err := b.buildQueuedRequestRunner(req); err != nil {
		if req.chatID != 0 {
			_ = b.sendMessageWithReply(req.chatID, fmt.Sprintf("Failed: %s", b.sanitizeTelegramErr(err)), nil, req.replyToID)
		}
		return
	}
	if req.run == nil {
		if req.chatID != 0 {
			_ = b.sendMessageWithReply(req.chatID, "Failed: task runner is unavailable", nil, req.replyToID)
		}
		return
	}
	if err := req.run(b, ctx); err != nil && req.chatID != 0 {
		_ = b.sendMessageWithReply(req.chatID, fmt.Sprintf("Failed: %s", b.sanitizeTelegramErr(err)), nil, req.replyToID)
	}
}
