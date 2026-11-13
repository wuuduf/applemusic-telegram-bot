package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// captionSignatureDisabled is the telegram-caption-signature value that turns
// the signature line off.
const captionSignatureDisabled = "-"

// telegramCaptionSignature is the last line of media captions, e.g.
// "via @my_bot". It is empty until configured or resolved from getMe.
var telegramCaptionSignature atomic.Value

func captionSignature() string {
	value, _ := telegramCaptionSignature.Load().(string)
	return value
}

func setCaptionSignature(signature string) {
	telegramCaptionSignature.Store(strings.TrimSpace(signature))
}

func withCaptionSignature(caption string) string {
	signature := captionSignature()
	if signature == "" {
		return caption
	}
	if caption == "" {
		return signature
	}
	return caption + "\n" + signature
}

// configureCaptionSignature applies telegram-caption-signature: a fixed text,
// "-" for none, or empty to sign with the bot's own username from getMe.
func (b *TelegramBot) configureCaptionSignature() {
	configured := strings.TrimSpace(Config.TelegramCaptionSignature)
	switch {
	case configured == captionSignatureDisabled:
		setCaptionSignature("")
	case configured != "":
		setCaptionSignature(configured)
	default:
		b.launchBackgroundTask("telegram caption signature", func(ctx context.Context) {
			username, err := b.fetchBotUsername(ctx)
			if err != nil {
				msg := fmt.Sprintf("telegram getMe failed, captions stay unsigned: %s", b.sanitizeTelegramErr(err))
				fmt.Println(msg)
				appendRuntimeErrorLog(msg)
				return
			}
			setCaptionSignature("via @" + username)
		})
	}
}

func (b *TelegramBot) fetchBotUsername(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.apiURL("getMe"), nil)
	if err != nil {
		return "", err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var parsed struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("getMe: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !parsed.OK {
		return "", fmt.Errorf("getMe: %s", parsed.Description)
	}
	username := strings.TrimSpace(parsed.Result.Username)
	if username == "" {
		return "", fmt.Errorf("getMe: empty username")
	}
	return username, nil
}
