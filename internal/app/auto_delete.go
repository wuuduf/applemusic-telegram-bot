package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (b *TelegramBot) shouldMarkAutoDeleteSticky(markup any) bool {
	if markup == nil {
		return false
	}
	keyboard, ok := markup.(InlineKeyboardMarkup)
	if !ok {
		if ptr, okPtr := markup.(*InlineKeyboardMarkup); okPtr && ptr != nil {
			keyboard = *ptr
			ok = true
		}
	}
	if !ok {
		return false
	}
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if strings.TrimSpace(button.CallbackData) != "" {
				return true
			}
		}
	}
	return false
}

func autoDeleteKey(chatID int64, messageID int) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}

func parseAutoDeleteKey(key string) (int64, int, bool) {
	parts := strings.Split(strings.TrimSpace(key), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || chatID == 0 {
		return 0, 0, false
	}
	messageID, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || messageID <= 0 {
		return 0, 0, false
	}
	return chatID, messageID, true
}

func (b *TelegramBot) scheduleAutoDeleteMessage(chatID int64, messageID int, sticky bool) {
	b.scheduleAutoDeleteMessageAt(chatID, messageID, sticky, time.Now().Add(telegramAutoDeleteAfter))
}

func (b *TelegramBot) scheduleAutoDeleteMessageAt(chatID int64, messageID int, sticky bool, deleteAt time.Time) {
	if b == nil || messageID <= 0 || chatID == 0 {
		return
	}
	if deleteAt.IsZero() {
		deleteAt = time.Now().Add(telegramAutoDeleteAfter)
	}
	delay := time.Until(deleteAt)
	if delay < 0 {
		delay = 0
	}
	key := autoDeleteKey(chatID, messageID)
	var timer *time.Timer
	b.autoDeleteMu.Lock()
	if b.autoDeleteMessages == nil {
		b.autoDeleteMessages = make(map[string]*time.Timer)
	}
	if b.autoDeleteSticky == nil {
		b.autoDeleteSticky = make(map[string]bool)
	}
	if b.autoDeleteDeadline == nil {
		b.autoDeleteDeadline = make(map[string]time.Time)
	}
	if existing := b.autoDeleteMessages[key]; existing != nil {
		existing.Stop()
	}
	b.autoDeleteSticky[key] = sticky
	b.autoDeleteDeadline[key] = deleteAt
	timer = time.AfterFunc(delay, func() {
		b.autoDeleteMu.Lock()
		if b.autoDeleteMessages[key] != timer {
			b.autoDeleteMu.Unlock()
			return
		}
		delete(b.autoDeleteMessages, key)
		delete(b.autoDeleteSticky, key)
		delete(b.autoDeleteDeadline, key)
		b.autoDeleteMu.Unlock()
		b.requestStateSave()
		_ = b.deleteMessage(chatID, messageID)
	})
	b.autoDeleteMessages[key] = timer
	b.autoDeleteMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) markMessageInteraction(chatID int64, messageID int) {
	key := autoDeleteKey(chatID, messageID)
	b.autoDeleteMu.Lock()
	sticky := b.autoDeleteSticky[key]
	b.autoDeleteMu.Unlock()
	if sticky {
		// 交互面板每次操作都刷新自动删除计时，避免“点击过一次后永久不删”。
		b.scheduleAutoDeleteMessage(chatID, messageID, true)
	}
}

func (b *TelegramBot) clearAutoDeleteMessage(chatID int64, messageID int) {
	if b == nil || messageID <= 0 || chatID == 0 {
		return
	}
	key := autoDeleteKey(chatID, messageID)
	changed := false
	b.autoDeleteMu.Lock()
	if timer := b.autoDeleteMessages[key]; timer != nil {
		timer.Stop()
		changed = true
	}
	if _, ok := b.autoDeleteMessages[key]; ok {
		delete(b.autoDeleteMessages, key)
		changed = true
	}
	if _, ok := b.autoDeleteSticky[key]; ok {
		delete(b.autoDeleteSticky, key)
		changed = true
	}
	if _, ok := b.autoDeleteDeadline[key]; ok {
		delete(b.autoDeleteDeadline, key)
		changed = true
	}
	b.autoDeleteMu.Unlock()
	if changed {
		b.requestStateSave()
	}
}

func (b *TelegramBot) clearAllAutoDeleteMessages() {
	if b == nil {
		return
	}
	changed := false
	b.autoDeleteMu.Lock()
	for key, timer := range b.autoDeleteMessages {
		if timer != nil {
			timer.Stop()
		}
		delete(b.autoDeleteMessages, key)
		changed = true
	}
	for key := range b.autoDeleteSticky {
		delete(b.autoDeleteSticky, key)
		changed = true
	}
	for key := range b.autoDeleteDeadline {
		delete(b.autoDeleteDeadline, key)
		changed = true
	}
	b.autoDeleteMu.Unlock()
	if changed {
		b.requestStateSave()
	}
}
