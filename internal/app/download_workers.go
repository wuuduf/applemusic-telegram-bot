package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (b *TelegramBot) startDownloadWorker() {
	for i := 0; i < downloadWorkerPoolSize; i++ {
		b.workerWG.Add(1)
		go func() {
			defer b.workerWG.Done()
			for req := range b.downloadQueue {
				if req == nil {
					continue
				}
				func() {
					b.queueMu.Lock()
					for b.activeWorkers >= b.workerLimit && !b.isRequestCanceled(req.requestID) && !b.isShuttingDown() {
						b.queueCond.Wait()
					}
					if b.isShuttingDown() || b.consumeCanceledRequest(req.requestID) {
						b.queueMu.Unlock()
						if strings.TrimSpace(req.requestID) != "" {
							b.untrackRequest(req.requestID)
						}
						if strings.TrimSpace(req.inflightKey) != "" {
							b.releaseInflightDownload(req.inflightKey)
						}
						return
					}
					b.activeWorkers++
					b.queueMu.Unlock()

					taskType := ""
					if req != nil {
						taskType = normalizeTelegramTaskType(req.taskType)
					}
					panicked := false
					defer func() {
						if panicked {
							appRuntimeMetrics.recordTaskPanic(taskType)
							return
						}
						appRuntimeMetrics.recordTaskFinished(taskType)
					}()

					defer func() {
						if rec := recover(); rec != nil {
							panicked = true
							reqID := ""
							mediaType := ""
							mediaID := ""
							chatID := int64(0)
							replyToID := 0
							if req != nil {
								reqID = strings.TrimSpace(req.requestID)
								mediaType = req.mediaType
								mediaID = req.mediaID
								chatID = req.chatID
								replyToID = req.replyToID
							}
							scope := fmt.Sprintf("telegram download worker request=%s media=%s:%s", reqID, mediaType, mediaID)
							_ = logRecoveredPanic(scope, rec)
							if chatID != 0 {
								_ = b.sendMessageWithReply(chatID, "任务发生异常，已自动跳过并继续后续任务。", nil, replyToID)
							}
						}
					}()

					defer func() {
						if strings.TrimSpace(req.requestID) != "" {
							b.clearRunningRequestCancel(req.requestID)
						}
						if strings.TrimSpace(req.requestID) != "" {
							b.untrackRequest(req.requestID)
						}
						if strings.TrimSpace(req.inflightKey) != "" {
							b.releaseInflightDownload(req.inflightKey)
						}
						b.queueMu.Lock()
						if b.activeWorkers > 0 {
							b.activeWorkers--
						}
						b.queueCond.Broadcast()
						b.queueMu.Unlock()
					}()

					reqCtx := b.operationContext()
					var reqCancel context.CancelFunc
					if strings.TrimSpace(req.requestID) != "" {
						reqCtx, reqCancel = context.WithCancel(b.operationContext())
						b.registerRunningRequestCancel(req.requestID, reqCancel)
						b.markRequestRunning(req.requestID)
					}
					if reqCancel != nil {
						defer reqCancel()
					}
					b.runQueuedRequestWithContext(req, reqCtx)
				}()
			}
		}()
	}
}

func (b *TelegramBot) stopDownloadWorkers() {
	if b == nil {
		return
	}
	b.queueCloseOnce.Do(func() {
		b.queueMu.Lock()
		b.queueClosed = true
		if b.downloadQueue != nil {
			close(b.downloadQueue)
		}
		b.queueMu.Unlock()
		// Wake workers that are blocked on queueCond.Wait so they observe
		// the shutdown signal and exit instead of hanging until another
		// task happens to broadcast.
		b.queueMu.Lock()
		if b.queueCond != nil {
			b.queueCond.Broadcast()
		}
		b.queueMu.Unlock()
	})
	b.workerWG.Wait()
}

func (b *TelegramBot) ensureBackgroundTaskControl() {
	if b == nil {
		return
	}
	b.bgInitOnce.Do(func() {
		b.bgSem = make(chan struct{}, defaultBackgroundTaskLimit)
	})
}

func (b *TelegramBot) launchBackgroundTask(scope string, fn func(ctx context.Context)) bool {
	if b == nil || fn == nil {
		return false
	}
	b.ensureBackgroundTaskControl()
	ctx := b.shutdownContext()
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case b.bgSem <- struct{}{}:
	}
	b.bgWG.Add(1)
	go func() {
		defer b.bgWG.Done()
		defer func() {
			<-b.bgSem
		}()
		runWithRecovery(scope, nil, func() {
			fn(ctx)
		})
	}()
	return true
}

func (b *TelegramBot) waitBackgroundTasks(timeout time.Duration) bool {
	if b == nil {
		return true
	}
	if timeout <= 0 {
		timeout = defaultTelegramBackgroundTaskWait
	}
	done := make(chan struct{})
	go func() {
		b.bgWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func normalizeTaskWorkerLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > maxTaskWorkerLimit {
		return maxTaskWorkerLimit
	}
	return limit
}

func parseTaskWorkerLimit(raw string) (int, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return 0, false
	}
	for _, prefix := range []string{"workers", "worker", "threads", "thread"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimPrefix(value, prefix)
			break
		}
	}
	value = strings.TrimSpace(strings.Trim(value, "=:_-x"))
	if value == "" {
		return 0, false
	}
	num, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return normalizeTaskWorkerLimit(num), true
}

func (b *TelegramBot) getTaskWorkerLimit() int {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	return b.workerLimit
}

func (b *TelegramBot) setTaskWorkerLimit(limit int) int {
	normalized := normalizeTaskWorkerLimit(limit)
	b.queueMu.Lock()
	b.workerLimit = normalized
	if b.queueCond != nil {
		b.queueCond.Broadcast()
	}
	b.queueMu.Unlock()
	return normalized
}
