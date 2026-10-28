package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCacheSnapshotIgnoresStaleSnapshot(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "telegram-cache.json")
	b := &TelegramBot{
		cacheFile: cachePath,
		cache:     map[string]CachedAudio{"old|alac|false": {FileID: "old"}},
	}
	b.cacheMu.Lock()
	stale, staleSeq := b.snapshotCacheLocked()
	b.cache["new|alac|false"] = CachedAudio{FileID: "new"}
	fresh, freshSeq := b.snapshotCacheLocked()
	b.cacheMu.Unlock()

	b.writeCacheSnapshot(fresh, freshSeq)
	b.writeCacheSnapshot(stale, staleSeq)

	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	var parsed telegramCacheFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse cache file: %v", err)
	}
	if _, ok := parsed.Items["new|alac|false"]; !ok {
		t.Fatalf("stale snapshot overwrote the newer cache file: %+v", parsed.Items)
	}
}
