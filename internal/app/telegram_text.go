package app

import (
	"fmt"
	"strings"
)

func botHelpText() string {
	return strings.TrimSpace(`
🎵 使用说明

【搜索音乐】
/sg <关键词> 搜索歌曲
/sa <关键词> 搜索专辑
/sr <关键词> 搜索艺人
/s <song|album|artist> <关键词> 统一搜索

【下载与导出】
/u <链接> 下载（支持歌曲/专辑/歌单/艺人/MV）
/dg <歌名或 歌手 - 歌名> 点歌（直接下载第一个匹配歌曲，无需选择）
/rf <链接> 强制重新下载（清缓存重传，文件损坏时用）
/ap <艺人链接> 导出艺人主页图 + 专辑封面合集
/cv <链接> 只下载封面
/ac <链接> 只下载动态封面
/ly <链接> 导出歌词

【我的任务】
/status 查看全局运行状态（队列/线程）
/queue 查看我的排队/运行任务
/history [n] 查看我最近的下载记录
/cancel <编号> 取消我的任务（编号用 /queue 查看）

【设置】
/st 查看或修改下载设置（音质/歌词/打包/线程等）

【其他】
/i 查看我的 chat_id
/amwhoami 查看我的 user_id / chat_id

💡 直接把 Apple Music 链接发给我就能下载，自动识别类型，一条消息可放多个链接。
`)
}

func (b *TelegramBot) botHelpTextForChat(chatID int64, userID int64) string {
	text := botHelpText()
	if b.getChatLanguage(chatID) == telegramLanguageEn {
		text = strings.TrimSpace(`
🎵 How to use

【Search】
/sg <keywords> Search songs
/sa <keywords> Search albums
/sr <keywords> Search artists
/s <song|album|artist> <keywords> Unified search

【Download & Export】
/u <link> Download (song/album/playlist/artist/MV)
/dg <song or artist - song> Request a song (auto-download the top match, no selection)
/rf <link> Force re-download (clear cache; use when files are corrupted)
/ap <artist link> Export artist image + album cover set
/cv <link> Download cover only
/ac <link> Download animated cover only
/ly <link> Export lyrics

【My tasks】
/status View global status (queue/workers)
/queue View my queued/running tasks
/history [n] View my recent download history
/cancel <id> Cancel one of my tasks (id from /queue)

【Settings】
/st View or change download settings (quality/lyrics/zip/workers)

【Other】
/i Show my chat_id
/amwhoami Show my user_id / chat_id

💡 You can also just send an Apple Music link directly — type is auto-detected, multiple links per message are supported.
`)
	}
	if b.isAdminUser(userID) {
		if b.getChatLanguage(chatID) == telegramLanguageEn {
			text += "\n\n⚙️ Admin features: open the admin panel with /amadmin (all admin commands are listed there)."
		} else {
			text += "\n\n⚙️ 管理员功能：使用 /amadmin 打开管理面板（全部管理命令已收纳在面板中）。"
		}
	}
	return text
}

func formatChatIDText(chatID int64) string {
	return fmt.Sprintf(
		"会话ID (chat_id): %d\n"+
			"把这个值写进 config.yaml 白名单：\n"+
			"telegram-allowed-chat-ids:\n"+
			"  - %d",
		chatID,
		chatID,
	)
}

func (b *TelegramBot) formatChatIDTextForChat(chatID int64) string {
	if b.getChatLanguage(chatID) == telegramLanguageEn {
		return fmt.Sprintf(
			"Session ID (chat_id): %d\n"+
				"Use this value in config.yaml whitelist:\n"+
				"telegram-allowed-chat-ids:\n"+
				"  - %d",
			chatID,
			chatID,
		)
	}
	return formatChatIDText(chatID)
}

func (b *TelegramBot) localizeOutgoingText(chatID int64, text string) string {
	lang := b.getChatLanguage(chatID)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return text
	}
	if lang == telegramLanguageEn {
		switch trimmed {
		case "Not authorized for this bot.":
			return "Not authorized for this bot."
		case "已取消。":
			return "Canceled."
		case "该会话未被授权使用此机器人。":
			return "Not authorized for this bot."
		case "请选择发送方式：":
			return "Choose transfer method:"
		case "请选择歌词导出方式：":
			return "Choose lyrics export method:"
		case "请选择艺人素材导出方式：":
			return "Choose artist assets export method:"
		case "无效的 Apple Music 链接。":
			return "Invalid Apple Music URL."
		case "无效的目标。":
			return "Invalid target."
		case "未知命令。发送 /help 查看用法。":
			return "Unknown command. Send /help for usage."
		}
		if strings.HasPrefix(trimmed, "用法：") {
			return "Usage: " + strings.TrimPrefix(trimmed, "用法：")
		}
		if strings.HasPrefix(trimmed, "失败：") {
			return "Failed: " + strings.TrimPrefix(trimmed, "失败：")
		}
		if strings.HasPrefix(trimmed, "搜索失败：") {
			return "Search failed: " + strings.TrimPrefix(trimmed, "搜索失败：")
		}
		return text
	}

	switch trimmed {
	case "Not authorized for this bot.":
		return "该会话未被授权使用此机器人。"
	case "Unknown command. Send /help for usage.":
		return "未知命令。发送 /help 查看用法。"
	case "Choose transfer method:":
		return "请选择发送方式："
	case "Choose lyrics export method:":
		return "请选择歌词导出方式："
	case "Choose artist assets export method:":
		return "请选择艺人素材导出方式："
	case "Selection expired. Please search again.":
		return "选择已过期，请重新搜索。"
	case "Selection out of range.":
		return "选择超出范围。"
	case "No results found.":
		return "没有搜索结果。"
	case "No content found for this artist.":
		return "该艺人暂无可用内容。"
	case "Invalid Apple Music URL.":
		return "无效的 Apple Music 链接。"
	case "Invalid target.":
		return "无效的目标。"
	case "Please provide a search query.":
		return "请提供搜索关键词。"
	case "Search type must be song, album, or artist.":
		return "搜索类型必须是 song、album 或 artist。"
	case "Music Video ID is invalid.":
		return "Music Video ID 无效。"
	case "MV download requires media-user-token in config.yaml.":
		return "MV 下载需要在 config.yaml 中配置 media-user-token。"
	case "MV download requires mp4decrypt in PATH.":
		return "MV 下载需要在 PATH 中提供 mp4decrypt。"
	case "Station download requires media-user-token in config.yaml.":
		return "电台下载需要在 config.yaml 中配置 media-user-token。"
	case "Same MV task is already running for this chat. Please wait.":
		return "该会话已有相同 MV 任务在运行，请稍候。"
	case "Same download task is already running for this chat. Please wait.":
		return "该会话已有相同下载任务在运行，请稍候。"
	case "animatedcover only supports song/album/playlist/station.":
		return "animatedcover 仅支持 song/album/playlist/station。"
	case "animatedcover requires ffmpeg in PATH.":
		return "animatedcover 需要在 PATH 中提供 ffmpeg。"
	case "Invalid song target.":
		return "无效的歌曲目标。"
	case "Album ID is empty.":
		return "专辑 ID 不能为空。"
	case "Artist ID is empty.":
		return "艺人 ID 不能为空。"
	case "Playlist ID is empty.":
		return "歌单 ID 不能为空。"
	case "Station ID is empty.":
		return "电台 ID 不能为空。"
	case "Music Video ID is empty.":
		return "MV ID 不能为空。"
	case "Lyrics export requires media-user-token in config.yaml.":
		return "歌词导出需要在 config.yaml 中配置 media-user-token。"
	case "Selection expired. Please request the artist again.":
		return "选择已过期，请重新请求艺人内容。"
	case "Selection expired. Please request it again.":
		return "选择已过期，请重新请求。"
	case "Unknown artist view.":
		return "未知的艺人视图。"
	case "Artist assets export mode: one by one.":
		return "艺人素材导出方式：逐个发送。"
	case "Artist assets export mode: ZIP.":
		return "艺人素材导出方式：ZIP。"
	case "Unknown artist assets export mode.":
		return "未知的艺人素材导出方式。"
	case "Lyrics export mode: one by one.":
		return "歌词导出方式：逐个发送。"
	case "Lyrics export mode: ZIP.":
		return "歌词导出方式：ZIP。"
	case "Unknown lyrics export mode.":
		return "未知的歌词导出方式。"
	case "Transfer mode: one by one (refresh).":
		return "发送方式：逐个发送（刷新）。"
	case "Transfer mode: one by one.":
		return "发送方式：逐个发送。"
	case "Transfer mode: ZIP (cached).":
		return "发送方式：ZIP（命中缓存）。"
	case "Transfer mode: ZIP (refresh).":
		return "发送方式：ZIP（刷新）。"
	case "Transfer mode: ZIP.":
		return "发送方式：ZIP。"
	case "Unknown transfer mode.":
		return "未知的发送方式。"
	}
	if strings.HasPrefix(trimmed, "Usage: ") {
		return "用法：" + strings.TrimPrefix(trimmed, "Usage: ")
	}
	if strings.HasPrefix(trimmed, "Failed: ") {
		return "失败：" + strings.TrimPrefix(trimmed, "Failed: ")
	}
	if strings.HasPrefix(trimmed, "Failed to ") {
		return "失败：" + strings.TrimPrefix(trimmed, "Failed to ")
	}
	if strings.HasPrefix(trimmed, "Search failed: ") {
		return "搜索失败：" + strings.TrimPrefix(trimmed, "Search failed: ")
	}
	if strings.HasPrefix(trimmed, "Unsupported Apple Music URL: ") {
		return "不支持的 Apple Music 链接：" + strings.TrimPrefix(trimmed, "Unsupported Apple Music URL: ")
	}
	return text
}

func formatChatIDTextLegacy(chatID int64) string {
	return fmt.Sprintf(
		"Session ID (chat_id): %d\n"+
			"Use this value in config.yaml whitelist:\n"+
			"telegram-allowed-chat-ids:\n"+
			"  - %d",
		chatID,
		chatID,
	)
}
