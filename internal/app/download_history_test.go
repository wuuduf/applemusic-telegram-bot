package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readHistoryFileForTest(t *testing.T, path string) downloadHistoryFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read history file: %v", err)
	}
	var parsed downloadHistoryFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse history file: %v", err)
	}
	return parsed
}

func TestDownloadHistoryThrottledRecordIsPersistedLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	h := newDownloadHistory(true, 10, path)

	h.Record(42, downloadHistoryEntry{MediaID: "first", Success: true})
	h.Record(42, downloadHistoryEntry{MediaID: "second", Success: true})

	if got := len(readHistoryFileForTest(t, path).ByChat[42]); got != 1 {
		t.Fatalf("expected only the first record to be written immediately, got %d", got)
	}
	deadline := time.Now().Add(historyMinSaveInterval + 2*time.Second)
	for time.Now().Before(deadline) {
		if len(readHistoryFileForTest(t, path).ByChat[42]) == 2 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("throttled record was never persisted")
}

func TestDownloadHistoryFlushPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	h := newDownloadHistory(true, 2, path)
	for _, id := range []string{"a", "b", "c"} {
		h.Record(7, downloadHistoryEntry{MediaID: id, Success: id != "b"})
	}
	h.Flush()

	reloaded := newDownloadHistory(true, 2, path)
	entries := reloaded.Entries(7)
	if len(entries) != 2 || entries[0].MediaID != "c" || entries[1].MediaID != "b" {
		t.Fatalf("unexpected reloaded entries: %+v", entries)
	}
	if total, success, failed := reloaded.Stats(7); total != 2 || success != 1 || failed != 1 {
		t.Fatalf("unexpected stats total=%d success=%d failed=%d", total, success, failed)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected history file with 0600 perms, got %v (err=%v)", info.Mode().Perm(), err)
	}
}
