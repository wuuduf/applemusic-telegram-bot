package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func resolveTelegramErrorLogFile(cacheFile string, stateFile string) string {
	if raw := strings.TrimSpace(os.Getenv("AMDL_TELEGRAM_ERROR_LOG_FILE")); raw != "" {
		return filepath.Clean(raw)
	}
	stateFile = strings.TrimSpace(stateFile)
	if stateFile == "" {
		stateFile = resolveTelegramStateFile(strings.TrimSpace(cacheFile))
	}
	if strings.TrimSpace(stateFile) == "" {
		return defaultTelegramErrorLogFile
	}
	dir := filepath.Dir(filepath.Clean(stateFile))
	if strings.TrimSpace(dir) == "" || dir == "." {
		return defaultTelegramErrorLogFile
	}
	return filepath.Clean(filepath.Join(dir, defaultTelegramErrorLogFile))
}

func setRuntimeErrorLogPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		runtimeErrorLogPath.Store("")
		return
	}
	runtimeErrorLogPath.Store(filepath.Clean(path))
}

func currentRuntimeErrorLogPath() string {
	value := runtimeErrorLogPath.Load()
	if value == nil {
		return ""
	}
	path, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(path)
}

func appendRuntimeErrorLog(message string) {
	path := currentRuntimeErrorLogPath()
	message = strings.TrimSpace(message)
	if path == "" || message == "" {
		return
	}
	runtimeErrorLogMu.Lock()
	defer runtimeErrorLogMu.Unlock()

	if err := ensureNonSymlinkRegularFile(path); err != nil {
		fmt.Printf("telegram error log write skipped (%s): %v\n", path, err)
		return
	}
	if _, err := ensureCacheDirectory(path); err != nil {
		fmt.Printf("telegram error log write skipped (%s): %v\n", path, err)
		return
	}
	rotateRuntimeErrorLogIfNeededLocked(path, runtimeErrorLogMaxBytesFn())
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, telegramCacheFilePerm)
	if err != nil {
		fmt.Printf("telegram error log write skipped (%s): %v\n", path, err)
		return
	}
	line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), message)
	if _, err := file.WriteString(line); err != nil {
		fmt.Printf("telegram error log write skipped (%s): %v\n", path, err)
	}
	if err := file.Close(); err != nil {
		fmt.Printf("telegram error log close failed (%s): %v\n", path, err)
	}
	if err := os.Chmod(path, telegramCacheFilePerm); err != nil && !os.IsPermission(err) {
		fmt.Printf("telegram error log chmod failed (%s): %v\n", path, err)
	}
}

func appendRuntimeErrorLogf(format string, args ...any) {
	appendRuntimeErrorLog(fmt.Sprintf(format, args...))
}

const (
	defaultRuntimeErrorLogMaxBytes = 10 * 1024 * 1024
	runtimeErrorLogBackupSuffix    = ".1"
)

// runtimeErrorLogMaxBytesFn returns the maximum size of the runtime error
// log file before it is rotated. Exposed as a variable so tests can shrink
// the threshold without depending on environment variables.
var runtimeErrorLogMaxBytesFn = func() int64 {
	if mb, ok := parsePositiveIntEnv("AMDL_TELEGRAM_ERROR_LOG_MAX_MB"); ok {
		return int64(mb) * 1024 * 1024
	}
	return defaultRuntimeErrorLogMaxBytes
}

// rotateRuntimeErrorLogIfNeededLocked rotates the runtime error log when it
// exceeds maxBytes. The current file is moved to <path>.1 (replacing any
// previous backup); if the rename fails the file is truncated so a stuck
// rotation can never let the log grow without bound. Caller must hold
// runtimeErrorLogMu.
func rotateRuntimeErrorLogIfNeededLocked(path string, maxBytes int64) {
	if path == "" || maxBytes <= 0 {
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		return
	}
	if !info.Mode().IsRegular() {
		return
	}
	if info.Size() < maxBytes {
		return
	}
	backup := path + runtimeErrorLogBackupSuffix
	_ = os.Remove(backup)
	if err := os.Rename(path, backup); err != nil {
		if truncErr := os.Truncate(path, 0); truncErr != nil {
			fmt.Printf("telegram error log rotation failed (%s): rename=%v truncate=%v\n", path, err, truncErr)
		}
	}
}
