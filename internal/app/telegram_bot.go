package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/cmdrunner"
)

func newTelegramBot(token, appleToken string) *TelegramBot {
	allowed := buildIDSet(Config.TelegramAllowedChatIDs)
	admins := buildIDSet(Config.TelegramAdminUserIDs)
	searchLimit := Config.TelegramSearchLimit
	if searchLimit <= 0 {
		searchLimit = defaultSearchLimit
	}
	maxFileBytes := int64(Config.TelegramMaxFileMB) * 1024 * 1024
	if maxFileBytes <= 0 {
		maxFileBytes = 50 * 1024 * 1024
	}
	cacheFile := strings.TrimSpace(Config.TelegramCacheFile)
	if cacheFile == "" {
		cacheFile = "telegram-cache.json"
	}
	stateFile := resolveTelegramStateFile(cacheFile)
	queueSize := defaultQueueSize
	if queueSize <= 0 {
		queueSize = 1
	}
	apiBase := normalizeTelegramAPIBase(Config.TelegramAPIURL)
	warnInsecureTelegramAPIBase(apiBase)
	apiTimeout := time.Duration(Config.TelegramHTTPTimeoutSec) * time.Second
	if apiTimeout <= 0 {
		apiTimeout = defaultTelegramHTTPTimeout
	}
	pollTimeout := time.Duration(Config.TelegramPollTimeoutSec) * time.Second
	if pollTimeout <= 0 {
		pollTimeout = defaultTelegramPollTimeout
	}
	if pollTimeout < minTelegramPollTimeout {
		pollTimeout = minTelegramPollTimeout
	}
	proxyFunc, proxyInfo := resolveTelegramProxy()
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	bot := &TelegramBot{
		token:                   token,
		apiBase:                 apiBase,
		proxyInfo:               proxyInfo,
		appleToken:              appleToken,
		client:                  newTelegramHTTPClient(apiTimeout, proxyFunc),
		pollClient:              newTelegramHTTPClient(pollTimeout, proxyFunc),
		allowedChats:            allowed,
		adminUsers:              admins,
		forwardChatID:           Config.TelegramForwardChatID,
		searchLimit:             searchLimit,
		maxFileBytes:            maxFileBytes,
		errorLogFile:            resolveTelegramErrorLogFile(cacheFile, stateFile),
		getUpdatesErrorDelay:    defaultTelegramGetUpdatesErrorSleep,
		getUpdatesConflictDelay: defaultTelegramGetUpdatesConflictSleep,
		getUpdatesRestartAfter:  defaultTelegramGetUpdatesRestartThreshold,
		userWhitelistEnabled:    Config.TelegramUserWhitelistEnabled,
		userWhitelist:           make(map[int64]bool),
		userBlacklist:           make(map[int64]bool),
		forwardEnabled:          Config.TelegramForwardEnabled,
		chatSettings:            make(map[int64]ChatDownloadSettings),
		pending:                 make(map[int64]map[int]*PendingSelection),
		pendingTransfers:        make(map[int64]map[int]*PendingTransfer),
		pendingMVStreams:        make(map[int64]map[int]*PendingMVStream),
		pendingArtistModes:      make(map[int64]map[int]*PendingArtistMode),
		downloadQueue:           make(chan *downloadRequest, queueSize),
		workerLimit:             defaultTaskWorkerLimit,
		cacheFile:               cacheFile,
		stateFile:               stateFile,
		cache:                   make(map[string]CachedAudio),
		docCache:                make(map[string]CachedDocument),
		videoCache:              make(map[string]CachedVideo),
		inflightDownloads:       make(map[string]struct{}),
		activeRequests:          make(map[string]telegramPersistedRequest),
		subscriptions:           make(map[string]telegramSubscription),
		temporaryReleases:       make(map[string]telegramTemporaryRelease),
		runningRequestCtx:       make(map[string]context.CancelFunc),
		canceledRequestIDs:      make(map[string]struct{}),
		autoDeleteMessages:      make(map[string]*time.Timer),
		autoDeleteSticky:        make(map[string]bool),
		autoDeleteDeadline:      make(map[string]time.Time),
		songCommentCache:        make(map[string]songCommentCacheEntry),
		sendLimiter:             newTelegramSendLimiter(telegramSendGlobalInterval(), telegramSendChatInterval()),
		userLimiter:             newUserRateLimiter(telegramRateLimitEnabled(), Config.TelegramRateLimitPerUserIntervalSec, Config.TelegramRateLimitPerUserBurst, Config.TelegramRateLimitPerUserDailyQuota),
		shutdownCtx:             shutdownCtx,
		shutdownCancel:          shutdownCancel,
	}
	configureExternalCommandRetries()
	historyFile := resolveTelegramHistoryFile(cacheFile, stateFile)
	bot.openStore(historyFile)
	bot.history = newDownloadHistoryWithStore(telegramHistoryEnabled(), Config.TelegramHistoryPerChatLimit, historyFile, bot.store)
	bot.queueCond = sync.NewCond(&bot.queueMu)
	bot.ensureBackgroundTaskControl()
	bot.loadCache()
	bot.startCacheSaver()
	bot.startStateSaver()
	bot.startDownloadWorker()
	bot.restoreRuntimeState()
	bot.startMetricsReporter()
	bot.resourceGuard = newTelegramResourceGuard(telegramCleanupRootPaths())
	if bot.resourceGuard != nil {
		bot.resourceGuard.start()
	}
	bot.cleanupTracker = newTelegramCleanupTracker(
		telegramCleanupRoots(),
		cacheFile,
		telegramDownloadMaxBytes(),
		telegramCleanupInterval(),
		telegramCleanupScanInterval(),
		telegramCleanupProtectAge(),
	)
	bot.cleanupTracker.onDelete = func(path string, size int64) {
		appRuntimeMetrics.recordCleanupRemoval(size)
	}
	bot.cleanupTracker.start()
	bot.requestStateSave()
	bot.startMetricsHTTPServer()
	bot.startSubscriptionWatcher()
	return bot
}

func telegramRateLimitEnabled() bool {
	if Config.TelegramRateLimitEnabled != nil {
		return *Config.TelegramRateLimitEnabled
	}
	return true
}

func telegramHistoryEnabled() bool {
	if Config.TelegramHistoryEnabled != nil {
		return *Config.TelegramHistoryEnabled
	}
	return true
}

func resolveTelegramHistoryFile(cacheFile, stateFile string) string {
	if raw := strings.TrimSpace(Config.TelegramHistoryFile); raw != "" {
		return filepath.Clean(raw)
	}
	base := strings.TrimSpace(stateFile)
	if base == "" {
		base = strings.TrimSpace(cacheFile)
	}
	if base != "" {
		trimmed := strings.TrimSuffix(base, filepath.Ext(base))
		trimmed = strings.TrimSuffix(trimmed, ".state")
		if strings.TrimSpace(trimmed) != "" {
			return filepath.Clean(trimmed + ".history.json")
		}
	}
	return downloadHistoryFileFallback
}

func configureExternalCommandRetries() {
	backoff := time.Duration(Config.TelegramCmdRetryBackoffMS) * time.Millisecond
	cmdrunner.ConfigureDefault(Config.TelegramCmdMaxRetries, backoff)
}
