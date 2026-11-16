package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sharedstorage "github.com/wuuduf/applemusic-telegram-bot/internal/storage"
	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
)

// Captions are signed with the bot's username at runtime (getMe); tests use a
// fixed signature so caption assertions do not depend on the network.
func init() {
	setCaptionSignature("via @test_bot")
}

func newCleanupTrackerForTest(root string, maxBytes int64) *telegramCleanupTracker {
	tracker := newTelegramCleanupTracker([]sharedstorage.CleanupRoot{{Owner: sharedstorage.OwnerTelegram, Mode: sharedstorage.ModeDownload, Path: root}}, "", maxBytes, time.Minute, time.Hour, 0)
	tracker.mu.Lock()
	tracker.lastScan = time.Now()
	tracker.mu.Unlock()
	return tracker
}

func TestCleanupTrackerKeepsPinnedPathsUntilReleased(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	oldPath := writeSizedFileForTest(t, root, "old.m4a", 80)
	newPath := writeSizedFileForTest(t, root, "new.m4a", 80)
	now := time.Now()
	mustChtimes(t, oldPath, now.Add(-2*time.Hour))
	mustChtimes(t, newPath, now.Add(-1*time.Hour))

	tracker := newCleanupTrackerForTest(root, 120)
	tracker.RecordPaths([]string{oldPath, newPath})
	unpin := tracker.pinPaths([]string{oldPath})

	tracker.cleanupOnce(false)
	assertFileExists(t, oldPath)
	// The quota still has to be met, so the unpinned file goes instead.
	assertFileMissing(t, newPath)

	unpin()
	unpin() // releasing twice must not unbalance the count
	tracker.mu.Lock()
	tracker.maxBytes = 10
	tracker.mu.Unlock()
	tracker.cleanupOnce(false)
	assertFileMissing(t, oldPath)
}

func TestCleanupTrackerProtectsFilesOfRunningTask(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stalePath := writeSizedFileForTest(t, root, "stale.m4a", 80)
	mustChtimes(t, stalePath, time.Now().Add(-2*time.Hour))

	tracker := newCleanupTrackerForTest(root, 100)
	release := tracker.beginTask()
	freshPath := writeSizedFileForTest(t, root, "fresh.m4a", 80)
	mustChtimes(t, freshPath, time.Now().Add(time.Second))
	tracker.RecordPaths([]string{stalePath, freshPath})

	tracker.mu.Lock()
	tracker.maxBytes = 10
	tracker.mu.Unlock()
	tracker.cleanupOnce(false)
	assertFileMissing(t, stalePath)
	assertFileExists(t, freshPath)

	release()
	tracker.cleanupOnce(false)
	assertFileMissing(t, freshPath)
}

func TestNilCleanupTrackerProtectionIsNoop(t *testing.T) {
	var tracker *telegramCleanupTracker
	tracker.beginTask()()
	tracker.pinPaths([]string{"a"})()
}

func TestDownloadWorkersStartTasksInQueueOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const total = 12
	b := &TelegramBot{
		downloadQueue:     make(chan *downloadRequest, total),
		workerLimit:       1,
		inflightDownloads: make(map[string]struct{}),
		activeRequests:    make(map[string]telegramPersistedRequest),
		shutdownCtx:       ctx,
		shutdownCancel:    cancel,
	}
	b.queueCond = sync.NewCond(&b.queueMu)

	var mu sync.Mutex
	var order []int
	done := make(chan struct{})
	for i := 0; i < total; i++ {
		index := i
		b.downloadQueue <- &downloadRequest{
			requestID: fmt.Sprintf("req-%d", index),
			taskType:  telegramTaskCover,
			mediaType: mediaTypeAlbum,
			mediaID:   fmt.Sprintf("album-%d", index),
			run: func(*TelegramBot, context.Context) error {
				time.Sleep(2 * time.Millisecond)
				mu.Lock()
				order = append(order, index)
				finished := len(order) == total
				mu.Unlock()
				if finished {
					close(done)
				}
				return nil
			},
		}
	}
	b.startDownloadWorker()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("workers did not finish all tasks")
	}
	b.stopDownloadWorkers()

	mu.Lock()
	defer mu.Unlock()
	for i, got := range order {
		if got != i {
			t.Fatalf("tasks ran out of queue order: %v", order)
		}
	}
}

func TestCanceledQueuedRequestDoesNotBlockLaterTickets(t *testing.T) {
	b := &TelegramBot{}
	b.queueCond = sync.NewCond(&b.queueMu)
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	// Ticket 1 is skipped (canceled) while ticket 0 has not started yet.
	b.finishQueueTicketLocked(1)
	if b.queueNextTicket != 0 {
		t.Fatalf("ticket 0 must still be next, got %d", b.queueNextTicket)
	}
	b.finishQueueTicketLocked(0)
	if b.queueNextTicket != 2 {
		t.Fatalf("expected next ticket 2 after 0 and 1 finished, got %d", b.queueNextTicket)
	}
	if len(b.queueDoneTickets) != 0 {
		t.Fatalf("finished tickets must be forgotten, got %v", b.queueDoneTickets)
	}
}

func TestSelectionStartsDownloadOnlyForMediaResults(t *testing.T) {
	b := &TelegramBot{pending: make(map[int64]map[int]*PendingSelection)}
	b.setPending(1, "song", "q", "us", 0, []apputils.SearchResultItem{{ID: "1"}}, false, 0, 10, "")
	b.setPending(1, "artist", "q", "us", 0, []apputils.SearchResultItem{{ID: "2"}}, false, 0, 11, "")
	if !b.selectionStartsDownload(1, 10) {
		t.Fatalf("picking a song result must be charged")
	}
	if b.selectionStartsDownload(1, 11) {
		t.Fatalf("picking an artist only opens another panel")
	}
	if b.selectionStartsDownload(1, 99) {
		t.Fatalf("unknown panel must not be charged")
	}
}

func TestZipFallbackUsesTypedErrors(t *testing.T) {
	if !zipSendFailureNeedsFallback(fileTooLargeError{"ZIP exceeds Telegram limit (50MB)"}) {
		t.Fatalf("oversized ZIP must fall back to one-by-one")
	}
	if !zipSendFailureNeedsFallback(fmt.Errorf("document %w: no progress for 2m", errUploadStalled)) {
		t.Fatalf("stalled upload must fall back to one-by-one")
	}
	if zipSendFailureNeedsFallback(errors.New("telegram sendDocument failed: Bad Request")) {
		t.Fatalf("other failures must not fall back")
	}
	if got := (fileTooLargeError{"ZIP exceeds Telegram limit (50MB)"}).Error(); got != "ZIP exceeds Telegram limit (50MB)" {
		t.Fatalf("user-facing message changed: %q", got)
	}
}

func TestGetUpdatesBackoffUsesStatusCode(t *testing.T) {
	b := &TelegramBot{getUpdatesConflictDelay: 7 * time.Second, getUpdatesErrorDelay: time.Second}
	backoff, reason := b.getUpdatesBackoff(telegramStatusError{code: http.StatusConflict, msg: "getUpdates failed"})
	if reason != "conflict" || backoff != 7*time.Second {
		t.Fatalf("expected conflict backoff, got %s %q", backoff, reason)
	}
	// A 5xx whose text happens to contain "409" is not a conflict.
	_, reason = b.getUpdatesBackoff(telegramStatusError{code: http.StatusBadGateway, msg: "getUpdates failed: 502 (upstream 409)"})
	if reason == "conflict" {
		t.Fatalf("status code must decide, not the message text")
	}
}

func TestParseMemoryStatValue(t *testing.T) {
	content := "anon 1048576\nfile 999\nanon_thp 0\n"
	if value, ok := parseMemoryStatValue(content, "anon"); !ok || value != 1048576 {
		t.Fatalf("expected anon=1048576, got %d %t", value, ok)
	}
	if _, ok := parseMemoryStatValue(content, "total_rss"); ok {
		t.Fatalf("missing key must not be reported")
	}
}

func TestCompressedUploadIsReusedAcrossRetries(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "song.flac")
	compressed := writeSizedFileForTest(t, dir, "song-small.flac", 16)
	session := newDownloadSession(Config)

	if session.retainCompressedUpload(source, compressed) {
		t.Fatalf("session must not keep files when reuse was not requested")
	}
	session.beginCompressedUploadReuse(source)
	if got := session.reusableCompressedUpload(source); got != "" {
		t.Fatalf("nothing compressed yet, got %q", got)
	}
	if !session.retainCompressedUpload(source, compressed) {
		t.Fatalf("session should keep the compressed copy for retries")
	}
	if got := session.reusableCompressedUpload(source); got != compressed {
		t.Fatalf("expected reuse of %q, got %q", compressed, got)
	}
	session.endCompressedUploadReuse(source)
	if _, err := os.Stat(compressed); !os.IsNotExist(err) {
		t.Fatalf("compressed copy must be removed when the upload ends")
	}
}

func TestCaptionSignatureIsConfigurable(t *testing.T) {
	previous := captionSignature()
	t.Cleanup(func() { setCaptionSignature(previous) })

	setCaptionSignature("")
	if got := formatTelegramCaption(1024*1024, 256, telegramFormatAac); got != "#AppleMusic #aac 文件大小1.00MB 256.00kbps" {
		t.Fatalf("unsigned caption changed: %q", got)
	}
	setCaptionSignature("via @my_bot")
	if got := formatMVCaption(AudioMeta{}, 1024*1024); got != "#AppleMusic #mv 1.00MB\nvia @my_bot" {
		t.Fatalf("signed MV caption wrong: %q", got)
	}
}

func TestRecoveryDropsRequestThatKeepsCrashing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // background notice must not be sent in this test
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")
	payload := fmt.Sprintf(`{"version":1,"requests":[
		{"request_id":"poison","chat_id":1,"task_type":"cover","media_type":"album","media_id":"1","storefront":"us","inflight_key":"k1","state":"running","attempts":%d},
		{"request_id":"fine","chat_id":1,"task_type":"cover","media_type":"album","media_id":"2","storefront":"us","inflight_key":"k2","state":"running","attempts":1}
	]}`, maxRecoveredRequestAttempts)
	if err := os.WriteFile(stateFile, []byte(payload), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	b := &TelegramBot{
		stateFile:         stateFile,
		downloadQueue:     make(chan *downloadRequest, 4),
		inflightDownloads: make(map[string]struct{}),
		activeRequests:    make(map[string]telegramPersistedRequest),
		shutdownCtx:       ctx,
		shutdownCancel:    cancel,
	}
	b.queueCond = sync.NewCond(&b.queueMu)
	b.restoreRuntimeState()

	if len(b.downloadQueue) != 1 {
		t.Fatalf("expected only the healthy request to be re-queued, got %d", len(b.downloadQueue))
	}
	if req := <-b.downloadQueue; req.requestID != "fine" {
		t.Fatalf("unexpected recovered request %q", req.requestID)
	}
	b.requestStateMu.Lock()
	_, poisonTracked := b.activeRequests["poison"]
	b.requestStateMu.Unlock()
	if poisonTracked {
		t.Fatalf("crash-looping request must not stay tracked")
	}
	if !b.acquireInflightDownload("k1") {
		t.Fatalf("inflight key of the dropped request must be released")
	}
}

func TestDownloadCoreGateLimitsAndSerializesSameItem(t *testing.T) {
	gate := newDownloadCoreGate(2)
	var mu sync.Mutex
	running, peak := 0, 0
	sameItemRunning, sameItemOverlap := 0, false
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("album:%d", i)
		same := i%3 == 0
		if same {
			key = "album:shared"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := gate.acquire(key)
			mu.Lock()
			running++
			if running > peak {
				peak = running
			}
			if same {
				sameItemRunning++
				if sameItemRunning > 1 {
					sameItemOverlap = true
				}
			}
			mu.Unlock()
			time.Sleep(3 * time.Millisecond)
			mu.Lock()
			running--
			if same {
				sameItemRunning--
			}
			mu.Unlock()
			release()
			release() // double release must be harmless
		}()
	}
	wg.Wait()
	if peak > 2 {
		t.Fatalf("limit 2 exceeded: %d downloads ran at once", peak)
	}
	if sameItemOverlap {
		t.Fatalf("two downloads of the same item overlapped")
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.keys) != 0 {
		t.Fatalf("key locks leaked: %d", len(gate.keys))
	}
}

func TestPruneExpiredCacheKeepsFreshAndUndatedEntries(t *testing.T) {
	now := time.Now()
	b := &TelegramBot{
		cache: map[string]CachedAudio{
			"old":     {FileID: "a", UpdatedAt: now.Add(-40 * 24 * time.Hour)},
			"fresh":   {FileID: "b", UpdatedAt: now.Add(-time.Hour)},
			"undated": {FileID: "c"},
		},
		docCache:   map[string]CachedDocument{"old-doc": {FileID: "d", UpdatedAt: now.Add(-40 * 24 * time.Hour)}},
		videoCache: map[string]CachedVideo{"fresh-video": {FileID: "e", UpdatedAt: now}},
	}
	if removed := b.pruneExpiredCacheLocked(0, now); removed != 0 {
		t.Fatalf("max age 0 must keep everything, removed %d", removed)
	}
	if removed := b.pruneExpiredCacheLocked(30*24*time.Hour, now); removed != 2 {
		t.Fatalf("expected 2 expired entries, removed %d", removed)
	}
	if _, ok := b.cache["old"]; ok {
		t.Fatalf("expired audio entry kept")
	}
	if len(b.cache) != 2 || len(b.docCache) != 0 || len(b.videoCache) != 1 {
		t.Fatalf("unexpected cache contents after prune")
	}
}

func TestShutdownKeepsQueuedRequestForRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &TelegramBot{
		downloadQueue:     make(chan *downloadRequest, 2),
		workerLimit:       1,
		inflightDownloads: make(map[string]struct{}),
		activeRequests:    map[string]telegramPersistedRequest{"queued-1": {RequestID: "queued-1", State: "queued"}},
		shutdownCtx:       ctx,
		shutdownCancel:    cancel,
	}
	b.queueCond = sync.NewCond(&b.queueMu)
	b.downloadQueue <- &downloadRequest{requestID: "queued-1", taskType: telegramTaskCover, mediaType: mediaTypeAlbum, mediaID: "1",
		run: func(*TelegramBot, context.Context) error {
			t.Errorf("queued task must not run during shutdown")
			return nil
		}}
	cancel()
	b.startDownloadWorker()
	b.stopDownloadWorkers()
	b.requestStateMu.Lock()
	defer b.requestStateMu.Unlock()
	if _, ok := b.activeRequests["queued-1"]; !ok {
		t.Fatalf("a request skipped by shutdown must stay tracked so the next start re-queues it")
	}
}
