package app

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sharedstorage "github.com/wuuduf/applemusic-telegram-bot/internal/storage"
)

type telegramCleanupTracker struct {
	roots      []sharedstorage.CleanupRoot
	cacheFile  string
	maxBytes   int64
	interval   time.Duration
	scanEvery  time.Duration
	protectAge time.Duration

	mu       sync.Mutex
	files    map[string]downloadFileEntry
	total    int64
	lastScan time.Time
	scanRuns int

	nowFn      func() time.Time
	scanFolder func(root string, cacheFile string) (int64, []downloadFileEntry, error)
	removeFile func(path string) error
	onDelete   func(path string, size int64)

	stopCh chan struct{}
	wg     sync.WaitGroup
}

func newTelegramCleanupTracker(roots []sharedstorage.CleanupRoot, cacheFile string, maxBytes int64, interval time.Duration, scanEvery time.Duration, protectAge time.Duration) *telegramCleanupTracker {
	return &telegramCleanupTracker{
		roots:      append([]sharedstorage.CleanupRoot{}, roots...),
		cacheFile:  cacheFile,
		maxBytes:   maxBytes,
		interval:   interval,
		scanEvery:  scanEvery,
		protectAge: protectAge,
		files:      make(map[string]downloadFileEntry),
		nowFn:      time.Now,
		scanFolder: scanDownloadFolder,
		removeFile: os.Remove,
	}
}

func telegramCleanupInterval() time.Duration {
	sec := Config.TelegramCleanupIntervalSec
	if sec <= 0 {
		return defaultTelegramCleanupInterval
	}
	return time.Duration(sec) * time.Second
}

func telegramCleanupScanInterval() time.Duration {
	sec := Config.TelegramCleanupScanIntervalSec
	if sec <= 0 {
		sec = int(defaultTelegramCleanupScanInterval / time.Second)
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AMDL_TELEGRAM_CLEANUP_ENABLE_SCAN"))) {
	case "1", "true", "yes", "on":
		return time.Duration(sec) * time.Second
	default:
		return 0
	}
}

func telegramCleanupProtectAge() time.Duration {
	sec := Config.TelegramCleanupProtectSec
	if sec <= 0 {
		return defaultTelegramCleanupProtectAge
	}
	return time.Duration(sec) * time.Second
}

func (t *telegramCleanupTracker) start() {
	if t == nil || len(t.roots) == 0 || t.maxBytes <= 0 || t.interval <= 0 {
		return
	}
	t.mu.Lock()
	if t.stopCh != nil {
		t.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	t.stopCh = stop
	t.wg.Add(1)
	t.mu.Unlock()

	go func() {
		defer t.wg.Done()
		runWithRecovery("telegram cleanup tracker", nil, func() {
			ticker := time.NewTicker(t.interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					runWithRecovery("telegram cleanup tick", nil, func() {
						t.cleanupOnce(false)
					})
				case <-stop:
					return
				}
			}
		})
	}()
}

func (t *telegramCleanupTracker) stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	stop := t.stopCh
	if stop != nil {
		close(stop)
		t.stopCh = nil
	}
	t.mu.Unlock()
	if stop != nil {
		t.wg.Wait()
	}
}

func (t *telegramCleanupTracker) RecordPaths(paths []string) {
	if t == nil {
		return
	}
	now := t.nowFn()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, path := range paths {
		cleanPath := filepath.Clean(strings.TrimSpace(path))
		root, ok := t.cleanupRootForPathLocked(cleanPath)
		if cleanPath == "" || !ok {
			continue
		}
		info, err := os.Stat(cleanPath)
		if err != nil || info.IsDir() || !info.Mode().IsRegular() {
			continue
		}
		if prev, ok := t.files[cleanPath]; ok {
			t.total -= prev.size
		}
		entry := downloadFileEntry{
			path:    cleanPath,
			size:    info.Size(),
			modTime: info.ModTime(),
			owner:   string(root.Owner),
			mode:    string(root.Mode),
		}
		if entry.modTime.IsZero() {
			entry.modTime = now
		}
		t.files[cleanPath] = entry
		t.total += entry.size
	}
}

func (t *telegramCleanupTracker) cleanupOnce(forceScan bool) {
	if t == nil {
		return
	}
	now := t.nowFn()
	if forceScan || t.shouldScan(now) {
		t.rebuildFromScan(now)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.total <= t.maxBytes {
		return
	}
	entries := make([]downloadFileEntry, 0, len(t.files))
	for _, entry := range t.files {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].modTime.Equal(entries[j].modTime) {
			return entries[i].path < entries[j].path
		}
		return entries[i].modTime.Before(entries[j].modTime)
	})
	for _, entry := range entries {
		if t.total <= t.maxBytes {
			break
		}
		if t.protectAge > 0 && now.Sub(entry.modTime) < t.protectAge {
			continue
		}
		err := t.removeFile(entry.path)
		if err != nil {
			if os.IsNotExist(err) {
				t.total -= entry.size
				delete(t.files, entry.path)
				if t.onDelete != nil {
					t.onDelete(entry.path, entry.size)
				}
			}
			continue
		}
		t.total -= entry.size
		delete(t.files, entry.path)
		if t.onDelete != nil {
			t.onDelete(entry.path, entry.size)
		}
	}
	if t.total < 0 {
		t.total = 0
	}
}

func (t *telegramCleanupTracker) shouldScan(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.scanEvery <= 0 {
		return false
	}
	if t.lastScan.IsZero() {
		return true
	}
	if len(t.files) == 0 {
		return true
	}
	return now.Sub(t.lastScan) >= t.scanEvery
}

func (t *telegramCleanupTracker) rebuildFromScan(now time.Time) {
	files := make(map[string]downloadFileEntry)
	var scanRuns int
	for _, root := range t.roots {
		_, entries, err := t.scanFolder(root.Path, t.cacheFile)
		if err != nil {
			continue
		}
		scanRuns++
		for _, entry := range entries {
			entry.owner = string(root.Owner)
			entry.mode = string(root.Mode)
			files[entry.path] = entry
		}
	}
	var total int64
	for _, entry := range files {
		total += entry.size
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if scanRuns > 0 {
		t.files = files
		t.total = total
		t.lastScan = now
		t.scanRuns += scanRuns
	}
}

func (t *telegramCleanupTracker) cleanupRootForPathLocked(path string) (sharedstorage.CleanupRoot, bool) {
	dir := filepath.Dir(path)
	for _, root := range t.roots {
		if isParentDir(root.Path, dir) {
			return root, true
		}
	}
	return sharedstorage.CleanupRoot{}, false
}

func (t *telegramCleanupTracker) withinRootsLocked(path string) bool {
	_, ok := t.cleanupRootForPathLocked(path)
	return ok
}
