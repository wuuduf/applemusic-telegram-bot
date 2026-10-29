package app

import (
	"fmt"
	"strings"
	"time"

	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
)

func normalizeArtistRelationship(relationship string) string {
	switch strings.ToLower(strings.TrimSpace(relationship)) {
	case "albums", "album":
		return "albums"
	case "full-albums", "fullalbums", "all-lps", "all-lp", "lp", "lps":
		return "full-albums"
	case "music-videos", "musicvideos", "music_video", "musicvideo", "mv", "mvs", "videos", "video":
		return "music-videos"
	case "songs", "song", "all-songs", "allsongs":
		return "songs"
	default:
		return ""
	}
}

func (b *TelegramBot) startArtistSelection(chatID int64, artistID string, artistName string, storefront string, replyToID int) {
	artistID = strings.TrimSpace(artistID)
	if artistID == "" {
		_ = b.sendMessageWithReply(chatID, "Artist ID is empty.", nil, replyToID)
		return
	}
	artistName = strings.TrimSpace(artistName)
	if artistName == "" {
		artistName = "artist " + artistID
	}
	message := fmt.Sprintf("Choose what to browse from %s:", artistName)
	messageID, err := b.sendMessageWithReplyReturn(chatID, message, buildArtistModeKeyboard(b.getChatLanguage(chatID)), replyToID)
	if err != nil {
		return
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	b.setPendingArtistMode(chatID, artistID, artistName, storefront, replyToID, messageID)
}

func (b *TelegramBot) handleArtistModeSelection(chatID int64, messageID int, relationship string) {
	pending, ok := b.getPendingArtistMode(chatID, messageID)
	if !ok {
		return
	}
	if time.Since(pending.CreatedAt) > pendingTTL {
		b.clearPendingArtistModeByMessage(chatID, messageID)
		_ = b.editMessageText(chatID, messageID, "Selection expired. Please request the artist again.", nil)
		return
	}
	normalizedRelationship := normalizeArtistRelationship(relationship)
	if normalizedRelationship == "" {
		_ = b.editMessageText(chatID, messageID, "Unknown artist view.", nil)
		return
	}
	replyToID := pending.ReplyToMessageID
	if normalizedRelationship == "songs" {
		b.clearPendingArtistModeByMessage(chatID, messageID)
		b.promptMediaTransfer(chatID, mediaTypeArtistSongs, pending.ArtistID, pending.Storefront, pending.ArtistName, replyToID, false)
		return
	}
	if normalizedRelationship == "full-albums" {
		b.clearPendingArtistModeByMessage(chatID, messageID)
		b.promptMediaTransfer(chatID, mediaTypeArtistLPs, pending.ArtistID, pending.Storefront, pending.ArtistName, replyToID, false)
		return
	}
	var (
		items   []apputils.SearchResultItem
		hasNext bool
		err     error
		kind    string
		text    string
	)
	switch normalizedRelationship {
	case "albums":
		items, hasNext, err = apputils.FetchArtistAlbums(pending.Storefront, pending.ArtistID, b.appleToken, b.searchLimit, 0, b.searchLanguage())
		kind = "artist_album"
		if err == nil {
			text = apputils.FormatArtistAlbums(pending.ArtistName, items)
		}
	case "music-videos":
		items, hasNext, err = apputils.FetchArtistMusicVideos(pending.Storefront, pending.ArtistID, b.appleToken, b.searchLimit, 0, b.searchLanguage())
		kind = "artist_mv"
		if err == nil {
			text = apputils.FormatArtistMusicVideos(pending.ArtistName, items)
		}
	}
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to load artist content: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	if len(items) == 0 {
		_ = b.sendMessageWithReply(chatID, "No content found for this artist.", nil, replyToID)
		return
	}
	resultMessageID, err := b.sendMessageWithReplyReturn(chatID, text, buildInlineKeyboard(len(items), false, hasNext, b.getChatLanguage(chatID)), replyToID)
	if err != nil {
		return
	}
	b.setPending(chatID, kind, pending.ArtistID, pending.Storefront, 0, items, hasNext, replyToID, resultMessageID, pending.ArtistName)
	b.clearPendingArtistModeByMessage(chatID, messageID)
}

func (b *TelegramBot) handleSearch(chatID int64, kind string, query string, replyToID int) {
	query = strings.TrimSpace(query)
	if query == "" {
		_ = b.sendMessageWithReply(chatID, "Please provide a search query.", nil, replyToID)
		return
	}
	kind = strings.ToLower(kind)
	if kind != "song" && kind != "album" && kind != "artist" {
		_ = b.sendMessageWithReply(chatID, "Search type must be song, album, or artist.", nil, replyToID)
		return
	}
	offset := 0
	items, hasNext, err := b.fetchSearchPage(kind, query, offset)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Search failed: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	if len(items) == 0 {
		_ = b.sendMessageWithReply(chatID, "No results found.", nil, replyToID)
		return
	}
	message := apputils.FormatSearchResults(kind, query, items)
	messageID, err := b.sendMessageWithReplyReturn(chatID, message, buildInlineKeyboard(len(items), offset > 0, hasNext, b.getChatLanguage(chatID)), replyToID)
	if err != nil {
		return
	}
	b.setPending(chatID, kind, query, Config.Storefront, offset, items, hasNext, replyToID, messageID, "")
}

func (b *TelegramBot) searchLanguage() string {
	lang := strings.TrimSpace(Config.TelegramSearchLanguage)
	if lang == "" {
		lang = strings.TrimSpace(Config.Language)
	}
	return lang
}

// handleDirectSongDownload 实现 /dg 点歌：搜索歌曲并直接下载第一个最匹配的结果，
// 不弹选择面板。若结果里第一个是歌曲则立即排队下载并反馈歌手+歌名。
func (b *TelegramBot) handleDirectSongDownload(chatID int64, query string, replyToID int) {
	query = strings.TrimSpace(query)
	if query == "" {
		_ = b.sendMessageWithReply(chatID, "Usage: /dg <song name or artist - song>", nil, replyToID)
		return
	}
	items, _, err := b.fetchSearchPage("song", query, 0)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Search failed: %s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	if len(items) == 0 {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("No results found for %q.", query), nil, replyToID)
		return
	}
	var selected *apputils.SearchResultItem
	for i := range items {
		if strings.EqualFold(items[i].Type, "song") {
			selected = &items[i]
			break
		}
	}
	if selected == nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("No song result found for %q.", query), nil, replyToID)
		return
	}
	setSearchMeta(selected.ID, selected.Name, selected.Artist)
	b.queueDownloadSongWithStorefront(chatID, selected.ID, Config.Storefront, replyToID)
}

func (b *TelegramBot) fetchSearchPage(kind string, query string, offset int) ([]apputils.SearchResultItem, bool, error) {
	apiType := kind + "s"
	resp, err := ampapi.Search(Config.Storefront, query, apiType, b.searchLanguage(), b.appleToken, b.searchLimit, offset)
	if err != nil {
		return nil, false, err
	}
	items, hasNext := apputils.BuildSearchItems(kind, resp)
	return items, hasNext, nil
}

func (b *TelegramBot) handleSelection(chatID int64, messageID int, choice int) {
	pending, ok := b.getPending(chatID, messageID)
	if !ok {
		_ = b.sendMessage(chatID, "No active selection. Start with /search.", nil)
		return
	}
	replyToID := pending.ReplyToMessageID
	if time.Since(pending.CreatedAt) > pendingTTL {
		b.clearPendingByMessage(chatID, messageID)
		_ = b.sendMessageWithReply(chatID, "Selection expired. Please search again.", nil, replyToID)
		return
	}
	if choice < 1 || choice > len(pending.Items) {
		_ = b.sendMessageWithReply(chatID, "Selection out of range.", nil, replyToID)
		return
	}
	selected := pending.Items[choice-1]
	storefront := pending.Storefront
	if storefront == "" {
		storefront = Config.Storefront
	}
	// Selection confirmed: remove the search list message and clear pending state.
	b.clearPendingByMessage(chatID, messageID)
	_ = b.deleteMessage(chatID, messageID)
	switch pending.Kind {
	case "song":
		setSearchMeta(selected.ID, selected.Name, selected.Artist)
		b.queueDownloadSongWithStorefront(chatID, selected.ID, storefront, replyToID)
	case "album", "artist_album":
		b.queueDownloadAlbumWithStorefront(chatID, selected.ID, storefront, replyToID)
	case "artist_mv":
		b.queueDownloadMusicVideoWithStorefront(chatID, selected.ID, storefront, replyToID, false)
	case "artist":
		b.startArtistSelection(chatID, selected.ID, selected.Name, storefront, replyToID)
	}
}

func (b *TelegramBot) handleMediaTransfer(chatID int64, messageID int, mode string) {
	pending, ok := b.getPendingTransfer(chatID, messageID)
	if !ok {
		return
	}
	if time.Since(pending.CreatedAt) > pendingTTL {
		b.clearPendingTransferByMessage(chatID, messageID)
		_ = b.editMessageText(chatID, messageID, "Selection expired. Please request it again.", nil)
		return
	}
	mediaID := pending.MediaID
	mediaType := pending.MediaType
	replyToID := pending.ReplyToMessageID
	b.clearPendingTransferByMessage(chatID, messageID)
	settings := b.getChatSettings(chatID)

	if mediaType == mediaTypeArtistAsset {
		switch mode {
		case transferModeOneByOne:
			_ = b.editMessageText(chatID, messageID, "Artist assets export mode: one by one.", nil)
			b.enqueueArtistAssetsTask(chatID, replyToID, mediaID, pending.Storefront, transferModeOneByOne)
		case transferModeZip:
			_ = b.editMessageText(chatID, messageID, "Artist assets export mode: ZIP.", nil)
			b.enqueueArtistAssetsTask(chatID, replyToID, mediaID, pending.Storefront, transferModeZip)
		default:
			_ = b.editMessageText(chatID, messageID, "Unknown artist assets export mode.", nil)
		}
		return
	}

	if mediaType == mediaTypeAlbumLyrics {
		switch mode {
		case transferModeOneByOne:
			_ = b.editMessageText(chatID, messageID, "Lyrics export mode: one by one.", nil)
			b.enqueueAlbumLyricsTask(chatID, replyToID, mediaID, pending.Storefront, transferModeOneByOne)
		case transferModeZip:
			_ = b.editMessageText(chatID, messageID, "Lyrics export mode: ZIP.", nil)
			b.enqueueAlbumLyricsTask(chatID, replyToID, mediaID, pending.Storefront, transferModeZip)
		default:
			_ = b.editMessageText(chatID, messageID, "Unknown lyrics export mode.", nil)
		}
		return
	}

	switch mode {
	case transferModeOneByOne:
		if pending.ForceRefresh {
			_ = b.editMessageText(chatID, messageID, "Transfer mode: one by one (refresh).", nil)
		} else {
			_ = b.editMessageText(chatID, messageID, "Transfer mode: one by one.", nil)
		}
		if mediaType == mediaTypeSong {
			b.enqueueSongDownload(chatID, mediaID, pending.Storefront, replyToID, transferModeOneByOne, pending.ForceRefresh)
			return
		}
		b.enqueueCollectionDownload(chatID, mediaType, mediaID, pending.Storefront, replyToID, transferModeOneByOne, pending.ForceRefresh)
	case transferModeZip:
		allowCollectionZipCache := mediaType != mediaTypeArtistLPs && mediaType != mediaTypeCuratorLPs
		if mediaType == mediaTypeSong {
			if !pending.ForceRefresh && b.trySendCachedBundleZip(chatID, mediaType, mediaID, replyToID, settings) {
				_ = b.editMessageText(chatID, messageID, "Transfer mode: ZIP (cached).", nil)
				return
			}
			if pending.ForceRefresh {
				_ = b.editMessageText(chatID, messageID, "Transfer mode: ZIP (refresh).", nil)
			} else {
				_ = b.editMessageText(chatID, messageID, "Transfer mode: ZIP.", nil)
			}
			b.enqueueSongDownload(chatID, mediaID, pending.Storefront, replyToID, transferModeZip, pending.ForceRefresh)
			return
		}
		if allowCollectionZipCache && !pending.ForceRefresh && b.trySendCachedBundleZip(chatID, mediaType, mediaID, replyToID, settings) {
			_ = b.editMessageText(chatID, messageID, "Transfer mode: ZIP (cached).", nil)
			return
		}
		if pending.ForceRefresh {
			_ = b.editMessageText(chatID, messageID, "Transfer mode: ZIP (refresh).", nil)
		} else {
			_ = b.editMessageText(chatID, messageID, "Transfer mode: ZIP.", nil)
		}
		b.enqueueCollectionDownload(chatID, mediaType, mediaID, pending.Storefront, replyToID, transferModeZip, pending.ForceRefresh)
	default:
		_ = b.editMessageText(chatID, messageID, "Unknown transfer mode.", nil)
	}
}

func (b *TelegramBot) handlePage(chatID int64, messageID int, delta int) {
	pending, ok := b.getPending(chatID, messageID)
	if !ok {
		return
	}
	if pending.Query == "" {
		return
	}
	newOffset := pending.Offset + delta*b.searchLimit
	if newOffset < 0 {
		return
	}
	var (
		items   []apputils.SearchResultItem
		hasNext bool
		err     error
		message string
	)
	switch pending.Kind {
	case "song", "album", "artist":
		items, hasNext, err = b.fetchSearchPage(pending.Kind, pending.Query, newOffset)
		if err != nil {
			_ = b.editMessageText(chatID, messageID, fmt.Sprintf("Search failed: %s", b.sanitizeTelegramErr(err)), nil)
			return
		}
		if len(items) == 0 {
			return
		}
		message = apputils.FormatSearchResults(pending.Kind, pending.Query, items)
	case "artist_album":
		storefront := pending.Storefront
		if storefront == "" {
			storefront = Config.Storefront
		}
		items, hasNext, err = apputils.FetchArtistAlbums(storefront, pending.Query, b.appleToken, b.searchLimit, newOffset, b.searchLanguage())
		if err != nil {
			_ = b.editMessageText(chatID, messageID, fmt.Sprintf("Failed to load artist albums: %v", err), nil)
			return
		}
		if len(items) == 0 {
			return
		}
		message = apputils.FormatArtistAlbums(pending.Title, items)
	case "artist_mv":
		storefront := pending.Storefront
		if storefront == "" {
			storefront = Config.Storefront
		}
		items, hasNext, err = apputils.FetchArtistMusicVideos(storefront, pending.Query, b.appleToken, b.searchLimit, newOffset, b.searchLanguage())
		if err != nil {
			_ = b.editMessageText(chatID, messageID, fmt.Sprintf("Failed to load artist music videos: %v", err), nil)
			return
		}
		if len(items) == 0 {
			return
		}
		message = apputils.FormatArtistMusicVideos(pending.Title, items)
	default:
		return
	}
	_ = b.editMessageText(chatID, messageID, message, buildInlineKeyboard(len(items), newOffset > 0, hasNext, b.getChatLanguage(chatID)))
	b.setPending(chatID, pending.Kind, pending.Query, pending.Storefront, newOffset, items, hasNext, pending.ReplyToMessageID, messageID, pending.Title)
}
