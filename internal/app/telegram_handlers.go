package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (b *TelegramBot) handleMessage(msg *Message) {
	defer b.finishActionCharge(msg.Chat.ID)
	if b.maybeIngestArchiveMessage(msg) && msg.Text == "" {
		return
	}
	if msg.Text == "" {
		return
	}
	text := strings.TrimSpace(msg.Text)
	if cmd, args, ok := parseCommand(text); ok {
		cmd = normalizeTelegramBotCommand(cmd)
		if !b.isAllowedChat(msg.Chat.ID) {
			// Allow querying chat_id even when not in allowlist.
			if cmd == "id" && len(args) == 0 {
				_ = b.sendMessage(msg.Chat.ID, b.formatChatIDTextForChat(msg.Chat.ID), nil)
			} else if cmd == "amwhoami" && len(args) == 0 {
				_ = b.sendMessage(msg.Chat.ID, formatWhoAmIText(messageUserID(msg), msg.Chat.ID), nil)
			} else {
				_ = b.sendMessage(msg.Chat.ID, b.localizeOutgoingText(msg.Chat.ID, "Not authorized for this bot."), nil)
			}
			return
		}
		if !b.isAllowedUser(messageUserID(msg)) {
			if cmd == "amwhoami" && len(args) == 0 {
				_ = b.sendMessage(msg.Chat.ID, formatWhoAmIText(messageUserID(msg), msg.Chat.ID), nil)
			} else {
				_ = b.sendMessage(msg.Chat.ID, b.localizeOutgoingText(msg.Chat.ID, "Not authorized for this bot."), nil)
			}
			return
		}
		if isRateLimitedCommand(cmd, args) && !b.allowUserAction(msg.Chat.ID, messageUserID(msg), msg.MessageID) {
			return
		}
		b.handleCommandWithContext(msg.Chat.ID, msg.Chat.Type, messageUserID(msg), cmd, args, msg.MessageID, msg.ReplyToMessage)
		return
	}
	if !b.isAllowedChat(msg.Chat.ID) {
		_ = b.sendMessage(msg.Chat.ID, b.localizeOutgoingText(msg.Chat.ID, "Not authorized for this bot."), nil)
		return
	}
	if !b.isAllowedUser(messageUserID(msg)) {
		_ = b.sendMessage(msg.Chat.ID, b.localizeOutgoingText(msg.Chat.ID, "Not authorized for this bot."), nil)
		return
	}
	if len(extractAppleMusicURLs(text)) > 0 && !b.allowUserAction(msg.Chat.ID, messageUserID(msg), msg.MessageID) {
		return
	}
	if b.handleURLTextBatch(msg.Chat.ID, msg.MessageID, text, false) == 0 {
		return
	}
}

// allowUserAction enforces the per-user rate limiter. It returns true when the
// action may proceed. When the action is throttled it notifies the user and
// returns false. Admins are always allowed.
func (b *TelegramBot) allowUserAction(chatID, userID int64, replyToID int) bool {
	if b == nil || b.userLimiter == nil {
		return true
	}
	if b.isAdminUser(userID) {
		return true
	}
	ok, retryAfter, reason := b.userLimiter.Allow(userID)
	if ok {
		if b.userLimiter.enabled && userID > 0 {
			b.beginActionCharge(chatID, userID)
		}
		return true
	}
	msg := "Rate limit exceeded"
	if strings.TrimSpace(reason) != "" {
		msg = msg + ": " + reason
	}
	if retryAfter > 0 {
		msg = msg + fmt.Sprintf(". Try again in %s.", formatRetryAfter(retryAfter))
	}
	_ = b.sendMessageWithReply(chatID, b.localizeOutgoingText(chatID, msg), nil, replyToID)
	return false
}

func formatRetryAfter(d time.Duration) string {
	if d < time.Second {
		d = time.Second
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
	}
	return fmt.Sprintf("%dh", int(d.Round(time.Hour)/time.Hour))
}

func (b *TelegramBot) handleCallback(cb *CallbackQuery) {
	if cb == nil || cb.Message == nil {
		return
	}
	if !b.isAllowedChat(cb.Message.Chat.ID) || !b.isAllowedUser(callbackUserID(cb)) {
		_ = b.answerCallbackQuery(cb.ID)
		return
	}
	b.markMessageInteraction(cb.Message.Chat.ID, cb.Message.MessageID)
	data := strings.TrimSpace(cb.Data)
	if strings.HasPrefix(data, "admin:") {
		b.handleAdminCallback(cb)
		_ = b.answerCallbackQuery(cb.ID)
		return
	}
	if data == "panel_cancel" || data == "setting_exit" || data == "setting_close" {
		b.cancelPanelAndDelete(cb.Message.Chat.ID, cb.Message.MessageID)
		_ = b.answerCallbackQuery(cb.ID)
		return
	}
	if strings.HasPrefix(data, "sel:") {
		numStr := strings.TrimPrefix(data, "sel:")
		if n, err := strconv.Atoi(numStr); err == nil {
			b.handleSelection(cb.Message.Chat.ID, cb.Message.MessageID, n)
		}
	} else if strings.HasPrefix(data, "mvstream:") {
		numStr := strings.TrimPrefix(data, "mvstream:")
		if n, err := strconv.Atoi(numStr); err == nil {
			b.handleMusicVideoStreamSelection(cb.Message.Chat.ID, cb.Message.MessageID, n)
		}
	} else if strings.HasPrefix(data, "setting_format:") {
		format := strings.TrimPrefix(data, "setting_format:")
		settings := b.setChatFormat(cb.Message.Chat.ID, format)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_aac:") {
		aacType := strings.TrimPrefix(data, "setting_aac:")
		settings := b.setChatAACType(cb.Message.Chat.ID, aacType)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_mv_audio:") {
		mvAudioType := strings.TrimPrefix(data, "setting_mv_audio:")
		settings := b.setChatMVAudioType(cb.Message.Chat.ID, mvAudioType)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_lyrics_format:") {
		lyricsFormat := strings.TrimPrefix(data, "setting_lyrics_format:")
		settings := b.setChatLyricsFormat(cb.Message.Chat.ID, lyricsFormat)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_embed:lyrics" {
		settings := b.toggleChatEmbedLyrics(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_embed:cover" {
		settings := b.toggleChatEmbedCover(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_lang:") {
		language := strings.TrimPrefix(data, "setting_lang:")
		settings := b.setChatLanguage(cb.Message.Chat.ID, language)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_auto:lyrics" {
		settings := b.toggleChatAutoLyrics(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_auto:cover" {
		settings := b.toggleChatAutoCover(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_auto:animated" {
		settings := b.toggleChatAutoAnimated(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_auto:comment" {
		settings := b.toggleChatSongComment(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
		b.maybeNotifySongCommentConfig(cb.Message.Chat.ID, cb.Message.MessageID, settings)
	} else if data == "setting_song_zip" {
		settings := b.toggleChatSongZip(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_worker:") {
		workerRaw := strings.TrimPrefix(data, "setting_worker:")
		if limit, ok := parseTaskWorkerLimit(workerRaw); ok {
			_ = b.setTaskWorkerLimit(limit)
		}
		settings := b.getChatSettings(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting:") {
		// Backward compatibility for old callbacks.
		format := strings.TrimPrefix(data, "setting:")
		settings := b.setChatFormat(cb.Message.Chat.ID, format)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "transfer:") {
		mode := strings.TrimPrefix(data, "transfer:")
		b.handleMediaTransfer(cb.Message.Chat.ID, cb.Message.MessageID, mode)
	} else if strings.HasPrefix(data, "album_transfer:") {
		// Backward compatibility for old callbacks.
		mode := strings.TrimPrefix(data, "album_transfer:")
		b.handleMediaTransfer(cb.Message.Chat.ID, cb.Message.MessageID, mode)
	} else if strings.HasPrefix(data, "artist_rel:") {
		relationship := strings.TrimPrefix(data, "artist_rel:")
		b.handleArtistModeSelection(cb.Message.Chat.ID, cb.Message.MessageID, relationship)
	} else if strings.HasPrefix(data, "page:") {
		deltaStr := strings.TrimPrefix(data, "page:")
		if delta, err := strconv.Atoi(deltaStr); err == nil {
			b.handlePage(cb.Message.Chat.ID, cb.Message.MessageID, delta)
		}
	}
	_ = b.answerCallbackQuery(cb.ID)
}

func (b *TelegramBot) handleAdminCallback(cb *CallbackQuery) {
	if b == nil || cb == nil || cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	messageID := cb.Message.MessageID
	userID := callbackUserID(cb)
	if !b.isAdminUser(userID) {
		_ = b.sendMessageWithReply(chatID, "只有管理员可以操作管理员面板。", nil, messageID)
		return
	}
	data := strings.TrimSpace(cb.Data)
	switch data {
	case "admin:close":
		_ = b.deleteMessage(chatID, messageID)
	case "admin:refresh":
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false))
	case "admin:whitelist:toggle":
		b.setUserWhitelistEnabled(!b.isUserWhitelistEnabled())
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false))
	case "admin:forward:toggle":
		if b.forwardChatID == 0 && !b.isForwardEnabled() {
			_ = b.sendMessageWithReply(chatID, "未配置归档群 chat_id，无法开启归档转发。", nil, messageID)
			return
		}
		b.setForwardEnabled(!b.isForwardEnabled())
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false))
	case "admin:cachepush:confirm":
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(true))
	case "admin:cachepush:run":
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false))
		b.triggerAdminCachePush(chatID, messageID)
	}
}

func (b *TelegramBot) triggerAdminCachePush(chatID int64, replyToID int) {
	if b == nil {
		return
	}
	targetChatID := b.forwardChatID
	if targetChatID == 0 {
		_ = b.sendMessageWithReply(chatID, "未配置归档群 chat_id，无法执行缓存转存。", nil, replyToID)
		return
	}
	items := b.listCachedAudioItems()
	if len(items) == 0 {
		_ = b.sendMessageWithReply(chatID, "当前没有可转存的缓存音频。", nil, replyToID)
		return
	}
	if !b.beginCachePush() {
		_ = b.sendMessageWithReply(chatID, "缓存转存任务已经在运行中，请稍后再试。", nil, replyToID)
		return
	}
	total := len(items)
	_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已开始缓存转存，共 %d 条音频，目标群：%d", total, targetChatID), nil, replyToID)
	if !b.launchBackgroundTask("telegram admin cache push", func(ctx context.Context) {
		defer b.endCachePush()
		success := 0
		failed := 0
		retriedSuccess := 0
		rateLimitedItems := make([]cachedAudioItem, 0)
		for idx, item := range items {
			if ctx.Err() != nil {
				return
			}
			if err := b.sendAudioByFileIDWithoutSongComment(targetChatID, item.Entry, 0, item.TrackID); err != nil {
				failed++
				if isTelegramRateLimitError(err) {
					rateLimitedItems = append(rateLimitedItems, item)
				}
				b.logTelegramPrintf("admin cache push failed idx=%d track=%s err=%v\n", idx+1, item.TrackID, err)
				b.appendTelegramRuntimeErrorLogf("admin cache push failed idx=%d track=%s err=%v", idx+1, item.TrackID, err)
			} else {
				success++
			}
			if ctx.Err() == nil && idx+1 < total && (idx+1)%25 == 0 {
				_ = b.sendMessageWithReply(chatID, fmt.Sprintf("缓存转存进度：%d/%d（成功 %d，失败 %d）", idx+1, total, success, failed), nil, 0)
			}
		}
		const retryRounds = 3
		pendingRateLimited := rateLimitedItems
		for round := 1; round <= retryRounds && len(pendingRateLimited) > 0; round++ {
			if ctx.Err() != nil {
				return
			}
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("检测到 %d 条触发 429，开始第 %d/%d 轮重试。", len(pendingRateLimited), round, retryRounds), nil, 0)
			nextRound := make([]cachedAudioItem, 0, len(pendingRateLimited))
			for idx, item := range pendingRateLimited {
				if ctx.Err() != nil {
					return
				}
				if err := b.sendAudioByFileIDWithoutSongComment(targetChatID, item.Entry, 0, item.TrackID); err != nil {
					b.logTelegramPrintf("admin cache push retry failed round=%d idx=%d track=%s err=%v\n", round, idx+1, item.TrackID, err)
					b.appendTelegramRuntimeErrorLogf("admin cache push retry failed round=%d idx=%d track=%s err=%v", round, idx+1, item.TrackID, err)
					if isTelegramRateLimitError(err) {
						nextRound = append(nextRound, item)
					}
					continue
				}
				retriedSuccess++
				success++
				failed--
			}
			pendingRateLimited = nextRound
		}
		if ctx.Err() != nil {
			return
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("缓存转存完成：成功 %d，最终失败 %d，429重试补回 %d，目标群：%d", success, failed, retriedSuccess, targetChatID), nil, 0)
	}) {
		b.endCachePush()
	}
}

func (b *TelegramBot) cancelPanelAndDelete(chatID int64, messageID int) {
	b.clearPanelStateByMessage(chatID, messageID)
	if err := b.deleteMessage(chatID, messageID); err != nil {
		_ = b.editMessageText(chatID, messageID, b.localizeOutgoingText(chatID, "已取消。"), nil)
	}
}

func (b *TelegramBot) clearPanelStateByMessage(chatID int64, messageID int) {
	b.clearPendingByMessage(chatID, messageID)
	b.clearPendingTransferByMessage(chatID, messageID)
	b.clearPendingMVStreamByMessage(chatID, messageID)
	b.clearPendingArtistModeByMessage(chatID, messageID)
}

func (b *TelegramBot) handleInlineQuery(q *InlineQuery) {
	if q == nil || q.ID == "" {
		return
	}
	if !b.isAllowedUser(inlineQueryUserID(q)) {
		_ = b.answerInlineQuery(q.ID, []any{}, true)
		return
	}
	query := strings.TrimSpace(q.Query)
	if query == "" {
		_ = b.answerInlineQuery(q.ID, []any{}, true)
		return
	}
	trackID := extractInlineTrackID(query)
	if trackID == "" {
		_ = b.answerInlineQuery(q.ID, []any{}, true)
		return
	}
	entry, ok := b.getCachedAudio(trackID, b.maxFileBytes, "")
	results := []any{}
	if ok {
		entry = b.enrichCachedAudio(trackID, entry)
		format := normalizeTelegramFormat(entry.Format)
		if format == "" {
			format = defaultTelegramFormat
		}
		results = append(results, InlineQueryResultCachedAudio{
			Type:        "audio",
			ID:          fmt.Sprintf("song_%s", trackID),
			AudioFileID: entry.FileID,
			Caption:     formatTelegramCaption(entry.SizeBytes, entry.BitrateKbps, format),
		})
	} else {
		meta, err := b.fetchTrackMeta(trackID)
		title := "Send /songid " + trackID
		description := ""
		if err == nil {
			if meta.Title != "" && meta.Performer != "" {
				title = meta.Performer + " - " + meta.Title
				description = "Send /songid " + trackID
			} else if meta.Title != "" {
				title = meta.Title
				description = "Send /songid " + trackID
			}
		}
		results = append(results, InlineQueryResultArticle{
			Type:        "article",
			ID:          fmt.Sprintf("songcmd_%s", trackID),
			Title:       title,
			Description: description,
			InputMessageContent: InputMessageContent{
				MessageText: "/songid " + trackID,
			},
		})
	}
	_ = b.answerInlineQuery(q.ID, results, true)
}

func (b *TelegramBot) handleCommand(chatID int64, chatType string, cmd string, args []string, replyToID int) {
	b.handleCommandWithContext(chatID, chatType, 0, cmd, args, replyToID, nil)
}
