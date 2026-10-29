package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func messageUserID(msg *Message) int64 {
	if msg == nil || msg.From == nil {
		return 0
	}
	return msg.From.ID
}

func callbackUserID(cb *CallbackQuery) int64 {
	if cb == nil || cb.From == nil {
		return 0
	}
	return cb.From.ID
}

func inlineQueryUserID(q *InlineQuery) int64 {
	if q == nil || q.From == nil {
		return 0
	}
	return q.From.ID
}

func parseTelegramUserID(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("user_id is empty")
	}
	userID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || userID <= 0 {
		return 0, fmt.Errorf("invalid user_id")
	}
	return userID, nil
}

func resolveAdminTargetUserID(args []string, replyTo *Message) (int64, error) {
	if len(args) > 0 {
		return parseTelegramUserID(args[0])
	}
	if replyTo != nil && replyTo.From != nil && replyTo.From.ID > 0 {
		return replyTo.From.ID, nil
	}
	return 0, fmt.Errorf("target user is required")
}

func boolPtr(v bool) *bool {
	value := v
	return &value
}

func snapshotIDSet(items map[int64]bool) []int64 {
	if len(items) == 0 {
		return nil
	}
	out := make([]int64, 0, len(items))
	for id, enabled := range items {
		if !enabled || id == 0 {
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (b *TelegramBot) isAdminUser(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	return b.adminUsers[userID]
}

func (b *TelegramBot) isAllowedChat(chatID int64) bool {
	if len(b.allowedChats) == 0 {
		return true
	}
	return b.allowedChats[chatID]
}

func (b *TelegramBot) isUserWhitelisted(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return b.userWhitelist[userID]
}

func (b *TelegramBot) isUserBlacklisted(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return b.userBlacklist[userID]
}

func (b *TelegramBot) isUserWhitelistEnabled() bool {
	if b == nil {
		return false
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return b.userWhitelistEnabled
}

func (b *TelegramBot) isAllowedUser(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	if b.isAdminUser(userID) {
		return true
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	if b.userBlacklist[userID] {
		return false
	}
	if !b.userWhitelistEnabled {
		return true
	}
	return b.userWhitelist[userID]
}

func (b *TelegramBot) setUserWhitelistEnabled(enabled bool) bool {
	if b == nil {
		return false
	}
	b.accessMu.Lock()
	changed := b.userWhitelistEnabled != enabled
	b.userWhitelistEnabled = enabled
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) isForwardEnabled() bool {
	if b == nil {
		return false
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return b.forwardEnabled
}

func (b *TelegramBot) setForwardEnabled(enabled bool) bool {
	if b == nil {
		return false
	}
	b.accessMu.Lock()
	changed := b.forwardEnabled != enabled
	b.forwardEnabled = enabled
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) addUserWhitelist(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.Lock()
	if b.userWhitelist == nil {
		b.userWhitelist = make(map[int64]bool)
	}
	changed := !b.userWhitelist[userID]
	b.userWhitelist[userID] = true
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) removeUserWhitelist(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.Lock()
	changed := b.userWhitelist[userID]
	delete(b.userWhitelist, userID)
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) addUserBlacklist(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.Lock()
	if b.userBlacklist == nil {
		b.userBlacklist = make(map[int64]bool)
	}
	changed := !b.userBlacklist[userID]
	b.userBlacklist[userID] = true
	delete(b.userWhitelist, userID)
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) removeUserBlacklist(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.Lock()
	changed := b.userBlacklist[userID]
	delete(b.userBlacklist, userID)
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) userWhitelistCount() int {
	if b == nil {
		return 0
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return len(b.userWhitelist)
}

func (b *TelegramBot) userBlacklistCount() int {
	if b == nil {
		return 0
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return len(b.userBlacklist)
}

func (b *TelegramBot) adminUserCount() int {
	if b == nil {
		return 0
	}
	return len(b.adminUsers)
}

func (b *TelegramBot) isCachePushRunning() bool {
	if b == nil {
		return false
	}
	b.adminTaskMu.Lock()
	defer b.adminTaskMu.Unlock()
	return b.cachePushRunning
}

func (b *TelegramBot) beginCachePush() bool {
	if b == nil {
		return false
	}
	b.adminTaskMu.Lock()
	defer b.adminTaskMu.Unlock()
	if b.cachePushRunning {
		return false
	}
	b.cachePushRunning = true
	return true
}

func (b *TelegramBot) endCachePush() {
	if b == nil {
		return
	}
	b.adminTaskMu.Lock()
	b.cachePushRunning = false
	b.adminTaskMu.Unlock()
}
