package app

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type cachedAudioItem struct {
	Key     string
	TrackID string
	Entry   CachedAudio
}

func cachedAudioSortTime(entry CachedAudio) time.Time {
	if !entry.CreatedAt.IsZero() {
		return entry.CreatedAt
	}
	return entry.UpdatedAt
}

func (b *TelegramBot) listCachedAudioItems() []cachedAudioItem {
	if b == nil {
		return nil
	}
	b.cacheMu.Lock()
	items := make([]cachedAudioItem, 0, len(b.cache))
	for key, entry := range b.cache {
		parts := strings.Split(key, "|")
		if len(parts) < 3 {
			continue
		}
		items = append(items, cachedAudioItem{
			Key:     key,
			TrackID: strings.TrimSpace(parts[0]),
			Entry:   entry,
		})
	}
	b.cacheMu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		left := cachedAudioSortTime(items[i].Entry)
		right := cachedAudioSortTime(items[j].Entry)
		switch {
		case left.IsZero() && !right.IsZero():
			return false
		case !left.IsZero() && right.IsZero():
			return true
		case !left.Equal(right):
			return left.Before(right)
		default:
			return items[i].Key < items[j].Key
		}
	})
	return items
}

func (b *TelegramBot) formatAdminPanelText(chatID int64) string {
	whitelistMode := "关闭"
	if b.isUserWhitelistEnabled() {
		whitelistMode = "开启"
	}
	forwardStatus := "关闭"
	if b.isForwardEnabled() {
		forwardStatus = "开启"
	}
	forwardChatLabel := "未配置"
	if b != nil && b.forwardChatID != 0 {
		forwardChatLabel = strconv.FormatInt(b.forwardChatID, 10)
	}
	cachePushStatus := "空闲"
	if b.isCachePushRunning() {
		cachePushStatus = "运行中"
	}
	if b.getChatLanguage(chatID) == telegramLanguageEn {
		return fmt.Sprintf("Admin panel\n- Whitelist mode: %s\n- Archive forward: %s\n- Archive chat_id: %s\n- Cache push task: %s\n- Admin users: %d\n- Whitelisted users: %d\n- Blacklisted users: %d\n\nAdmin commands:\n/sub artist <link> Subscribe to artist albums\n/sub list|del|pause|resume <...> Manage subscriptions\n/subtemp [...] List temporary subscription releases\n/subrefresh <id> Trigger one final refresh download\n/subrefreshall Trigger refresh for all due releases\n/amwlon /amwloff Toggle whitelist mode\n/amwladd <user_id> Add whitelisted user\n/amwldel <user_id> Remove whitelisted user\n/amban <user_id> Ban a user\n/amunban <user_id> Unban a user\n/amcachepush Push cached audio to archive chat",
			map[bool]string{true: "ON", false: "OFF"}[b.isUserWhitelistEnabled()],
			map[bool]string{true: "ON", false: "OFF"}[b.isForwardEnabled()],
			forwardChatLabel,
			map[bool]string{true: "RUNNING", false: "IDLE"}[b.isCachePushRunning()],
			b.adminUserCount(),
			b.userWhitelistCount(),
			b.userBlacklistCount(),
		)
	}
	return fmt.Sprintf("管理员面板\n- 白名单模式：%s\n- 归档转发：%s\n- 归档群 chat_id：%s\n- 缓存转存任务：%s\n- 管理员数量：%d\n- 白名单人数：%d\n- 黑名单人数：%d\n\n管理命令：\n/sub artist <链接> 订阅艺人新专辑\n/sub list|del|pause|resume <...> 管理订阅\n/subtemp [...] 查看临时订阅发布记录\n/subrefresh <id> 触发单个正式刷新下载\n/subrefreshall 触发全部到期刷新\n/amwlon /amwloff 开/关白名单模式\n/amwladd <user_id> 添加白名单用户\n/amwldel <user_id> 移除白名单用户\n/amban <user_id> 封禁用户\n/amunban <user_id> 解除封禁\n/amcachepush 批量转存缓存到归档群",
		whitelistMode,
		forwardStatus,
		forwardChatLabel,
		cachePushStatus,
		b.adminUserCount(),
		b.userWhitelistCount(),
		b.userBlacklistCount(),
	)
}

func (b *TelegramBot) buildAdminPanelKeyboard(confirmCachePush bool) InlineKeyboardMarkup {
	whitelistLabel := "白名单：关闭"
	if b.isUserWhitelistEnabled() {
		whitelistLabel = "白名单：开启"
	}
	forwardLabel := "归档转发：关闭"
	if b.isForwardEnabled() {
		forwardLabel = "归档转发：开启"
	}
	rows := [][]InlineKeyboardButton{
		{{Text: whitelistLabel, CallbackData: "admin:whitelist:toggle"}},
		{{Text: forwardLabel, CallbackData: "admin:forward:toggle"}},
	}
	if confirmCachePush {
		rows = append(rows,
			[]InlineKeyboardButton{
				{Text: "确认转存", CallbackData: "admin:cachepush:run"},
				{Text: "返回", CallbackData: "admin:refresh"},
			},
		)
	} else {
		rows = append(rows,
			[]InlineKeyboardButton{
				{Text: "缓存转存到归档群", CallbackData: "admin:cachepush:confirm"},
			},
		)
	}
	rows = append(rows,
		[]InlineKeyboardButton{
			{Text: "刷新", CallbackData: "admin:refresh"},
			{Text: "关闭", CallbackData: "admin:close"},
		},
	)
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *TelegramBot) openAdminPanel(chatID int64, replyToID int) {
	_ = b.sendMessageWithReply(chatID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false), replyToID)
}

func formatWhoAmIText(userID int64, chatID int64) string {
	return fmt.Sprintf("用户ID: %d\n会话ID(chat_id): %d", userID, chatID)
}
