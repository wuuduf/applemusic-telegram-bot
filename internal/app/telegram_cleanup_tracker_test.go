package app

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sharedstorage "github.com/wuuduf/applemusic-telegram-bot/internal/storage"
)

func TestTelegramCleanupRecordPathsDoesNotTriggerFullScan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := writeSizedFileForTest(t, root, "track.m4a", 64)
	scanCalls := 0
	tracker := newTelegramCleanupTracker([]sharedstorage.CleanupRoot{{Owner: sharedstorage.OwnerTelegram, Mode: sharedstorage.ModeDownload, Path: root}}, "", 1024, time.Minute, time.Hour, 0)
	tracker.scanFolder = func(root string, cacheFile string) (int64, []downloadFileEntry, error) {
		scanCalls++
		return 0, nil, nil
	}

	tracker.RecordPaths([]string{path})
	if scanCalls != 0 {
		t.Fatalf("recording paths should not trigger full scan")
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.files) != 1 {
		t.Fatalf("expected 1 tracked file, got %d", len(tracker.files))
	}
	for _, entry := range tracker.files {
		if entry.owner != string(sharedstorage.OwnerTelegram) || entry.mode != string(sharedstorage.ModeDownload) {
			t.Fatalf("expected owner/mode metadata, got %#v", entry)
		}
	}
}

func TestTelegramCleanupTrackerQuotaEvictionWithoutScan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	oldPath := writeSizedFileForTest(t, root, "old.m4a", 80)
	newPath := writeSizedFileForTest(t, root, "new.m4a", 80)
	now := time.Now()
	mustChtimes(t, oldPath, now.Add(-2*time.Hour))
	mustChtimes(t, newPath, now.Add(-1*time.Hour))

	tracker := newTelegramCleanupTracker([]sharedstorage.CleanupRoot{{Owner: sharedstorage.OwnerTelegram, Mode: sharedstorage.ModeDownload, Path: root}}, "", 120, time.Minute, time.Hour, 0)
	tracker.RecordPaths([]string{oldPath, newPath})
	tracker.mu.Lock()
	tracker.lastScan = now
	tracker.mu.Unlock()
	tracker.cleanupOnce(false)

	assertFileMissing(t, oldPath)
	assertFileExists(t, newPath)
	tracker.mu.Lock()
	if tracker.scanRuns != 0 {
		t.Fatalf("expected zero fallback scans, got %d", tracker.scanRuns)
	}
	tracker.mu.Unlock()
}

func TestTelegramCleanupTrackerShouldScanBeforeFirstRebuildEvenWithTrackedFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := writeSizedFileForTest(t, root, "fresh.m4a", 64)
	now := time.Now()

	tracker := newTelegramCleanupTracker([]sharedstorage.CleanupRoot{{Owner: sharedstorage.OwnerTelegram, Mode: sharedstorage.ModeDownload, Path: root}}, "", 1024, time.Minute, time.Hour, 0)
	tracker.RecordPaths([]string{path})

	tracker.mu.Lock()
	hasTrackedFiles := len(tracker.files) > 0
	lastScan := tracker.lastScan
	tracker.mu.Unlock()
	if !hasTrackedFiles {
		t.Fatalf("expected tracked files after RecordPaths")
	}
	if !lastScan.IsZero() {
		t.Fatalf("expected zero lastScan before first rebuild, got %v", lastScan)
	}
	if !tracker.shouldScan(now) {
		t.Fatalf("expected first scan to run even when files were already recorded")
	}
}

func TestTelegramCleanupTrackerFallbackScan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pathA := filepath.Join(root, "a.m4a")
	pathB := filepath.Join(root, "b.m4a")
	removed := map[string]bool{}

	tracker := newTelegramCleanupTracker([]sharedstorage.CleanupRoot{{Owner: sharedstorage.OwnerTelegram, Mode: sharedstorage.ModeDownload, Path: root}}, "", 50, time.Minute, time.Second, 0)
	tracker.scanFolder = func(root string, cacheFile string) (int64, []downloadFileEntry, error) {
		return 120, []downloadFileEntry{
			{path: pathA, size: 60, modTime: time.Now().Add(-2 * time.Hour)},
			{path: pathB, size: 60, modTime: time.Now().Add(-1 * time.Hour)},
		}, nil
	}
	tracker.removeFile = func(path string) error {
		removed[path] = true
		return nil
	}

	tracker.cleanupOnce(false)
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.scanRuns == 0 {
		t.Fatalf("expected fallback scan to run")
	}
	if len(removed) == 0 {
		t.Fatalf("expected quota cleanup to remove at least one file")
	}
}

func TestTelegramCleanupTrackerJanitor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	oldPath := writeSizedFileForTest(t, root, "janitor-old.m4a", 80)
	newPath := writeSizedFileForTest(t, root, "janitor-new.m4a", 80)
	now := time.Now()
	mustChtimes(t, oldPath, now.Add(-2*time.Hour))
	mustChtimes(t, newPath, now.Add(-1*time.Hour))

	tracker := newTelegramCleanupTracker([]sharedstorage.CleanupRoot{{Owner: sharedstorage.OwnerTelegram, Mode: sharedstorage.ModeDownload, Path: root}}, "", 120, 20*time.Millisecond, time.Hour, 0)
	tracker.RecordPaths([]string{newPath})
	tracker.start()
	defer tracker.stop()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(oldPath); os.IsNotExist(err) {
			assertFileExists(t, newPath)
			tracker.mu.Lock()
			scanRuns := tracker.scanRuns
			tracker.mu.Unlock()
			if scanRuns == 0 {
				t.Fatalf("expected first janitor tick to rebuild from scan")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	tracker.mu.Lock()
	scanRuns := tracker.scanRuns
	tracker.mu.Unlock()
	t.Fatalf("expected janitor to evict old file after startup scan, scanRuns=%d", scanRuns)
}

func TestTelegramCleanupStartRunsImmediateScan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker := newTelegramCleanupTracker([]sharedstorage.CleanupRoot{{Owner: sharedstorage.OwnerTelegram, Mode: sharedstorage.ModeDownload, Path: root}}, "", 1024, time.Hour, time.Hour, 0)
	var scanCalls atomic.Int32
	tracker.scanFolder = func(root string, cacheFile string) (int64, []downloadFileEntry, error) {
		scanCalls.Add(1)
		return 0, nil, nil
	}
	tracker.start()
	defer tracker.stop()

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if scanCalls.Load() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected startup scan to run immediately")
}

func TestTelegramCleanupScanIntervalEnabledByDefault(t *testing.T) {
	t.Setenv("AMDL_TELEGRAM_CLEANUP_ENABLE_SCAN", "")
	if got := telegramCleanupScanInterval(); got <= 0 {
		t.Fatalf("expected scan interval to be enabled by default, got %s", got)
	}
}

func TestTelegramCleanupScanIntervalCanBeEnabledByEnv(t *testing.T) {
	t.Setenv("AMDL_TELEGRAM_CLEANUP_ENABLE_SCAN", "1")
	if got := telegramCleanupScanInterval(); got <= 0 {
		t.Fatalf("expected scan interval to be enabled with env, got %s", got)
	}
}

func TestTelegramCleanupScanIntervalCanBeDisabledByEnv(t *testing.T) {
	t.Setenv("AMDL_TELEGRAM_CLEANUP_ENABLE_SCAN", "0")
	if got := telegramCleanupScanInterval(); got != 0 {
		t.Fatalf("expected scan interval to be disabled by env, got %s", got)
	}
}
