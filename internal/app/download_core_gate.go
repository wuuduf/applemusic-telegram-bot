package app

import (
	"strings"
	"sync"
)

const maxDownloadCoreConcurrency = 4

// downloadCoreGate limits how many downloads run in the download core
// (manifest handling, decryption, MP4Box/ffmpeg post-processing) at once. With
// the default limit of 1 it behaves exactly like a mutex. With a higher limit
// different items download in parallel, while two downloads of the same item
// (which write the same files) still run one after the other.
type downloadCoreGate struct {
	slots chan struct{}

	mu   sync.Mutex
	keys map[string]*downloadCoreKeyLock
}

type downloadCoreKeyLock struct {
	mu      sync.Mutex
	waiters int
}

func newDownloadCoreGate(limit int) *downloadCoreGate {
	if limit < 1 {
		limit = 1
	}
	if limit > maxDownloadCoreConcurrency {
		limit = maxDownloadCoreConcurrency
	}
	return &downloadCoreGate{
		slots: make(chan struct{}, limit),
		keys:  make(map[string]*downloadCoreKeyLock),
	}
}

// acquire blocks until the item identified by key may download and returns
// the func that gives the slot back. An empty key only takes a slot.
func (g *downloadCoreGate) acquire(key string) func() {
	key = strings.TrimSpace(key)
	var keyLock *downloadCoreKeyLock
	if key != "" {
		g.mu.Lock()
		keyLock = g.keys[key]
		if keyLock == nil {
			keyLock = &downloadCoreKeyLock{}
			g.keys[key] = keyLock
		}
		keyLock.waiters++
		g.mu.Unlock()
		// Key before slot: a download waiting for its twin must not hold a
		// slot that another item could use.
		keyLock.mu.Lock()
	}
	g.slots <- struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-g.slots
			if keyLock == nil {
				return
			}
			keyLock.mu.Unlock()
			g.mu.Lock()
			keyLock.waiters--
			if keyLock.waiters == 0 {
				delete(g.keys, key)
			}
			g.mu.Unlock()
		})
	}
}

func telegramDownloadConcurrency() int {
	n := Config.TelegramDownloadConcurrency
	if n < 1 {
		return 1
	}
	if n > maxDownloadCoreConcurrency {
		return maxDownloadCoreConcurrency
	}
	return n
}

// acquireDownloadCore takes a download-core slot for one media item. Bots
// built without a gate (tests) get a serial one on first use.
func (b *TelegramBot) acquireDownloadCore(mediaType string, mediaID string) func() {
	b.downloadCoreOnce.Do(func() {
		if b.downloadCore == nil {
			b.downloadCore = newDownloadCoreGate(1)
		}
	})
	key := ""
	if strings.TrimSpace(mediaID) != "" {
		key = strings.TrimSpace(mediaType) + ":" + strings.TrimSpace(mediaID)
	}
	return b.downloadCore.acquire(key)
}
