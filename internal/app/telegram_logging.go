package app

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func normalizeTelegramAPIBase(raw string) string {
	base := strings.TrimSpace(raw)
	if base == "" {
		return "https://api.telegram.org"
	}
	return strings.TrimRight(base, "/")
}

func sanitizeTelegramError(err error, token string) string {
	if err == nil {
		return ""
	}
	return sanitizeTelegramText(err.Error(), token)
}

func sanitizeTelegramText(text string, token string) string {
	if text == "" {
		return text
	}
	if token != "" {
		text = strings.ReplaceAll(text, token, "<redacted-token>")
	}
	return redactAppleSecrets(text)
}

// redactAppleSecrets masks Apple Music credentials (media-user-token /
// authorization-token) if they ever appear in logged text, as defense in
// depth. These tokens normally travel only in request headers and are not
// logged, but this guarantees they never leak through an unexpected error path.
func redactAppleSecrets(text string) string {
	if text == "" {
		return text
	}
	for _, secret := range []string{
		strings.TrimSpace(Config.MediaUserToken),
		strings.TrimSpace(Config.AuthorizationToken),
	} {
		// Guard against short/empty values to avoid over-redacting.
		if len(secret) >= 8 {
			text = strings.ReplaceAll(text, secret, "<redacted-apple-token>")
		}
	}
	return text
}

func sanitizeTelegramLogValue(token string, value any) any {
	switch typed := value.(type) {
	case error:
		return sanitizeTelegramError(typed, token)
	case string:
		return sanitizeTelegramText(typed, token)
	default:
		return value
	}
}

func sanitizeTelegramLogArgs(token string, args ...any) []any {
	if len(args) == 0 {
		return nil
	}
	sanitized := make([]any, len(args))
	for i, arg := range args {
		sanitized[i] = sanitizeTelegramLogValue(token, arg)
	}
	return sanitized
}

func (b *TelegramBot) sanitizeTelegramErr(err error) string {
	if b == nil {
		return sanitizeTelegramError(err, "")
	}
	return sanitizeTelegramError(err, b.token)
}

func (b *TelegramBot) logTelegramPrintf(format string, args ...any) {
	fmt.Printf(format, sanitizeTelegramLogArgs(func() string {
		if b == nil {
			return ""
		}
		return b.token
	}(), args...)...)
}

func (b *TelegramBot) appendTelegramRuntimeErrorLogf(format string, args ...any) {
	appendRuntimeErrorLogf(format, sanitizeTelegramLogArgs(func() string {
		if b == nil {
			return ""
		}
		return b.token
	}(), args...)...)
}

func warnInsecureTelegramAPIBase(apiBase string) {
	parsed, err := url.Parse(apiBase)
	if err != nil {
		fmt.Printf("Warning: invalid telegram-api-url %q. Falling back to runtime errors if unreachable.\n", apiBase)
		return
	}
	if !strings.EqualFold(parsed.Scheme, "http") {
		return
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "localhost" {
		fmt.Printf("Warning: telegram-api-url uses plain HTTP (%s). Use only trusted local environments.\n", apiBase)
		return
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		fmt.Printf("Warning: telegram-api-url uses plain HTTP (%s). Use only trusted local environments.\n", apiBase)
		return
	}
	fmt.Printf("Warning: telegram-api-url uses plain HTTP (%s). Telegram bot token may be exposed in transit.\n", apiBase)
}
