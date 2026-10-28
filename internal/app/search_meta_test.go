package app

import (
	"fmt"
	"testing"
	"time"
)

func withCleanSearchMetaForTest(t *testing.T) {
	t.Helper()
	searchMetaMu.Lock()
	oldByID, oldAt, oldPrune := searchMetaByID, searchMetaAt, searchMetaLastPrune
	searchMetaByID = make(map[string]AudioMeta)
	searchMetaAt = make(map[string]time.Time)
	searchMetaLastPrune = time.Time{}
	searchMetaMu.Unlock()
	t.Cleanup(func() {
		searchMetaMu.Lock()
		searchMetaByID, searchMetaAt, searchMetaLastPrune = oldByID, oldAt, oldPrune
		searchMetaMu.Unlock()
	})
}

func TestSearchMetaExpiresAfterTTL(t *testing.T) {
	withCleanSearchMetaForTest(t)
	start := time.Unix(1_700_000_000, 0)
	searchMetaMu.Lock()
	storeSearchMetaLocked("stale", AudioMeta{Title: "stale"}, start)
	storeSearchMetaLocked("fresh", AudioMeta{Title: "fresh"}, start.Add(searchMetaTTL+searchMetaPruneEvery))
	_, staleKept := searchMetaByID["stale"]
	_, freshKept := searchMetaByID["fresh"]
	searchMetaMu.Unlock()
	if staleKept {
		t.Fatalf("expired search meta should have been pruned")
	}
	if !freshKept {
		t.Fatalf("fresh search meta should be kept")
	}
}

func TestSearchMetaCapsEntriesDroppingOldest(t *testing.T) {
	withCleanSearchMetaForTest(t)
	start := time.Unix(1_700_000_000, 0)
	searchMetaMu.Lock()
	for i := 0; i < searchMetaMaxEntries+10; i++ {
		storeSearchMetaLocked(fmt.Sprintf("t%d", i), AudioMeta{Title: "x"}, start.Add(time.Duration(i)*time.Second))
	}
	count := len(searchMetaByID)
	_, oldestKept := searchMetaByID["t0"]
	_, newestKept := searchMetaByID[fmt.Sprintf("t%d", searchMetaMaxEntries+9)]
	searchMetaMu.Unlock()
	if count != searchMetaMaxEntries {
		t.Fatalf("expected %d entries, got %d", searchMetaMaxEntries, count)
	}
	if oldestKept || !newestKept {
		t.Fatalf("expected oldest dropped and newest kept (oldest=%v newest=%v)", oldestKept, newestKept)
	}
}

func TestPopSearchMetaRemovesTimestamp(t *testing.T) {
	withCleanSearchMetaForTest(t)
	setSearchMeta("123", "Song", "Artist")
	if meta, ok := popSearchMeta("123"); !ok || meta.Title != "Song" {
		t.Fatalf("unexpected pop result: %+v ok=%v", meta, ok)
	}
	searchMetaMu.Lock()
	_, left := searchMetaAt["123"]
	searchMetaMu.Unlock()
	if left {
		t.Fatalf("timestamp should be removed with the entry")
	}
}
