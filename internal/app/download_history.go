package app

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	downloadHistoryVersion      = 1
	defaultHistoryPerChatLimit  = 30
	maxHistoryPerChatLimit      = 200
	historyMinSaveInterval      = 3 * time.Second
	downloadHistoryFileFallback = "telegram-history.json"
)

// downloadHistoryEntry is a single terminal download outcome for a chat.
type downloadHistoryEntry struct {
	RequestID  string    `json:"request_id,omitempty"`
	MediaType  string    `json:"media_type,omitempty"`
	MediaID    string    `json:"media_id,omitempty"`
	Result     string    `json:"result,omitempty"`
	Success    bool      `json:"success"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	At         time.Time `json:"at"`
}

type downloadHistoryFile struct {
	Version int                              `json:"version"`
	SavedAt time.Time                        `json:"saved_at"`
	ByChat  map[int64][]downloadHistoryEntry `json:"by_chat"`
}

// downloadHistory keeps a bounded, per-chat ring of recent download outcomes
// and persists them atomically to disk. It is self-contained: no long-lived
// goroutine is required. Writes are throttled so a burst of completions does
// not hammer the disk; a throttled write is deferred, not dropped.
type downloadHistory struct {
	enabled    bool
	perChatCap int
	filePath   string
	maxChats   int
	clock      func() time.Time

	// persistMu serializes snapshot+write so an older snapshot can never be
	// renamed over a newer one. Lock order: persistMu before mu.
	persistMu sync.Mutex

	mu          sync.Mutex
	byChat      map[int64][]downloadHistoryEntry
	lastSave    time.Time
	saveTimer   *time.Timer
	savePending bool

	// store, when set, replaces the JSON file: each Record is written as its
	// own row, so there is nothing to throttle or flush.
	store *telegramStore
}

func newDownloadHistory(enabled bool, perChatCap int, filePath string) *downloadHistory {
	return newDownloadHistoryWithStore(enabled, perChatCap, filePath, nil)
}

func newDownloadHistoryWithStore(enabled bool, perChatCap int, filePath string, store *telegramStore) *downloadHistory {
	if perChatCap <= 0 {
		perChatCap = defaultHistoryPerChatLimit
	}
	if perChatCap > maxHistoryPerChatLimit {
		perChatCap = maxHistoryPerChatLimit
	}
	h := &downloadHistory{
		enabled:    enabled,
		perChatCap: perChatCap,
		filePath:   strings.TrimSpace(filePath),
		maxChats:   2000,
		clock:      time.Now,
		byChat:     make(map[int64][]downloadHistoryEntry),
		store:      store,
	}
	if store != nil {
		h.loadFromStore()
	} else {
		h.load()
	}
	return h
}

func (h *downloadHistory) loadFromStore() {
	loaded, err := h.store.loadHistory(h.perChatCap)
	if err != nil {
		fmt.Println("download history load error:", err)
		return
	}
	h.mu.Lock()
	h.byChat = loaded
	h.mu.Unlock()
}

func (h *downloadHistory) now() time.Time {
	if h != nil && h.clock != nil {
		return h.clock()
	}
	return time.Now()
}

// Record appends a terminal outcome for chatID and persists (throttled).
func (h *downloadHistory) Record(chatID int64, entry downloadHistoryEntry) {
	if h == nil || !h.enabled || chatID == 0 {
		return
	}
	if entry.At.IsZero() {
		entry.At = h.now()
	}
	h.mu.Lock()
	list := append(h.byChat[chatID], entry)
	if len(list) > h.perChatCap {
		list = list[len(list)-h.perChatCap:]
	}
	// Copy into a fresh backing array so trimming can release memory.
	trimmed := make([]downloadHistoryEntry, len(list))
	copy(trimmed, list)
	h.byChat[chatID] = trimmed
	evicted := h.evictIdleChatsLocked()
	if h.store != nil {
		h.mu.Unlock()
		h.persistMu.Lock()
		defer h.persistMu.Unlock()
		if err := h.store.appendHistory(chatID, entry, h.perChatCap); err != nil {
			fmt.Println("download history write error:", err)
		}
		if err := h.store.deleteHistoryChats(evicted); err != nil {
			fmt.Println("download history evict error:", err)
		}
		return
	}
	sinceSave := h.now().Sub(h.lastSave)
	if sinceSave < historyMinSaveInterval {
		// Throttled: schedule one trailing write so this entry still reaches
		// disk without waiting for the next Record or a shutdown Flush.
		if !h.savePending {
			h.savePending = true
			h.saveTimer = time.AfterFunc(historyMinSaveInterval-sinceSave, h.Flush)
		}
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	h.Flush()
}

// Entries returns a copy of the chat's history, newest first.
func (h *downloadHistory) Entries(chatID int64) []downloadHistoryEntry {
	if h == nil || !h.enabled {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	src := h.byChat[chatID]
	if len(src) == 0 {
		return nil
	}
	out := make([]downloadHistoryEntry, len(src))
	for i := range src {
		out[i] = src[len(src)-1-i]
	}
	return out
}

// Stats returns aggregate counts for a chat's retained history.
func (h *downloadHistory) Stats(chatID int64) (total, success, failed int) {
	if h == nil || !h.enabled {
		return 0, 0, 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.byChat[chatID] {
		total++
		if e.Success {
			success++
		} else {
			failed++
		}
	}
	return total, success, failed
}

// Flush forces a synchronous persist of the current state (best effort).
func (h *downloadHistory) Flush() {
	if h == nil || !h.enabled || h.store != nil {
		return
	}
	h.persistMu.Lock()
	defer h.persistMu.Unlock()
	h.mu.Lock()
	if h.saveTimer != nil {
		h.saveTimer.Stop()
		h.saveTimer = nil
	}
	h.savePending = false
	h.lastSave = h.now()
	snapshot := h.snapshotLocked()
	h.mu.Unlock()
	h.persist(snapshot)
}

// evictIdleChatsLocked drops the least recently active chats beyond maxChats
// and returns their IDs.
func (h *downloadHistory) evictIdleChatsLocked() []int64 {
	if len(h.byChat) <= h.maxChats {
		return nil
	}
	type chatAge struct {
		id   int64
		last time.Time
	}
	ages := make([]chatAge, 0, len(h.byChat))
	for id, list := range h.byChat {
		last := time.Time{}
		if len(list) > 0 {
			last = list[len(list)-1].At
		}
		ages = append(ages, chatAge{id: id, last: last})
	}
	sort.Slice(ages, func(i, j int) bool { return ages[i].last.Before(ages[j].last) })
	var evicted []int64
	for i := 0; i < len(ages) && len(h.byChat) > h.maxChats; i++ {
		delete(h.byChat, ages[i].id)
		evicted = append(evicted, ages[i].id)
	}
	return evicted
}

func (h *downloadHistory) snapshotLocked() downloadHistoryFile {
	byChat := make(map[int64][]downloadHistoryEntry, len(h.byChat))
	for id, list := range h.byChat {
		cp := make([]downloadHistoryEntry, len(list))
		copy(cp, list)
		byChat[id] = cp
	}
	return downloadHistoryFile{
		Version: downloadHistoryVersion,
		SavedAt: h.now(),
		ByChat:  byChat,
	}
}

func (h *downloadHistory) persist(snapshot downloadHistoryFile) {
	if h == nil || strings.TrimSpace(h.filePath) == "" {
		return
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		fmt.Println("download history marshal error:", err)
		return
	}
	if err := atomicWriteTelegramCacheFile(h.filePath, data); err != nil {
		fmt.Println("download history write error:", err)
	}
}

func (h *downloadHistory) load() {
	if h == nil || strings.TrimSpace(h.filePath) == "" {
		return
	}
	data, err := os.ReadFile(h.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Println("download history read error:", err)
		}
		return
	}
	var parsed downloadHistoryFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		fmt.Println("download history parse error:", err)
		return
	}
	if parsed.ByChat == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, list := range parsed.ByChat {
		if len(list) > h.perChatCap {
			list = list[len(list)-h.perChatCap:]
		}
		cp := make([]downloadHistoryEntry, len(list))
		copy(cp, list)
		h.byChat[id] = cp
	}
}
