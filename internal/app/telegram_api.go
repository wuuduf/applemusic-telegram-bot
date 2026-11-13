package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// isRetryableUploadError reports whether an upload is worth repeating. A
// Bot API failure is judged by its structured fields; anything else (network
// and pipe errors) by the well-known transient phrases in its text.
func isRetryableUploadError(err error) bool {
	if err == nil {
		return false
	}
	if apiErr, ok := asTelegramAPIError(err); ok {
		return apiErr.retryable()
	}
	if _, ok := retryAfterFromText(err.Error()); ok {
		return true
	}
	return containsRetryableHint(err.Error())
}

// parseTelegramRetryAfter returns the wait Telegram asked for. It reads the
// structured error when there is one and parses the text only for errors
// that carry no structure (e.g. ones rebuilt from sanitized strings).
func parseTelegramRetryAfter(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	if apiErr, ok := asTelegramAPIError(err); ok {
		return apiErr.retryAfter()
	}
	return retryAfterFromText(err.Error())
}

func isTelegramRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errTelegramSendDeferred) {
		return true
	}
	if apiErr, ok := asTelegramAPIError(err); ok {
		return apiErr.rateLimited()
	}
	if _, ok := retryAfterFromText(err.Error()); ok {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "429") && strings.Contains(lower, "too many requests")
}

// telegramAPIHTTPErrorText is "<operation> failed: <status> <body>".
func telegramAPIHTTPErrorText(operation string, status string, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Sprintf("%s failed: %s", operation, status)
	}
	return fmt.Sprintf("%s failed: %s %s", operation, status, body)
}

// telegramAPIResponseErrorText is "<operation> error: <description>", with
// parameters.retry_after spelled out after the description when present.
func telegramAPIResponseErrorText(operation string, description string, retryAfter int) string {
	description = strings.TrimSpace(description)
	if retryAfter > 0 {
		if description == "" {
			description = fmt.Sprintf(`{"parameters":{"retry_after":%d}}`, retryAfter)
		} else {
			description = fmt.Sprintf(`%s {"parameters":{"retry_after":%d}}`, description, retryAfter)
		}
	}
	if description == "" {
		return fmt.Sprintf("%s error", operation)
	}
	return fmt.Sprintf("%s error: %s", operation, description)
}

// telegramStatusError carries the HTTP status of a failed Bot API call so
// callers can branch on it instead of searching the message text.
type telegramStatusError struct {
	code int
	msg  string
}

func (e telegramStatusError) Error() string { return e.msg }

func isPipeClosedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "read/write on closed pipe")
}

// telegramAnswerErrorText words a failed answerCallbackQuery /
// answerInlineQuery. Callers treat these answers as best effort.
func telegramAnswerErrorText(operation string) telegramErrorFormat {
	return telegramErrorText(
		func(e *telegramAPIError) string {
			return telegramAPIHTTPErrorText(operation, e.Status, e.Body)
		},
		telegramErrorWithParameters(operation),
	)
}

func (b *TelegramBot) answerCallbackQuery(callbackID string) error {
	if callbackID == "" {
		return nil
	}
	payload := map[string]any{
		"callback_query_id": callbackID,
	}
	_, err := callTelegramJSON[telegramNoResult](b.operationContext(), b, "answerCallbackQuery", payload,
		telegramAnswerErrorText("telegram answerCallbackQuery"))
	return err
}

func (b *TelegramBot) answerInlineQuery(inlineQueryID string, results any, personal bool) error {
	if inlineQueryID == "" {
		return nil
	}
	payload := map[string]any{
		"inline_query_id": inlineQueryID,
		"results":         results,
		"is_personal":     personal,
		"cache_time":      0,
	}
	_, err := callTelegramJSON[telegramNoResult](b.operationContext(), b, "answerInlineQuery", payload,
		telegramAnswerErrorText("telegram answerInlineQuery"))
	return err
}

func (b *TelegramBot) editMessageText(chatID int64, messageID int, text string, markup any) error {
	return b.editMessageTextWithMode(chatID, messageID, text, markup, false)
}

func (b *TelegramBot) editMessageTextBestEffort(chatID int64, messageID int, text string, markup any) error {
	return b.editMessageTextWithMode(chatID, messageID, text, markup, true)
}

var editMessageTextErrorText = telegramErrorText(
	func(e *telegramAPIError) string {
		if e.Decoded && e.Description != "" {
			return "telegram editMessageText error: " + e.Description
		}
		return "telegram editMessageText failed: " + e.Body
	},
	telegramErrorDescription("telegram editMessageText"),
)

func (b *TelegramBot) editMessageTextWithMode(chatID int64, messageID int, text string, markup any, bestEffort bool) error {
	if messageID == 0 {
		return nil
	}
	ctx := b.operationContext()
	if bestEffort {
		if !b.tryTelegramSend(chatID) {
			return errTelegramSendDeferred
		}
	} else {
		if err := b.waitTelegramSend(ctx, chatID); err != nil {
			return err
		}
	}
	text = b.localizeOutgoingText(chatID, text)
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	_, err := callTelegramJSON[telegramNoResult](ctx, b, "editMessageText", payload, editMessageTextErrorText)
	if apiErr, ok := asTelegramAPIError(err); ok && apiErr.Decoded && strings.Contains(apiErr.Description, "message is not modified") {
		return nil
	}
	return err
}

var deleteMessageErrorText = telegramErrorText(
	func(e *telegramAPIError) string {
		if desc := strings.TrimSpace(e.Description); e.Decoded && desc != "" {
			return "telegram deleteMessage failed: " + desc
		}
		return "telegram deleteMessage failed: " + e.Body
	},
	telegramErrorDescription("telegram deleteMessage"),
)

func (b *TelegramBot) deleteMessage(chatID int64, messageID int) error {
	if messageID == 0 {
		return nil
	}
	b.clearAutoDeleteMessage(chatID, messageID)
	b.clearPanelStateByMessage(chatID, messageID)
	ctx := b.operationContext()
	if err := b.waitTelegramSend(ctx, chatID); err != nil {
		return err
	}
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	_, err := callTelegramJSON[telegramNoResult](ctx, b, "deleteMessage", payload, deleteMessageErrorText)
	if apiErr, ok := asTelegramAPIError(err); ok {
		if apiErr.Decoded && isIgnorableDeleteMessageError(apiErr.Description) {
			return nil
		}
		// An empty HTTP 200 body counts as deleted.
		if apiErr.StatusCode == http.StatusOK && apiErr.Body == "" {
			return nil
		}
	}
	return err
}

func isIgnorableDeleteMessageError(description string) bool {
	desc := strings.ToLower(strings.TrimSpace(description))
	return strings.Contains(desc, "message to delete not found")
}

func (b *TelegramBot) getUpdates(offset int) ([]Update, error) {
	return b.getUpdatesWithOptions(offset, 30, 0)
}

func (b *TelegramBot) getUpdatesWithOptions(offset int, timeoutSec int, limit int) ([]Update, error) {
	req, err := http.NewRequestWithContext(b.shutdownContext(), "GET", b.apiURL("getUpdates"), nil)
	if err != nil {
		return nil, err
	}
	query := req.URL.Query()
	if timeoutSec < 0 {
		timeoutSec = 0
	}
	query.Set("timeout", strconv.Itoa(timeoutSec))
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}
	req.URL.RawQuery = query.Encode()
	pollClient := b.pollClient
	if pollClient == nil {
		pollClient = b.client
	}
	resp, err := pollClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		apiResp := apiResponse{}
		if err := json.Unmarshal(responseBody, &apiResp); err == nil && strings.TrimSpace(apiResp.Description) != "" {
			return nil, telegramStatusError{code: resp.StatusCode, msg: fmt.Sprintf("getUpdates failed: %s (%s)", resp.Status, apiResp.Description)}
		}
		return nil, telegramStatusError{code: resp.StatusCode, msg: fmt.Sprintf("getUpdates failed: %s", resp.Status)}
	}
	var data getUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if !data.OK {
		return nil, fmt.Errorf("getUpdates error: %s", data.Description)
	}
	return data.Result, nil
}

func (b *TelegramBot) shutdownContext() context.Context {
	if b == nil || b.shutdownCtx == nil {
		return context.Background()
	}
	return b.shutdownCtx
}

func (b *TelegramBot) requestShutdown() {
	if b == nil || b.shutdownCancel == nil {
		return
	}
	b.shutdownCancel()
}

func (b *TelegramBot) isShuttingDown() bool {
	if b == nil || b.shutdownCtx == nil {
		return false
	}
	select {
	case <-b.shutdownCtx.Done():
		return true
	default:
		return false
	}
}

func (b *TelegramBot) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", b.apiBase, b.token, method)
}
