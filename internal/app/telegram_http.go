package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func resolveTelegramProxy() (func(*http.Request) (*url.URL, error), string) {
	if Config.TelegramNoProxy {
		return nil, "disabled"
	}
	rawProxy := strings.TrimSpace(Config.TelegramProxyURL)
	if rawProxy == "" {
		return http.ProxyFromEnvironment, "env"
	}
	parsed, err := url.Parse(rawProxy)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		fmt.Printf("Warning: invalid telegram-proxy-url %q. Falling back to environment proxy.\n", rawProxy)
		return http.ProxyFromEnvironment, "env(fallback)"
	}
	return http.ProxyURL(parsed), parsed.Redacted()
}

func newTelegramHTTPClient(timeout time.Duration, proxyFunc func(*http.Request) (*url.URL, error)) *http.Client {
	transport := &http.Transport{
		Proxy: proxyFunc,
		DialContext: (&net.Dialer{
			Timeout:   telegramDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// Force HTTP/1.1 for better compatibility with some proxy/tunnel paths.
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   telegramTLSHandshakeTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func telegramDownloadMaxBytes() int64 {
	gb := Config.TelegramDownloadMaxGB
	if gb <= 0 {
		gb = defaultTelegramDownloadMaxGB
	}
	return int64(gb) * 1024 * 1024 * 1024
}

type telegramSendLimiter struct {
	globalInterval time.Duration
	chatInterval   time.Duration

	mu           sync.Mutex
	lastAll      time.Time
	lastChat     map[int64]time.Time
	blockedUntil time.Time
	nowFn        func() time.Time
}

func newTelegramSendLimiter(globalInterval time.Duration, chatInterval time.Duration) *telegramSendLimiter {
	if globalInterval < 0 {
		globalInterval = 0
	}
	if chatInterval < 0 {
		chatInterval = 0
	}
	if globalInterval == 0 && chatInterval == 0 {
		return nil
	}
	return &telegramSendLimiter{
		globalInterval: globalInterval,
		chatInterval:   chatInterval,
		lastChat:       make(map[int64]time.Time),
		nowFn:          time.Now,
	}
}

func (l *telegramSendLimiter) wait(ctx context.Context, chatID int64) error {
	if l == nil {
		return nil
	}
	for {
		wait := l.reserve(chatID)
		if wait <= 0 {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *telegramSendLimiter) reserve(chatID int64) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.nowFn()
	wait := l.nextWaitLocked(now, chatID)
	if wait <= 0 {
		if l.globalInterval > 0 {
			l.lastAll = now
		}
		if chatID != 0 && l.chatInterval > 0 {
			l.lastChat[chatID] = now
		}
	}
	return wait
}

func (l *telegramSendLimiter) tryReserve(chatID int64) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.nowFn()
	if l.nextWaitLocked(now, chatID) > 0 {
		return false
	}
	if l.globalInterval > 0 {
		l.lastAll = now
	}
	if chatID != 0 && l.chatInterval > 0 {
		l.lastChat[chatID] = now
	}
	return true
}

func (l *telegramSendLimiter) nextWait(chatID int64) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nextWaitLocked(l.nowFn(), chatID)
}

func (l *telegramSendLimiter) nextWaitLocked(now time.Time, chatID int64) time.Duration {
	wait := time.Duration(0)
	if !l.blockedUntil.IsZero() {
		if remain := l.blockedUntil.Sub(now); remain > wait {
			wait = remain
		}
	}
	if l.globalInterval > 0 && !l.lastAll.IsZero() {
		if remain := l.globalInterval - now.Sub(l.lastAll); remain > wait {
			wait = remain
		}
	}
	if chatID != 0 && l.chatInterval > 0 {
		if last, ok := l.lastChat[chatID]; ok && !last.IsZero() {
			if remain := l.chatInterval - now.Sub(last); remain > wait {
				wait = remain
			}
		}
	}
	return wait
}

func (l *telegramSendLimiter) blockFor(duration time.Duration) {
	if l == nil || duration <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	until := l.nowFn().Add(duration)
	if until.After(l.blockedUntil) {
		l.blockedUntil = until
	}
}

func parsePositiveIntEnv(name string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func telegramSendGlobalInterval() time.Duration {
	if ms, ok := parsePositiveIntEnv("AMDL_TELEGRAM_SEND_GLOBAL_INTERVAL_MS"); ok {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultTelegramSendGlobalInterval
}

func telegramSendChatInterval() time.Duration {
	if ms, ok := parsePositiveIntEnv("AMDL_TELEGRAM_SEND_CHAT_INTERVAL_MS"); ok {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultTelegramSendChatInterval
}

func buildIDSet(ids []int64) map[int64]bool {
	result := make(map[int64]bool)
	for _, id := range ids {
		if id == 0 {
			continue
		}
		result[id] = true
	}
	return result
}
