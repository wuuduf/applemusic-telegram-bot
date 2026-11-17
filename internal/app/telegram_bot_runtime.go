package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	sharedstorage "github.com/wuuduf/applemusic-telegram-bot/internal/storage"
	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func nextLocalMidnight(now time.Time) time.Time {
	current := now
	if current.IsZero() {
		current = time.Now()
	}
	loc := current.Location()
	year, month, day := current.In(loc).Date()
	return time.Date(year, month, day+1, 0, 0, 0, 0, loc)
}

func telegramDailyRestartEnabled() bool {
	if Config.TelegramDailyRestartEnabled == nil {
		return true
	}
	return *Config.TelegramDailyRestartEnabled
}

func runTelegramBot(appleToken string) {
	botToken := strings.TrimSpace(Config.TelegramBotToken)
	if botToken == "" {
		botToken = strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	}
	if botToken == "" {
		logWarnf("telegram-bot-token is not set. Add it to config.yaml or TELEGRAM_BOT_TOKEN.")
		return
	}
	sharedstorage.ApplyTelegramStorageOverrides(&Config)
	configureTelegramTempDir(&Config)
	signalCtx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()

	for {
		// The Apple web token expires; each runtime (daily restart included)
		// starts from the current one instead of the token read at launch.
		appleTokenAuto := false
		if fresh, err := ampapi.GetToken(); err == nil && strings.TrimSpace(fresh) != "" {
			appleToken = fresh
			appleTokenAuto = true
		}
		bot := newTelegramBot(botToken, appleToken, appleTokenAuto)
		setRuntimeErrorLogPath(bot.errorLogFile)
		logInfof("Telegram bot started. Waiting for updates...")
		logInfof("Telegram API base: %s (proxy=%s, api timeout=%s, poll timeout=%s)", bot.apiBase, bot.proxyInfo, bot.client.Timeout, bot.pollClient.Timeout)
		logInfof("Telegram runtime error log: %s", bot.errorLogFile)

		dailyRestartEnabled := telegramDailyRestartEnabled()
		var restartTimer *time.Timer
		var restartCh <-chan time.Time
		if dailyRestartEnabled {
			restartAt := nextLocalMidnight(time.Now())
			untilRestart := time.Until(restartAt)
			if untilRestart <= 0 {
				untilRestart = time.Second
			}
			restartTimer = time.NewTimer(untilRestart)
			restartCh = restartTimer.C
			logInfof("Telegram daily auto-restart scheduled at %s", restartAt.Format(time.RFC3339))
		} else {
			logInfof("Telegram daily auto-restart disabled by config.")
		}

		loopDone := bot.startRuntimeLoop()

		reason := "loop-exit"
		dailyRestartDeferred := false
	waitLoop:
		for {
			select {
			case <-signalCtx.Done():
				reason = "signal"
				logInfof("Telegram bot shutdown requested.")
				break waitLoop
			case <-restartCh:
				load := bot.dailyRestartTaskLoad()
				if load.hasPendingWork() {
					dailyRestartDeferred = true
					logWarnf("Telegram daily auto-restart deferred for %s because tasks are still active (%s)", defaultTelegramDailyRestartDeferCheck, load)
					if restartTimer != nil {
						restartTimer.Reset(defaultTelegramDailyRestartDeferCheck)
						restartCh = restartTimer.C
					}
					continue
				}
				reason = "daily-restart"
				if dailyRestartDeferred {
					logInfof("Telegram daily auto-restart triggered after active tasks drained.")
				} else {
					logInfof("Telegram daily auto-restart triggered.")
				}
				break waitLoop
			case <-loopDone:
				if bot.isShuttingDown() {
					reason = "shutdown"
				} else {
					reason = "loop-exit"
					logWarnf("Telegram loop stopped unexpectedly. Restarting bot runtime.")
				}
				break waitLoop
			}
		}
		if restartTimer != nil {
			if !restartTimer.Stop() {
				select {
				case <-restartTimer.C:
				default:
				}
			}
		}

		bot.shutdownRuntime(loopDone)

		switch reason {
		case "signal":
			return
		case "daily-restart":
			continue
		case "shutdown":
			logErrorf("telegram bot loop stopped after shutdown request; restarting runtime")
		default:
			logErrorf("telegram bot loop stopped unexpectedly; restarting runtime")
		}
		select {
		case <-signalCtx.Done():
			return
		case <-time.After(defaultTelegramLoopRestartDelay):
		}
	}
}

// startRuntimeLoop runs the polling loop in the background; the returned
// channel is closed when it stops.
func (b *TelegramBot) startRuntimeLoop() <-chan struct{} {
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		b.loopWithAutoRestart()
	}()
	return loopDone
}

// shutdownRuntime stops one bot runtime: polling, update handlers,
// background tasks and workers, then flushes cache, state and history and
// closes the store. Requests that had not finished stay in the saved state
// for the next runtime.
func (b *TelegramBot) shutdownRuntime(loopDone <-chan struct{}) {
	b.requestShutdown()
	closeHTTPIdleConnections(b.pollClient)
	closeHTTPIdleConnections(b.client)
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		logErrorf("telegram bot shutdown timed out waiting for polling loop to stop")
	}
	if !b.waitBackgroundTasks(defaultTelegramBackgroundTaskWait) {
		logErrorf("telegram bot shutdown timed out waiting for background tasks to stop")
	}

	b.stopDownloadWorkers()
	if b.cleanupTracker != nil {
		b.cleanupTracker.stop()
	}
	if b.resourceGuard != nil {
		b.resourceGuard.stop()
	}
	b.stopSubscriptionWatcher()
	b.stopMetricsHTTPServer()
	b.stopMetricsReporter()
	b.stopCacheSaver()
	b.stopStateSaver()
	if b.history != nil {
		b.history.Flush()
	}
	b.closeStore()
	b.clearAllAutoDeleteMessages()
}

func configureTelegramTempDir(cfg *structs.ConfigSet) {
	if cfg == nil {
		return
	}

	// AMDL_TMPDIR names a directory that belongs to the bot, so the download
	// quota cleanup may delete files in it.
	if explicitTmp := strings.TrimSpace(os.Getenv("AMDL_TMPDIR")); explicitTmp != "" {
		clean := filepath.Clean(explicitTmp)
		if err := os.MkdirAll(clean, 0755); err != nil {
			logWarnf("Failed to prepare temp dir %s: %v", clean, err)
			return
		}
		_ = os.Setenv("AMDL_TMPDIR", clean)
		if strings.TrimSpace(os.Getenv("TMPDIR")) == "" {
			_ = os.Setenv("TMPDIR", clean)
		}
		logInfof("Telegram temp dir: %s (from AMDL_TMPDIR)", clean)
		return
	}
	// A TMPDIR inherited from the environment is shared with other programs
	// (on macOS it is always set, to the per-user temp folder). Temporary
	// files still go there, but it is deliberately not recorded as
	// AMDL_TMPDIR, so the quota cleanup never deletes other programs' files.
	if inherited := strings.TrimSpace(os.Getenv("TMPDIR")); inherited != "" {
		logInfof("Telegram temp dir: %s (inherited TMPDIR; not managed by download cleanup)", filepath.Clean(inherited))
		return
	}

	base := resolveTelegramTempBaseDir(cfg)
	if base == "" {
		return
	}
	cleanBase := filepath.Clean(base)
	if !filepath.IsAbs(cleanBase) {
		if abs, err := filepath.Abs(cleanBase); err == nil {
			cleanBase = abs
		}
	}
	tmpDir := filepath.Join(cleanBase, ".tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		logWarnf("Failed to create telegram temp dir %s: %v", tmpDir, err)
		return
	}
	_ = os.Setenv("AMDL_TMPDIR", tmpDir)
	_ = os.Setenv("TMPDIR", tmpDir)
	logInfof("Telegram temp dir: %s", tmpDir)
}

func resolveTelegramTempBaseDir(cfg *structs.ConfigSet) string {
	if cfg == nil {
		return ""
	}
	candidates := []string{
		strings.TrimSpace(cfg.TelegramDownloadFolder),
		strings.TrimSpace(cfg.AlacSaveFolder),
		strings.TrimSpace(cfg.AtmosSaveFolder),
		strings.TrimSpace(cfg.AacSaveFolder),
	}
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func panicErrorWithStack(scope string, rec any) error {
	label := strings.TrimSpace(scope)
	if label == "" {
		label = "runtime"
	}
	return fmt.Errorf("%s panic: %v\n%s", label, rec, string(debug.Stack()))
}

func logRecoveredPanic(scope string, rec any) error {
	err := panicErrorWithStack(scope, rec)
	logErrorf("%s", err.Error())
	return err
}

func runWithRecovery(scope string, onPanic func(error), fn func()) (panicked bool) {
	if fn == nil {
		return false
	}
	defer func() {
		if rec := recover(); rec != nil {
			panicked = true
			err := logRecoveredPanic(scope, rec)
			if onPanic != nil {
				onPanic(err)
			}
		}
	}()
	fn()
	return false
}

func (b *TelegramBot) loopWithAutoRestart() {
	for {
		if b.isShuttingDown() {
			return
		}
		panicked := runWithRecovery("telegram loop", nil, func() {
			b.loop()
		})
		if !panicked {
			return
		}
		if b.isShuttingDown() {
			return
		}
		logErrorf("telegram loop recovered from panic; restarting in %s", defaultTelegramLoopRestartDelay)
		closeHTTPIdleConnections(b.pollClient)
		closeHTTPIdleConnections(b.client)
		select {
		case <-b.shutdownContext().Done():
			return
		case <-time.After(defaultTelegramLoopRestartDelay):
		}
	}
}

func (b *TelegramBot) getUpdatesErrorSleep() time.Duration {
	if b != nil && b.getUpdatesErrorDelay > 0 {
		return b.getUpdatesErrorDelay
	}
	return defaultTelegramGetUpdatesErrorSleep
}

func (b *TelegramBot) getUpdatesConflictSleep() time.Duration {
	if b != nil && b.getUpdatesConflictDelay > 0 {
		return b.getUpdatesConflictDelay
	}
	return defaultTelegramGetUpdatesConflictSleep
}

func (b *TelegramBot) getUpdatesRestartThreshold() int {
	if b != nil && b.getUpdatesRestartAfter > 0 {
		return b.getUpdatesRestartAfter
	}
	return defaultTelegramGetUpdatesRestartThreshold
}
