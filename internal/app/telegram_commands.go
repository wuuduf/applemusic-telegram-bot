package app

import (
	"fmt"
	"strings"
)

func (b *TelegramBot) handleCommandWithContext(chatID int64, chatType string, userID int64, cmd string, args []string, replyToID int, replyTo *Message) {
	cmd = normalizeTelegramBotCommand(cmd)
	switch cmd {
	case "start", "help":
		_ = b.sendMessage(chatID, b.botHelpTextForChat(chatID, userID), nil)
	case "amwhoami":
		_ = b.sendMessageWithReply(chatID, formatWhoAmIText(userID, chatID), nil, replyToID)
	case "status":
		_ = b.sendMessageWithReply(chatID, b.formatStatusText(chatID), nil, replyToID)
	case "queue":
		_ = b.sendMessageWithReply(chatID, b.formatQueueText(chatID), nil, replyToID)
	case "history":
		_ = b.sendMessageWithReply(chatID, b.formatHistoryText(chatID, args), nil, replyToID)
	case "cancel":
		if len(args) != 1 {
			_ = b.sendMessageWithReply(chatID, "Usage: /cancel <request_id>", nil, replyToID)
			return
		}
		requestID := strings.TrimSpace(args[0])
		if state, ok := b.cancelChatRequest(chatID, requestID); ok {
			switch state {
			case "running":
				_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Cancellation requested for running task: %s", requestID), nil, replyToID)
			default:
				_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Canceled queued task: %s", requestID), nil, replyToID)
			}
			return
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Request not found for this chat: %s", requestID), nil, replyToID)
	case "sub":
		b.handleSubscriptionCommand(chatID, userID, args, replyToID)
	case "subtemp":
		b.handleSubscriptionTempCommand(chatID, userID, args, replyToID)
	case "subrefresh":
		b.handleSubscriptionRefreshCommand(chatID, userID, args, replyToID)
	case "subrefreshall":
		b.handleSubscriptionRefreshAllCommand(chatID, userID, replyToID)
	case "search_song":
		b.handleSearch(chatID, "song", strings.Join(args, " "), replyToID)
	case "download_song":
		b.handleDirectSongDownload(chatID, strings.Join(args, " "), replyToID)
	case "search_album":
		b.handleSearch(chatID, "album", strings.Join(args, " "), replyToID)
	case "search_artist":
		b.handleSearch(chatID, "artist", strings.Join(args, " "), replyToID)
	case "search":
		if len(args) < 2 {
			_ = b.sendMessageWithReply(chatID, "Usage: /search <song|album|artist> <keywords>", nil, replyToID)
			return
		}
		kind := strings.ToLower(args[0])
		b.handleSearch(chatID, kind, strings.Join(args[1:], " "), replyToID)
	case "url":
		if len(args) == 0 {
			_ = b.sendMessageWithReply(chatID, "Usage: /url <apple-music-url> [more-urls...]", nil, replyToID)
			return
		}
		if b.handleURLTextBatch(chatID, replyToID, strings.Join(args, " "), false) > 0 {
			return
		}
		raw := args[0]
		target, err := parseAppleMusicURL(raw)
		if err != nil {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Unsupported Apple Music URL: %s", raw), nil, replyToID)
			return
		}
		b.handleURLTarget(chatID, replyToID, target)
	case "refresh":
		if len(extractAppleMusicURLs(strings.Join(args, " "))) > 1 {
			_ = b.handleURLTextBatch(chatID, replyToID, strings.Join(args, " "), true)
			return
		}
		target, err := resolveRefreshURLTarget(args)
		if err != nil {
			_ = b.sendMessageWithReply(chatID, "Usage: /refresh <apple-music-url> OR /refresh url <apple-music-url>", nil, replyToID)
			return
		}
		if target.MediaType == mediaTypeArtist {
			_ = b.sendMessageWithReply(chatID, "refresh supports song/album/playlist/station/mv URLs only.", nil, replyToID)
			return
		}
		b.handleURLTargetWithOptions(chatID, replyToID, target, true)
	case "artistphoto":
		target, err := resolveCommandTarget(args, mediaTypeArtist)
		if err != nil {
			_ = b.sendMessageWithReply(chatID, "Usage: /artistphoto <artist-url|artist-id>", nil, replyToID)
			return
		}
		if target.MediaType != mediaTypeArtist {
			_ = b.sendMessageWithReply(chatID, "artistphoto only supports artist URL/ID.", nil, replyToID)
			return
		}
		b.promptMediaTransfer(chatID, mediaTypeArtistAsset, target.ID, resolveStorefront(target), "", replyToID, false)
	case "cover":
		target, err := resolveCommandTarget(args, "")
		if err != nil {
			_ = b.sendMessageWithReply(chatID, "Usage: /cover <apple-music-url> OR /cover <song|album|playlist|station|mv|artist> <id>", nil, replyToID)
			return
		}
		b.enqueueCoverTask(chatID, replyToID, target)
	case "animatedcover", "motioncover":
		target, err := resolveCommandTarget(args, "")
		if err != nil {
			_ = b.sendMessageWithReply(chatID, "Usage: /animatedcover <apple-music-url> OR /animatedcover <song|album|playlist|station> <id>", nil, replyToID)
			return
		}
		b.enqueueAnimatedCoverTask(chatID, replyToID, target)
	case "lyrics", "lyric":
		if len(args) == 0 {
			_ = b.sendMessageWithReply(chatID, "Usage: /lyrics <song-url|song-id|album-url|album <id>>", nil, replyToID)
			return
		}
		target, err := resolveCommandTarget(args, "")
		if err != nil && len(args) == 1 {
			target, err = resolveCommandTarget(args, mediaTypeSong)
		}
		if err != nil {
			_ = b.sendMessageWithReply(chatID, "Usage: /lyrics <song-url|song-id|album-url|album <id>>", nil, replyToID)
			return
		}
		switch target.MediaType {
		case mediaTypeSong:
			b.enqueueSongLyricsTask(chatID, replyToID, target)
		case mediaTypeAlbum:
			b.promptMediaTransfer(chatID, mediaTypeAlbumLyrics, target.ID, resolveStorefront(target), "", replyToID, false)
		default:
			_ = b.sendMessageWithReply(chatID, "lyrics command supports song/album only.", nil, replyToID)
		}
	case "id":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, b.formatChatIDTextForChat(chatID), nil)
			return
		}
		if len(args) == 1 {
			if b.handleURLTextBatch(chatID, replyToID, args[0], false) > 0 {
				return
			}
			if target, err := parseAppleMusicURL(args[0]); err == nil {
				b.handleURLTarget(chatID, replyToID, target)
				return
			}
			b.queueDownloadSong(chatID, args[0])
			return
		}
		kind := strings.ToLower(args[0])
		mediaType := normalizeCommandMediaType(kind)
		if mediaType == "" {
			_ = b.sendMessage(chatID, "Usage: /id <song|album|playlist|station|mv|artist|curator> <id> [more-ids...]", nil)
			return
		}
		if b.enqueueMediaIDBatch(chatID, replyToID, mediaType, args[1:]) == 0 {
			_ = b.sendMessage(chatID, "Usage: /id <song|album|playlist|station|mv|artist|curator> <id> [more-ids...]", nil)
		}
	case "songid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /songid <id> [more-ids...]", nil)
			return
		}
		if b.enqueueMediaIDBatch(chatID, replyToID, mediaTypeSong, args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /songid <id> [more-ids...]", nil)
		}
	case "albumid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /albumid <id> [more-ids...]", nil)
			return
		}
		if b.enqueueMediaIDBatch(chatID, replyToID, mediaTypeAlbum, args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /albumid <id> [more-ids...]", nil)
		}
	case "playlistid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /playlistid <id> [more-ids...]", nil)
			return
		}
		if b.enqueueMediaIDBatch(chatID, replyToID, mediaTypePlaylist, args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /playlistid <id> [more-ids...]", nil)
		}
	case "stationid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /stationid <id> [more-ids...]", nil)
			return
		}
		if b.enqueueMediaIDBatch(chatID, replyToID, mediaTypeStation, args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /stationid <id> [more-ids...]", nil)
		}
	case "mvid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /mvid <id> [more-ids...]", nil)
			return
		}
		if b.enqueueMediaIDBatch(chatID, replyToID, mediaTypeMusicVideo, args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /mvid <id> [more-ids...]", nil)
		}
	case "artistid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /artistid <id> [more-ids...]", nil)
			return
		}
		if b.enqueueMediaIDBatch(chatID, replyToID, mediaTypeArtist, args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /artistid <id> [more-ids...]", nil)
		}
	case "settings":
		if len(args) > 0 {
			settings := b.getChatSettings(chatID)
			raw := strings.ToLower(strings.TrimSpace(args[0]))
			if normalized := normalizeTelegramFormat(raw); normalized != "" {
				settings = b.setChatFormat(chatID, normalized)
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			}
			if normalized := normalizeTelegramAACType(raw); normalized != "" {
				settings = b.setChatAACType(chatID, normalized)
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			}
			if normalized := normalizeTelegramMVAudioType(raw); normalized != "" {
				settings = b.setChatMVAudioType(chatID, normalized)
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			}
			if normalized := normalizeTelegramLyricsFormat(raw); normalized != "" {
				settings = b.setChatLyricsFormat(chatID, normalized)
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			}
			if language := normalizeTelegramLanguage(raw); language != "" {
				settings = b.setChatLanguage(chatID, language)
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			}
			if workerLimit, ok := parseTaskWorkerLimit(raw); ok {
				b.setTaskWorkerLimit(workerLimit)
				settings = b.getChatSettings(chatID)
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			}
			switch raw {
			case "lyrics", "lyrics_on", "lyrics_off", "embed_lyrics", "embed_lyrics_on", "embed_lyrics_off", "lyrics_embed", "lyrics_embed_on", "lyrics_embed_off":
				if strings.HasSuffix(raw, "_on") {
					settings = b.setChatEmbedLyrics(chatID, true)
				} else if strings.HasSuffix(raw, "_off") {
					settings = b.setChatEmbedLyrics(chatID, false)
				} else {
					settings = b.toggleChatEmbedLyrics(chatID)
				}
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			case "cover", "cover_on", "cover_off", "embed_cover", "embed_cover_on", "embed_cover_off", "cover_embed", "cover_embed_on", "cover_embed_off":
				if strings.HasSuffix(raw, "_on") {
					settings = b.setChatEmbedCover(chatID, true)
				} else if strings.HasSuffix(raw, "_off") {
					settings = b.setChatEmbedCover(chatID, false)
				} else {
					settings = b.toggleChatEmbedCover(chatID)
				}
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			case "auto_lyrics", "auto_lyrics_on", "auto_lyrics_off", "extra_lyrics", "extra_lyrics_on", "extra_lyrics_off":
				if strings.HasSuffix(raw, "_on") {
					if !settings.AutoLyrics {
						settings = b.toggleChatAutoLyrics(chatID)
					}
				} else if strings.HasSuffix(raw, "_off") {
					if settings.AutoLyrics {
						settings = b.toggleChatAutoLyrics(chatID)
					}
				} else {
					settings = b.toggleChatAutoLyrics(chatID)
				}
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			case "auto_cover", "auto_cover_on", "auto_cover_off", "extra_cover", "extra_cover_on", "extra_cover_off":
				if strings.HasSuffix(raw, "_on") {
					if !settings.AutoCover {
						settings = b.toggleChatAutoCover(chatID)
					}
				} else if strings.HasSuffix(raw, "_off") {
					if settings.AutoCover {
						settings = b.toggleChatAutoCover(chatID)
					}
				} else {
					settings = b.toggleChatAutoCover(chatID)
				}
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			case "animated", "animated_on", "animated_off":
				if raw == "animated_on" {
					if !settings.AutoAnimated {
						settings = b.toggleChatAutoAnimated(chatID)
					}
				} else if raw == "animated_off" {
					if settings.AutoAnimated {
						settings = b.toggleChatAutoAnimated(chatID)
					}
				} else {
					settings = b.toggleChatAutoAnimated(chatID)
				}
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			case "comment", "comment_on", "comment_off",
				"song_comment", "song_comment_on", "song_comment_off",
				"analysis", "analysis_on", "analysis_off",
				"song_analysis", "song_analysis_on", "song_analysis_off",
				"赏析", "赏析_on", "赏析_off":
				if strings.HasSuffix(raw, "_on") {
					if !settings.SongComment {
						settings = b.toggleChatSongComment(chatID)
					}
				} else if strings.HasSuffix(raw, "_off") {
					if settings.SongComment {
						settings = b.toggleChatSongComment(chatID)
					}
				} else {
					settings = b.toggleChatSongComment(chatID)
				}
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				b.maybeNotifySongCommentConfig(chatID, replyToID, settings)
				return
			case "songzip", "song_zip", "songzip_toggle", "song_zip_toggle":
				settings = b.toggleChatSongZip(chatID)
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			case "songzip_on", "song_zip_on", "songzip_zip", "song_zip_zip":
				if !settings.SongZip {
					settings = b.toggleChatSongZip(chatID)
				}
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			case "songzip_off", "song_zip_off", "song_one", "song_onebyone", "song_one_by_one":
				if settings.SongZip {
					settings = b.toggleChatSongZip(chatID)
				}
				_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
				return
			}
			_ = b.sendMessageWithReply(chatID, "Usage: /settings [alac|flac|aac|atmos|aac-lc|aac-binaural|aac-downmix|ac3|lrc|ttml|zh|en|lyrics|cover|auto_lyrics|auto_cover|animated|comment|songzip|worker1..worker4]", nil, replyToID)
			return
		}
		settings := b.getChatSettings(chatID)
		_ = b.sendMessageWithReply(chatID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings), replyToID)
	case "amadmin":
		if !b.isAdminUser(userID) {
			_ = b.sendMessageWithReply(chatID, "只有管理员可以打开管理员面板。", nil, replyToID)
			return
		}
		b.openAdminPanel(chatID, replyToID)
	case "amcachepush":
		if !b.isAdminUser(userID) {
			_ = b.sendMessageWithReply(chatID, "只有管理员可以执行缓存转存。", nil, replyToID)
			return
		}
		b.triggerAdminCachePush(chatID, replyToID)
	case "amwlon":
		if !b.isAdminUser(userID) {
			_ = b.sendMessageWithReply(chatID, "只有管理员可以开启白名单模式。", nil, replyToID)
			return
		}
		changed := b.setUserWhitelistEnabled(true)
		if changed {
			_ = b.sendMessageWithReply(chatID, "已开启用户白名单模式。", nil, replyToID)
		} else {
			_ = b.sendMessageWithReply(chatID, "用户白名单模式已经处于开启状态。", nil, replyToID)
		}
	case "amwloff":
		if !b.isAdminUser(userID) {
			_ = b.sendMessageWithReply(chatID, "只有管理员可以关闭白名单模式。", nil, replyToID)
			return
		}
		changed := b.setUserWhitelistEnabled(false)
		if changed {
			_ = b.sendMessageWithReply(chatID, "已关闭用户白名单模式。", nil, replyToID)
		} else {
			_ = b.sendMessageWithReply(chatID, "用户白名单模式已经处于关闭状态。", nil, replyToID)
		}
	case "amwladd":
		if !b.isAdminUser(userID) {
			_ = b.sendMessageWithReply(chatID, "只有管理员可以添加白名单用户。", nil, replyToID)
			return
		}
		targetUserID, err := resolveAdminTargetUserID(args, replyTo)
		if err != nil {
			_ = b.sendMessageWithReply(chatID, "Usage: /amwladd <user_id> 或回复某个用户消息后发送 /amwladd", nil, replyToID)
			return
		}
		if b.addUserWhitelist(targetUserID) {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已加入白名单：%d", targetUserID), nil, replyToID)
		} else {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("该用户已经在白名单中：%d", targetUserID), nil, replyToID)
		}
	case "amwldel":
		if !b.isAdminUser(userID) {
			_ = b.sendMessageWithReply(chatID, "只有管理员可以移除白名单用户。", nil, replyToID)
			return
		}
		targetUserID, err := resolveAdminTargetUserID(args, replyTo)
		if err != nil {
			_ = b.sendMessageWithReply(chatID, "Usage: /amwldel <user_id> 或回复某个用户消息后发送 /amwldel", nil, replyToID)
			return
		}
		if b.removeUserWhitelist(targetUserID) {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已移出白名单：%d", targetUserID), nil, replyToID)
		} else {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("该用户不在白名单中：%d", targetUserID), nil, replyToID)
		}
	case "amban":
		if !b.isAdminUser(userID) {
			_ = b.sendMessageWithReply(chatID, "只有管理员可以封禁用户。", nil, replyToID)
			return
		}
		targetUserID, err := resolveAdminTargetUserID(args, replyTo)
		if err != nil {
			_ = b.sendMessageWithReply(chatID, "Usage: /amban <user_id> 或回复某个用户消息后发送 /amban", nil, replyToID)
			return
		}
		if b.isAdminUser(targetUserID) {
			_ = b.sendMessageWithReply(chatID, "不能封禁管理员。", nil, replyToID)
			return
		}
		if b.addUserBlacklist(targetUserID) {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已封禁用户：%d", targetUserID), nil, replyToID)
		} else {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("该用户已经在黑名单中：%d", targetUserID), nil, replyToID)
		}
	case "amunban":
		if !b.isAdminUser(userID) {
			_ = b.sendMessageWithReply(chatID, "只有管理员可以解除封禁。", nil, replyToID)
			return
		}
		targetUserID, err := resolveAdminTargetUserID(args, replyTo)
		if err != nil {
			_ = b.sendMessageWithReply(chatID, "Usage: /amunban <user_id> 或回复某个用户消息后发送 /amunban", nil, replyToID)
			return
		}
		if b.removeUserBlacklist(targetUserID) {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已解除封禁：%d", targetUserID), nil, replyToID)
		} else {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("该用户不在黑名单中：%d", targetUserID), nil, replyToID)
		}
	default:
		if strings.EqualFold(strings.TrimSpace(chatType), "private") {
			_ = b.sendMessage(chatID, b.localizeOutgoingText(chatID, "Unknown command. Send /help for usage."), nil)
		}
		return
	}
}

func (b *TelegramBot) handleURLTarget(chatID int64, replyToID int, target *AppleURLTarget) {
	b.handleURLTargetWithOptions(chatID, replyToID, target, false)
}

func (b *TelegramBot) handleURLTextBatch(chatID int64, replyToID int, text string, forceRefresh bool) int {
	urls := extractAppleMusicURLs(text)
	if len(urls) == 0 {
		return 0
	}

	handled := 0
	for _, raw := range urls {
		target, err := parseAppleMusicURL(raw)
		if err != nil {
			_ = b.sendMessageWithReply(chatID, b.localizeOutgoingText(chatID, fmt.Sprintf("Unsupported Apple Music URL: %s", raw)), nil, replyToID)
			continue
		}
		if forceRefresh && target.MediaType == mediaTypeArtist {
			_ = b.sendMessageWithReply(chatID, b.localizeOutgoingText(chatID, fmt.Sprintf("refresh does not support artist URLs, skipped: %s", raw)), nil, replyToID)
			continue
		}
		b.handleURLTargetWithOptions(chatID, replyToID, target, forceRefresh)
		handled++
	}
	return handled
}

func (b *TelegramBot) handleURLTargetWithOptions(chatID int64, replyToID int, target *AppleURLTarget, forceRefresh bool) {
	if target == nil {
		_ = b.sendMessageWithReply(chatID, "Invalid Apple Music URL.", nil, replyToID)
		return
	}
	storefront := resolveStorefront(target)
	switch target.MediaType {
	case mediaTypeSong:
		b.queueDownloadSongWithStorefrontOptions(chatID, target.ID, storefront, replyToID, forceRefresh)
	case mediaTypeAlbum:
		b.queueDownloadAlbumWithStorefrontOptions(chatID, target.ID, storefront, replyToID, forceRefresh)
	case mediaTypePlaylist:
		b.queueDownloadPlaylistWithStorefrontOptions(chatID, target.ID, storefront, replyToID, forceRefresh)
	case mediaTypeStation:
		b.queueDownloadStationWithStorefront(chatID, target.ID, storefront, replyToID, forceRefresh)
	case mediaTypeMusicVideo:
		b.queueDownloadMusicVideoWithStorefront(chatID, target.ID, storefront, replyToID, forceRefresh)
	case mediaTypeArtist:
		artistName := ""
		if target.RawURL != "" {
			if name, _, err := getUrlArtistName(target.RawURL, b.appleToken); err == nil {
				artistName = name
			}
		}
		b.startArtistSelection(chatID, target.ID, artistName, storefront, replyToID)
	case mediaTypeCurator:
		curatorName := ""
		if target.RawURL != "" {
			if name, _, err := getUrlCuratorName(target.RawURL, b.appleToken); err == nil {
				curatorName = name
			}
		}
		b.promptMediaTransfer(chatID, mediaTypeCuratorLPs, target.ID, storefront, curatorName, replyToID, forceRefresh)
	default:
		_ = b.sendMessageWithReply(chatID, "Unsupported Apple Music URL type.", nil, replyToID)
	}
}

func (b *TelegramBot) enqueueMediaIDBatch(chatID int64, replyToID int, mediaType string, args []string) int {
	mediaType = normalizeCommandMediaType(mediaType)
	if mediaType == "" {
		return 0
	}
	ids := splitBatchArgs(args)
	if len(ids) == 0 {
		return 0
	}

	queued := 0
	invalid := make([]string, 0)
	for _, rawID := range ids {
		normalizedID, err := normalizeMediaIdentifier(mediaType, rawID)
		if err != nil {
			invalid = append(invalid, rawID)
			continue
		}
		switch mediaType {
		case mediaTypeSong:
			b.queueDownloadSong(chatID, normalizedID)
		case mediaTypeAlbum:
			b.queueDownloadAlbum(chatID, normalizedID)
		case mediaTypePlaylist:
			b.queueDownloadPlaylist(chatID, normalizedID)
		case mediaTypeStation:
			b.queueDownloadStation(chatID, normalizedID)
		case mediaTypeMusicVideo:
			b.queueDownloadMusicVideo(chatID, normalizedID)
		case mediaTypeArtist:
			b.startArtistSelection(chatID, normalizedID, "", Config.Storefront, replyToID)
		case mediaTypeCurator:
			b.promptMediaTransfer(chatID, mediaTypeCuratorLPs, normalizedID, Config.Storefront, "", replyToID, false)
		default:
			invalid = append(invalid, rawID)
			continue
		}
		queued++
	}

	if len(invalid) > 0 {
		preview := invalid
		if len(preview) > 5 {
			preview = preview[:5]
		}
		suffix := ""
		if len(invalid) > len(preview) {
			suffix = fmt.Sprintf(" (+%d more)", len(invalid)-len(preview))
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Skipped invalid %s ID(s): %s%s", mediaType, strings.Join(preview, ", "), suffix), nil, replyToID)
	}
	return queued
}
