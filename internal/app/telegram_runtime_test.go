package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
)

func TestTelegramSendLimiterBlockFor(t *testing.T) {
	limiter := newTelegramSendLimiter(100*time.Millisecond, 100*time.Millisecond)
	if limiter == nil {
		t.Fatalf("expected limiter")
	}
	now := time.Unix(2000, 0)
	limiter.nowFn = func() time.Time { return now }
	limiter.blockFor(3 * time.Second)
	wait := limiter.nextWaitLocked(now, 1001)
	if wait != 3*time.Second {
		t.Fatalf("expected 3s wait, got %s", wait)
	}
}

func TestTelegramRuntimeStateSaveLoadRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "telegram-state.json")
	b := &TelegramBot{
		stateFile: statePath,
		pending: map[int64]map[int]*PendingSelection{
			1: {
				10: {
					Kind:       "song",
					Query:      "hello",
					Storefront: "us",
					Items: []apputils.SearchResultItem{
						{ID: "song-1", Name: "Song 1", Artist: "Artist 1"},
					},
					CreatedAt:        time.Now(),
					ReplyToMessageID: 99,
					ResultsMessageID: 10,
				},
			},
		},
		pendingTransfers: map[int64]map[int]*PendingTransfer{
			1: {
				11: {
					MediaType:        mediaTypeAlbum,
					MediaID:          "album-1",
					Storefront:       "us",
					ReplyToMessageID: 100,
					MessageID:        11,
					CreatedAt:        time.Now(),
				},
			},
		},
		pendingArtistModes: map[int64]map[int]*PendingArtistMode{
			1: {
				12: {
					ArtistID:         "artist-1",
					ArtistName:       "Artist",
					Storefront:       "us",
					ReplyToMessageID: 101,
					MessageID:        12,
					CreatedAt:        time.Now(),
				},
			},
		},
		activeRequests: map[string]telegramPersistedRequest{
			"req-1": {
				RequestID:   "req-1",
				ChatID:      1,
				MediaType:   mediaTypeSong,
				MediaID:     "song-1",
				Storefront:  "us",
				InflightKey: "k1",
				State:       "queued",
			},
		},
		inflightDownloads: map[string]struct{}{"k1": {}},
		chatSettings: map[int64]ChatDownloadSettings{
			1: {Format: telegramFormatAlac, Language: telegramLanguageEn, SettingsInited: true},
		},
		userWhitelistEnabled: true,
		userWhitelist:        map[int64]bool{3001: true},
		userBlacklist:        map[int64]bool{4001: true},
		forwardEnabled:       true,
		autoDeleteMessages:   make(map[string]*time.Timer),
		autoDeleteSticky:     make(map[string]bool),
		autoDeleteDeadline:   make(map[string]time.Time),
	}
	autoDeleteAt := time.Now().Add(5 * time.Minute)
	b.scheduleAutoDeleteMessageAt(1, 99, true, autoDeleteAt)
	defer b.clearAllAutoDeleteMessages()

	if err := b.saveRuntimeStateNow(); err != nil {
		t.Fatalf("saveRuntimeStateNow failed: %v", err)
	}
	loaded, err := loadRuntimeStateFromFile(statePath)
	if err != nil {
		t.Fatalf("loadRuntimeStateFromFile failed: %v", err)
	}
	if len(loaded.Pending) != 1 {
		t.Fatalf("expected pending data")
	}
	if len(loaded.Requests) != 1 || loaded.Requests[0].RequestID != "req-1" {
		t.Fatalf("expected persisted request")
	}
	if len(loaded.InflightKeys) != 1 || loaded.InflightKeys[0] != "k1" {
		t.Fatalf("expected inflight keys derived from requests, got %+v", loaded.InflightKeys)
	}
	if len(loaded.AutoDelete) != 1 {
		t.Fatalf("expected persisted auto-delete entries, got %+v", loaded.AutoDelete)
	}
	autoDelete := loaded.AutoDelete[0]
	if autoDelete.ChatID != 1 || autoDelete.MessageID != 99 || !autoDelete.Sticky {
		t.Fatalf("unexpected auto-delete entry: %+v", autoDelete)
	}
	if autoDelete.DeleteAt.IsZero() {
		t.Fatalf("expected persisted auto-delete deadline")
	}
	if loaded.ChatSettings[1].Language != telegramLanguageEn {
		t.Fatalf("expected persisted chat language to be en, got %+v", loaded.ChatSettings[1])
	}
	if loaded.UserWhitelistEnabled == nil || !*loaded.UserWhitelistEnabled {
		t.Fatalf("expected persisted whitelist mode enabled, got %+v", loaded.UserWhitelistEnabled)
	}
	if len(loaded.UserWhitelist) != 1 || loaded.UserWhitelist[0] != 3001 {
		t.Fatalf("expected persisted whitelist users, got %+v", loaded.UserWhitelist)
	}
	if len(loaded.UserBlacklist) != 1 || loaded.UserBlacklist[0] != 4001 {
		t.Fatalf("expected persisted blacklist users, got %+v", loaded.UserBlacklist)
	}
	if loaded.ForwardEnabled == nil || !*loaded.ForwardEnabled {
		t.Fatalf("expected persisted forward flag enabled, got %+v", loaded.ForwardEnabled)
	}
}

func TestTelegramRuntimeStateDoesNotPersistOrphanInflightKeys(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "telegram-state.json")
	b := &TelegramBot{
		stateFile:          statePath,
		pending:            make(map[int64]map[int]*PendingSelection),
		pendingTransfers:   make(map[int64]map[int]*PendingTransfer),
		pendingArtistModes: make(map[int64]map[int]*PendingArtistMode),
		activeRequests:     make(map[string]telegramPersistedRequest),
		inflightDownloads:  map[string]struct{}{"orphan-key": {}},
		chatSettings:       make(map[int64]ChatDownloadSettings),
	}

	if err := b.saveRuntimeStateNow(); err != nil {
		t.Fatalf("saveRuntimeStateNow failed: %v", err)
	}
	loaded, err := loadRuntimeStateFromFile(statePath)
	if err != nil {
		t.Fatalf("loadRuntimeStateFromFile failed: %v", err)
	}
	if len(loaded.InflightKeys) != 0 {
		t.Fatalf("expected no orphan inflight keys to be persisted, got %+v", loaded.InflightKeys)
	}
}

func TestTelegramRuntimeStateRestoreQueuesRecoveredRequests(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "telegram-state.json")
	state := telegramPersistedState{
		Version: telegramStateVersion,
		Requests: []telegramPersistedRequest{
			{
				RequestID:    "req-song-1",
				ChatID:       101,
				ReplyToID:    9,
				Single:       true,
				Settings:     ChatDownloadSettings{Format: telegramFormatAlac, SettingsInited: true},
				TransferMode: transferModeOneByOne,
				MediaType:    mediaTypeSong,
				MediaID:      "12345",
				Storefront:   "us",
				InflightKey:  "k-song-1",
				State:        "queued",
				UpdatedAt:    time.Now(),
			},
		},
	}
	payload, err := jsonMarshalIndentForTest(state)
	if err != nil {
		t.Fatalf("marshal state failed: %v", err)
	}
	if err := os.WriteFile(statePath, payload, 0644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	b := &TelegramBot{
		stateFile:          statePath,
		appleToken:         "token",
		pending:            make(map[int64]map[int]*PendingSelection),
		pendingTransfers:   make(map[int64]map[int]*PendingTransfer),
		pendingArtistModes: make(map[int64]map[int]*PendingArtistMode),
		chatSettings:       make(map[int64]ChatDownloadSettings),
		inflightDownloads:  make(map[string]struct{}),
		activeRequests:     make(map[string]telegramPersistedRequest),
		downloadQueue:      make(chan *downloadRequest, 2),
		stateSave:          make(chan struct{}, 1),
	}
	b.restoreRuntimeState()

	if len(b.downloadQueue) != 1 {
		t.Fatalf("expected recovered request in queue, got %d", len(b.downloadQueue))
	}
	req := <-b.downloadQueue
	if req == nil {
		t.Fatalf("expected non-nil request")
	}
	if req.mediaType != mediaTypeSong || req.mediaID != "12345" || req.storefront != "us" {
		t.Fatalf("unexpected recovered request: %+v", req)
	}
	if _, ok := b.inflightDownloads["k-song-1"]; !ok {
		t.Fatalf("expected recovered inflight key")
	}
}

func TestTelegramRuntimeStateRestoreSchedulesAutoDeleteTimers(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "telegram-state.json")
	deleteAt := time.Now().Add(10 * time.Minute)
	state := telegramPersistedState{
		Version: telegramStateVersion,
		AutoDelete: []telegramPersistedAutoDelete{
			{
				ChatID:    2024,
				MessageID: 88,
				Sticky:    true,
				DeleteAt:  deleteAt,
			},
		},
	}
	payload, err := jsonMarshalIndentForTest(state)
	if err != nil {
		t.Fatalf("marshal state failed: %v", err)
	}
	if err := os.WriteFile(statePath, payload, 0644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	b := &TelegramBot{
		stateFile:          statePath,
		appleToken:         "token",
		pending:            make(map[int64]map[int]*PendingSelection),
		pendingTransfers:   make(map[int64]map[int]*PendingTransfer),
		pendingArtistModes: make(map[int64]map[int]*PendingArtistMode),
		chatSettings:       make(map[int64]ChatDownloadSettings),
		inflightDownloads:  make(map[string]struct{}),
		activeRequests:     make(map[string]telegramPersistedRequest),
		downloadQueue:      make(chan *downloadRequest, 1),
		stateSave:          make(chan struct{}, 1),
		autoDeleteMessages: make(map[string]*time.Timer),
		autoDeleteSticky:   make(map[string]bool),
		autoDeleteDeadline: make(map[string]time.Time),
	}
	b.restoreRuntimeState()
	defer b.clearAllAutoDeleteMessages()

	key := autoDeleteKey(2024, 88)
	b.autoDeleteMu.Lock()
	_, hasTimer := b.autoDeleteMessages[key]
	sticky := b.autoDeleteSticky[key]
	restoredDeleteAt := b.autoDeleteDeadline[key]
	b.autoDeleteMu.Unlock()
	if !hasTimer {
		t.Fatalf("expected restored auto-delete timer")
	}
	if !sticky {
		t.Fatalf("expected restored sticky auto-delete marker")
	}
	if restoredDeleteAt.IsZero() {
		t.Fatalf("expected restored auto-delete deadline")
	}
}

func TestTelegramRuntimeStateRestoreRecoversUserAccessState(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "telegram-state.json")
	state := telegramPersistedState{
		Version:              telegramStateVersion,
		UserWhitelistEnabled: boolPtr(true),
		UserWhitelist:        []int64{3001, 3002},
		UserBlacklist:        []int64{4001},
		ForwardEnabled:       boolPtr(true),
	}
	payload, err := jsonMarshalIndentForTest(state)
	if err != nil {
		t.Fatalf("marshal state failed: %v", err)
	}
	if err := os.WriteFile(statePath, payload, 0644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	b := &TelegramBot{
		stateFile:            statePath,
		appleToken:           "token",
		pending:              make(map[int64]map[int]*PendingSelection),
		pendingTransfers:     make(map[int64]map[int]*PendingTransfer),
		pendingArtistModes:   make(map[int64]map[int]*PendingArtistMode),
		chatSettings:         make(map[int64]ChatDownloadSettings),
		inflightDownloads:    make(map[string]struct{}),
		activeRequests:       make(map[string]telegramPersistedRequest),
		downloadQueue:        make(chan *downloadRequest, 1),
		stateSave:            make(chan struct{}, 1),
		userWhitelistEnabled: false,
		userWhitelist:        make(map[int64]bool),
		userBlacklist:        make(map[int64]bool),
		autoDeleteMessages:   make(map[string]*time.Timer),
		autoDeleteSticky:     make(map[string]bool),
		autoDeleteDeadline:   make(map[string]time.Time),
	}
	b.restoreRuntimeState()

	if !b.isUserWhitelistEnabled() {
		t.Fatalf("expected whitelist mode restored")
	}
	if !b.isUserWhitelisted(3001) || !b.isUserWhitelisted(3002) {
		t.Fatalf("expected whitelist users restored")
	}
	if !b.isUserBlacklisted(4001) {
		t.Fatalf("expected blacklist user restored")
	}
	if !b.isForwardEnabled() {
		t.Fatalf("expected forward flag restored")
	}
}

func TestTelegramRuntimeStateRestoreIgnoresLegacyOrphanInflightKeys(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "telegram-state.json")
	state := telegramPersistedState{
		Version:      telegramStateVersion,
		InflightKeys: []string{"legacy-orphan"},
	}
	payload, err := jsonMarshalIndentForTest(state)
	if err != nil {
		t.Fatalf("marshal state failed: %v", err)
	}
	if err := os.WriteFile(statePath, payload, 0644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	b := &TelegramBot{
		stateFile:          statePath,
		appleToken:         "token",
		pending:            make(map[int64]map[int]*PendingSelection),
		pendingTransfers:   make(map[int64]map[int]*PendingTransfer),
		pendingArtistModes: make(map[int64]map[int]*PendingArtistMode),
		chatSettings:       make(map[int64]ChatDownloadSettings),
		inflightDownloads:  make(map[string]struct{}),
		activeRequests:     make(map[string]telegramPersistedRequest),
		downloadQueue:      make(chan *downloadRequest, 1),
		stateSave:          make(chan struct{}, 1),
	}
	b.restoreRuntimeState()

	if len(b.inflightDownloads) != 0 {
		t.Fatalf("expected legacy orphan inflight keys to be ignored, got %+v", b.inflightDownloads)
	}
}

func TestTelegramRuntimeStateRestoreQueuesRecoveredHeavyTaskRequests(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "telegram-state.json")
	state := telegramPersistedState{
		Version: telegramStateVersion,
		Requests: []telegramPersistedRequest{
			{
				RequestID:    "req-cover-1",
				ChatID:       202,
				ReplyToID:    5,
				Single:       true,
				TaskType:     telegramTaskCover,
				Settings:     ChatDownloadSettings{Format: telegramFormatAlac, SettingsInited: true},
				TransferMode: transferModeOneByOne,
				MediaType:    mediaTypeAlbum,
				MediaID:      "album-123",
				Storefront:   "us",
				State:        "queued",
				UpdatedAt:    time.Now(),
			},
		},
	}
	payload, err := jsonMarshalIndentForTest(state)
	if err != nil {
		t.Fatalf("marshal state failed: %v", err)
	}
	if err := os.WriteFile(statePath, payload, 0644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	b := &TelegramBot{
		stateFile:          statePath,
		appleToken:         "token",
		pending:            make(map[int64]map[int]*PendingSelection),
		pendingTransfers:   make(map[int64]map[int]*PendingTransfer),
		pendingArtistModes: make(map[int64]map[int]*PendingArtistMode),
		chatSettings:       make(map[int64]ChatDownloadSettings),
		inflightDownloads:  make(map[string]struct{}),
		activeRequests:     make(map[string]telegramPersistedRequest),
		downloadQueue:      make(chan *downloadRequest, 2),
		stateSave:          make(chan struct{}, 1),
	}
	b.restoreRuntimeState()

	if len(b.downloadQueue) != 1 {
		t.Fatalf("expected recovered heavy request in queue, got %d", len(b.downloadQueue))
	}
	req := <-b.downloadQueue
	if req == nil {
		t.Fatalf("expected non-nil request")
	}
	if req.taskType != telegramTaskCover {
		t.Fatalf("expected recovered cover task, got %q", req.taskType)
	}
	if req.mediaType != mediaTypeAlbum || req.mediaID != "album-123" {
		t.Fatalf("unexpected recovered heavy request: %+v", req)
	}
	if req.run == nil {
		t.Fatalf("expected recovered heavy request runner to be rebuilt")
	}
}

func TestTelegramResourceGuardLowDisk(t *testing.T) {
	guard := &telegramResourceGuard{
		minDiskFreeBytes: 100,
		roots:            []string{"/data"},
		diskFreeFn: func(path string) (int64, error) {
			return 80, nil
		},
		memoryFn: func() uint64 { return 10 },
	}
	guard.evaluate()
	blocked, reason := guard.snapshot()
	if !blocked {
		t.Fatalf("expected guard to block")
	}
	if !strings.Contains(reason, "low disk space") {
		t.Fatalf("unexpected reason: %s", reason)
	}
}

func TestRunExternalCommandTimeoutMetrics(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep command not available")
	}
	before := appRuntimeMetrics.snapshot().ExternalCmdTimeouts
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _ = runExternalCommand(ctx, "sleep", "1")
	after := appRuntimeMetrics.snapshot().ExternalCmdTimeouts
	if after <= before {
		t.Fatalf("expected external timeout metrics to increase")
	}
}

func TestRuntimeMetricsTaskTypeLifecycle(t *testing.T) {
	metrics := &runtimeMetrics{}

	metrics.recordTaskQueued(telegramTaskCover)
	metrics.recordTaskStarted(telegramTaskCover)
	metrics.recordTaskFinished(telegramTaskCover)
	metrics.recordTaskPanic(telegramTaskCover)

	snapshot := metrics.snapshot()
	taskMetrics, ok := snapshot.TaskTypes[telegramTaskCover]
	if !ok {
		t.Fatalf("expected cover task metrics in snapshot")
	}
	if taskMetrics.QueuedTotal != 1 || taskMetrics.StartedTotal != 1 || taskMetrics.FinishedTotal != 1 || taskMetrics.PanicTotal != 1 {
		t.Fatalf("unexpected task lifecycle metrics: %#v", taskMetrics)
	}
}

func TestRuntimeMetricsQueueFullDrop(t *testing.T) {
	metrics := &runtimeMetrics{}
	metrics.recordQueueFullDrop()
	metrics.recordQueueFullDrop()

	snapshot := metrics.snapshot()
	if snapshot.QueueFullDrops != 2 {
		t.Fatalf("expected queue full drops=2, got %d", snapshot.QueueFullDrops)
	}
}

func TestTrackedRequestStatsByType(t *testing.T) {
	b := &TelegramBot{
		activeRequests: map[string]telegramPersistedRequest{
			"req-download": {TaskType: telegramTaskDownload, State: "queued"},
			"req-cover":    {TaskType: telegramTaskCover, State: "running"},
			"req-lyrics":   {TaskType: telegramTaskSongLyrics, State: "queued"},
		},
	}

	stats := b.trackedRequestStatsByType()

	if got := stats[telegramTaskDownload].QueuedCurrent; got != 1 {
		t.Fatalf("expected download queued current=1, got %d", got)
	}
	if got := stats[telegramTaskCover].RunningCurrent; got != 1 {
		t.Fatalf("expected cover running current=1, got %d", got)
	}
	if got := stats[telegramTaskSongLyrics].QueuedCurrent; got != 1 {
		t.Fatalf("expected song-lyrics queued current=1, got %d", got)
	}
}

func TestServeMetricsHTTP(t *testing.T) {
	oldMetrics := appRuntimeMetrics
	appRuntimeMetrics = &runtimeMetrics{}
	defer func() {
		appRuntimeMetrics = oldMetrics
	}()

	appRuntimeMetrics.recordUploadSuccess()
	appRuntimeMetrics.recordUploadFailure()
	appRuntimeMetrics.recordTelegramRetryAfter()
	appRuntimeMetrics.recordExternalCommandTimeout()
	appRuntimeMetrics.recordCleanupRemoval(2048)
	appRuntimeMetrics.recordQueueFullDrop()
	appRuntimeMetrics.recordTaskQueued(telegramTaskCover)
	appRuntimeMetrics.recordTaskStarted(telegramTaskCover)
	appRuntimeMetrics.recordTaskFinished(telegramTaskCover)

	b := &TelegramBot{
		downloadQueue: make(chan *downloadRequest, 4),
		workerLimit:   2,
		activeWorkers: 1,
		inflightDownloads: map[string]struct{}{
			"inflight-1": {},
		},
		activeRequests: map[string]telegramPersistedRequest{
			"req-cover-queued":  {TaskType: telegramTaskCover, State: "queued"},
			"req-cover-running": {TaskType: telegramTaskCover, State: "running"},
		},
		shutdownCtx: context.Background(),
	}
	b.downloadQueue <- &downloadRequest{requestID: "queued-1"}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	b.serveMetricsHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"amdl_telegram_queue_current 1",
		"amdl_telegram_workers_active 1",
		"amdl_telegram_workers_limit 2",
		"amdl_telegram_upload_success_total 1",
		"amdl_telegram_upload_failure_total 1",
		"amdl_telegram_queue_full_drop_total 1",
		`amdl_telegram_task_queued_current{task_type="cover"} 1`,
		`amdl_telegram_task_running_current{task_type="cover"} 1`,
		`amdl_telegram_task_enqueued_total{task_type="cover"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics body to contain %q, got:\n%s", want, body)
		}
	}
}

func TestServeHealthzHTTPBlocked(t *testing.T) {
	guard := &telegramResourceGuard{}
	guard.set(true, "low disk space")
	b := &TelegramBot{
		resourceGuard: guard,
		shutdownCtx:   context.Background(),
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	b.serveHealthzHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", recorder.Code)
	}

	var response telegramHealthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode health response: %v", err)
	}
	if response.OK {
		t.Fatalf("expected health response to be unhealthy")
	}
	if !response.ResourceBlocked || response.ResourceReason != "low disk space" {
		t.Fatalf("unexpected health response: %+v", response)
	}
}

func TestStartMetricsHTTPServer(t *testing.T) {
	oldConfig := Config
	oldMetrics := appRuntimeMetrics
	Config.TelegramMetricsListenAddr = "127.0.0.1:0"
	appRuntimeMetrics = &runtimeMetrics{}
	defer func() {
		Config = oldConfig
		appRuntimeMetrics = oldMetrics
	}()

	b := &TelegramBot{
		downloadQueue:     make(chan *downloadRequest, 1),
		inflightDownloads: make(map[string]struct{}),
		activeRequests:    make(map[string]telegramPersistedRequest),
		shutdownCtx:       context.Background(),
	}

	b.startMetricsHTTPServer()
	defer b.stopMetricsHTTPServer()

	if strings.TrimSpace(b.metricsHTTPAddr) == "" {
		t.Fatalf("expected metrics HTTP server address")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + b.metricsHTTPAddr + "/healthz")
	if err != nil {
		t.Fatalf("failed to call metrics health endpoint: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected health status 200, got %d", response.StatusCode)
	}
}

func TestSubscriptionSnapshotRestoreRoundTrip(t *testing.T) {
	now := time.Now()
	source := &TelegramBot{
		subscriptions: map[string]telegramSubscription{
			"sub-1": {
				ID:             "sub-1",
				Kind:           telegramSubscriptionKindArtist,
				TargetID:       "artist-1",
				Storefront:     "us",
				Title:          "Artist 1",
				CreatedBy:      1001,
				DeliveryChatID: 2001,
				CreatedAt:      now,
				LastCheckAt:    now,
				LastSeenAlbum:  "album-1",
				Enabled:        false,
			},
		},
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-1": {
				ID:                "tmp-1",
				SubscriptionID:    "sub-1",
				AlbumID:           "album-1",
				Storefront:        "us",
				Title:             "Album 1",
				ArtistName:        "Artist 1",
				DeliveryChatID:    2001,
				ArchiveChatID:     -10001,
				DiscoveredAt:      now,
				TemporarySentAt:   now,
				RefreshEligibleAt: now.Add(defaultTelegramTemporaryReleaseWindow),
			},
		},
	}

	target := &TelegramBot{}
	target.restoreSubscriptions(source.snapshotSubscriptions(), source.snapshotTemporaryReleases())

	items := target.listSubscriptions()
	if len(items) != 1 || items[0].ID != "sub-1" || items[0].LastSeenAlbum != "album-1" || items[0].Enabled {
		t.Fatalf("unexpected restored subscriptions: %+v", items)
	}
	if got := target.temporaryReleases["tmp-1"].AlbumID; got != "album-1" {
		t.Fatalf("unexpected restored temporary release album id: %s", got)
	}
}

func TestMarkTemporaryReleaseSentSetsWindow(t *testing.T) {
	b := &TelegramBot{
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-1": {
				ID:             "tmp-1",
				SubscriptionID: "sub-1",
				AlbumID:        "album-1",
			},
		},
	}

	b.markTemporaryReleaseSent("tmp-1")
	release := b.temporaryReleases["tmp-1"]
	if release.TemporarySentAt.IsZero() {
		t.Fatalf("expected temporary sent time to be set")
	}
	if release.RefreshEligibleAt.Sub(release.TemporarySentAt) != defaultTelegramTemporaryReleaseWindow {
		t.Fatalf("unexpected refresh eligible window: %s", release.RefreshEligibleAt.Sub(release.TemporarySentAt))
	}
}

func TestFormatSubscriptionListText(t *testing.T) {
	b := &TelegramBot{
		subscriptions: map[string]telegramSubscription{
			"sub-1": {
				ID:             "sub-1",
				Kind:           telegramSubscriptionKindArtist,
				TargetID:       "artist-1",
				Storefront:     "us",
				Title:          "Artist 1",
				DeliveryChatID: 12345,
				CreatedAt:      time.Unix(100, 0),
				LastCheckAt:    time.Now().Add(-2 * time.Hour),
				LastSeenAlbum:  "album-1",
				Enabled:        true,
			},
		},
	}

	text := b.formatSubscriptionListText(subscriptionListFilterAll)
	for _, want := range []string{"当前订阅：", "summary: total=1 enabled=1 paused=0 matched=1", "sub-1", "Artist 1", "artist-1", "album-1", "state=enabled", "checked="} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected subscription list to contain %q, got %s", want, text)
		}
	}
}

func TestTemporaryReleaseRefreshState(t *testing.T) {
	now := time.Now()
	if got := temporaryReleaseRefreshState(telegramTemporaryRelease{}, now); got != "temporary-pending" {
		t.Fatalf("expected temporary-pending, got %s", got)
	}
	if got := temporaryReleaseRefreshState(telegramTemporaryRelease{
		TemporarySentAt:   now.Add(-time.Hour),
		RefreshEligibleAt: now.Add(time.Hour),
	}, now); got != "waiting-window" {
		t.Fatalf("expected waiting-window, got %s", got)
	}
	if got := temporaryReleaseRefreshState(telegramTemporaryRelease{
		TemporarySentAt:   now.Add(-2 * time.Hour),
		RefreshEligibleAt: now.Add(-time.Minute),
	}, now); got != "refresh-ready" {
		t.Fatalf("expected refresh-ready, got %s", got)
	}
	if got := temporaryReleaseRefreshState(telegramTemporaryRelease{
		Refreshed: true,
	}, now); got != "refreshed" {
		t.Fatalf("expected refreshed, got %s", got)
	}
}

func TestMarkTemporaryReleaseRefreshed(t *testing.T) {
	b := &TelegramBot{
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-1": {
				ID:             "tmp-1",
				SubscriptionID: "sub-1",
				AlbumID:        "album-1",
			},
		},
	}

	b.markTemporaryReleaseRefreshed("tmp-1")
	release := b.temporaryReleases["tmp-1"]
	if !release.Refreshed {
		t.Fatalf("expected release to be marked refreshed")
	}
	if release.RefreshedAt.IsZero() {
		t.Fatalf("expected refreshed at to be set")
	}
}

func TestFormatTemporaryReleaseListText(t *testing.T) {
	now := time.Now()
	b := &TelegramBot{
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-1": {
				ID:                "tmp-1",
				SubscriptionID:    "sub-1",
				AlbumID:           "album-1",
				Title:             "Album 1",
				DiscoveredAt:      now,
				TemporarySentAt:   now,
				RefreshEligibleAt: now.Add(defaultTelegramTemporaryReleaseWindow),
			},
		},
	}

	text := b.formatTemporaryReleaseListText(temporaryReleaseFilterAll, "", "")
	for _, want := range []string{"临时订阅发布记录", "summary: total=1 pending=1 ready=0 refreshed=0 matched=1", "tmp-1", "Album 1", "album-1", "waiting-window", "remaining="} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected temporary release list to contain %q, got %s", want, text)
		}
	}
}

func TestTemporaryReleaseMatchesFilter(t *testing.T) {
	cases := []struct {
		status string
		filter temporaryReleaseFilter
		want   bool
	}{
		{status: "temporary-pending", filter: temporaryReleaseFilterPending, want: true},
		{status: "waiting-window", filter: temporaryReleaseFilterPending, want: true},
		{status: "refresh-ready", filter: temporaryReleaseFilterReady, want: true},
		{status: "refreshed", filter: temporaryReleaseFilterRefreshed, want: true},
		{status: "refreshed", filter: temporaryReleaseFilterReady, want: false},
	}
	for _, tc := range cases {
		if got := temporaryReleaseMatchesFilter(tc.status, tc.filter); got != tc.want {
			t.Fatalf("status=%s filter=%s want=%t got=%t", tc.status, tc.filter, tc.want, got)
		}
	}
}

func TestSetSubscriptionEnabled(t *testing.T) {
	b := &TelegramBot{
		subscriptions: map[string]telegramSubscription{
			"sub-1": {
				ID:      "sub-1",
				Enabled: true,
			},
		},
	}
	item, changed, ok := b.setSubscriptionEnabled("sub-1", false)
	if !ok || !changed || item.Enabled {
		t.Fatalf("expected subscription to be paused: ok=%t changed=%t item=%+v", ok, changed, item)
	}
	if b.subscriptions["sub-1"].Enabled {
		t.Fatalf("expected subscription enabled=false after pause")
	}
}

func TestShouldDeliverTemporaryReleaseToArchiveDedupesByEarliest(t *testing.T) {
	now := time.Now()
	b := &TelegramBot{
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-1": {
				ID:            "tmp-1",
				AlbumID:       "album-1",
				Storefront:    "us",
				ArchiveChatID: -1001,
				DiscoveredAt:  now.Add(-time.Minute),
			},
			"tmp-2": {
				ID:            "tmp-2",
				AlbumID:       "album-1",
				Storefront:    "us",
				ArchiveChatID: -1001,
				DiscoveredAt:  now,
			},
		},
	}
	if !b.shouldDeliverTemporaryReleaseToArchive(b.temporaryReleases["tmp-1"]) {
		t.Fatalf("expected earliest release to be canonical for archive delivery")
	}
	if b.shouldDeliverTemporaryReleaseToArchive(b.temporaryReleases["tmp-2"]) {
		t.Fatalf("expected later duplicate release to be skipped for archive delivery")
	}
}

func TestFormatTemporaryReleaseListTextFilterPending(t *testing.T) {
	now := time.Now()
	b := &TelegramBot{
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-pending": {
				ID:                "tmp-pending",
				AlbumID:           "album-pending",
				DiscoveredAt:      now,
				TemporarySentAt:   now,
				RefreshEligibleAt: now.Add(time.Hour),
			},
			"tmp-ready": {
				ID:                "tmp-ready",
				AlbumID:           "album-ready",
				DiscoveredAt:      now.Add(time.Minute),
				TemporarySentAt:   now.Add(-2 * time.Hour),
				RefreshEligibleAt: now.Add(-time.Minute),
			},
		},
	}
	text := b.formatTemporaryReleaseListText(temporaryReleaseFilterPending, "", "")
	if !strings.Contains(text, "tmp-pending") {
		t.Fatalf("expected pending list to contain tmp-pending, got %s", text)
	}
	if strings.Contains(text, "tmp-ready") {
		t.Fatalf("expected pending list to exclude tmp-ready, got %s", text)
	}
}

func TestFormatTemporaryReleaseListTextFilterReady(t *testing.T) {
	now := time.Now()
	b := &TelegramBot{
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-ready": {
				ID:                "tmp-ready",
				AlbumID:           "album-ready",
				Title:             "Album Ready",
				ArtistName:        "Artist Ready",
				DiscoveredAt:      now,
				TemporarySentAt:   now.Add(-3 * time.Hour),
				RefreshEligibleAt: now.Add(-2 * time.Hour),
			},
		},
	}
	text := b.formatTemporaryReleaseListText(temporaryReleaseFilterReady, "", "")
	for _, want := range []string{"summary: total=1 pending=0 ready=1 refreshed=0 matched=1", "tmp-ready", "ready_for=", "artist=Artist Ready"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected ready list to contain %q, got %s", want, text)
		}
	}
}

func TestFormatSubscriptionRemaining(t *testing.T) {
	if got := formatSubscriptionRemaining(26*time.Hour + 30*time.Minute); got != "1d2h" {
		t.Fatalf("unexpected remaining format: %s", got)
	}
	if got := formatSubscriptionRemaining(95 * time.Minute); got != "1h35m" {
		t.Fatalf("unexpected remaining format: %s", got)
	}
	if got := formatSubscriptionRemaining(20 * time.Minute); got != "20m" {
		t.Fatalf("unexpected remaining format: %s", got)
	}
}

func TestNormalizeSubscriptionListFilter(t *testing.T) {
	if got, ok := normalizeSubscriptionListFilter("enabled"); !ok || got != subscriptionListFilterEnabled {
		t.Fatalf("expected enabled filter, got %q ok=%t", got, ok)
	}
	if got, ok := normalizeSubscriptionListFilter("paused"); !ok || got != subscriptionListFilterPaused {
		t.Fatalf("expected paused filter, got %q ok=%t", got, ok)
	}
	if _, ok := normalizeSubscriptionListFilter("weird"); ok {
		t.Fatalf("expected invalid filter to fail")
	}
}

func TestParseTemporaryReleaseListArgs(t *testing.T) {
	filter, kind, value, ok := parseTemporaryReleaseListArgs([]string{"artist", "Taylor"})
	if !ok || filter != temporaryReleaseFilterAll || kind != "artist" || value != "Taylor" {
		t.Fatalf("unexpected artist parse result: filter=%q kind=%q value=%q ok=%t", filter, kind, value, ok)
	}
	filter, kind, value, ok = parseTemporaryReleaseListArgs([]string{"album", "album-1"})
	if !ok || kind != "album" || value != "album-1" {
		t.Fatalf("unexpected album parse result: filter=%q kind=%q value=%q ok=%t", filter, kind, value, ok)
	}
	if _, _, _, ok = parseTemporaryReleaseListArgs([]string{"artist"}); ok {
		t.Fatalf("expected missing artist query to fail")
	}
}

func TestFormatSubscriptionListTextFilterPaused(t *testing.T) {
	b := &TelegramBot{
		subscriptions: map[string]telegramSubscription{
			"sub-on":  {ID: "sub-on", Title: "On", Enabled: true, CreatedAt: time.Unix(1, 0)},
			"sub-off": {ID: "sub-off", Title: "Off", Enabled: false, CreatedAt: time.Unix(2, 0)},
		},
	}
	text := b.formatSubscriptionListText(subscriptionListFilterPaused)
	for _, want := range []string{"summary: total=2 enabled=1 paused=1 matched=1", "sub-off"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected paused list to contain %q, got %s", want, text)
		}
	}
	if strings.Contains(text, "sub-on") {
		t.Fatalf("expected paused list to exclude sub-on, got %s", text)
	}
}

func TestFormatTemporaryReleaseListTextArtistFilter(t *testing.T) {
	now := time.Now()
	b := &TelegramBot{
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-1": {ID: "tmp-1", Title: "Album A", ArtistName: "Artist A", AlbumID: "album-a", DiscoveredAt: now},
			"tmp-2": {ID: "tmp-2", Title: "Album B", ArtistName: "Artist B", AlbumID: "album-b", DiscoveredAt: now.Add(time.Minute)},
		},
	}
	text := b.formatTemporaryReleaseListText(temporaryReleaseFilterAll, "artist", "artist a")
	for _, want := range []string{"matched=1", "tmp-1", "match_artist=artist a"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected artist filtered list to contain %q, got %s", want, text)
		}
	}
	if strings.Contains(text, "tmp-2") {
		t.Fatalf("expected artist filtered list to exclude tmp-2, got %s", text)
	}
}

func TestFormatTemporaryReleaseListTextAlbumFilter(t *testing.T) {
	now := time.Now()
	b := &TelegramBot{
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-1": {ID: "tmp-1", Title: "Album A", ArtistName: "Artist A", AlbumID: "album-a", DiscoveredAt: now},
			"tmp-2": {ID: "tmp-2", Title: "Album B", ArtistName: "Artist B", AlbumID: "album-b", DiscoveredAt: now.Add(time.Minute)},
		},
	}
	text := b.formatTemporaryReleaseListText(temporaryReleaseFilterAll, "album", "album-b")
	for _, want := range []string{"matched=1", "tmp-2", "match_album=album-b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected album filtered list to contain %q, got %s", want, text)
		}
	}
	if strings.Contains(text, "tmp-1") {
		t.Fatalf("expected album filtered list to exclude tmp-1, got %s", text)
	}
}

func TestTriggerTemporaryReleaseRefreshRejectsNotEligible(t *testing.T) {
	now := time.Now()
	b := &TelegramBot{
		subscriptions: map[string]telegramSubscription{
			"sub-1": {
				ID:             "sub-1",
				Kind:           telegramSubscriptionKindArtist,
				TargetID:       "artist-1",
				Storefront:     "us",
				Title:          "Artist 1",
				DeliveryChatID: 123,
				Enabled:        true,
			},
		},
		temporaryReleases: map[string]telegramTemporaryRelease{
			"tmp-1": {
				ID:                "tmp-1",
				SubscriptionID:    "sub-1",
				AlbumID:           "album-1",
				Storefront:        "us",
				TemporarySentAt:   now.Add(-time.Hour),
				RefreshEligibleAt: now.Add(time.Hour),
			},
		},
	}

	_, err := b.triggerTemporaryReleaseRefresh(b.temporaryReleases["tmp-1"])
	if err == nil || !strings.Contains(err.Error(), "not eligible") {
		t.Fatalf("expected not eligible error, got %v", err)
	}
}

func TestRunWithRecoveryRecoversPanic(t *testing.T) {
	var callbackErr error
	panicked := runWithRecovery("test panic", func(err error) {
		callbackErr = err
	}, func() {
		panic("boom")
	})
	if !panicked {
		t.Fatalf("expected panic to be recovered")
	}
	if callbackErr == nil {
		t.Fatalf("expected panic callback error")
	}
	if !strings.Contains(callbackErr.Error(), "test panic panic: boom") {
		t.Fatalf("unexpected callback error: %v", callbackErr)
	}
}

func TestDownloadWorkerContinuesAfterTaskPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/sendMessage"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer server.Close()

	b := &TelegramBot{
		token:             "test",
		apiBase:           server.URL,
		client:            server.Client(),
		pollClient:        server.Client(),
		downloadQueue:     make(chan *downloadRequest, 4),
		workerLimit:       1,
		inflightDownloads: make(map[string]struct{}),
		activeRequests:    make(map[string]telegramPersistedRequest),
	}
	b.queueCond = sync.NewCond(&b.queueMu)
	b.startDownloadWorker()

	done := make(chan struct{})
	b.downloadQueue <- &downloadRequest{
		chatID:    1,
		replyToID: 11,
		requestID: "panic-task",
		mediaType: mediaTypeSong,
		mediaID:   "song-panic",
		single:    true,
		settings:  normalizeChatSettings(ChatDownloadSettings{}),
		fn: func(session *DownloadSession) error {
			panic("task panic")
		},
	}
	b.downloadQueue <- &downloadRequest{
		chatID:    1,
		replyToID: 12,
		requestID: "next-task",
		mediaType: mediaTypeSong,
		mediaID:   "song-next",
		single:    true,
		settings:  normalizeChatSettings(ChatDownloadSettings{}),
		fn: func(session *DownloadSession) error {
			close(done)
			return nil
		},
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("second task did not run after first task panic")
	}
	b.stopDownloadWorkers()
}

func TestTaskWorkerContinuesAfterHeavyTaskPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/sendMessage"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer server.Close()

	b := &TelegramBot{
		token:             "test",
		apiBase:           server.URL,
		client:            server.Client(),
		pollClient:        server.Client(),
		downloadQueue:     make(chan *downloadRequest, 4),
		workerLimit:       1,
		inflightDownloads: make(map[string]struct{}),
		activeRequests:    make(map[string]telegramPersistedRequest),
	}
	b.queueCond = sync.NewCond(&b.queueMu)
	b.startDownloadWorker()

	done := make(chan struct{})
	b.downloadQueue <- &downloadRequest{
		chatID:    1,
		replyToID: 21,
		requestID: "panic-heavy-task",
		taskType:  telegramTaskCover,
		mediaType: mediaTypeAlbum,
		mediaID:   "album-panic",
		run: func(*TelegramBot, context.Context) error {
			panic("heavy task panic")
		},
	}
	b.downloadQueue <- &downloadRequest{
		chatID:    1,
		replyToID: 22,
		requestID: "next-heavy-task",
		taskType:  telegramTaskCover,
		mediaType: mediaTypeAlbum,
		mediaID:   "album-next",
		run: func(*TelegramBot, context.Context) error {
			close(done)
			return nil
		},
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("second heavy task did not run after first task panic")
	}
	b.stopDownloadWorkers()
}

func TestStopStateSaverFlushesLatestState(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "telegram-state.json")
	b := &TelegramBot{
		stateFile:          statePath,
		pending:            make(map[int64]map[int]*PendingSelection),
		pendingTransfers:   make(map[int64]map[int]*PendingTransfer),
		pendingArtistModes: make(map[int64]map[int]*PendingArtistMode),
		activeRequests: map[string]telegramPersistedRequest{
			"req-1": {RequestID: "req-1", InflightKey: "k1", MediaType: mediaTypeSong, MediaID: "song-1", Storefront: "us"},
		},
		inflightDownloads: map[string]struct{}{"k1": {}},
		chatSettings:      make(map[int64]ChatDownloadSettings),
	}

	b.startStateSaver()
	b.stopStateSaver()

	loaded, err := loadRuntimeStateFromFile(statePath)
	if err != nil {
		t.Fatalf("loadRuntimeStateFromFile failed: %v", err)
	}
	if len(loaded.Requests) != 1 || loaded.Requests[0].RequestID != "req-1" {
		t.Fatalf("expected latest request to be flushed, got %+v", loaded.Requests)
	}
}

func TestStopDownloadWorkersWaitsForRunningTask(t *testing.T) {
	b := &TelegramBot{
		downloadQueue:     make(chan *downloadRequest, 1),
		workerLimit:       1,
		inflightDownloads: make(map[string]struct{}),
		activeRequests:    make(map[string]telegramPersistedRequest),
	}
	b.queueCond = sync.NewCond(&b.queueMu)
	b.startDownloadWorker()

	done := make(chan struct{})
	release := make(chan struct{})
	b.downloadQueue <- &downloadRequest{
		requestID: "req-1",
		taskType:  telegramTaskCover,
		mediaType: mediaTypeAlbum,
		mediaID:   "album-1",
		run: func(bot *TelegramBot, ctx context.Context) error {
			close(done)
			<-release
			return nil
		},
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker did not start task")
	}

	stopped := make(chan struct{})
	go func() {
		b.stopDownloadWorkers()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatalf("stopDownloadWorkers returned before running task finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatalf("stopDownloadWorkers did not wait for task completion")
	}
}

func TestWaitBackgroundTasksWaitsForRunningTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := &TelegramBot{
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
	}
	started := make(chan struct{})
	release := make(chan struct{})
	if !b.launchBackgroundTask("test background task", func(ctx context.Context) {
		close(started)
		<-release
	}) {
		t.Fatalf("expected background task to launch")
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("background task did not start")
	}

	if b.waitBackgroundTasks(100 * time.Millisecond) {
		t.Fatalf("waitBackgroundTasks returned before background task completed")
	}

	close(release)

	if !b.waitBackgroundTasks(5 * time.Second) {
		t.Fatalf("waitBackgroundTasks did not observe background task completion")
	}
}

func TestLaunchBackgroundTaskRefusesAfterShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b := &TelegramBot{
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
	}
	if b.launchBackgroundTask("test background task", func(ctx context.Context) {}) {
		t.Fatalf("expected background task launch to be rejected after shutdown")
	}
}

func TestNextLocalMidnight(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, time.April, 8, 23, 59, 59, 0, loc)
	got := nextLocalMidnight(now)
	want := time.Date(2026, time.April, 9, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("nextLocalMidnight mismatch: got %s want %s", got, want)
	}
}

func TestTelegramDailyRestartEnabledDefaultsAndOverride(t *testing.T) {
	original := Config.TelegramDailyRestartEnabled
	defer func() {
		Config.TelegramDailyRestartEnabled = original
	}()

	Config.TelegramDailyRestartEnabled = nil
	if !telegramDailyRestartEnabled() {
		t.Fatalf("expected daily restart enabled by default")
	}

	disabled := false
	Config.TelegramDailyRestartEnabled = &disabled
	if telegramDailyRestartEnabled() {
		t.Fatalf("expected daily restart disabled when configured false")
	}

	enabled := true
	Config.TelegramDailyRestartEnabled = &enabled
	if !telegramDailyRestartEnabled() {
		t.Fatalf("expected daily restart enabled when configured true")
	}
}

func TestGetUpdatesBackoffUsesRetryAfter(t *testing.T) {
	b := &TelegramBot{
		getUpdatesErrorDelay:    2 * time.Second,
		getUpdatesConflictDelay: 5 * time.Second,
	}
	backoff, reason := b.getUpdatesBackoff(errors.New(`getUpdates failed: 429 Too Many Requests (Too Many Requests: retry after 5)`))
	if reason != "retry_after" {
		t.Fatalf("expected retry_after reason, got %q", reason)
	}
	if backoff != 5*time.Second {
		t.Fatalf("expected 5s backoff, got %s", backoff)
	}
}

func TestGetUpdatesBackoffUsesConflictDelay(t *testing.T) {
	b := &TelegramBot{
		getUpdatesErrorDelay:    2 * time.Second,
		getUpdatesConflictDelay: 5 * time.Second,
	}
	backoff, reason := b.getUpdatesBackoff(errors.New(`getUpdates failed: 409 Conflict (terminated by other getUpdates request)`))
	if reason != "conflict" {
		t.Fatalf("expected conflict reason, got %q", reason)
	}
	if backoff != 5*time.Second {
		t.Fatalf("expected conflict backoff 5s, got %s", backoff)
	}
}

func TestDailyRestartTaskLoadPendingWork(t *testing.T) {
	b := &TelegramBot{
		downloadQueue:     make(chan *downloadRequest, 3),
		workerLimit:       2,
		inflightDownloads: make(map[string]struct{}),
		activeRequests:    make(map[string]telegramPersistedRequest),
	}
	b.downloadQueue <- &downloadRequest{requestID: "queued-1"}
	b.activeWorkers = 1
	b.inflightDownloads["song:1"] = struct{}{}
	b.activeRequests["req-1"] = telegramPersistedRequest{RequestID: "req-1", State: "running"}

	load := b.dailyRestartTaskLoad()

	if !load.hasPendingWork() {
		t.Fatalf("expected pending work for daily restart, got %+v", load)
	}
	if load.Queued != 1 || load.Active != 1 || load.Limit != 2 || load.Inflight != 1 || load.Tracked != 1 {
		t.Fatalf("unexpected daily restart task load: %+v", load)
	}
	if got, want := load.String(), "queue=1 active=1/2 inflight=1 tracked=1"; got != want {
		t.Fatalf("task load string mismatch: got %q want %q", got, want)
	}
}

func TestDailyRestartTaskLoadIdle(t *testing.T) {
	b := &TelegramBot{
		downloadQueue:     make(chan *downloadRequest, 1),
		workerLimit:       1,
		inflightDownloads: make(map[string]struct{}),
		activeRequests:    make(map[string]telegramPersistedRequest),
	}

	load := b.dailyRestartTaskLoad()

	if load.hasPendingWork() {
		t.Fatalf("expected idle load, got %+v", load)
	}
}

func TestCancelChatRequestCancelsRunningRequest(t *testing.T) {
	canceled := make(chan struct{}, 1)
	b := &TelegramBot{
		activeRequests: map[string]telegramPersistedRequest{
			"req-running": {RequestID: "req-running", ChatID: 42, State: "running", UpdatedAt: time.Now()},
		},
		runningRequestCtx: make(map[string]context.CancelFunc),
	}
	b.runningRequestCtx["req-running"] = func() {
		select {
		case canceled <- struct{}{}:
		default:
		}
	}

	state, ok := b.cancelChatRequest(42, "req-running")
	if !ok {
		t.Fatalf("expected running request to be cancelable")
	}
	if state != "running" {
		t.Fatalf("expected running state, got %q", state)
	}
	select {
	case <-canceled:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected running request cancel func to be invoked")
	}
}

func TestWorkerSkipsCanceledQueuedRequest(t *testing.T) {
	var ran bool
	b := &TelegramBot{
		downloadQueue:      make(chan *downloadRequest, 1),
		workerLimit:        1,
		activeRequests:     map[string]telegramPersistedRequest{"req-1": {RequestID: "req-1", ChatID: 42, State: "queued", MediaType: mediaTypeSong, MediaID: "song-1"}},
		inflightDownloads:  map[string]struct{}{"song-1": {}},
		canceledRequestIDs: map[string]struct{}{"req-1": {}},
		runningRequestCtx:  make(map[string]context.CancelFunc),
	}
	b.queueCond = sync.NewCond(&b.queueMu)
	b.startDownloadWorker()
	b.downloadQueue <- &downloadRequest{
		requestID:   "req-1",
		taskType:    telegramTaskCover,
		mediaType:   mediaTypeSong,
		mediaID:     "song-1",
		inflightKey: "song-1",
		run: func(*TelegramBot, context.Context) error {
			ran = true
			return nil
		},
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if len(b.chatTrackedRequests(42)) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected canceled queued request to be untracked")
		}
		time.Sleep(10 * time.Millisecond)
	}

	b.stopDownloadWorkers()

	if ran {
		t.Fatalf("expected canceled request runner to be skipped")
	}
	b.inflightMu.Lock()
	_, ok := b.inflightDownloads["song-1"]
	b.inflightMu.Unlock()
	if ok {
		t.Fatalf("expected inflight lock to be released")
	}
}

func TestResolveTelegramErrorLogFile(t *testing.T) {
	t.Setenv("AMDL_TELEGRAM_ERROR_LOG_FILE", "")
	cacheFile := filepath.Join(string(filepath.Separator), "tmp", "telegram-cache.json")
	stateFile := filepath.Join(string(filepath.Separator), "tmp", "state", "telegram.state.json")
	got := resolveTelegramErrorLogFile(cacheFile, stateFile)
	want := filepath.Clean(filepath.Join(filepath.Dir(stateFile), defaultTelegramErrorLogFile))
	if got != want {
		t.Fatalf("resolveTelegramErrorLogFile mismatch: got %q want %q", got, want)
	}
}

func TestResolveTelegramErrorLogFileRespectsEnv(t *testing.T) {
	custom := filepath.Join("custom", "telegram-errors.log")
	t.Setenv("AMDL_TELEGRAM_ERROR_LOG_FILE", custom)
	got := resolveTelegramErrorLogFile("telegram-cache.json", "")
	if got != filepath.Clean(custom) {
		t.Fatalf("expected env override %q, got %q", filepath.Clean(custom), got)
	}
}

func TestAppendRuntimeErrorLogWritesFile(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "telegram-error.log")
	oldPath := currentRuntimeErrorLogPath()
	setRuntimeErrorLogPath(logPath)
	defer setRuntimeErrorLogPath(oldPath)

	appendRuntimeErrorLog("runtime failure")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read runtime error log failed: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "runtime failure") {
		t.Fatalf("expected runtime failure in log content, got %q", text)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat runtime error log failed: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("expected private log permissions, got %o", info.Mode().Perm())
	}
}

func TestTelegramLoopReturnsAfterConsecutiveGetUpdatesErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &TelegramBot{
		token:                   "test-token",
		apiBase:                 "http://127.0.0.1:1",
		client:                  &http.Client{Timeout: 30 * time.Millisecond},
		pollClient:              &http.Client{Timeout: 30 * time.Millisecond},
		shutdownCtx:             ctx,
		shutdownCancel:          cancel,
		getUpdatesErrorDelay:    1 * time.Millisecond,
		getUpdatesConflictDelay: 1 * time.Millisecond,
		getUpdatesRestartAfter:  3,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.loop()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("telegram loop did not stop after consecutive getUpdates errors")
	}
}

func jsonMarshalIndentForTest(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
