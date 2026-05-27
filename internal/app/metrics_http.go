package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

const defaultTelegramMetricsHTTPShutdownWait = 5 * time.Second

type telegramMetricsHTTPSnapshot struct {
	TakenAt         time.Time
	ShuttingDown    bool
	Queue           telegramTaskLoad
	Metrics         runtimeMetricsSnapshot
	TaskCurrent     map[string]taskTypeCurrentStats
	ResourceBlocked bool
	ResourceReason  string
}

type telegramHealthResponse struct {
	OK              bool                            `json:"ok"`
	TakenAt         string                          `json:"taken_at"`
	ShuttingDown    bool                            `json:"shutting_down"`
	ResourceBlocked bool                            `json:"resource_blocked"`
	ResourceReason  string                          `json:"resource_reason,omitempty"`
	Queue           telegramTaskLoad                `json:"queue"`
	Metrics         runtimeMetricsSnapshot          `json:"metrics"`
	TaskCurrent     map[string]taskTypeCurrentStats `json:"task_current,omitempty"`
}

func telegramMetricsListenAddr() string {
	return strings.TrimSpace(Config.TelegramMetricsListenAddr)
}

func (b *TelegramBot) collectMetricsHTTPSnapshot() telegramMetricsHTTPSnapshot {
	snapshot := telegramMetricsHTTPSnapshot{
		TakenAt:      time.Now(),
		ShuttingDown: b != nil && b.isShuttingDown(),
		Metrics:      appRuntimeMetrics.snapshot(),
	}
	if b == nil {
		return snapshot
	}
	snapshot.Queue = b.dailyRestartTaskLoad()
	snapshot.TaskCurrent = b.trackedRequestStatsByType()
	if b.resourceGuard != nil {
		snapshot.ResourceBlocked, snapshot.ResourceReason = b.resourceGuard.snapshot()
	}
	return snapshot
}

func (b *TelegramBot) serveMetricsHTTP(w http.ResponseWriter, _ *http.Request) {
	snapshot := b.collectMetricsHTTPSnapshot()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(formatPrometheusMetrics(snapshot)))
}

func (b *TelegramBot) serveHealthzHTTP(w http.ResponseWriter, _ *http.Request) {
	snapshot := b.collectMetricsHTTPSnapshot()
	resp := telegramHealthResponse{
		OK:              !snapshot.ShuttingDown && !snapshot.ResourceBlocked,
		TakenAt:         snapshot.TakenAt.Format(time.RFC3339),
		ShuttingDown:    snapshot.ShuttingDown,
		ResourceBlocked: snapshot.ResourceBlocked,
		ResourceReason:  snapshot.ResourceReason,
		Queue:           snapshot.Queue,
		Metrics:         snapshot.Metrics,
		TaskCurrent:     snapshot.TaskCurrent,
	}
	statusCode := http.StatusOK
	if !resp.OK {
		statusCode = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(resp)
}

func formatPrometheusMetrics(snapshot telegramMetricsHTTPSnapshot) string {
	var builder strings.Builder

	writePrometheusMetric(&builder, "amdl_telegram_shutting_down", nil, boolToFloat(snapshot.ShuttingDown))
	writePrometheusMetric(&builder, "amdl_telegram_resource_blocked", nil, boolToFloat(snapshot.ResourceBlocked))
	writePrometheusMetric(&builder, "amdl_telegram_queue_current", nil, snapshot.Queue.Queued)
	writePrometheusMetric(&builder, "amdl_telegram_workers_active", nil, snapshot.Queue.Active)
	writePrometheusMetric(&builder, "amdl_telegram_workers_limit", nil, snapshot.Queue.Limit)
	writePrometheusMetric(&builder, "amdl_telegram_inflight_downloads_current", nil, snapshot.Queue.Inflight)
	writePrometheusMetric(&builder, "amdl_telegram_tracked_requests_current", nil, snapshot.Queue.Tracked)
	writePrometheusMetric(&builder, "amdl_telegram_upload_success_total", nil, snapshot.Metrics.UploadSuccesses)
	writePrometheusMetric(&builder, "amdl_telegram_upload_failure_total", nil, snapshot.Metrics.UploadFailures)
	writePrometheusMetric(&builder, "amdl_telegram_retry_after_total", nil, snapshot.Metrics.TelegramRetryAfter)
	writePrometheusMetric(&builder, "amdl_telegram_external_command_timeout_total", nil, snapshot.Metrics.ExternalCmdTimeouts)
	writePrometheusMetric(&builder, "amdl_telegram_cleanup_deleted_files_total", nil, snapshot.Metrics.CleanupDeletedFiles)
	writePrometheusMetric(&builder, "amdl_telegram_cleanup_deleted_bytes_total", nil, snapshot.Metrics.CleanupDeletedBytes)
	writePrometheusMetric(&builder, "amdl_telegram_queue_full_drop_total", nil, snapshot.Metrics.QueueFullDrops)

	for _, taskType := range orderedTaskTypes(snapshot.Metrics.TaskTypes, snapshot.TaskCurrent) {
		current := snapshot.TaskCurrent[taskType]
		totals := snapshot.Metrics.TaskTypes[taskType]
		labels := map[string]string{"task_type": taskType}
		writePrometheusMetric(&builder, "amdl_telegram_task_queued_current", labels, current.QueuedCurrent)
		writePrometheusMetric(&builder, "amdl_telegram_task_running_current", labels, current.RunningCurrent)
		writePrometheusMetric(&builder, "amdl_telegram_task_enqueued_total", labels, totals.QueuedTotal)
		writePrometheusMetric(&builder, "amdl_telegram_task_started_total", labels, totals.StartedTotal)
		writePrometheusMetric(&builder, "amdl_telegram_task_finished_total", labels, totals.FinishedTotal)
		writePrometheusMetric(&builder, "amdl_telegram_task_panic_total", labels, totals.PanicTotal)
	}

	return builder.String()
}

func writePrometheusMetric(builder *strings.Builder, name string, labels map[string]string, value any) {
	if builder == nil || strings.TrimSpace(name) == "" {
		return
	}
	builder.WriteString(name)
	if len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for key := range labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for idx, key := range keys {
			if idx > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(key)
			builder.WriteString("=\"")
			builder.WriteString(escapePrometheusLabelValue(labels[key]))
			builder.WriteByte('"')
		}
		builder.WriteByte('}')
	}
	builder.WriteByte(' ')
	builder.WriteString(fmt.Sprint(value))
	builder.WriteByte('\n')
}

func escapePrometheusLabelValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
	return replacer.Replace(value)
}

func boolToFloat(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (b *TelegramBot) startMetricsHTTPServer() {
	if b == nil {
		return
	}
	addr := telegramMetricsListenAddr()
	if addr == "" {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", b.serveMetricsHTTP)
	mux.HandleFunc("/healthz", b.serveHealthzHTTP)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("telegram metrics http listen failed (%s): %v\n", addr, err)
		appendRuntimeErrorLogf("telegram metrics http listen failed (%s): %v", addr, err)
		return
	}

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	actualAddr := listener.Addr().String()
	b.metricsHTTPMu.Lock()
	if b.metricsHTTPServer != nil {
		b.metricsHTTPMu.Unlock()
		_ = listener.Close()
		return
	}
	b.metricsHTTPServer = server
	b.metricsHTTPAddr = actualAddr
	b.metricsHTTPMu.Unlock()

	fmt.Printf("telegram metrics http listening on %s\n", actualAddr)
	b.metricsHTTPWG.Add(1)
	go func() {
		defer b.metricsHTTPWG.Done()
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			fmt.Printf("telegram metrics http server failed (%s): %v\n", actualAddr, serveErr)
			appendRuntimeErrorLogf("telegram metrics http server failed (%s): %v", actualAddr, serveErr)
		}
	}()
}

func (b *TelegramBot) stopMetricsHTTPServer() {
	if b == nil {
		return
	}
	b.metricsHTTPMu.Lock()
	server := b.metricsHTTPServer
	addr := b.metricsHTTPAddr
	b.metricsHTTPServer = nil
	b.metricsHTTPAddr = ""
	b.metricsHTTPMu.Unlock()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTelegramMetricsHTTPShutdownWait)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Printf("telegram metrics http shutdown failed (%s): %v\n", addr, err)
		appendRuntimeErrorLogf("telegram metrics http shutdown failed (%s): %v", addr, err)
	}
	b.metricsHTTPWG.Wait()
	if strings.TrimSpace(addr) != "" {
		fmt.Printf("telegram metrics http stopped (%s)\n", addr)
	}
}
