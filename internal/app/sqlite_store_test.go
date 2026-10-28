package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openStoreForTest(t *testing.T, dir string) *telegramStore {
	t.Helper()
	store, err := openTelegramStore(filepath.Join(dir, "telegram.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func withConfigForTest(t *testing.T, mutate func()) {
	t.Helper()
	saved := Config
	mutate()
	t.Cleanup(func() { Config = saved })
}

func TestTelegramStoreCacheRoundTripWritesOnlyChanges(t *testing.T) {
	dir := t.TempDir()
	store := openStoreForTest(t, dir)
	b := &TelegramBot{store: store, cacheFile: filepath.Join(dir, "telegram-cache.json")}
	b.loadCache()
	// Pretend the background saver is running so mutations only mark entries
	// dirty and this test controls when flushes happen.
	b.cacheSave = make(chan struct{}, 1)

	b.storeCachedAudio("111", CachedAudio{FileID: "audio-1", Format: "alac", GenreNames: []string{"Pop"}})
	b.storeCachedAudio("222", CachedAudio{FileID: "audio-2", Format: "flac"})
	b.storeCachedDocument("album:1|zip", CachedDocument{FileID: "doc-1"})
	b.storeCachedVideo("music-video:9|x", CachedVideo{FileID: "vid-1"})
	b.saveCacheNow()
	if len(b.cacheDirty) != 0 {
		t.Fatalf("dirty set should be empty after a flush, got %d", len(b.cacheDirty))
	}

	b.deleteCachedAudio("222", "flac", false)
	if got := len(b.cacheDirty); got != 1 {
		t.Fatalf("expected only the deleted entry to be dirty, got %d", got)
	}
	b.saveCacheNow()

	audio, docs, videos, err := store.loadCache()
	if err != nil {
		t.Fatalf("load cache: %v", err)
	}
	if len(audio) != 1 || audio[b.cacheKey("111", "alac", false)].FileID != "audio-1" {
		t.Fatalf("unexpected audio rows: %+v", audio)
	}
	if got := audio[b.cacheKey("111", "alac", false)].GenreNames; len(got) != 1 || got[0] != "Pop" {
		t.Fatalf("genre names not persisted: %v", got)
	}
	if docs["album:1|zip"].FileID != "doc-1" || videos["music-video:9|x"].FileID != "vid-1" {
		t.Fatalf("unexpected document/video rows: %+v %+v", docs, videos)
	}

	// A fresh bot on the same database sees the same cache.
	reloaded := &TelegramBot{store: store}
	reloaded.loadCache()
	if _, ok := reloaded.getCachedDocument("album:1|zip"); !ok {
		t.Fatalf("reloaded bot is missing the cached document")
	}
}

func TestTelegramStorePurgeDeletesRows(t *testing.T) {
	store := openStoreForTest(t, t.TempDir())
	b := &TelegramBot{store: store}
	b.loadCache()
	b.storeCachedAudio("333", CachedAudio{FileID: "a", Format: "alac"})
	b.storeCachedDocument(mediaTypeSong+":333|lyrics", CachedDocument{FileID: "d"})
	b.saveCacheNow()

	if removed := b.purgeTargetCaches(&AppleURLTarget{MediaType: mediaTypeSong, ID: "333"}); removed != 2 {
		t.Fatalf("expected 2 entries purged, got %d", removed)
	}
	b.saveCacheNow()
	audio, docs, _, err := store.loadCache()
	if err != nil {
		t.Fatalf("load cache: %v", err)
	}
	if len(audio) != 0 || len(docs) != 0 {
		t.Fatalf("purged rows still present: audio=%v docs=%v", audio, docs)
	}
}

func TestTelegramStoreFailedFlushKeepsEntriesDirty(t *testing.T) {
	store := openStoreForTest(t, t.TempDir())
	b := &TelegramBot{store: store}
	b.loadCache()
	_ = store.db.Close()

	// No saver is running, so this write is flushed synchronously and fails.
	b.storeCachedDocument("k", CachedDocument{FileID: "d"})
	if _, ok := b.cacheDirty[cacheEntryRef{kind: cacheKindDocument, key: "k"}]; !ok {
		t.Fatalf("entry should stay dirty after a failed write so it is retried")
	}
}

func TestTelegramStoreRuntimeStateRoundTrip(t *testing.T) {
	store := openStoreForTest(t, t.TempDir())
	payload, err := json.Marshal(telegramPersistedState{
		Version:      telegramStateVersion,
		ChatSettings: map[int64]ChatDownloadSettings{42: {Format: "flac"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.saveRuntimeState(payload); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := store.saveRuntimeState(payload); err != nil {
		t.Fatalf("overwrite state: %v", err)
	}
	state, found, err := store.loadRuntimeState()
	if err != nil || !found {
		t.Fatalf("load state: found=%v err=%v", found, err)
	}
	if state.ChatSettings[42].Format != "flac" {
		t.Fatalf("unexpected state: %+v", state.ChatSettings)
	}
}

func TestTelegramStoreHistoryRecordReloadAndTrim(t *testing.T) {
	store := openStoreForTest(t, t.TempDir())
	h := newDownloadHistoryWithStore(true, 2, "", store)
	for _, id := range []string{"a", "b", "c"} {
		h.Record(7, downloadHistoryEntry{MediaID: id, Success: true})
	}
	h.Record(8, downloadHistoryEntry{MediaID: "x", Success: false})

	var rows int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM download_history WHERE chat_id = 7").Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 2 {
		t.Fatalf("expected chat 7 trimmed to 2 rows, got %d", rows)
	}

	reloaded := newDownloadHistoryWithStore(true, 2, "", store)
	entries := reloaded.Entries(7)
	if len(entries) != 2 || entries[0].MediaID != "c" || entries[1].MediaID != "b" {
		t.Fatalf("unexpected reloaded entries: %+v", entries)
	}
	if total, _, failed := reloaded.Stats(8); total != 1 || failed != 1 {
		t.Fatalf("unexpected stats for chat 8: total=%d failed=%d", total, failed)
	}
}

func TestTelegramStoreHistoryEvictionDeletesRows(t *testing.T) {
	store := openStoreForTest(t, t.TempDir())
	h := newDownloadHistoryWithStore(true, 5, "", store)
	h.maxChats = 1
	h.Record(1, downloadHistoryEntry{MediaID: "old", At: time.Now().Add(-time.Hour)})
	h.Record(2, downloadHistoryEntry{MediaID: "new"})
	var rows int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM download_history WHERE chat_id = 1").Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("evicted chat should have no rows left, got %d", rows)
	}
}

func TestOpenStoreImportsLegacyJSONOnce(t *testing.T) {
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, "telegram-cache.json")
	stateFile := filepath.Join(dir, "telegram-cache.state.json")
	historyFile := filepath.Join(dir, "telegram-cache.history.json")
	withConfigForTest(t, func() {
		Config.TelegramStorageBackend = ""
		Config.TelegramDBFile = ""
		Config.TelegramStateFile = ""
		Config.TelegramHistoryPerChatLimit = 0
	})

	probe := &TelegramBot{}
	writeJSONForTest(t, cacheFile, telegramCacheFile{
		Version:   4,
		Items:     map[string]CachedAudio{probe.cacheKey("555", "alac", false): {FileID: "legacy-audio", Format: "alac"}},
		Documents: map[string]CachedDocument{"album:5|zip": {FileID: "legacy-doc"}},
	})
	writeJSONForTest(t, stateFile, telegramPersistedState{
		Version:      telegramStateVersion,
		ChatSettings: map[int64]ChatDownloadSettings{99: {Format: "aac"}},
	})
	writeJSONForTest(t, historyFile, downloadHistoryFile{
		Version: downloadHistoryVersion,
		ByChat:  map[int64][]downloadHistoryEntry{99: {{MediaID: "legacy", Success: true}}},
	})

	b := &TelegramBot{cacheFile: cacheFile, stateFile: stateFile}
	b.openStore(historyFile)
	if b.store == nil {
		t.Fatalf("expected sqlite store to be opened")
	}
	defer b.closeStore()
	b.loadCache()
	if entry, ok := b.cache[b.cacheKey("555", "alac", false)]; !ok || entry.FileID != "legacy-audio" {
		t.Fatalf("legacy audio cache not imported: %+v", b.cache)
	}
	if _, ok := b.getCachedDocument("album:5|zip"); !ok {
		t.Fatalf("legacy document cache not imported")
	}
	state, found, err := b.store.loadRuntimeState()
	if err != nil || !found || state.ChatSettings[99].Format != "aac" {
		t.Fatalf("legacy state not imported: found=%v err=%v state=%+v", found, err, state.ChatSettings)
	}
	h := newDownloadHistoryWithStore(true, 10, historyFile, b.store)
	if entries := h.Entries(99); len(entries) != 1 || entries[0].MediaID != "legacy" {
		t.Fatalf("legacy history not imported: %+v", entries)
	}
	for _, path := range []string{cacheFile, stateFile, historyFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should have been renamed after import (err=%v)", path, err)
		}
		if _, err := os.Stat(path + legacyMigratedSuffix); err != nil {
			t.Fatalf("backup %s%s missing: %v", path, legacyMigratedSuffix, err)
		}
	}

	// A stray JSON file appearing later must not be merged over live data.
	writeJSONForTest(t, cacheFile, telegramCacheFile{
		Version: 4,
		Items:   map[string]CachedAudio{probe.cacheKey("666", "alac", false): {FileID: "stray", Format: "alac"}},
	})
	b.closeStore()
	again := &TelegramBot{cacheFile: cacheFile, stateFile: stateFile}
	again.openStore(historyFile)
	defer again.closeStore()
	again.loadCache()
	if _, ok := again.cache[again.cacheKey("666", "alac", false)]; ok {
		t.Fatalf("legacy import ran twice")
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("stray JSON file should be left untouched: %v", err)
	}
}

func TestOpenStoreKeepsUnreadableLegacyFile(t *testing.T) {
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, "telegram-cache.json")
	withConfigForTest(t, func() {
		Config.TelegramStorageBackend = ""
		Config.TelegramDBFile = ""
		Config.TelegramStateFile = ""
	})
	if err := os.WriteFile(cacheFile, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	b := &TelegramBot{cacheFile: cacheFile, stateFile: filepath.Join(dir, "missing.state.json")}
	b.openStore("")
	defer b.closeStore()
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("unreadable legacy file must be kept for manual recovery: %v", err)
	}
	if _, done, _ := b.store.getMeta("legacy_import:cache"); done {
		t.Fatalf("failed import must not be marked done")
	}
}

func TestOpenStoreRespectsJSONBackend(t *testing.T) {
	dir := t.TempDir()
	withConfigForTest(t, func() { Config.TelegramStorageBackend = "json" })
	b := &TelegramBot{cacheFile: filepath.Join(dir, "telegram-cache.json")}
	b.openStore("")
	if b.store != nil {
		t.Fatalf("json backend must not open a sqlite store")
	}
	if _, err := os.Stat(filepath.Join(dir, defaultTelegramDBFileName)); !os.IsNotExist(err) {
		t.Fatalf("json backend must not create a database file (err=%v)", err)
	}
}

func TestTelegramPersistentFilesProtectsDatabaseSidecars(t *testing.T) {
	withConfigForTest(t, func() {
		Config.TelegramDBFile = ""
		Config.TelegramStateFile = ""
		Config.TelegramHistoryFile = ""
	})
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, "telegram-cache.json")
	protected := telegramPersistentFiles(cacheFile)
	dbFile := filepath.Join(dir, defaultTelegramDBFileName)
	for _, path := range []string{cacheFile, dbFile, dbFile + "-wal", dbFile + "-shm", cacheFile + legacyMigratedSuffix} {
		if !protected[filepath.Clean(path)] {
			t.Fatalf("%s should be protected from download cleanup", path)
		}
	}

	if err := os.WriteFile(dbFile+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	song := filepath.Join(dir, "song.m4a")
	if err := os.WriteFile(song, []byte("audio"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, entries, err := scanDownloadFolder(dir, cacheFile)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(entries) != 1 || entries[0].path != song {
		t.Fatalf("cleanup scan should only see the download, got %+v", entries)
	}
}

func writeJSONForTest(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
