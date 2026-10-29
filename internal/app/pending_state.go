package app

import (
	"time"

	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
)

func (b *TelegramBot) setPending(chatID int64, kind string, query string, storefront string, offset int, items []apputils.SearchResultItem, hasNext bool, replyToID int, resultsMessageID int, title string) {
	b.pendingMu.Lock()
	if b.pending == nil {
		b.pending = make(map[int64]map[int]*PendingSelection)
	}
	if b.pending[chatID] == nil {
		b.pending[chatID] = make(map[int]*PendingSelection)
	}
	b.pending[chatID][resultsMessageID] = &PendingSelection{
		Kind:             kind,
		Query:            query,
		Title:            title,
		Storefront:       storefront,
		Offset:           offset,
		HasNext:          hasNext,
		Items:            items,
		CreatedAt:        time.Now(),
		ReplyToMessageID: replyToID,
		ResultsMessageID: resultsMessageID,
	}
	b.pendingMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) getPending(chatID int64, messageID int) (*PendingSelection, bool) {
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	chatPending, ok := b.pending[chatID]
	if !ok {
		return nil, false
	}
	pending, ok := chatPending[messageID]
	return pending, ok
}

func (b *TelegramBot) clearPending(chatID int64) {
	b.pendingMu.Lock()
	delete(b.pending, chatID)
	b.pendingMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) clearPendingByMessage(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	b.pendingMu.Lock()
	chatPending, ok := b.pending[chatID]
	if !ok {
		b.pendingMu.Unlock()
		return
	}
	delete(chatPending, messageID)
	if len(chatPending) == 0 {
		delete(b.pending, chatID)
	}
	b.pendingMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) setPendingTransfer(chatID int64, mediaType string, mediaID string, mediaName string, storefront string, replyToID int, messageID int, forceRefresh bool) {
	b.transferMu.Lock()
	if b.pendingTransfers == nil {
		b.pendingTransfers = make(map[int64]map[int]*PendingTransfer)
	}
	if b.pendingTransfers[chatID] == nil {
		b.pendingTransfers[chatID] = make(map[int]*PendingTransfer)
	}
	b.pendingTransfers[chatID][messageID] = &PendingTransfer{
		MediaType:        mediaType,
		MediaID:          mediaID,
		MediaName:        mediaName,
		Storefront:       storefront,
		ForceRefresh:     forceRefresh,
		ReplyToMessageID: replyToID,
		MessageID:        messageID,
		CreatedAt:        time.Now(),
	}
	b.transferMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) getPendingTransfer(chatID int64, messageID int) (*PendingTransfer, bool) {
	b.transferMu.Lock()
	defer b.transferMu.Unlock()
	chatPending, ok := b.pendingTransfers[chatID]
	if !ok {
		return nil, false
	}
	pending, ok := chatPending[messageID]
	return pending, ok
}

func (b *TelegramBot) clearPendingTransfer(chatID int64) {
	b.transferMu.Lock()
	delete(b.pendingTransfers, chatID)
	b.transferMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) clearPendingTransferByMessage(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	b.transferMu.Lock()
	chatPending, ok := b.pendingTransfers[chatID]
	if !ok {
		b.transferMu.Unlock()
		return
	}
	delete(chatPending, messageID)
	if len(chatPending) == 0 {
		delete(b.pendingTransfers, chatID)
	}
	b.transferMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) setPendingArtistMode(chatID int64, artistID string, artistName string, storefront string, replyToID int, messageID int) {
	b.artistModeMu.Lock()
	if b.pendingArtistModes == nil {
		b.pendingArtistModes = make(map[int64]map[int]*PendingArtistMode)
	}
	if b.pendingArtistModes[chatID] == nil {
		b.pendingArtistModes[chatID] = make(map[int]*PendingArtistMode)
	}
	b.pendingArtistModes[chatID][messageID] = &PendingArtistMode{
		ArtistID:         artistID,
		ArtistName:       artistName,
		Storefront:       storefront,
		ReplyToMessageID: replyToID,
		MessageID:        messageID,
		CreatedAt:        time.Now(),
	}
	b.artistModeMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) getPendingArtistMode(chatID int64, messageID int) (*PendingArtistMode, bool) {
	b.artistModeMu.Lock()
	defer b.artistModeMu.Unlock()
	chatPending, ok := b.pendingArtistModes[chatID]
	if !ok {
		return nil, false
	}
	pending, ok := chatPending[messageID]
	return pending, ok
}

func (b *TelegramBot) clearPendingArtistMode(chatID int64) {
	b.artistModeMu.Lock()
	delete(b.pendingArtistModes, chatID)
	b.artistModeMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) clearPendingArtistModeByMessage(chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	b.artistModeMu.Lock()
	chatPending, ok := b.pendingArtistModes[chatID]
	if !ok {
		b.artistModeMu.Unlock()
		return
	}
	delete(chatPending, messageID)
	if len(chatPending) == 0 {
		delete(b.pendingArtistModes, chatID)
	}
	b.artistModeMu.Unlock()
	b.requestStateSave()
}
