package app

import (
	"strings"
)

func normalizeTelegramFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case telegramFormatAlac:
		return telegramFormatAlac
	case telegramFormatFlac:
		return telegramFormatFlac
	case telegramFormatAac:
		return telegramFormatAac
	case telegramFormatAtmos:
		return telegramFormatAtmos
	default:
		return ""
	}
}

func normalizeTelegramAACType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "aac", "aac-lc", "aac-binaural", "aac-downmix":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeTelegramMVAudioType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "atmos", "ac3", "aac":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeTelegramLyricsFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "lrc", "ttml":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeTelegramLanguage(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case telegramLanguageZh, "cn", "zh-cn", "chinese", "中文":
		return telegramLanguageZh
	case telegramLanguageEn, "en-us", "english":
		return telegramLanguageEn
	default:
		return ""
	}
}

func normalizeChatSettings(settings ChatDownloadSettings) ChatDownloadSettings {
	normalizedFormat := normalizeTelegramFormat(settings.Format)
	if normalizedFormat == "" {
		normalizedFormat = defaultTelegramFormat
	}
	aacType := normalizeTelegramAACType(settings.AACType)
	if aacType == "" {
		aacType = normalizeTelegramAACType(Config.AacType)
	}
	if aacType == "" {
		aacType = defaultTelegramAACType
	}
	mvAudioType := normalizeTelegramMVAudioType(settings.MVAudioType)
	if mvAudioType == "" {
		mvAudioType = normalizeTelegramMVAudioType(Config.MVAudioType)
	}
	if mvAudioType == "" {
		mvAudioType = defaultTelegramMVAudioType
	}
	lyricsFormat := normalizeTelegramLyricsFormat(settings.LyricsFormat)
	if lyricsFormat == "" {
		lyricsFormat = normalizeTelegramLyricsFormat(Config.LrcFormat)
	}
	if lyricsFormat == "" {
		lyricsFormat = defaultTelegramLyricsFormat
	}
	language := normalizeTelegramLanguage(settings.Language)
	if language == "" {
		language = normalizeTelegramLanguage(defaultTelegramLanguage)
	}
	if language == "" {
		language = telegramLanguageZh
	}
	embedLyrics := settings.EmbedLyrics
	embedCover := settings.EmbedCover
	// 新增字段兼容：历史状态未写入 EmbedInited 时，默认都开启。
	if !settings.EmbedInited {
		embedLyrics = true
		embedCover = true
	}
	songZip := settings.SongZip
	autoLyrics := settings.AutoLyrics
	autoCover := settings.AutoCover
	autoAnimated := settings.AutoAnimated
	songComment := settings.SongComment
	if !settings.SettingsInited {
		songZip = false
		autoLyrics = false
		autoCover = false
		autoAnimated = false
		songComment = false
	}
	return ChatDownloadSettings{
		Format:         normalizedFormat,
		AACType:        aacType,
		MVAudioType:    mvAudioType,
		LyricsFormat:   lyricsFormat,
		Language:       language,
		EmbedLyrics:    embedLyrics,
		EmbedCover:     embedCover,
		EmbedInited:    true,
		SongZip:        songZip,
		AutoLyrics:     autoLyrics,
		AutoCover:      autoCover,
		AutoAnimated:   autoAnimated,
		SongComment:    songComment,
		SettingsInited: true,
	}
}

func (b *TelegramBot) getChatLanguage(chatID int64) string {
	return b.getChatSettings(chatID).Language
}

func (b *TelegramBot) getChatSettings(chatID int64) ChatDownloadSettings {
	b.settingsMu.Lock()
	defer b.settingsMu.Unlock()
	if b.chatSettings == nil {
		b.chatSettings = make(map[int64]ChatDownloadSettings)
	}
	settings, ok := b.chatSettings[chatID]
	if !ok {
		return normalizeChatSettings(ChatDownloadSettings{})
	}
	normalized := normalizeChatSettings(settings)
	if normalized != settings {
		b.chatSettings[chatID] = normalized
	}
	return normalized
}

func (b *TelegramBot) updateChatSettings(chatID int64, updateFn func(current ChatDownloadSettings) ChatDownloadSettings) ChatDownloadSettings {
	b.settingsMu.Lock()
	if b.chatSettings == nil {
		b.chatSettings = make(map[int64]ChatDownloadSettings)
	}
	current := normalizeChatSettings(b.chatSettings[chatID])
	updated := normalizeChatSettings(updateFn(current))
	b.chatSettings[chatID] = updated
	b.settingsMu.Unlock()
	b.requestStateSave()
	return updated
}

func (b *TelegramBot) setChatFormat(chatID int64, format string) ChatDownloadSettings {
	normalized := normalizeTelegramFormat(format)
	if normalized == "" {
		return b.getChatSettings(chatID)
	}
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.Format = normalized
		return current
	})
}

func (b *TelegramBot) setChatAACType(chatID int64, aacType string) ChatDownloadSettings {
	normalized := normalizeTelegramAACType(aacType)
	if normalized == "" {
		return b.getChatSettings(chatID)
	}
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.AACType = normalized
		return current
	})
}

func (b *TelegramBot) setChatMVAudioType(chatID int64, mvAudioType string) ChatDownloadSettings {
	normalized := normalizeTelegramMVAudioType(mvAudioType)
	if normalized == "" {
		return b.getChatSettings(chatID)
	}
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.MVAudioType = normalized
		return current
	})
}

func (b *TelegramBot) setChatLyricsFormat(chatID int64, lyricsFormat string) ChatDownloadSettings {
	normalized := normalizeTelegramLyricsFormat(lyricsFormat)
	if normalized == "" {
		return b.getChatSettings(chatID)
	}
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.LyricsFormat = normalized
		return current
	})
}

func (b *TelegramBot) setChatEmbedLyrics(chatID int64, enabled bool) ChatDownloadSettings {
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.EmbedLyrics = enabled
		current.EmbedInited = true
		return current
	})
}

func (b *TelegramBot) setChatEmbedCover(chatID int64, enabled bool) ChatDownloadSettings {
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.EmbedCover = enabled
		current.EmbedInited = true
		return current
	})
}

func (b *TelegramBot) toggleChatEmbedLyrics(chatID int64) ChatDownloadSettings {
	settings := b.getChatSettings(chatID)
	return b.setChatEmbedLyrics(chatID, !settings.EmbedLyrics)
}

func (b *TelegramBot) toggleChatEmbedCover(chatID int64) ChatDownloadSettings {
	settings := b.getChatSettings(chatID)
	return b.setChatEmbedCover(chatID, !settings.EmbedCover)
}

func (b *TelegramBot) setChatLanguage(chatID int64, language string) ChatDownloadSettings {
	normalized := normalizeTelegramLanguage(language)
	if normalized == "" {
		return b.getChatSettings(chatID)
	}
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.Language = normalized
		return current
	})
}

func (b *TelegramBot) toggleChatAutoLyrics(chatID int64) ChatDownloadSettings {
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.AutoLyrics = !current.AutoLyrics
		return current
	})
}

func (b *TelegramBot) toggleChatAutoCover(chatID int64) ChatDownloadSettings {
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.AutoCover = !current.AutoCover
		return current
	})
}

func (b *TelegramBot) toggleChatAutoAnimated(chatID int64) ChatDownloadSettings {
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.AutoAnimated = !current.AutoAnimated
		return current
	})
}

func (b *TelegramBot) toggleChatSongComment(chatID int64) ChatDownloadSettings {
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.SongComment = !current.SongComment
		return current
	})
}

func (b *TelegramBot) maybeNotifySongCommentConfig(chatID int64, replyToID int, settings ChatDownloadSettings) {
	normalized := normalizeChatSettings(settings)
	if !normalized.SongComment {
		return
	}
	if strings.TrimSpace(resolveLastFMAPIKey()) != "" {
		return
	}
	message := "歌曲赏析已开启，但尚未配置 Last.fm API Key。\n请在 config.yaml 设置 lastfm-api-key，或设置环境变量 LASTFM_API_KEY。"
	if normalized.Language == telegramLanguageEn {
		message = "Song comment is enabled, but Last.fm API key is missing.\nSet `lastfm-api-key` in config.yaml, or export LASTFM_API_KEY."
	}
	_ = b.sendMessageWithReply(chatID, message, nil, replyToID)
}

func (b *TelegramBot) toggleChatSongZip(chatID int64) ChatDownloadSettings {
	return b.updateChatSettings(chatID, func(current ChatDownloadSettings) ChatDownloadSettings {
		current.SongZip = !current.SongZip
		return current
	})
}

func (b *TelegramBot) getChatFormat(chatID int64) string {
	return b.getChatSettings(chatID).Format
}
