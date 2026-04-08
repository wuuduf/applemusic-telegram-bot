package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
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
	g.stopCh = make(chan struct{})
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
				case <-g.stopCh:
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

func diskFreeBytes(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	target := path
	if !info.IsDir() {
		target = filepath.Dir(path)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(target, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

func currentProcessMemoryBytes() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.Alloc
}
