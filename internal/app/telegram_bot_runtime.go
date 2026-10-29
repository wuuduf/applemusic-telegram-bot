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
		fmt.Println("telegram-bot-token is not set. Add it to config.yaml or TELEGRAM_BOT_TOKEN.")
		return
	}
	sharedstorage.ApplyTelegramStorageOverrides(&Config)
	configureTelegramTempDir(&Config)
	signalCtx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()

	for {
		bot := newTelegramBot(botToken, appleToken)
		setRuntimeErrorLogPath(bot.errorLogFile)
		fmt.Println("Telegram bot started. Waiting for updates...")
		fmt.Printf("Telegram API base: %s (proxy=%s, api timeout=%s, poll timeout=%s)\n", bot.apiBase, bot.proxyInfo, bot.client.Timeout, bot.pollClient.Timeout)
		fmt.Printf("Telegram runtime error log: %s\n", bot.errorLogFile)

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
			fmt.Printf("Telegram daily auto-restart scheduled at %s\n", restartAt.Format(time.RFC3339))
		} else {
			fmt.Println("Telegram daily auto-restart disabled by config.")
		}

		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			bot.loopWithAutoRestart()
		}()

		reason := "loop-exit"
		dailyRestartDeferred := false
	waitLoop:
		for {
			select {
			case <-signalCtx.Done():
				reason = "signal"
				fmt.Println("Telegram bot shutdown requested.")
				break waitLoop
			case <-restartCh:
				load := bot.dailyRestartTaskLoad()
				if load.hasPendingWork() {
					dailyRestartDeferred = true
					fmt.Printf("Telegram daily auto-restart deferred for %s because tasks are still active (%s)\n", defaultTelegramDailyRestartDeferCheck, load)
					if restartTimer != nil {
						restartTimer.Reset(defaultTelegramDailyRestartDeferCheck)
						restartCh = restartTimer.C
					}
					continue
				}
				reason = "daily-restart"
				if dailyRestartDeferred {
					fmt.Println("Telegram daily auto-restart triggered after active tasks drained.")
				} else {
					fmt.Println("Telegram daily auto-restart triggered.")
				}
				break waitLoop
			case <-loopDone:
				if bot.isShuttingDown() {
					reason = "shutdown"
				} else {
					reason = "loop-exit"
					fmt.Println("Telegram loop stopped unexpectedly. Restarting bot runtime.")
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

		bot.requestShutdown()
		closeHTTPIdleConnections(bot.pollClient)
		closeHTTPIdleConnections(bot.client)
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			fmt.Println("Telegram bot shutdown timed out waiting for polling loop to stop.")
			appendRuntimeErrorLog("telegram bot shutdown timed out waiting for polling loop to stop")
		}
		if !bot.waitBackgroundTasks(defaultTelegramBackgroundTaskWait) {
			fmt.Println("Telegram bot shutdown timed out waiting for background tasks to stop.")
			appendRuntimeErrorLog("telegram bot shutdown timed out waiting for background tasks to stop")
		}

		bot.stopDownloadWorkers()
		if bot.cleanupTracker != nil {
			bot.cleanupTracker.stop()
		}
		if bot.resourceGuard != nil {
			bot.resourceGuard.stop()
		}
		bot.stopSubscriptionWatcher()
		bot.stopMetricsHTTPServer()
		bot.stopMetricsReporter()
		bot.stopCacheSaver()
		bot.stopStateSaver()
		if bot.history != nil {
			bot.history.Flush()
		}
		bot.closeStore()
		bot.clearAllAutoDeleteMessages()

		switch reason {
		case "signal":
			return
		case "daily-restart":
			continue
		case "shutdown":
			appendRuntimeErrorLog("telegram bot loop stopped after shutdown request; restarting runtime")
		default:
			appendRuntimeErrorLog("telegram bot loop stopped unexpectedly; restarting runtime")
		}
		select {
		case <-signalCtx.Done():
			return
		case <-time.After(defaultTelegramLoopRestartDelay):
		}
	}
}

func configureTelegramTempDir(cfg *structs.ConfigSet) {
	if cfg == nil {
		return
	}

	explicitTmp := strings.TrimSpace(os.Getenv("AMDL_TMPDIR"))
	if explicitTmp == "" {
		explicitTmp = strings.TrimSpace(os.Getenv("TMPDIR"))
	}
	if explicitTmp != "" {
		clean := filepath.Clean(explicitTmp)
		if err := os.MkdirAll(clean, 0755); err != nil {
			fmt.Printf("Failed to prepare temp dir %s: %v\n", clean, err)
			return
		}
		_ = os.Setenv("AMDL_TMPDIR", clean)
		if strings.TrimSpace(os.Getenv("TMPDIR")) == "" {
			_ = os.Setenv("TMPDIR", clean)
		}
		fmt.Printf("Telegram temp dir: %s (from environment)\n", clean)
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
		fmt.Printf("Failed to create telegram temp dir %s: %v\n", tmpDir, err)
		return
	}
	_ = os.Setenv("AMDL_TMPDIR", tmpDir)
	_ = os.Setenv("TMPDIR", tmpDir)
	fmt.Printf("Telegram temp dir: %s\n", tmpDir)
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
	fmt.Println(err.Error())
	appendRuntimeErrorLog(err.Error())
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
		fmt.Printf("telegram loop recovered from panic, restarting in %s\n", defaultTelegramLoopRestartDelay)
		appendRuntimeErrorLogf("telegram loop recovered from panic; restarting in %s", defaultTelegramLoopRestartDelay)
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
