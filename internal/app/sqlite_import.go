package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const legacyMigratedSuffix = ".migrated"

// importLegacyJSONIntoStore copies the legacy JSON cache, runtime state and
// download history into the SQLite store the first time it is used. Each file
// is imported in its own transaction together with a meta marker, so a crash
// mid-import is retried on the next start without duplicating rows. Imported
// files are renamed with a ".migrated" suffix and kept as a backup; renaming
// them back and setting telegram-storage-backend: json restores the old mode.
func (b *TelegramBot) importLegacyJSONIntoStore(historyFile string, historyCap int) {
	if b == nil || b.store == nil {
		return
	}
	b.importLegacyFile("cache", b.cacheFile, func(tx *sql.Tx, path string) error {
		audio, docs, videos, _, ok := b.readLegacyCacheFile(path)
		if !ok {
			return fmt.Errorf("unreadable cache file")
		}
		return applyCacheChangesTx(tx, fullCacheChanges(audio, docs, videos))
	})
	b.importLegacyFile("state", b.stateFile, func(tx *sql.Tx, path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return nil
		}
		var state telegramPersistedState
		if err := json.Unmarshal(data, &state); err != nil {
			return err
		}
		return saveRuntimeStateTx(tx, data)
	})
	b.importLegacyFile("history", historyFile, func(tx *sql.Tx, path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var parsed downloadHistoryFile
		if err := json.Unmarshal(data, &parsed); err != nil {
			return err
		}
		for chatID, entries := range parsed.ByChat {
			if len(entries) > historyCap {
				entries = entries[len(entries)-historyCap:]
			}
			for _, entry := range entries {
				if err := appendHistoryTx(tx, chatID, entry, 0); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// importLegacyFile runs one import unless it already happened. A missing
// file is recorded as imported so a later stray copy is never merged over
// live data.
func (b *TelegramBot) importLegacyFile(name string, path string, load func(tx *sql.Tx, path string) error) {
	metaKey := "legacy_import:" + name
	if _, done, err := b.store.getMeta(metaKey); err != nil || done {
		if err != nil {
			b.logLegacyImport("telegram sqlite: checking %s import failed: %v", name, err)
		}
		return
	}
	path = strings.TrimSpace(path)
	exists := false
	if path != "" {
		path = filepath.Clean(path)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			exists = true
		}
	}

	tx, err := b.store.db.Begin()
	if err != nil {
		b.logLegacyImport("telegram sqlite: %s import failed: %v", name, err)
		return
	}
	marker := "absent"
	if exists {
		if err := load(tx, path); err != nil {
			_ = tx.Rollback()
			b.logLegacyImport("telegram sqlite: %s import from %s failed, keeping the JSON file: %v", name, path, err)
			return
		}
		marker = path
	}
	if err := setMetaTx(tx, metaKey, marker); err != nil {
		_ = tx.Rollback()
		b.logLegacyImport("telegram sqlite: %s import failed: %v", name, err)
		return
	}
	if err := tx.Commit(); err != nil {
		b.logLegacyImport("telegram sqlite: %s import failed: %v", name, err)
		return
	}
	if !exists {
		return
	}
	fmt.Printf("telegram sqlite: imported %s from %s\n", name, path)
	if err := os.Rename(path, path+legacyMigratedSuffix); err != nil {
		b.logLegacyImport("telegram sqlite: imported %s but could not rename %s: %v", name, path, err)
	}
}

func (b *TelegramBot) logLegacyImport(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(msg)
	appendRuntimeErrorLog(msg)
}

// openStore opens the SQLite backend when configured and imports legacy JSON
// files into it. If the database cannot be opened the bot keeps running on
// the JSON files rather than refusing to start.
func (b *TelegramBot) openStore(historyFile string) {
	if b == nil || telegramStorageBackend() != storageBackendSQLite {
		return
	}
	dbFile := resolveTelegramDBFile(b.cacheFile)
	store, err := openTelegramStore(dbFile)
	if err != nil {
		b.logLegacyImport("telegram sqlite: opening %s failed, falling back to JSON files: %v", dbFile, err)
		return
	}
	b.store = store
	fmt.Printf("Telegram storage: sqlite (%s)\n", dbFile)
	historyCap := Config.TelegramHistoryPerChatLimit
	if historyCap <= 0 {
		historyCap = defaultHistoryPerChatLimit
	}
	if historyCap > maxHistoryPerChatLimit {
		historyCap = maxHistoryPerChatLimit
	}
	b.importLegacyJSONIntoStore(historyFile, historyCap)
}

func (b *TelegramBot) closeStore() {
	if b == nil || b.store == nil {
		return
	}
	if err := b.store.Close(); err != nil {
		b.logLegacyImport("telegram sqlite: closing %s failed: %v", b.store.path, err)
	}
}
