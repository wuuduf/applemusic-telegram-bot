package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type telegramResourceGuard struct {
	minDiskFreeBytes int64
	minTmpFreeBytes  int64
	maxMemoryBytes   int64
	checkInterval    time.Duration
	roots            []string
	tmpRoot          string

	diskFreeFn func(path string) (int64, error)
	memoryFn   func() uint64

	mu      sync.RWMutex
	blocked bool
	reason  string
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

func telegramResourceCheckInterval() time.Duration {
	sec := Config.TelegramResourceCheckIntervalSec
	if sec <= 0 {
		return defaultTelegramResourceCheck
	}
	return time.Duration(sec) * time.Second
}

func telegramMinFreeDiskBytes() int64 {
	mb := Config.TelegramMinFreeDiskMB
	if mb <= 0 {
		if env, ok := parsePositiveIntEnv("AMDL_TELEGRAM_MIN_FREE_DISK_MB"); ok {
			mb = env
		} else {
			mb = defaultTelegramMinFreeDiskMB
		}
	}
	return int64(mb) * 1024 * 1024
}

func telegramMinFreeTmpBytes() int64 {
	mb := Config.TelegramMinFreeTmpMB
	if mb <= 0 {
		if env, ok := parsePositiveIntEnv("AMDL_TELEGRAM_MIN_FREE_TMP_MB"); ok {
			mb = env
		} else {
			mb = defaultTelegramMinFreeTmpMB
		}
	}
	return int64(mb) * 1024 * 1024
}

func telegramMaxMemoryBytes() int64 {
	mb := Config.TelegramMaxMemoryMB
	if mb <= 0 {
		if env, ok := parsePositiveIntEnv("AMDL_TELEGRAM_MAX_MEMORY_MB"); ok {
			mb = env
		} else if Config.MaxMemoryLimit > 0 {
			mb = Config.MaxMemoryLimit
		}
	}
	if mb <= 0 {
		return 0
	}
	return int64(mb) * 1024 * 1024
}

func resolveTelegramTmpRoot() string {
	candidates := []string{
		strings.TrimSpace(os.Getenv("AMDL_TMPDIR")),
		strings.TrimSpace(os.Getenv("TMPDIR")),
		strings.TrimSpace(os.TempDir()),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		return filepath.Clean(candidate)
	}
	return ""
}

func newTelegramResourceGuard(roots []string) *telegramResourceGuard {
	guard := &telegramResourceGuard{
		minDiskFreeBytes: telegramMinFreeDiskBytes(),
		minTmpFreeBytes:  telegramMinFreeTmpBytes(),
		maxMemoryBytes:   telegramMaxMemoryBytes(),
		checkInterval:    telegramResourceCheckInterval(),
		roots:            append([]string{}, roots...),
		tmpRoot:          resolveTelegramTmpRoot(),
		diskFreeFn:       diskFreeBytes,
		memoryFn:         currentProcessMemoryBytes,
	}
	if guard.minDiskFreeBytes <= 0 && guard.minTmpFreeBytes <= 0 && guard.maxMemoryBytes <= 0 {
		return nil
	}
	if guard.checkInterval <= 0 {
		guard.checkInterval = defaultTelegramResourceCheck
	}
	return guard
}

func (g *telegramResourceGuard) start() {
	if g == nil {
		return
	}
	g.evaluate()
	if g.stopCh != nil {
		return
	}
	// The loop keeps its own copy: stop() clears the field while the loop
	// may still be selecting on it.
	stopCh := make(chan struct{})
	g.stopCh = stopCh
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		runWithRecovery("telegram resource guard", nil, func() {
			ticker := time.NewTicker(g.checkInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					runWithRecovery("telegram resource guard tick", nil, func() {
						g.evaluate()
					})
				case <-stopCh:
					return
				}
			}
		})
	}()
}

func (g *telegramResourceGuard) stop() {
	if g == nil || g.stopCh == nil {
		return
	}
	close(g.stopCh)
	g.stopCh = nil
	g.wg.Wait()
}

func (g *telegramResourceGuard) snapshot() (bool, string) {
	if g == nil {
		return false, ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.blocked, g.reason
}

func (g *telegramResourceGuard) allow() (bool, string) {
	if g == nil {
		return true, ""
	}
	g.evaluate()
	blocked, reason := g.snapshot()
	return !blocked, reason
}

func (g *telegramResourceGuard) set(blocked bool, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.blocked = blocked
	g.reason = reason
}

func (g *telegramResourceGuard) evaluate() {
	if g == nil {
		return
	}
	if g.maxMemoryBytes > 0 {
		used := int64(g.memoryFn())
		if used > g.maxMemoryBytes {
			g.set(true, fmt.Sprintf("memory usage is high (%s > %s)", formatBytes(used), formatBytes(g.maxMemoryBytes)))
			return
		}
	}

	if g.minDiskFreeBytes > 0 {
		for _, root := range g.roots {
			clean := filepath.Clean(strings.TrimSpace(root))
			if clean == "" {
				continue
			}
			free, err := g.diskFreeFn(clean)
			if err != nil {
				continue
			}
			if free < g.minDiskFreeBytes {
				g.set(true, fmt.Sprintf("low disk space on %s (%s free < %s)", clean, formatBytes(free), formatBytes(g.minDiskFreeBytes)))
				return
			}
		}
	}

	if g.minTmpFreeBytes > 0 && g.tmpRoot != "" {
		free, err := g.diskFreeFn(g.tmpRoot)
		if err == nil && free < g.minTmpFreeBytes {
			g.set(true, fmt.Sprintf("low temp space on %s (%s free < %s)", g.tmpRoot, formatBytes(free), formatBytes(g.minTmpFreeBytes)))
			return
		}
	}

	g.set(false, "")
}

// currentProcessMemoryBytes reports the memory the bot is really using. The
// download work happens in child processes (ffmpeg, MP4Box, mp4decrypt), so
// the Go heap alone says little. Preference order:
//  1. the container's anonymous memory from cgroup accounting, which covers
//     child processes and excludes page cache from downloaded files;
//  2. this process's resident set size;
//  3. memory obtained from the OS by the Go runtime (non-Linux fallback).
func currentProcessMemoryBytes() uint64 {
	if value, ok := cgroupAnonymousMemoryBytes(); ok {
		return value
	}
	if value, ok := processResidentMemoryBytes(); ok {
		return value
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.Sys
}

func cgroupAnonymousMemoryBytes() (uint64, bool) {
	for _, candidate := range []struct{ path, key string }{
		{"/sys/fs/cgroup/memory.stat", "anon"},             // cgroup v2
		{"/sys/fs/cgroup/memory/memory.stat", "total_rss"}, // cgroup v1
	} {
		data, err := os.ReadFile(candidate.path)
		if err != nil {
			continue
		}
		if value, ok := parseMemoryStatValue(string(data), candidate.key); ok {
			return value, true
		}
	}
	return 0, false
}

func parseMemoryStatValue(content string, key string) (uint64, bool) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != key {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

func processResidentMemoryBytes() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * uint64(os.Getpagesize()), true
}
