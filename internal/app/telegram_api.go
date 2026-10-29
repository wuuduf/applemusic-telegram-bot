package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func isRetryableUploadError(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := parseTelegramRetryAfter(err); ok {
		return true
	}
	lower := strings.ToLower(err.Error())
	retryableHints := []string{
		"context deadline exceeded",
		"client.timeout exceeded",
		"tls handshake timeout",
		"connection reset by peer",
		"use of closed network connection",
		"broken pipe",
		"read/write on closed pipe",
		"i/o timeout",
		"eof",
		"unexpected eof",
		"bad gateway",
		"temporarily unavailable",
		"too many requests",
		"file_parts_invalid",
	}
	for _, hint := range retryableHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func parseTelegramRetryAfter(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	msg := err.Error()
	for _, re := range []*regexp.Regexp{retryAfterJSONRe, retryAfterTextRe} {
		matches := re.FindStringSubmatch(msg)
		if len(matches) < 2 {
			continue
		}
		sec, convErr := strconv.Atoi(matches[1])
		if convErr != nil || sec <= 0 {
			continue
		}
		return time.Duration(sec) * time.Second, true
	}
	return 0, false
}

func isTelegramRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errTelegramSendDeferred) {
		return true
	}
	if _, ok := parseTelegramRetryAfter(err); ok {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "429") && strings.Contains(lower, "too many requests")
}

func telegramAPIHTTPError(operation string, status string, responseBody []byte) error {
	bodyText := strings.TrimSpace(string(bytes.TrimSpace(responseBody)))
	if bodyText == "" {
		return fmt.Errorf("%s failed: %s", operation, status)
	}
	return fmt.Errorf("%s failed: %s %s", operation, status, bodyText)
}

func telegramAPIResponseError(operation string, description string, params apiResponseParameters) error {
	description = strings.TrimSpace(description)
	if params.RetryAfter > 0 {
		if description == "" {
			description = fmt.Sprintf(`{"parameters":{"retry_after":%d}}`, params.RetryAfter)
		} else {
			description = fmt.Sprintf(`%s {"parameters":{"retry_after":%d}}`, description, params.RetryAfter)
		}
	}
	if description == "" {
		return fmt.Errorf("%s error", operation)
	}
	return fmt.Errorf("%s error: %s", operation, description)
}

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

func (b *TelegramBot) answerCallbackQuery(callbackID string) error {
	if callbackID == "" {
		return nil
	}
	payload := map[string]any{
		"callback_query_id": callbackID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", b.apiURL("answerCallbackQuery"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
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
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", b.apiURL("answerInlineQuery"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (b *TelegramBot) editMessageText(chatID int64, messageID int, text string, markup any) error {
	return b.editMessageTextWithMode(chatID, messageID, text, markup, false)
}

func (b *TelegramBot) editMessageTextBestEffort(chatID int64, messageID int, text string, markup any) error {
	return b.editMessageTextWithMode(chatID, messageID, text, markup, true)
}

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
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.apiURL("editMessageText"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		apiResp := apiResponse{}
		if err := json.Unmarshal(responseBody, &apiResp); err == nil {
			if strings.Contains(apiResp.Description, "message is not modified") {
				return nil
			}
			if apiResp.Description != "" {
				err = fmt.Errorf("telegram editMessageText error: %s", apiResp.Description)
				b.noteTelegramRateLimit(err)
				return err
			}
		}
		err = fmt.Errorf("telegram editMessageText failed: %s", strings.TrimSpace(string(responseBody)))
		b.noteTelegramRateLimit(err)
		return err
	}
	apiResp := apiResponse{}
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		if strings.Contains(apiResp.Description, "message is not modified") {
			return nil
		}
		err = fmt.Errorf("telegram editMessageText error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	return nil
}

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
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.apiURL("deleteMessage"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		apiResp := apiResponse{}
		if err := json.Unmarshal(responseBody, &apiResp); err == nil && strings.TrimSpace(apiResp.Description) != "" {
			if isIgnorableDeleteMessageError(apiResp.Description) {
				return nil
			}
			err = fmt.Errorf("telegram deleteMessage failed: %s", strings.TrimSpace(apiResp.Description))
			b.noteTelegramRateLimit(err)
			return err
		}
		err = fmt.Errorf("telegram deleteMessage failed: %s", strings.TrimSpace(string(responseBody)))
		b.noteTelegramRateLimit(err)
		return err
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return nil
	}
	apiResp := apiResponse{}
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		if isIgnorableDeleteMessageError(apiResp.Description) {
			return nil
		}
		err = fmt.Errorf("telegram deleteMessage error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	return nil
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
			return nil, fmt.Errorf("getUpdates failed: %s (%s)", resp.Status, apiResp.Description)
		}
		return nil, fmt.Errorf("getUpdates failed: %s", resp.Status)
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
