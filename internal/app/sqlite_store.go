package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	telegramStoreSchemaVersion = 1

	storageBackendSQLite = "sqlite"
	storageBackendJSON   = "json"

	defaultTelegramDBFileName = "telegram.db"

	cacheKindAudio    = "audio"
	cacheKindDocument = "document"
	cacheKindVideo    = "video"
)

// telegramStore persists the bot's cache, runtime state and download history
// in a single SQLite database. Cache entries and history rows are written
// individually, so a change no longer rewrites the whole data set the way the
// JSON files do. Runtime state is small and changes as a unit, so it is kept
// as one JSON document in a single-row table.
type telegramStore struct {
	db   *sql.DB
	path string
}

type cacheEntryRef struct {
	kind string
	key  string
}

// cacheChange is one pending cache write; a nil value deletes the entry.
type cacheChange struct {
	ref   cacheEntryRef
	value any
}

func telegramStorageBackend() string {
	switch strings.ToLower(strings.TrimSpace(Config.TelegramStorageBackend)) {
	case storageBackendJSON:
		return storageBackendJSON
	default:
		return storageBackendSQLite
	}
}

func resolveTelegramDBFile(cacheFile string) string {
	if raw := strings.TrimSpace(Config.TelegramDBFile); raw != "" {
		return filepath.Clean(raw)
	}
	dir := filepath.Dir(filepath.Clean(strings.TrimSpace(cacheFile)))
	return filepath.Join(dir, defaultTelegramDBFileName)
}

// sqliteSidecarPaths lists the database file and the files SQLite keeps next
// to it, which must never be treated as disposable downloads.
func sqliteSidecarPaths(dbPath string) []string {
	return []string{dbPath, dbPath + "-wal", dbPath + "-shm", dbPath + "-journal"}
}

func openTelegramStore(path string) (*telegramStore, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return nil, fmt.Errorf("sqlite store path is empty")
	}
	if err := ensureNonSymlinkRegularFile(path); err != nil {
		return nil, err
	}
	if _, err := ensureCacheDirectory(path); err != nil {
		return nil, err
	}
	// Create the file up front so it starts private instead of inheriting
	// the process umask.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, telegramCacheFilePerm)
	if err != nil {
		return nil, err
	}
	_ = file.Close()

	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: writes are serialized anyway, and it keeps per-connection
	// pragmas in effect for every statement.
	db.SetMaxOpenConns(1)
	store := &telegramStore{db: db, path: path}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *telegramStore) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > telegramStoreSchemaVersion {
		return fmt.Errorf("sqlite store %s has schema version %d, newer than supported %d", s.path, version, telegramStoreSchemaVersion)
	}
	if version == telegramStoreSchemaVersion {
		return nil
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS cache_entries (
			kind       TEXT NOT NULL,
			key        TEXT NOT NULL,
			value      TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (kind, key)
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS runtime_state (
			id       INTEGER PRIMARY KEY CHECK (id = 1),
			value    TEXT NOT NULL,
			saved_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS download_history (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			at      INTEGER NOT NULL,
			value   TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS download_history_chat ON download_history (chat_id, id)`,
		fmt.Sprintf("PRAGMA user_version = %d", telegramStoreSchemaVersion),
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlite store migrate: %w", err)
		}
	}
	return tx.Commit()
}

func (s *telegramStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *telegramStore) getMeta(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func setMetaTx(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec("INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// --- cache ---------------------------------------------------------------

func (s *telegramStore) loadCache() (map[string]CachedAudio, map[string]CachedDocument, map[string]CachedVideo, error) {
	audio := make(map[string]CachedAudio)
	docs := make(map[string]CachedDocument)
	videos := make(map[string]CachedVideo)
	rows, err := s.db.Query("SELECT kind, key, value FROM cache_entries")
	if err != nil {
		return audio, docs, videos, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, key, value string
		if err := rows.Scan(&kind, &key, &value); err != nil {
			return audio, docs, videos, err
		}
		switch kind {
		case cacheKindAudio:
			var entry CachedAudio
			if json.Unmarshal([]byte(value), &entry) == nil {
				audio[key] = entry
			}
		case cacheKindDocument:
			var entry CachedDocument
			if json.Unmarshal([]byte(value), &entry) == nil {
				docs[key] = entry
			}
		case cacheKindVideo:
			var entry CachedVideo
			if json.Unmarshal([]byte(value), &entry) == nil {
				videos[key] = entry
			}
		}
	}
	return audio, docs, videos, rows.Err()
}

func applyCacheChangesTx(tx *sql.Tx, changes []cacheChange) error {
	now := time.Now().Unix()
	for _, change := range changes {
		if change.value == nil {
			if _, err := tx.Exec("DELETE FROM cache_entries WHERE kind = ? AND key = ?", change.ref.kind, change.ref.key); err != nil {
				return err
			}
			continue
		}
		data, err := json.Marshal(change.value)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO cache_entries (kind, key, value, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(kind, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			change.ref.kind, change.ref.key, string(data), now); err != nil {
			return err
		}
	}
	return nil
}

func (s *telegramStore) applyCacheChanges(changes []cacheChange) error {
	if len(changes) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := applyCacheChangesTx(tx, changes); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func fullCacheChanges(audio map[string]CachedAudio, docs map[string]CachedDocument, videos map[string]CachedVideo) []cacheChange {
	changes := make([]cacheChange, 0, len(audio)+len(docs)+len(videos))
	for key, entry := range audio {
		changes = append(changes, cacheChange{ref: cacheEntryRef{cacheKindAudio, key}, value: entry})
	}
	for key, entry := range docs {
		changes = append(changes, cacheChange{ref: cacheEntryRef{cacheKindDocument, key}, value: entry})
	}
	for key, entry := range videos {
		changes = append(changes, cacheChange{ref: cacheEntryRef{cacheKindVideo, key}, value: entry})
	}
	return changes
}

// replaceCache swaps the whole cache for the given maps in one transaction.
func (s *telegramStore) replaceCache(audio map[string]CachedAudio, docs map[string]CachedDocument, videos map[string]CachedVideo) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM cache_entries"); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := applyCacheChangesTx(tx, fullCacheChanges(audio, docs, videos)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// --- runtime state -------------------------------------------------------

func (s *telegramStore) loadRuntimeState() (telegramPersistedState, bool, error) {
	state := telegramPersistedState{}
	var value string
	err := s.db.QueryRow("SELECT value FROM runtime_state WHERE id = 1").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	if err := json.Unmarshal([]byte(value), &state); err != nil {
		return state, false, err
	}
	return state, true, nil
}

func saveRuntimeStateTx(tx *sql.Tx, payload []byte) error {
	_, err := tx.Exec(`INSERT INTO runtime_state (id, value, saved_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET value = excluded.value, saved_at = excluded.saved_at`,
		string(payload), time.Now().Unix())
	return err
}

func (s *telegramStore) saveRuntimeState(payload []byte) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := saveRuntimeStateTx(tx, payload); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// --- download history ----------------------------------------------------

// loadHistory returns up to perChat most recent entries per chat, oldest first.
func (s *telegramStore) loadHistory(perChat int) (map[int64][]downloadHistoryEntry, error) {
	out := make(map[int64][]downloadHistoryEntry)
	rows, err := s.db.Query(`SELECT chat_id, value FROM (
			SELECT chat_id, id, value, ROW_NUMBER() OVER (PARTITION BY chat_id ORDER BY id DESC) AS rn
			FROM download_history
		) WHERE rn <= ? ORDER BY chat_id, id`, perChat)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var chatID int64
		var value string
		if err := rows.Scan(&chatID, &value); err != nil {
			return out, err
		}
		var entry downloadHistoryEntry
		if json.Unmarshal([]byte(value), &entry) == nil {
			out[chatID] = append(out[chatID], entry)
		}
	}
	return out, rows.Err()
}

func appendHistoryTx(tx *sql.Tx, chatID int64, entry downloadHistoryEntry, keep int) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO download_history (chat_id, at, value) VALUES (?, ?, ?)", chatID, entry.At.Unix(), string(data)); err != nil {
		return err
	}
	if keep > 0 {
		if _, err := tx.Exec(`DELETE FROM download_history WHERE chat_id = ? AND id NOT IN (
				SELECT id FROM download_history WHERE chat_id = ? ORDER BY id DESC LIMIT ?)`, chatID, chatID, keep); err != nil {
			return err
		}
	}
	return nil
}

// appendHistory records one entry and trims the chat to its newest keep rows.
func (s *telegramStore) appendHistory(chatID int64, entry downloadHistoryEntry, keep int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := appendHistoryTx(tx, chatID, entry, keep); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *telegramStore) deleteHistoryChats(chatIDs []int64) error {
	if len(chatIDs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, id := range chatIDs {
		if _, err := tx.Exec("DELETE FROM download_history WHERE chat_id = ?", id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
