package app

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	defaultTelegramUpdateConcurrency = 8
	maxTelegramUpdateConcurrency     = 64
	defaultUpdateQueuePerChat        = 50
	defaultUpdateQueueTotal          = 1000
	defaultTelegramUpdateDrainWait   = 4 * time.Second
)

// updateDispatcher runs Telegram update handlers concurrently across chats
// while keeping updates from the same chat strictly in arrival order. Handler
// state such as search/selection panels is keyed by chat and was written for
// serial handling, so per-chat ordering preserves that invariant.
//
// Each chat with pending updates gets one short-lived goroutine that drains
// its queue; a semaphore caps how many handlers run at once. Submit blocks
// when the total backlog is full, which pauses polling instead of growing
// memory without bound.
type updateDispatcher struct {
	ctx      context.Context
	handle   func(Update)
	sem      chan struct{}
	perChat  int
	maxTotal int

	mu      sync.Mutex
	cond    *sync.Cond
	queues  map[int64][]Update
	total   int
	closed  bool
	wg      sync.WaitGroup
	dropped uint64
}

func newUpdateDispatcher(ctx context.Context, concurrency int, handle func(Update)) *updateDispatcher {
	if ctx == nil {
		ctx = context.Background()
	}
	if concurrency < 1 {
		concurrency = 1
	}
	d := &updateDispatcher{
		ctx:      ctx,
		handle:   handle,
		sem:      make(chan struct{}, concurrency),
		perChat:  defaultUpdateQueuePerChat,
		maxTotal: defaultUpdateQueueTotal,
		queues:   make(map[int64][]Update),
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

// updateDispatchKey returns the ordering key for an update: the chat it
// belongs to, or the sender when there is no chat (inline queries, callbacks
// on inline messages).
func updateDispatchKey(upd Update) int64 {
	if upd.Message != nil {
		return upd.Message.Chat.ID
	}
	if upd.CallbackQuery != nil {
		if upd.CallbackQuery.Message != nil {
			return upd.CallbackQuery.Message.Chat.ID
		}
		if upd.CallbackQuery.From != nil {
			return upd.CallbackQuery.From.ID
		}
	}
	if upd.InlineQuery != nil && upd.InlineQuery.From != nil {
		return upd.InlineQuery.From.ID
	}
	return 0
}

// Submit queues an update for its chat. It returns false when the update was
// dropped because the dispatcher is closed or the chat's backlog is full.
func (d *updateDispatcher) Submit(upd Update) bool {
	key := updateDispatchKey(upd)
	d.mu.Lock()
	for d.total >= d.maxTotal && !d.closed && d.ctx.Err() == nil {
		d.cond.Wait()
	}
	if d.closed || d.ctx.Err() != nil {
		d.mu.Unlock()
		return false
	}
	queue, active := d.queues[key]
	if len(queue) >= d.perChat {
		d.dropped++
		d.mu.Unlock()
		msg := fmt.Sprintf("telegram update %d dropped: chat %d backlog is full (%d pending)", upd.UpdateID, key, d.perChat)
		fmt.Println(msg)
		appendRuntimeErrorLog(msg)
		return false
	}
	d.queues[key] = append(queue, upd)
	d.total++
	if !active {
		d.wg.Add(1)
		go d.drain(key)
	}
	d.mu.Unlock()
	return true
}

func (d *updateDispatcher) drain(key int64) {
	defer d.wg.Done()
	for {
		d.mu.Lock()
		queue := d.queues[key]
		if len(queue) == 0 || d.ctx.Err() != nil {
			// Shutting down: skip updates that have not started yet.
			d.total -= len(queue)
			delete(d.queues, key)
			d.cond.Broadcast()
			d.mu.Unlock()
			return
		}
		upd := queue[0]
		d.mu.Unlock()

		select {
		case d.sem <- struct{}{}:
		case <-d.ctx.Done():
			continue
		}
		d.handle(upd)
		<-d.sem

		d.mu.Lock()
		// Keep the entry (possibly empty) so concurrent Submits see the chat
		// as active until the next iteration removes it.
		d.queues[key] = d.queues[key][1:]
		d.total--
		d.cond.Broadcast()
		d.mu.Unlock()
	}
}

// Close stops accepting updates and waits up to timeout for running handlers.
// It reports whether every handler finished in time.
func (d *updateDispatcher) Close(timeout time.Duration) bool {
	d.mu.Lock()
	d.closed = true
	d.cond.Broadcast()
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func telegramUpdateConcurrency() int {
	n := Config.TelegramUpdateConcurrency
	if n <= 0 {
		return defaultTelegramUpdateConcurrency
	}
	if n > maxTelegramUpdateConcurrency {
		return maxTelegramUpdateConcurrency
	}
	return n
}
