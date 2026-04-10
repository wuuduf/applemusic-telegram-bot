package app

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type runtimeMetrics struct {
	uploadSuccesses       atomic.Uint64
	uploadFailures        atomic.Uint64
	telegramRetryAfterHit atomic.Uint64
	externalCmdTimeouts   atomic.Uint64
	cleanupDeletedFiles   atomic.Uint64
	cleanupDeletedBytes   atomic.Int64

	taskMu     sync.Mutex
	taskTotals map[string]taskTypeLifecycleTotals
}

type runtimeMetricsSnapshot struct {
	UploadSuccesses     uint64
	UploadFailures      uint64
	TelegramRetryAfter  uint64
	ExternalCmdTimeouts uint64
	CleanupDeletedFiles uint64
	CleanupDeletedBytes int64
	TaskTypes           map[string]taskTypeLifecycleTotals
}

type taskTypeLifecycleTotals struct {
	QueuedTotal   uint64
	StartedTotal  uint64
	FinishedTotal uint64
	PanicTotal    uint64
}

type taskTypeCurrentStats struct {
	QueuedCurrent  int
	RunningCurrent int
}

type telegramTaskLoad struct {
	Queued   int
	Active   int
	Limit    int
	Inflight int
	Tracked  int
}

var appRuntimeMetrics = &runtimeMetrics{}

func (m *runtimeMetrics) recordUploadSuccess() {
	if m == nil {
		return
	}
	m.uploadSuccesses.Add(1)
}

func (m *runtimeMetrics) recordUploadFailure() {
	if m == nil {
		return
	}
	m.uploadFailures.Add(1)
}

func (m *runtimeMetrics) recordTelegramRetryAfter() {
	if m == nil {
		return
	}
	m.telegramRetryAfterHit.Add(1)
}

func (m *runtimeMetrics) recordExternalCommandTimeout() {
	if m == nil {
		return
	}
	m.externalCmdTimeouts.Add(1)
}

func (m *runtimeMetrics) recordCleanupRemoval(size int64) {
	if m == nil {
		return
	}
	m.cleanupDeletedFiles.Add(1)
	if size > 0 {
		m.cleanupDeletedBytes.Add(size)
	}
}

func (m *runtimeMetrics) recordTaskQueued(taskType string) {
	if m == nil {
		return
	}
	m.taskMu.Lock()
	defer m.taskMu.Unlock()
	if m.taskTotals == nil {
		m.taskTotals = make(map[string]taskTypeLifecycleTotals)
	}
	key := normalizeTelegramTaskType(taskType)
	total := m.taskTotals[key]
	total.QueuedTotal++
	m.taskTotals[key] = total
}

func (m *runtimeMetrics) recordTaskStarted(taskType string) {
	if m == nil {
		return
	}
	m.taskMu.Lock()
	defer m.taskMu.Unlock()
	if m.taskTotals == nil {
		m.taskTotals = make(map[string]taskTypeLifecycleTotals)
	}
	key := normalizeTelegramTaskType(taskType)
	total := m.taskTotals[key]
	total.StartedTotal++
	m.taskTotals[key] = total
}

func (m *runtimeMetrics) recordTaskFinished(taskType string) {
	if m == nil {
		return
	}
	m.taskMu.Lock()
	defer m.taskMu.Unlock()
	if m.taskTotals == nil {
		m.taskTotals = make(map[string]taskTypeLifecycleTotals)
	}
	key := normalizeTelegramTaskType(taskType)
	total := m.taskTotals[key]
	total.FinishedTotal++
	m.taskTotals[key] = total
}

func (m *runtimeMetrics) recordTaskPanic(taskType string) {
	if m == nil {
		return
	}
	m.taskMu.Lock()
	defer m.taskMu.Unlock()
	if m.taskTotals == nil {
		m.taskTotals = make(map[string]taskTypeLifecycleTotals)
	}
	key := normalizeTelegramTaskType(taskType)
	total := m.taskTotals[key]
	total.PanicTotal++
	m.taskTotals[key] = total
}

func (m *runtimeMetrics) snapshot() runtimeMetricsSnapshot {
	if m == nil {
		return runtimeMetricsSnapshot{}
	}
	snapshot := runtimeMetricsSnapshot{
		UploadSuccesses:     m.uploadSuccesses.Load(),
		UploadFailures:      m.uploadFailures.Load(),
		TelegramRetryAfter:  m.telegramRetryAfterHit.Load(),
		ExternalCmdTimeouts: m.externalCmdTimeouts.Load(),
		CleanupDeletedFiles: m.cleanupDeletedFiles.Load(),
		CleanupDeletedBytes: m.cleanupDeletedBytes.Load(),
	}
	m.taskMu.Lock()
	if len(m.taskTotals) > 0 {
		snapshot.TaskTypes = make(map[string]taskTypeLifecycleTotals, len(m.taskTotals))
		for taskType, totals := range m.taskTotals {
			snapshot.TaskTypes[taskType] = totals
		}
	}
	m.taskMu.Unlock()
	return snapshot
}

func telegramMetricsInterval() time.Duration {
	sec := Config.TelegramMetricsIntervalSec
	if sec <= 0 {
		return defaultTelegramMetricsInterval
	}
	return time.Duration(sec) * time.Second
}

func (b *TelegramBot) queueStats() (queued int, active int, limit int) {
	if b == nil {
		return 0, 0, 0
	}
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	return len(b.downloadQueue), b.activeWorkers, b.workerLimit
}

func (b *TelegramBot) trackedRequestCount() int {
	if b == nil {
		return 0
	}
	b.requestStateMu.Lock()
	defer b.requestStateMu.Unlock()
	return len(b.activeRequests)
}

func (b *TelegramBot) trackedRequestStatsByType() map[string]taskTypeCurrentStats {
	if b == nil {
		return nil
	}
	b.requestStateMu.Lock()
	defer b.requestStateMu.Unlock()
	if len(b.activeRequests) == 0 {
		return nil
	}
	stats := make(map[string]taskTypeCurrentStats)
	for _, request := range b.activeRequests {
		taskType := normalizeTelegramTaskType(request.TaskType)
		current := stats[taskType]
		switch strings.TrimSpace(request.State) {
		case "running":
			current.RunningCurrent++
		default:
			current.QueuedCurrent++
		}
		stats[taskType] = current
	}
	return stats
}

func (b *TelegramBot) inflightCount() int {
	if b == nil {
		return 0
	}
	b.inflightMu.Lock()
	defer b.inflightMu.Unlock()
	return len(b.inflightDownloads)
}

func (b *TelegramBot) dailyRestartTaskLoad() telegramTaskLoad {
	if b == nil {
		return telegramTaskLoad{}
	}
	queued, active, limit := b.queueStats()
	return telegramTaskLoad{
		Queued:   queued,
		Active:   active,
		Limit:    limit,
		Inflight: b.inflightCount(),
		Tracked:  b.trackedRequestCount(),
	}
}

func (l telegramTaskLoad) hasPendingWork() bool {
	return l.Queued > 0 || l.Active > 0 || l.Inflight > 0
}

func (l telegramTaskLoad) String() string {
	return fmt.Sprintf("queue=%d active=%d/%d inflight=%d tracked=%d", l.Queued, l.Active, l.Limit, l.Inflight, l.Tracked)
}

func orderedTaskTypes(taskTotals map[string]taskTypeLifecycleTotals, current map[string]taskTypeCurrentStats) []string {
	known := []string{
		telegramTaskDownload,
		telegramTaskCover,
		telegramTaskAnimatedCover,
		telegramTaskSongLyrics,
		telegramTaskAlbumLyrics,
		telegramTaskArtistAssets,
	}
	seen := make(map[string]struct{}, len(known))
	ordered := make([]string, 0, len(known))
	appendIfPresent := func(taskType string) {
		if _, ok := seen[taskType]; ok {
			return
		}
		if _, ok := taskTotals[taskType]; ok {
			seen[taskType] = struct{}{}
			ordered = append(ordered, taskType)
			return
		}
		if _, ok := current[taskType]; ok {
			seen[taskType] = struct{}{}
			ordered = append(ordered, taskType)
		}
	}
	for _, taskType := range known {
		appendIfPresent(taskType)
	}
	extras := make([]string, 0)
	for taskType := range taskTotals {
		if _, ok := seen[taskType]; !ok {
			extras = append(extras, taskType)
		}
	}
	for taskType := range current {
		if _, ok := seen[taskType]; !ok {
			extras = append(extras, taskType)
		}
	}
	sort.Strings(extras)
	for _, taskType := range extras {
		appendIfPresent(taskType)
	}
	return ordered
}

func formatTaskTypeMetrics(taskTotals map[string]taskTypeLifecycleTotals, current map[string]taskTypeCurrentStats) string {
	if len(taskTotals) == 0 && len(current) == 0 {
		return ""
	}
	parts := make([]string, 0)
	for _, taskType := range orderedTaskTypes(taskTotals, current) {
		total := taskTotals[taskType]
		now := current[taskType]
		parts = append(parts, fmt.Sprintf(
			"%s[q=%d r=%d enq=%d start=%d done=%d panic=%d]",
			taskType,
			now.QueuedCurrent,
			now.RunningCurrent,
			total.QueuedTotal,
			total.StartedTotal,
			total.FinishedTotal,
			total.PanicTotal,
		))
	}
	if len(parts) == 0 {
		return ""
	}
	return " tasks=" + strings.Join(parts, ",")
}

func (b *TelegramBot) startMetricsReporter() {
	if b == nil {
		return
	}
	interval := telegramMetricsInterval()
	if interval <= 0 {
		return
	}
	if b.metricsStop != nil {
		return
	}
	stopCh := make(chan struct{})
	b.metricsStop = stopCh
	b.metricsWG.Add(1)
	go func() {
		defer b.metricsWG.Done()
		runWithRecovery("telegram metrics reporter", nil, func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					runWithRecovery("telegram metrics tick", nil, func() {
						b.reportMetricsOnce()
					})
				case <-stopCh:
					return
				}
			}
		})
	}()
}

func (b *TelegramBot) stopMetricsReporter() {
	if b == nil || b.metricsStop == nil {
		return
	}
	stopCh := b.metricsStop
	close(stopCh)
	b.metricsWG.Wait()
	b.metricsStop = nil
}

func (b *TelegramBot) reportMetricsOnce() {
	if b == nil {
		return
	}
	queueLen, active, limit := b.queueStats()
	inflight := b.inflightCount()
	tracked := b.trackedRequestCount()
	taskCurrent := b.trackedRequestStatsByType()
	metrics := appRuntimeMetrics.snapshot()
	totalUploads := metrics.UploadSuccesses + metrics.UploadFailures
	failRate := 0.0
	if totalUploads > 0 {
		failRate = float64(metrics.UploadFailures) / float64(totalUploads) * 100
	}
	resourceState := ""
	if b.resourceGuard != nil {
		if blocked, reason := b.resourceGuard.snapshot(); blocked {
			resourceState = fmt.Sprintf(" resource_blocked=true reason=%q", reason)
		}
	}
	taskState := formatTaskTypeMetrics(metrics.TaskTypes, taskCurrent)
	fmt.Printf(
		"[metrics] queue=%d active=%d/%d inflight=%d tracked=%d uploads_ok=%d uploads_fail=%d upload_fail_rate=%.1f%% retry_after=%d cmd_timeout=%d cleanup_deleted=%d cleanup_bytes=%s%s%s\n",
		queueLen,
		active,
		limit,
		inflight,
		tracked,
		metrics.UploadSuccesses,
		metrics.UploadFailures,
		failRate,
		metrics.TelegramRetryAfter,
		metrics.ExternalCmdTimeouts,
		metrics.CleanupDeletedFiles,
		formatBytes(metrics.CleanupDeletedBytes),
		taskState,
		resourceState,
	)
	if totalUploads >= 5 && failRate >= 40 {
		fmt.Printf("[warn] upload failure rate is high: %.1f%% (%d/%d)\n", failRate, metrics.UploadFailures, totalUploads)
	}
}
