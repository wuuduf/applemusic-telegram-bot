package app

import (
	"fmt"
	"net/http"
	"time"
)

func (b *TelegramBot) loop() {
	if err := b.dropPendingUpdatesOnStart(); err != nil {
		if b.isShuttingDown() {
			return
		}
		logErrorf("startup drop-pending-updates failed: %v", err)
	}
	offset := b.consumePendingUpdatesOnStart()
	lastConflictHint := time.Time{}
	consecutiveErrors := 0
	dispatcher := newUpdateDispatcher(b.shutdownContext(), telegramUpdateConcurrency(), b.dispatchUpdateSafely)
	defer func() {
		if !dispatcher.Close(defaultTelegramUpdateDrainWait) {
			logErrorf("telegram update handlers did not finish before shutdown deadline")
		}
	}()
	for {
		if b.isShuttingDown() {
			return
		}
		updates, err := b.getUpdates(offset)
		if err != nil {
			if b.isShuttingDown() {
				return
			}
			consecutiveErrors++
			logErrorf("getUpdates error (consecutive=%d): %v", consecutiveErrors, err)
			if threshold := b.getUpdatesRestartThreshold(); threshold > 0 && consecutiveErrors >= threshold {
				logErrorf("getUpdates hit %d consecutive errors; restarting bot runtime", consecutiveErrors)
				return
			}
			backoff, reason := b.getUpdatesBackoff(err)
			if reason == "conflict" {
				if lastConflictHint.IsZero() || time.Since(lastConflictHint) > 30*time.Second {
					logWarnf("Hint: 409 Conflict means another getUpdates consumer is active (another bot process) or webhook is set. Keep only one bot instance and clear webhook if needed.")
					lastConflictHint = time.Now()
				}
			} else if reason == "retry_after" {
				logErrorf("getUpdates rate limited; retry after %s", backoff.Round(time.Second))
			}
			if backoff <= 0 {
				backoff = b.getUpdatesErrorSleep()
			}
			if err := sleepWithContext(b.shutdownContext(), backoff); err != nil {
				return
			}
			continue
		}
		consecutiveErrors = 0
		for _, upd := range updates {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
			}
			dispatcher.Submit(upd)
		}
	}
}

func updateReplyContext(upd Update) (int64, int) {
	if upd.Message != nil {
		return upd.Message.Chat.ID, upd.Message.MessageID
	}
	if upd.CallbackQuery != nil && upd.CallbackQuery.Message != nil {
		return upd.CallbackQuery.Message.Chat.ID, upd.CallbackQuery.Message.MessageID
	}
	return 0, 0
}

func (b *TelegramBot) dispatchUpdate(upd Update) {
	if upd.Message != nil {
		b.handleMessage(upd.Message)
		return
	}
	if upd.CallbackQuery != nil {
		b.handleCallback(upd.CallbackQuery)
		return
	}
	if upd.InlineQuery != nil {
		b.handleInlineQuery(upd.InlineQuery)
	}
}

func (b *TelegramBot) dispatchUpdateSafely(upd Update) {
	scope := fmt.Sprintf("telegram update handler update_id=%d", upd.UpdateID)
	runWithRecovery(scope, func(_ error) {
		chatID, replyToID := updateReplyContext(upd)
		if chatID != 0 {
			_ = b.sendMessageWithReply(chatID, "任务异常已记录，已自动跳过并继续后续任务。", nil, replyToID)
		}
	}, func() {
		b.dispatchUpdate(upd)
	})
}

var deleteWebhookErrorText = telegramErrorText(
	telegramFailedBody("deleteWebhook"),
	telegramErrorDescription("deleteWebhook"),
)

func (b *TelegramBot) dropPendingUpdatesOnStart() error {
	payload := map[string]any{
		"drop_pending_updates": true,
	}
	_, err := callTelegramJSON[telegramNoResult](b.operationContext(), b, "deleteWebhook", payload, deleteWebhookErrorText)
	if apiErr, ok := asTelegramAPIError(err); ok && apiErr.StatusCode == http.StatusOK && !apiErr.Decoded {
		// An HTTP 200 without a Bot API envelope counts as success.
		return nil
	}
	return err
}

func (b *TelegramBot) consumePendingUpdatesOnStart() int {
	offset := 0
	skipped := 0
	for {
		updates, err := b.getUpdatesWithOptions(offset, 0, 100)
		if err != nil {
			logErrorf("startup pending-update check failed: %v", err)
			return offset
		}
		if len(updates) == 0 {
			break
		}
		for _, upd := range updates {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
				skipped++
			}
		}
		if len(updates) < 100 {
			break
		}
	}
	if skipped > 0 {
		logInfof("Skipped %d pending updates on startup.", skipped)
	}
	return offset
}
