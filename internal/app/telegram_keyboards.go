package app

import (
	"fmt"
	"strconv"
	"strings"
)

func parseCommand(text string) (string, []string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", nil, false
	}
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", nil, false
	}
	cmd := strings.TrimPrefix(parts[0], "/")
	if idx := strings.Index(cmd, "@"); idx != -1 {
		cmd = cmd[:idx]
	}
	return strings.ToLower(cmd), parts[1:], true
}

func buildInlineKeyboard(count int, hasPrev bool, hasNext bool, lang string) InlineKeyboardMarkup {
	rowSize := 4
	rows := [][]InlineKeyboardButton{}
	row := []InlineKeyboardButton{}
	for i := 1; i <= count; i++ {
		row = append(row, InlineKeyboardButton{
			Text:         strconv.Itoa(i),
			CallbackData: fmt.Sprintf("sel:%d", i),
		})
		if len(row) == rowSize {
			rows = append(rows, row)
			row = []InlineKeyboardButton{}
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	navRow := []InlineKeyboardButton{}
	prevText := "上一页"
	nextText := "下一页"
	if lang == telegramLanguageEn {
		prevText = "Prev"
		nextText = "Next"
	}
	if hasPrev {
		navRow = append(navRow, InlineKeyboardButton{
			Text:         prevText,
			CallbackData: "page:-1",
		})
	}
	if hasNext {
		navRow = append(navRow, InlineKeyboardButton{
			Text:         nextText,
			CallbackData: "page:1",
		})
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}
	rows = append(rows, []InlineKeyboardButton{
		{Text: "❌", CallbackData: "panel_cancel"},
	})
	return InlineKeyboardMarkup{
		InlineKeyboard: rows,
	}
}

func buildTransferKeyboard(lang string) InlineKeyboardMarkup {
	oneByOneText := "逐个发送"
	if lang == telegramLanguageEn {
		oneByOneText = "Transfer one by one"
	}
	return InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: oneByOneText, CallbackData: "transfer:one"},
				{Text: "ZIP", CallbackData: "transfer:zip"},
			},
			{
				{Text: "❌", CallbackData: "panel_cancel"},
			},
		},
	}
}

func buildArtistModeKeyboard(lang string) InlineKeyboardMarkup {
	albumsText := "专辑"
	allLPsText := "全部LP"
	musicVideosText := "音乐视频"
	allSongsText := "全部歌曲"
	if lang == telegramLanguageEn {
		albumsText = "Albums"
		allLPsText = "All LPs"
		musicVideosText = "Music Videos"
		allSongsText = "All Songs"
	}
	return InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: albumsText, CallbackData: "artist_rel:albums"},
				{Text: allLPsText, CallbackData: "artist_rel:full-albums"},
			},
			{
				{Text: musicVideosText, CallbackData: "artist_rel:music-videos"},
				{Text: allSongsText, CallbackData: "artist_rel:songs"},
			},
			{
				{Text: "❌", CallbackData: "panel_cancel"},
			},
		},
	}
}

func settingButtonText(label string, active bool) string {
	if active {
		return "✓ " + label
	}
	return label
}

func (b *TelegramBot) formatSettingsText(settings ChatDownloadSettings) string {
	normalized := normalizeChatSettings(settings)
	lang := normalized.Language
	songTransfer := "逐个发送"
	if normalized.SongZip {
		songTransfer = "ZIP"
	}
	workers := b.getTaskWorkerLimit()
	if lang == telegramLanguageEn {
		songTransfer = "one-by-one"
		if normalized.SongZip {
			songTransfer = "ZIP"
		}
		return fmt.Sprintf("Download settings:\n- Language: English\n- Format: %s\n- AAC type: %s\n- MV audio: %s\n- Lyrics format: %s\n- Song transfer: %s\n- Task workers(global): %d\n- Embed: lyrics=%t cover=%t\n- Auto extra files: lyrics=%t cover=%t animated=%t\n- Song comment: %t",
			strings.ToUpper(normalized.Format),
			normalized.AACType,
			normalized.MVAudioType,
			strings.ToUpper(normalized.LyricsFormat),
			songTransfer,
			workers,
			normalized.EmbedLyrics,
			normalized.EmbedCover,
			normalized.AutoLyrics,
			normalized.AutoCover,
			normalized.AutoAnimated,
			normalized.SongComment,
		)
	}
	return fmt.Sprintf("下载设置：\n- 语言：中文\n- 格式：%s\n- AAC 类型：%s\n- MV 音频：%s\n- 歌词格式：%s\n- 歌曲发送：%s\n- 任务线程（全局）：%d\n- 内嵌：歌词=%t 封面=%t\n- 自动附加文件：歌词=%t 封面=%t 动态封面=%t\n- 歌曲赏析：%t",
		strings.ToUpper(normalized.Format),
		normalized.AACType,
		normalized.MVAudioType,
		strings.ToUpper(normalized.LyricsFormat),
		songTransfer,
		workers,
		normalized.EmbedLyrics,
		normalized.EmbedCover,
		normalized.AutoLyrics,
		normalized.AutoCover,
		normalized.AutoAnimated,
		normalized.SongComment,
	)
}

func (b *TelegramBot) buildSettingsKeyboard(settings ChatDownloadSettings) InlineKeyboardMarkup {
	normalized := normalizeChatSettings(settings)
	lang := normalized.Language
	format := normalized.Format
	aacType := normalized.AACType
	mvAudioType := normalized.MVAudioType
	lyricsFormat := normalized.LyricsFormat
	workers := b.getTaskWorkerLimit()
	binauralLabel := "Binaural"
	downmixLabel := "Downmix"
	mvAtmosLabel := "MV Atmos"
	mvAC3Label := "MV AC3"
	mvAACLabel := "MV AAC"
	lyricsLRCLabel := "Lyrics LRC"
	lyricsTTMLLabel := "Lyrics TTML"
	songZIPLabel := "Song ZIP"
	embedLyricsLabel := "Embed Lyrics"
	embedCoverLabel := "Embed Cover"
	autoLyricsLabel := "Extra Lyrics"
	autoCoverLabel := "Extra Cover"
	autoAnimatedLabel := "Extra Animated"
	songCommentLabel := "Song Comment"
	workerLabel := "Worker"
	if lang != telegramLanguageEn {
		binauralLabel = "双耳"
		downmixLabel = "下混"
		mvAtmosLabel = "MV Atmos"
		mvAC3Label = "MV AC3"
		mvAACLabel = "MV AAC"
		lyricsLRCLabel = "歌词 LRC"
		lyricsTTMLLabel = "歌词 TTML"
		songZIPLabel = "歌曲 ZIP"
		embedLyricsLabel = "内嵌歌词"
		embedCoverLabel = "内嵌封面"
		autoLyricsLabel = "额外歌词"
		autoCoverLabel = "额外封面"
		autoAnimatedLabel = "额外动态封面"
		songCommentLabel = "歌曲赏析"
		workerLabel = "线程"
	}
	return InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: settingButtonText("ALAC", format == telegramFormatAlac), CallbackData: "setting_format:alac"},
				{Text: settingButtonText("FLAC", format == telegramFormatFlac), CallbackData: "setting_format:flac"},
			},
			{
				{Text: settingButtonText("AAC", format == telegramFormatAac), CallbackData: "setting_format:aac"},
				{Text: settingButtonText("ATMOS", format == telegramFormatAtmos), CallbackData: "setting_format:atmos"},
			},
			{
				{Text: settingButtonText("AAC-LC", aacType == "aac-lc"), CallbackData: "setting_aac:aac-lc"},
				{Text: settingButtonText("AAC", aacType == "aac"), CallbackData: "setting_aac:aac"},
			},
			{
				{Text: settingButtonText(binauralLabel, aacType == "aac-binaural"), CallbackData: "setting_aac:aac-binaural"},
				{Text: settingButtonText(downmixLabel, aacType == "aac-downmix"), CallbackData: "setting_aac:aac-downmix"},
			},
			{
				{Text: settingButtonText(mvAtmosLabel, mvAudioType == "atmos"), CallbackData: "setting_mv_audio:atmos"},
				{Text: settingButtonText(mvAC3Label, mvAudioType == "ac3"), CallbackData: "setting_mv_audio:ac3"},
				{Text: settingButtonText(mvAACLabel, mvAudioType == "aac"), CallbackData: "setting_mv_audio:aac"},
			},
			{
				{Text: settingButtonText(lyricsLRCLabel, lyricsFormat == "lrc"), CallbackData: "setting_lyrics_format:lrc"},
				{Text: settingButtonText(lyricsTTMLLabel, lyricsFormat == "ttml"), CallbackData: "setting_lyrics_format:ttml"},
			},
			{
				{Text: settingButtonText("中文", lang == telegramLanguageZh), CallbackData: "setting_lang:zh"},
				{Text: settingButtonText("English", lang == telegramLanguageEn), CallbackData: "setting_lang:en"},
			},
			{
				{Text: settingButtonText(songZIPLabel, normalized.SongZip), CallbackData: "setting_song_zip"},
			},
			{
				{Text: settingButtonText(embedLyricsLabel, normalized.EmbedLyrics), CallbackData: "setting_embed:lyrics"},
				{Text: settingButtonText(embedCoverLabel, normalized.EmbedCover), CallbackData: "setting_embed:cover"},
			},
			{
				{Text: settingButtonText(fmt.Sprintf("%s 1", workerLabel), workers == 1), CallbackData: "setting_worker:1"},
				{Text: settingButtonText(fmt.Sprintf("%s 2", workerLabel), workers == 2), CallbackData: "setting_worker:2"},
			},
			{
				{Text: settingButtonText(fmt.Sprintf("%s 3", workerLabel), workers == 3), CallbackData: "setting_worker:3"},
				{Text: settingButtonText(fmt.Sprintf("%s 4", workerLabel), workers == 4), CallbackData: "setting_worker:4"},
			},
			{
				{Text: settingButtonText(autoLyricsLabel, normalized.AutoLyrics), CallbackData: "setting_auto:lyrics"},
				{Text: settingButtonText(autoCoverLabel, normalized.AutoCover), CallbackData: "setting_auto:cover"},
				{Text: settingButtonText(autoAnimatedLabel, normalized.AutoAnimated), CallbackData: "setting_auto:animated"},
			},
			{
				{Text: settingButtonText(songCommentLabel, normalized.SongComment), CallbackData: "setting_auto:comment"},
			},
			{
				{Text: "❌", CallbackData: "panel_cancel"},
			},
		},
	}
}
