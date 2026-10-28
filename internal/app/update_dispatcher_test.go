package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func chatUpdateForTest(updateID int, chatID int64) Update {
	msg := &Message{MessageID: updateID}
	msg.Chat.ID = chatID
	return Update{UpdateID: updateID, Message: msg}
}

func waitForTest(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestUpdateDispatchKey(t *testing.T) {
	if got := updateDispatchKey(chatUpdateForTest(1, -100)); got != -100 {
		t.Fatalf("message key = %d, want -100", got)
	}
	cbMsg := &Message{}
	cbMsg.Chat.ID = 55
	if got := updateDispatchKey(Update{CallbackQuery: &CallbackQuery{Message: cbMsg, From: &User{ID: 9}}}); got != 55 {
		t.Fatalf("callback key = %d, want chat 55", got)
	}
	if got := updateDispatchKey(Update{CallbackQuery: &CallbackQuery{From: &User{ID: 9}}}); got != 9 {
		t.Fatalf("inline-message callback key = %d, want sender 9", got)
	}
	if got := updateDispatchKey(Update{InlineQuery: &InlineQuery{From: &User{ID: 7}}}); got != 7 {
		t.Fatalf("inline query key = %d, want sender 7", got)
	}
}

func TestUpdateDispatcherKeepsPerChatOrder(t *testing.T) {
	var mu sync.Mutex
	seen := map[int64][]int{}
	d := newUpdateDispatcher(context.Background(), 4, func(upd Update) {
		// Vary handler latency so out-of-order execution would show up.
		time.Sleep(time.Duration(upd.UpdateID%3) * time.Millisecond)
		mu.Lock()
		seen[upd.Message.Chat.ID] = append(seen[upd.Message.Chat.ID], upd.UpdateID)
		mu.Unlock()
	})
	for i := 0; i < 60; i++ {
		d.Submit(chatUpdateForTest(i, int64(i%3)))
	}
	if !d.Close(2 * time.Second) {
		t.Fatalf("dispatcher did not drain")
	}
	for chat, ids := range seen {
		if len(ids) != 20 {
			t.Fatalf("chat %d handled %d updates, want 20", chat, len(ids))
		}
		for i := 1; i < len(ids); i++ {
			if ids[i] < ids[i-1] {
				t.Fatalf("chat %d handled out of order: %v", chat, ids)
			}
		}
	}
}

func TestUpdateDispatcherSlowChatDoesNotBlockOthers(t *testing.T) {
	release := make(chan struct{})
	var otherHandled atomic.Bool
	d := newUpdateDispatcher(context.Background(), 4, func(upd Update) {
		if upd.Message.Chat.ID == 1 {
			<-release
			return
		}
		otherHandled.Store(true)
	})
	d.Submit(chatUpdateForTest(1, 1))
	d.Submit(chatUpdateForTest(2, 2))
	waitForTest(t, "other chat to be handled while chat 1 is blocked", otherHandled.Load)
	close(release)
	if !d.Close(2 * time.Second) {
		t.Fatalf("dispatcher did not drain")
	}
}

func TestUpdateDispatcherCapsConcurrency(t *testing.T) {
	release := make(chan struct{})
	var running, peak atomic.Int32
	d := newUpdateDispatcher(context.Background(), 2, func(Update) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		running.Add(-1)
	})
	for i := 0; i < 5; i++ {
		d.Submit(chatUpdateForTest(i, int64(i)))
	}
	waitForTest(t, "two handlers to start", func() bool { return running.Load() == 2 })
	time.Sleep(20 * time.Millisecond)
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak concurrency = %d, want 2", got)
	}
	close(release)
	if !d.Close(2 * time.Second) {
		t.Fatalf("dispatcher did not drain")
	}
}

func TestUpdateDispatcherDropsWhenChatBacklogFull(t *testing.T) {
	release := make(chan struct{})
	var handled atomic.Int32
	d := newUpdateDispatcher(context.Background(), 1, func(Update) {
		<-release
		handled.Add(1)
	})
	d.perChat = 2
	if !d.Submit(chatUpdateForTest(1, 1)) || !d.Submit(chatUpdateForTest(2, 1)) {
		t.Fatalf("updates within the chat backlog should be accepted")
	}
	if d.Submit(chatUpdateForTest(3, 1)) {
		t.Fatalf("update beyond the chat backlog should be dropped")
	}
	if !d.Submit(chatUpdateForTest(4, 2)) {
		t.Fatalf("another chat should still be accepted")
	}
	close(release)
	if !d.Close(2 * time.Second) {
		t.Fatalf("dispatcher did not drain")
	}
	if got := handled.Load(); got != 3 {
		t.Fatalf("handled %d updates, want 3", got)
	}
}

func TestUpdateDispatcherBlocksSubmitWhenBacklogFull(t *testing.T) {
	release := make(chan struct{})
	d := newUpdateDispatcher(context.Background(), 2, func(Update) { <-release })
	d.maxTotal = 2
	d.Submit(chatUpdateForTest(1, 1))
	d.Submit(chatUpdateForTest(2, 2))

	submitted := make(chan struct{})
	go func() {
		d.Submit(chatUpdateForTest(3, 3))
		close(submitted)
	}()
	select {
	case <-submitted:
		t.Fatalf("Submit should block while the total backlog is full")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-submitted:
	case <-time.After(2 * time.Second):
		t.Fatalf("Submit did not resume after the backlog drained")
	}
	if !d.Close(2 * time.Second) {
		t.Fatalf("dispatcher did not drain")
	}
}

func TestUpdateDispatcherSkipsQueuedUpdatesOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var handled atomic.Int32
	d := newUpdateDispatcher(ctx, 1, func(Update) {
		handled.Add(1)
		started <- struct{}{}
		<-release
	})
	d.Submit(chatUpdateForTest(1, 1))
	d.Submit(chatUpdateForTest(2, 1))
	d.Submit(chatUpdateForTest(3, 2))
	<-started
	cancel()
	close(release)
	if !d.Close(2 * time.Second) {
		t.Fatalf("dispatcher did not stop")
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("handled %d updates after shutdown, want only the running one", got)
	}
	if d.Submit(chatUpdateForTest(4, 3)) {
		t.Fatalf("Submit after shutdown should be rejected")
	}
}

// TestUpdateDispatcherConcurrentCommandSmoke drives real command handlers for
// many chats at once so the race detector can flag state that assumed serial
// update handling.
func TestUpdateDispatcherConcurrentCommandSmoke(t *testing.T) {
	var sent atomic.Int32
	var panics atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if strings.Contains(string(body), "任务异常") {
			panics.Add(1)
		}
		sent.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &TelegramBot{
		token:          "test-token",
		apiBase:        server.URL,
		client:         server.Client(),
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
		chatSettings:   make(map[int64]ChatDownloadSettings),
	}
	d := newUpdateDispatcher(ctx, 8, b.dispatchUpdateSafely)
	commands := []string{"/help", "/start", "/i", "/amwhoami", "/history"}
	total := 0
	for chat := int64(1); chat <= 10; chat++ {
		for i, text := range commands {
			upd := chatUpdateForTest(total, chat)
			upd.Message.Text = text
			upd.Message.From = &User{ID: chat}
			upd.Message.MessageID = i + 1
			d.Submit(upd)
			total++
		}
	}
	if !d.Close(5 * time.Second) {
		t.Fatalf("dispatcher did not drain")
	}
	if got := panics.Load(); got != 0 {
		t.Fatalf("%d handlers panicked under concurrent dispatch", got)
	}
	if got := sent.Load(); got < int32(total) {
		t.Fatalf("expected at least %d replies, got %d", total, got)
	}
}
