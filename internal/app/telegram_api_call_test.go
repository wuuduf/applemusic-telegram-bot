package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func newAPICallTestBot(t *testing.T, handler http.HandlerFunc) *TelegramBot {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &TelegramBot{token: "test-token", apiBase: server.URL, client: server.Client()}
}

func TestCallTelegramJSONDecodesTypedResult(t *testing.T) {
	b := newAPICallTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("unexpected content type %q", got)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
	})
	msg, err := callTelegramJSON[Message](context.Background(), b, "sendMessage", map[string]any{"chat_id": 1}, sendMessageErrorText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.MessageID != 77 {
		t.Fatalf("expected message id 77, got %d", msg.MessageID)
	}
	// Results that are not objects decode fine when the result is unused.
	b = newAPICallTestBot(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":true}`) })
	if _, err := callTelegramJSON[telegramNoResult](context.Background(), b, "deleteMessage", nil, deleteMessageErrorText); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCallTelegramJSONStructuredErrorKeepsLegacyText(t *testing.T) {
	const body = `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 4","parameters":{"retry_after":4}}`
	cases := []struct {
		name     string
		status   int
		format   telegramErrorFormat
		wantText string
	}{
		{"sendMessage http", http.StatusTooManyRequests, sendMessageErrorText, "telegram sendMessage failed: 429 Too Many Requests " + body},
		{"sendMessage ok=false", http.StatusOK, sendMessageErrorText, `telegram sendMessage error: Too Many Requests: retry after 4 {"parameters":{"retry_after":4}}`},
		{"editMessageText http", http.StatusTooManyRequests, editMessageTextErrorText, "telegram editMessageText error: Too Many Requests: retry after 4"},
		{"deleteMessage http", http.StatusTooManyRequests, deleteMessageErrorText, "telegram deleteMessage failed: Too Many Requests: retry after 4"},
		{"deleteMessage ok=false", http.StatusOK, deleteMessageErrorText, "telegram deleteMessage error: Too Many Requests: retry after 4"},
		{"sendDocument http", http.StatusTooManyRequests, sendDocumentErrorText, "telegram sendDocument failed: " + body},
		{"sendVideo ok=false", http.StatusOK, sendVideoErrorText, "telegram sendVideo error: Too Many Requests: retry after 4"},
		{"sendAudio http", http.StatusTooManyRequests, sendAudioErrorText, "telegram sendAudio failed: " + body},
		{"deleteWebhook http", http.StatusTooManyRequests, deleteWebhookErrorText, "deleteWebhook failed: " + body},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newAPICallTestBot(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, body)
			})
			b.sendLimiter = newTelegramSendLimiter(time.Millisecond, 0)
			_, err := callTelegramJSON[telegramNoResult](context.Background(), b, "anyMethod", nil, tc.format)
			if err == nil || err.Error() != tc.wantText {
				t.Fatalf("unexpected error text:\n got %v\nwant %s", err, tc.wantText)
			}
			apiErr, ok := asTelegramAPIError(err)
			if !ok {
				t.Fatalf("expected a structured error, got %T", err)
			}
			if apiErr.StatusCode != tc.status || apiErr.ErrorCode != 429 || apiErr.RetryAfter != 4 || apiErr.Description != "Too Many Requests: retry after 4" {
				t.Fatalf("unexpected structured fields: %+v", apiErr)
			}
			if wait, ok := parseTelegramRetryAfter(err); !ok || wait != 4*time.Second {
				t.Fatalf("expected retry after 4s, got %s ok=%t", wait, ok)
			}
			if !isTelegramRateLimitError(err) || !isRetryableUploadError(err) {
				t.Fatalf("expected a retryable rate-limit error")
			}
			// The helper applies retry_after to the send limiter.
			if wait := b.sendLimiter.nextWait(1); wait < 3*time.Second {
				t.Fatalf("expected the send limiter to be blocked, next wait %s", wait)
			}
		})
	}
}

func TestStructuredErrorWinsOverMessageText(t *testing.T) {
	// retry_after lives only in the structured field: the message of these
	// methods does not spell it out.
	b := newAPICallTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"flood","parameters":{"retry_after":9}}`)
	})
	_, err := callTelegramJSON[telegramNoResult](context.Background(), b, "sendDocument", nil, sendDocumentErrorText)
	if err == nil || err.Error() != "telegram sendDocument error: flood" {
		t.Fatalf("unexpected error: %v", err)
	}
	wrapped := fmt.Errorf("upload: %w", err)
	if wait, ok := parseTelegramRetryAfter(wrapped); !ok || wait != 9*time.Second {
		t.Fatalf("expected structured retry after 9s, got %s ok=%t", wait, ok)
	}
	if !isTelegramRateLimitError(wrapped) || !isRetryableUploadError(wrapped) {
		t.Fatalf("expected structured rate limit to be detected")
	}

	// A plain 400 is neither rate limited nor retryable.
	b = newAPICallTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	})
	_, err = callTelegramJSON[telegramNoResult](context.Background(), b, "sendDocument", nil, sendDocumentErrorText)
	if _, ok := parseTelegramRetryAfter(err); ok || isTelegramRateLimitError(err) || isRetryableUploadError(err) {
		t.Fatalf("400 must not be treated as rate limited or retryable: %v", err)
	}

	// Untyped errors still fall back to their text.
	text := errors.New(`sendAudio failed: telegram sendAudio failed: {"parameters":{"retry_after":7}}`)
	if wait, ok := parseTelegramRetryAfter(text); !ok || wait != 7*time.Second {
		t.Fatalf("expected text fallback 7s, got %s ok=%t", wait, ok)
	}
	if !isRetryableUploadError(errors.New("read tcp: connection reset by peer")) {
		t.Fatalf("expected network error text to stay retryable")
	}
	if !isTelegramRateLimitError(errors.New("HTTP 429 Too Many Requests")) {
		t.Fatalf("expected 429 text to stay a rate-limit error")
	}
}

func TestTelegramCallSpecialCases(t *testing.T) {
	respond := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		}
	}
	b := newAPICallTestBot(t, respond(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified: same content"}`))
	if err := b.editMessageText(1, 2, "text", nil); err != nil {
		t.Fatalf("expected 'message is not modified' to be ignored, got %v", err)
	}
	b = newAPICallTestBot(t, respond(http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: MESSAGE to delete not found"}`))
	if err := b.deleteMessage(1, 2); err != nil {
		t.Fatalf("expected 'message to delete not found' to be ignored, got %v", err)
	}
	b = newAPICallTestBot(t, respond(http.StatusOK, ""))
	if err := b.deleteMessage(1, 2); err != nil {
		t.Fatalf("expected an empty 200 body to count as deleted, got %v", err)
	}
	b = newAPICallTestBot(t, respond(http.StatusInternalServerError, "upstream down"))
	if err := b.deleteMessage(1, 2); err == nil || err.Error() != "telegram deleteMessage failed: upstream down" {
		t.Fatalf("unexpected deleteMessage error: %v", err)
	}
	if err := b.editMessageText(1, 2, "text", nil); err == nil || err.Error() != "telegram editMessageText failed: upstream down" {
		t.Fatalf("unexpected editMessageText error: %v", err)
	}
	if err := b.dropPendingUpdatesOnStart(); err == nil || err.Error() != "deleteWebhook failed: upstream down" {
		t.Fatalf("unexpected deleteWebhook error: %v", err)
	}
	if err := b.postTelegramJSON(context.Background(), "setMyCommands", nil); err == nil || err.Error() != "setMyCommands: HTTP 500: upstream down" {
		t.Fatalf("unexpected setMyCommands error: %v", err)
	}
	b = newAPICallTestBot(t, respond(http.StatusOK, "not json"))
	if err := b.dropPendingUpdatesOnStart(); err != nil {
		t.Fatalf("expected a non-JSON 200 from deleteWebhook to be accepted, got %v", err)
	}
	b = newAPICallTestBot(t, respond(http.StatusOK, `{"ok":false,"description":"nope"}`))
	if err := b.dropPendingUpdatesOnStart(); err == nil || err.Error() != "deleteWebhook error: nope" {
		t.Fatalf("unexpected deleteWebhook error: %v", err)
	}
	if err := b.postTelegramJSON(context.Background(), "setMyCommands", nil); err == nil || err.Error() != "setMyCommands: nope" {
		t.Fatalf("unexpected setMyCommands error: %v", err)
	}
}

func TestAnswerQueriesStopWithOperationContext(t *testing.T) {
	b := newAPICallTestBot(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":true}`) })
	ctx, cancel := context.WithCancel(context.Background())
	b.shutdownCtx = ctx
	if err := b.answerCallbackQuery("cb"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cancel()
	if err := b.answerCallbackQuery("cb"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected answerCallbackQuery to follow shutdown, got %v", err)
	}
	if err := b.answerInlineQuery("iq", []any{}, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected answerInlineQuery to follow shutdown, got %v", err)
	}
	if err := b.dropPendingUpdatesOnStart(); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected deleteWebhook to follow shutdown, got %v", err)
	}
}

func TestRuntimeLoggerRedactsAndWritesErrorsToFile(t *testing.T) {
	const botToken = "987654:AAE-central_redaction_token"
	const mediaToken = "media-user-token-value-0123456789"
	const lastFMKey = "lastfmkey0123456789abcdef"
	registerLogSecret(botToken)
	oldConfig := Config
	Config = structs.ConfigSet{MediaUserToken: mediaToken, LastFMAPIKey: lastFMKey}
	t.Cleanup(func() { Config = oldConfig })
	logPath := filepath.Join(t.TempDir(), "errors.log")
	oldPath := currentRuntimeErrorLogPath()
	setRuntimeErrorLogPath(logPath)
	t.Cleanup(func() { setRuntimeErrorLogPath(oldPath) })

	leak := fmt.Errorf(`Post "https://api.telegram.org/bot%s/sendMessage": Media-User-Token: %s; https://ws.audioscrobbler.com/2.0/?api_key=%s&x=1; raw=%s; other /bot123456:ABCdef_unknown-token/getMe api_key=unknownkey123`, botToken, mediaToken, lastFMKey, botToken)
	out := captureStdoutForTest(t, func() {
		logErrorf("request failed: %v\n", leak)
		logWarnf("warned: %v", leak)
		logInfof("plain info line")
		logDebugf("hidden debug line")
		runtimeLog.Error("structured", "err", leak, "attempt", 2)
	})
	fileBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read error log: %v", err)
	}
	fileText := string(fileBytes)
	for name, text := range map[string]string{"stdout": out, "file": fileText} {
		for _, secret := range []string{botToken, mediaToken, lastFMKey, "ABCdef_unknown-token", "unknownkey123"} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s leaked %q: %s", name, secret, text)
			}
		}
		if !strings.Contains(text, "<redacted-token>") || !strings.Contains(text, "<redacted-apple-token>") || !strings.Contains(text, "<redacted-api-key>") {
			t.Fatalf("%s is missing redaction markers: %s", name, text)
		}
	}
	for _, want := range []string{" ERROR request failed: ", " WARN warned: ", " INFO plain info line\n", " ERROR structured err=", " attempt=2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout is missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "hidden debug line") {
		t.Fatalf("debug line must be hidden at the default level: %s", out)
	}
	// Only Error records reach the runtime error log file.
	if !strings.Contains(fileText, " request failed: ") || !strings.Contains(fileText, " structured err=") {
		t.Fatalf("error log file is missing error records: %s", fileText)
	}
	if strings.Contains(fileText, "warned:") || strings.Contains(fileText, "plain info line") {
		t.Fatalf("error log file must only contain errors: %s", fileText)
	}
	if lines := strings.Count(fileText, "\n"); lines != 2 {
		t.Fatalf("expected 2 error log lines, got %d: %s", lines, fileText)
	}
}

func TestWorkspaceFolderHelpers(t *testing.T) {
	if got := cleanFolderNameStage(" A.B. "); got != "A.B." {
		t.Fatalf("a name not ending in a dot keeps its dots, got %q", got)
	}
	if got := cleanFolderNameStage("A.B. C."); got != "AB C" {
		t.Fatalf("a name ending in a dot loses all dots, got %q", got)
	}
	cfg := &structs.ConfigSet{AppleMasterChoice: "[M]", ExplicitChoice: "[E]", CleanChoice: "[C]"}
	if got := buildFolderTagStage(cfg, true, "explicit"); got != "[M] [E]" {
		t.Fatalf("unexpected tag %q", got)
	}
	if got := buildFolderTagStage(cfg, false, "clean"); got != "[C]" {
		t.Fatalf("unexpected tag %q", got)
	}
	if got := buildFolderTagStage(&structs.ConfigSet{}, true, "explicit"); got != "" {
		t.Fatalf("unexpected tag %q", got)
	}

	root := t.TempDir()
	cfg = &structs.ConfigSet{
		AlacSaveFolder:     filepath.Join(root, "alac"),
		AtmosSaveFolder:    filepath.Join(root, "atmos"),
		ArtistFolderFormat: "{ArtistName} ({ArtistId}).",
		AlbumFolderFormat:  "{ReleaseYear} {AlbumName}: {Tag} [{Codec}] ",
	}
	var artistFolder, name, path string
	captureStdoutForTest(t, func() {
		artistFolder = prepareArtistFolderStage(cfg, &DownloadSession{DlAtmos: true}, "A/B", "42")
		name, path = prepareAlbumFolderStage(cfg, artistFolder, albumFolderFields{ReleaseYear: "2020", AlbumName: "Hi?", Codec: "ATMOS", Tag: "[E]"})
	})
	if want := filepath.Join(root, "atmos", "A_B (42)"); artistFolder != want {
		t.Fatalf("artist folder: got %q want %q", artistFolder, want)
	}
	if name != "2020 Hi?: [E] [ATMOS]" {
		t.Fatalf("album folder name: got %q", name)
	}
	if want := filepath.Join(artistFolder, "2020 Hi__ [E] [ATMOS]"); path != want {
		t.Fatalf("album folder path: got %q want %q", path, want)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("expected the album folder to be created: %v", err)
	}
}
