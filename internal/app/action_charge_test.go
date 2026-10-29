package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newChargeTestBot(t *testing.T, queueCap int) *TelegramBot {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b := &TelegramBot{
		token:          "test-token",
		apiBase:        server.URL,
		client:         server.Client(),
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
		downloadQueue:  make(chan *downloadRequest, queueCap),
		workerLimit:    1,
		activeRequests: make(map[string]telegramPersistedRequest),
		chatSettings:   make(map[int64]ChatDownloadSettings),
		userLimiter:    newUserRateLimiter(true, 3600, 1, 0),
	}
	b.queueCond = sync.NewCond(&b.queueMu)
	return b
}

func chargeTestRequest(id string) *downloadRequest {
	return &downloadRequest{
		chatID:    42,
		requestID: id,
		taskType:  telegramTaskCover,
		mediaType: mediaTypeAlbum,
		mediaID:   id,
		run:       func(*TelegramBot, context.Context) error { return nil },
	}
}

func TestActionChargeRefundedWhenQueueIsFull(t *testing.T) {
	b := newChargeTestBot(t, 0) // a zero-capacity queue rejects everything
	if !b.allowUserAction(42, 7, 1) {
		t.Fatalf("first action should be allowed")
	}
	if b.enqueueTaskRequest(chargeTestRequest("r1"), "", "") {
		t.Fatalf("enqueue into a full queue should fail")
	}
	b.finishActionCharge(42)
	if ok, _, _ := b.userLimiter.Allow(7); !ok {
		t.Fatalf("the rejected action should have been refunded")
	}
}

func TestActionChargeKeptWhenSomethingWasQueued(t *testing.T) {
	b := newChargeTestBot(t, 1)
	if !b.allowUserAction(42, 7, 1) {
		t.Fatalf("first action should be allowed")
	}
	if !b.enqueueTaskRequest(chargeTestRequest("r1"), "", "") {
		t.Fatalf("enqueue should succeed")
	}
	_ = b.enqueueTaskRequest(chargeTestRequest("r2"), "", "") // queue now full
	b.finishActionCharge(42)
	if ok, _, _ := b.userLimiter.Allow(7); ok {
		t.Fatalf("an action that queued a task must stay charged")
	}
}

func TestActionChargeKeptWhenNothingWasAttempted(t *testing.T) {
	// e.g. the message only opened a transfer-mode panel; the task is queued
	// later from the callback, so the charge must not be refunded here.
	b := newChargeTestBot(t, 1)
	if !b.allowUserAction(42, 7, 1) {
		t.Fatalf("first action should be allowed")
	}
	b.finishActionCharge(42)
	if ok, _, _ := b.userLimiter.Allow(7); ok {
		t.Fatalf("a panel-only action must stay charged")
	}
}

func TestEnqueueWithoutActiveChargeDoesNotRefund(t *testing.T) {
	b := newChargeTestBot(t, 0)
	if ok, _, _ := b.userLimiter.Allow(7); !ok {
		t.Fatalf("setup: first action should be allowed")
	}
	_ = b.enqueueTaskRequest(chargeTestRequest("cb"), "", "") // e.g. from a callback
	b.finishActionCharge(42)
	if ok, _, _ := b.userLimiter.Allow(7); ok {
		t.Fatalf("a rejection outside a charged message must not refund anything")
	}
}

func TestUserRateLimiterRefundAndStatus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newUserRateLimiter(true, 10, 2, 5)
	l.now = func() time.Time { return now }

	if s := l.Status(1); !s.Enabled || s.Available != 2 || s.DailyUsed != 0 {
		t.Fatalf("fresh user status: %+v", s)
	}
	l.Allow(1)
	l.Allow(1)
	s := l.Status(1)
	if s.Available != 0 || s.DailyUsed != 2 || s.NextIn != 10*time.Second || s.DailyResetIn != 24*time.Hour {
		t.Fatalf("after two actions: %+v", s)
	}
	now = now.Add(4 * time.Second)
	if s := l.Status(1); s.NextIn != 6*time.Second {
		t.Fatalf("next refill should be 6s away, got %s", s.NextIn)
	}
	l.Refund(1)
	if s := l.Status(1); s.Available != 1 || s.DailyUsed != 1 {
		t.Fatalf("after refund: %+v", s)
	}
	for i := 0; i < 5; i++ {
		l.Refund(1)
	}
	if s := l.Status(1); s.Available != 2 || s.DailyUsed != 0 {
		t.Fatalf("refunds must not exceed burst or go below zero: %+v", s)
	}
	if s := (*userRateLimiter)(nil).Status(1); s.Enabled {
		t.Fatalf("nil limiter should report disabled")
	}
}

func TestFormatQuotaText(t *testing.T) {
	b := &TelegramBot{
		chatSettings: map[int64]ChatDownloadSettings{1: {Language: telegramLanguageEn}},
		adminUsers:   map[int64]bool{9: true},
		userLimiter:  newUserRateLimiter(true, 5, 3, 10),
	}
	b.userLimiter.Allow(7)
	zh := b.formatQuotaText(2, 7)
	if !strings.Contains(zh, "当前可用：2/3") || !strings.Contains(zh, "24 小时内已用：1/10") {
		t.Fatalf("unexpected zh quota text:\n%s", zh)
	}
	en := b.formatQuotaText(1, 7)
	if !strings.Contains(en, "Available now: 2/3") || !strings.Contains(en, "Used in the last 24h: 1/10") {
		t.Fatalf("unexpected en quota text:\n%s", en)
	}
	if got := b.formatQuotaText(2, 9); !strings.Contains(got, "管理员") {
		t.Fatalf("admin quota text: %q", got)
	}
	b.userLimiter = nil
	if got := b.formatQuotaText(1, 7); !strings.Contains(got, "disabled") {
		t.Fatalf("disabled quota text: %q", got)
	}
}
