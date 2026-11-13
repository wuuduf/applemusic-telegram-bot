package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Bot-runtime logging.
//
// runtimeLog is a log/slog logger whose handler
//   - redacts secrets from every line (see redactLogSecrets), so call sites
//     log errors as they are instead of sanitizing by hand;
//   - writes "<time> <LEVEL> <message> [key=value ...]" to stdout;
//   - additionally appends records at Error level to the runtime error log
//     file (appendRuntimeErrorLog), which keeps its "<time> <message>" lines.
//
// The level defaults to Info and can be set with AMDL_LOG_LEVEL
// (debug, info, warn, error). The interactive CLI and the per-track progress
// output of the download pipeline print directly and do not go through it.
var (
	runtimeLogLevel = new(slog.LevelVar)
	runtimeLog      = slog.New(&runtimeLogHandler{level: runtimeLogLevel})
)

const runtimeLogLevelEnv = "AMDL_LOG_LEVEL"

func init() {
	if level, ok := parseRuntimeLogLevel(os.Getenv(runtimeLogLevelEnv)); ok {
		runtimeLogLevel.Set(level)
	}
}

func parseRuntimeLogLevel(raw string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return 0, false
}

func logDebugf(format string, args ...any) { logRuntimef(slog.LevelDebug, format, args...) }
func logInfof(format string, args ...any)  { logRuntimef(slog.LevelInfo, format, args...) }
func logWarnf(format string, args ...any)  { logRuntimef(slog.LevelWarn, format, args...) }

// logErrorf logs at Error level: stdout plus the runtime error log file.
func logErrorf(format string, args ...any) { logRuntimef(slog.LevelError, format, args...) }

func logRuntimef(level slog.Level, format string, args ...any) {
	ctx := context.Background()
	if !runtimeLog.Enabled(ctx, level) {
		return
	}
	runtimeLog.Log(ctx, level, strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
}

type runtimeLogHandler struct {
	level  slog.Leveler
	prefix string // group prefix for attribute keys
	attrs  string // preformatted " key=value" pairs from WithAttrs
}

func (h *runtimeLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *runtimeLogHandler) Handle(_ context.Context, record slog.Record) error {
	var text strings.Builder
	text.WriteString(record.Message)
	text.WriteString(h.attrs)
	record.Attrs(func(attr slog.Attr) bool {
		appendLogAttr(&text, h.prefix, attr)
		return true
	})
	message := redactLogSecrets(text.String())

	when := record.Time
	if when.IsZero() {
		when = time.Now()
	}
	// os.Stdout is looked up per record so a redirected stdout is honored.
	_, err := fmt.Fprintf(os.Stdout, "%s %s %s\n", when.Format(time.RFC3339), record.Level.String(), message)
	if record.Level >= slog.LevelError {
		appendRuntimeErrorLog(message)
	}
	return err
}

func (h *runtimeLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	var text strings.Builder
	text.WriteString(h.attrs)
	for _, attr := range attrs {
		appendLogAttr(&text, h.prefix, attr)
	}
	clone := *h
	clone.attrs = text.String()
	return &clone
}

func (h *runtimeLogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.prefix = h.prefix + name + "."
	return &clone
}

func appendLogAttr(text *strings.Builder, prefix string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		groupPrefix := prefix
		if attr.Key != "" {
			groupPrefix += attr.Key + "."
		}
		for _, member := range attr.Value.Group() {
			appendLogAttr(text, groupPrefix, member)
		}
		return
	}
	value := attr.Value.String()
	if value == "" || strings.ContainsAny(value, " \t\n\"=") {
		value = fmt.Sprintf("%q", value)
	}
	text.WriteString(" ")
	text.WriteString(prefix)
	text.WriteString(attr.Key)
	text.WriteString("=")
	text.WriteString(value)
}

// Secret redaction shared by the log handler and the runtime error log file.

const (
	redactedTokenMarker  = "<redacted-token>"
	redactedAPIKeyMarker = "<redacted-api-key>"
	// Shorter values are not treated as secrets, to avoid over-redacting.
	minLogSecretLength = 8
)

var (
	logSecretsMu sync.RWMutex
	logSecrets   = map[string]struct{}{}

	// Shapes of secrets that are redacted even when the value is unknown.
	botTokenInURLRe      = regexp.MustCompile(`/bot\d+:[A-Za-z0-9_-]+`)
	mediaUserTokenPairRe = regexp.MustCompile(`(?i)(media-user-token["']?\s*[:=]\s*["']?)[^\s"',;&]+`)
	apiKeyParamRe        = regexp.MustCompile(`(?i)(api_key=)[^&\s"']+`)
)

// registerLogSecret makes the log handler replace value (a bot token) with
// <redacted-token> wherever it appears.
func registerLogSecret(value string) {
	value = strings.TrimSpace(value)
	if len(value) < minLogSecretLength {
		return
	}
	logSecretsMu.RLock()
	_, known := logSecrets[value]
	logSecretsMu.RUnlock()
	if known {
		return
	}
	logSecretsMu.Lock()
	logSecrets[value] = struct{}{}
	logSecretsMu.Unlock()
}

// redactLogSecrets masks the Telegram bot token, the Apple Music tokens
// (Media-User-Token / authorization token) and the Last.fm api_key.
func redactLogSecrets(text string) string {
	if text == "" {
		return text
	}
	logSecretsMu.RLock()
	for secret := range logSecrets {
		text = strings.ReplaceAll(text, secret, redactedTokenMarker)
	}
	logSecretsMu.RUnlock()
	text = redactAppleSecrets(text)
	if key := resolveLastFMAPIKey(); len(key) >= minLogSecretLength {
		text = strings.ReplaceAll(text, key, redactedAPIKeyMarker)
	}
	text = botTokenInURLRe.ReplaceAllString(text, "/bot"+redactedTokenMarker)
	text = mediaUserTokenPairRe.ReplaceAllString(text, "${1}<redacted-apple-token>")
	text = apiKeyParamRe.ReplaceAllString(text, "${1}"+redactedAPIKeyMarker)
	return text
}
