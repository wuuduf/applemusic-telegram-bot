package app

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/runv3"
)

func buildMusicVideoStreamKeyboard(options []MusicVideoStreamOption, lang string) InlineKeyboardMarkup {
	rows := make([][]InlineKeyboardButton, 0, len(options)+1)
	for i, option := range options {
		rows = append(rows, []InlineKeyboardButton{{
			Text:         fmt.Sprintf("%d. %s", i+1, option.Label),
			CallbackData: fmt.Sprintf("mvstream:%d", i+1),
		}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "❌", CallbackData: "panel_cancel"}})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

func makeMusicVideoInflightKey(chatID int64, mvID string, storefront string, settings ChatDownloadSettings, variantKey string) string {
	key := makeDownloadInflightKey(chatID, mediaTypeMusicVideo, mvID, storefront, transferModeOneByOne, settings)
	variantKey = strings.TrimSpace(variantKey)
	if variantKey == "" {
		return key
	}
	return key + "|mvsel:" + variantKey
}

func (b *TelegramBot) validateMusicVideoRequest(chatID int64, mvID string, storefront string, replyToID int) (string, string, bool) {
	normalizedID, idErr := normalizeMediaIdentifier(mediaTypeMusicVideo, mvID)
	if idErr != nil {
		_ = b.sendMessageWithReply(chatID, "Music Video ID is invalid.", nil, replyToID)
		return "", "", false
	}
	if normalizedID == "" {
		_ = b.sendMessage(chatID, "Music Video ID is empty.", nil)
		return "", "", false
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "MV download requires media-user-token in config.yaml.", nil, replyToID)
		return "", "", false
	}
	if _, err := exec.LookPath("mp4decrypt"); err != nil {
		_ = b.sendMessageWithReply(chatID, "MV download requires mp4decrypt in PATH.", nil, replyToID)
		return "", "", false
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	return normalizedID, storefront, true
}

func (b *TelegramBot) promptMusicVideoStreamSelection(chatID int64, mvID string, storefront string, replyToID int, forceRefresh bool) {
	masterURL, _, _, err := runv3.GetWebplaybackWithContext(b.operationContext(), mvID, b.appleToken, Config.MediaUserToken, true)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to load MV streams: %v", err), nil, replyToID)
		return
	}
	if strings.TrimSpace(masterURL) == "" {
		_ = b.sendMessageWithReply(chatID, "Failed to load MV streams: empty playback url.", nil, replyToID)
		return
	}
	options, err := listMusicVideoStreamOptions(masterURL)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to parse MV streams: %v", err), nil, replyToID)
		return
	}
	if len(options) == 0 {
		_ = b.sendMessageWithReply(chatID, "No MV video streams found.", nil, replyToID)
		return
	}
	message := fmt.Sprintf("请选择 MV 视频流（共 %d 条）:", len(options))
	if b.getChatLanguage(chatID) == telegramLanguageEn {
		message = fmt.Sprintf("Choose MV video stream (%d options):", len(options))
	}
	messageID, err := b.sendMessageWithReplyReturn(chatID, message, buildMusicVideoStreamKeyboard(options, b.getChatLanguage(chatID)), replyToID)
	if err != nil {
		return
	}
	b.setPendingMVStream(chatID, mvID, "", storefront, replyToID, messageID, forceRefresh, options)
}

func (b *TelegramBot) handleMusicVideoStreamSelection(chatID int64, messageID int, choice int) {
	pending, ok := b.getPendingMVStream(chatID, messageID)
	if !ok {
		_ = b.sendMessage(chatID, "没有待选择的 MV 视频流。", nil)
		return
	}
	replyToID := pending.ReplyToMessageID
	if time.Since(pending.CreatedAt) > pendingTTL {
		b.clearPendingMVStreamByMessage(chatID, messageID)
		_ = b.editMessageText(chatID, messageID, "MV 视频流选择已过期，请重新发送链接。", nil)
		return
	}
	if choice < 1 || choice > len(pending.Options) {
		_ = b.sendMessageWithReply(chatID, "视频流序号超出范围。", nil, replyToID)
		return
	}
	selected := pending.Options[choice-1]
	b.clearPendingMVStreamByMessage(chatID, messageID)
	confirmText := fmt.Sprintf("已选择 MV 视频流：%s", selected.Label)
	if b.getChatLanguage(chatID) == telegramLanguageEn {
		confirmText = fmt.Sprintf("Selected MV stream: %s", selected.Label)
	}
	if pending.ForceRefresh {
		confirmText += "\nForce refresh enabled."
	}
	_ = b.editMessageText(chatID, messageID, confirmText, nil)
	b.enqueueMusicVideoDownloadWithOption(chatID, replyToID, pending.MediaID, pending.Storefront, pending.ForceRefresh, selected)
}

func (b *TelegramBot) enqueueMusicVideoDownloadWithOption(chatID int64, replyToID int, mvID string, storefront string, forceRefresh bool, option MusicVideoStreamOption) {
	settings := b.getChatSettings(chatID)
	variantKey := strings.TrimSpace(option.Key)
	variantURL := strings.TrimSpace(option.PlaylistURL)
	variantTag := strings.TrimSpace(option.Label)
	if !forceRefresh && b.trySendCachedMusicVideo(chatID, replyToID, mvID, settings, variantKey) {
		return
	}
	inflightKey := makeMusicVideoInflightKey(chatID, mvID, storefront, settings, variantKey)
	if !b.acquireInflightDownload(inflightKey) {
		_ = b.sendMessageWithReply(chatID, "Same MV task is already running for this chat. Please wait.", nil, replyToID)
		return
	}
	req := &downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       true,
		forceRefresh: forceRefresh,
		taskType:     telegramTaskDownload,
		settings:     settings,
		transferMode: transferModeOneByOne,
		mediaType:    mediaTypeMusicVideo,
		mediaID:      mvID,
		mvVariantKey: variantKey,
		mvVariantURL: variantURL,
		mvVariantTag: variantTag,
		storefront:   storefront,
		inflightKey:  inflightKey,
		requestID:    b.nextRequestID(),
	}
	if queued := b.enqueueTaskRequest(req, "Resource pressure detected. New download tasks are temporarily blocked.", "Download queue is full. Please try again later."); !queued {
		b.releaseInflightDownload(inflightKey)
	}
}

func (b *TelegramBot) setPendingMVStream(chatID int64, mediaID string, mediaName string, storefront string, replyToID int, messageID int, forceRefresh bool, options []MusicVideoStreamOption) {
	b.mvStreamMu.Lock()
	if b.pendingMVStreams == nil {
		b.pendingMVStreams = make(map[int64]map[int]*PendingMVStream)
	}
	if b.pendingMVStreams[chatID] == nil {
		b.pendingMVStreams[chatID] = make(map[int]*PendingMVStream)
	}
	cloned := append([]MusicVideoStreamOption{}, options...)
	b.pendingMVStreams[chatID][messageID] = &PendingMVStream{
		MediaID:          mediaID,
		MediaName:        mediaName,
		Storefront:       storefront,
		ForceRefresh:     forceRefresh,
		ReplyToMessageID: replyToID,
		MessageID:        messageID,
		CreatedAt:        time.Now(),
		Options:          cloned,
	}
	b.mvStreamMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) getPendingMVStream(chatID int64, messageID int) (*PendingMVStream, bool) {
	b.mvStreamMu.Lock()
	defer b.mvStreamMu.Unlock()
	chatPending, ok := b.pendingMVStreams[chatID]
	if !ok {
		return nil, false
	}
	pending, ok := chatPending[messageID]
	return pending, ok
}

func (b *TelegramBot) clearPendingMVStream(chatID int64) {
	b.mvStreamMu.Lock()
	delete(b.pendingMVStreams, chatID)
	b.mvStreamMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) clearPendingMVStreamByMessage(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	b.mvStreamMu.Lock()
	chatPending, ok := b.pendingMVStreams[chatID]
	if !ok {
		b.mvStreamMu.Unlock()
		return
	}
	delete(chatPending, messageID)
	if len(chatPending) == 0 {
		delete(b.pendingMVStreams, chatID)
	}
	b.mvStreamMu.Unlock()
	b.requestStateSave()
}
