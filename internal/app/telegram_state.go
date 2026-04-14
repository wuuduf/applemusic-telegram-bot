package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
)

const telegramStateVersion = 1

type telegramPersistedRequest struct {
	RequestID    string               `json:"request_id"`
	ChatID       int64                `json:"chat_id"`
	ReplyToID    int                  `json:"reply_to_id"`
	Single       bool                 `json:"single"`
	ForceRefresh bool                 `json:"force_refresh,omitempty"`
	TaskType     string               `json:"task_type,omitempty"`
	Settings     ChatDownloadSettings `json:"settings"`
	TransferMode string               `json:"transfer_mode"`
	MediaType    string               `json:"media_type"`
	MediaID      string               `json:"media_id"`
	Storefront   string               `json:"storefront"`
	InflightKey  string               `json:"inflight_key"`
	State        string               `json:"state"`
	UpdatedAt    time.Time            `json:"updated_at"`
}

type telegramPersistedAutoDelete struct {
	ChatID    int64     `json:"chat_id"`
	MessageID int       `json:"message_id"`
	Sticky    bool      `json:"sticky,omitempty"`
	DeleteAt  time.Time `json:"delete_at"`
}

type telegramPersistedState struct {
	Version              int                                 `json:"version"`
	SavedAt              time.Time                           `json:"saved_at"`
	Pending              map[int64]map[int]PendingSelection  `json:"pending,omitempty"`
	PendingTransfers     map[int64]map[int]PendingTransfer   `json:"pending_transfers,omitempty"`
	PendingArtistModes   map[int64]map[int]PendingArtistMode `json:"pending_artist_modes,omitempty"`
	Requests             []telegramPersistedRequest          `json:"requests,omitempty"`
	InflightKeys         []string                            `json:"inflight_keys,omitempty"`
	AutoDelete           []telegramPersistedAutoDelete       `json:"auto_delete,omitempty"`
	ChatSettings         map[int64]ChatDownloadSettings      `json:"chat_settings,omitempty"`
	SearchMeta           map[string]AudioMeta                `json:"search_meta,omitempty"`
	UserWhitelistEnabled *bool                               `json:"user_whitelist_enabled,omitempty"`
	UserWhitelist        []int64                             `json:"user_whitelist,omitempty"`
	UserBlacklist        []int64                             `json:"user_blacklist,omitempty"`
	ForwardEnabled       *bool                               `json:"forward_enabled,omitempty"`
}

func resolveTelegramStateFile(cacheFile string) string {
	if raw := strings.TrimSpace(Config.TelegramStateFile); raw != "" {
		return filepath.Clean(raw)
	}
	cacheFile = strings.TrimSpace(cacheFile)
	if cacheFile != "" {
		base := strings.TrimSuffix(cacheFile, filepath.Ext(cacheFile))
		if strings.TrimSpace(base) != "" {
			return filepath.Clean(base + ".state.json")
		}
	}
	return defaultTelegramStateFile
}

func (b *TelegramBot) startStateSaver() {
	if b == nil || strings.TrimSpace(b.stateFile) == "" {
		return
	}
	if b.stateSave != nil {
		return
	}
	saveCh := make(chan struct{}, 1)
	stopCh := make(chan struct{})
	b.stateSave = saveCh
	b.stateStop = stopCh
	b.stateWG.Add(1)
	go func() {
		defer b.stateWG.Done()
		runWithRecovery("telegram state saver", nil, func() {
			for {
				select {
				case <-saveCh:
					runWithRecovery("telegram saveRuntimeStateNow", nil, func() {
						if err := b.saveRuntimeStateNow(); err != nil {
							fmt.Printf("telegram runtime state save failed (%s): %v\n", strings.TrimSpace(b.stateFile), err)
							appendRuntimeErrorLogf("telegram runtime state save failed (%s): %v", strings.TrimSpace(b.stateFile), err)
						}
					})
				case <-stopCh:
					return
				}
			}
		})
	}()
}

func (b *TelegramBot) stopStateSaver() {
	if b == nil || b.stateStop == nil {
		return
	}
	if err := b.saveRuntimeStateNow(); err != nil {
		fmt.Printf("telegram runtime state save failed (%s): %v\n", strings.TrimSpace(b.stateFile), err)
		appendRuntimeErrorLogf("telegram runtime state save failed (%s): %v", strings.TrimSpace(b.stateFile), err)
	}
	stopCh := b.stateStop
	close(stopCh)
	b.stateWG.Wait()
	b.stateStop = nil
	b.stateSave = nil
}

func (b *TelegramBot) requestStateSave() {
	if b == nil || b.stateSave == nil {
		return
	}
	select {
	case b.stateSave <- struct{}{}:
	default:
	}
}

func (b *TelegramBot) nextRequestID() string {
	if b == nil {
		return ""
	}
	seq := b.requestSeq.Add(1)
	return fmt.Sprintf("tg-%d-%d", time.Now().UnixNano(), seq)
}

func (b *TelegramBot) trackQueuedRequest(req *downloadRequest) {
	if b == nil || req == nil {
		return
	}
	if strings.TrimSpace(req.requestID) == "" {
		req.requestID = b.nextRequestID()
	}
	record := telegramPersistedRequest{
		RequestID:    req.requestID,
		ChatID:       req.chatID,
		ReplyToID:    req.replyToID,
		Single:       req.single,
		ForceRefresh: req.forceRefresh,
		TaskType:     normalizeTelegramTaskType(req.taskType),
		Settings:     normalizeChatSettings(req.settings),
		TransferMode: req.transferMode,
		MediaType:    req.mediaType,
		MediaID:      req.mediaID,
		Storefront:   req.storefront,
		InflightKey:  req.inflightKey,
		State:        "queued",
		UpdatedAt:    time.Now(),
	}
	b.requestStateMu.Lock()
	if b.activeRequests == nil {
		b.activeRequests = make(map[string]telegramPersistedRequest)
	}
	b.activeRequests[record.RequestID] = record
	b.requestStateMu.Unlock()
	appRuntimeMetrics.recordTaskQueued(record.TaskType)
	b.requestStateSave()
}

func (b *TelegramBot) markRequestRunning(requestID string) {
	if b == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	b.requestStateMu.Lock()
	record, ok := b.activeRequests[requestID]
	if ok {
		record.State = "running"
		record.UpdatedAt = time.Now()
		b.activeRequests[requestID] = record
	}
	b.requestStateMu.Unlock()
	if ok {
		appRuntimeMetrics.recordTaskStarted(record.TaskType)
		b.requestStateSave()
	}
}

func (b *TelegramBot) untrackRequest(requestID string) {
	if b == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	b.requestStateMu.Lock()
	_, existed := b.activeRequests[requestID]
	delete(b.activeRequests, requestID)
	b.requestStateMu.Unlock()
	if existed {
		b.requestStateSave()
	}
}

func (b *TelegramBot) saveRuntimeStateNow() error {
	if b == nil || strings.TrimSpace(b.stateFile) == "" {
		return nil
	}
	snapshot := b.buildRuntimeStateSnapshot()
	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	target := filepath.Clean(b.stateFile)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".telegram-state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (b *TelegramBot) buildRuntimeStateSnapshot() telegramPersistedState {
	state := telegramPersistedState{
		Version:            telegramStateVersion,
		SavedAt:            time.Now(),
		Pending:            make(map[int64]map[int]PendingSelection),
		PendingTransfers:   make(map[int64]map[int]PendingTransfer),
		PendingArtistModes: make(map[int64]map[int]PendingArtistMode),
		Requests:           []telegramPersistedRequest{},
		InflightKeys:       []string{},
		AutoDelete:         []telegramPersistedAutoDelete{},
		ChatSettings:       make(map[int64]ChatDownloadSettings),
		SearchMeta:         make(map[string]AudioMeta),
	}

	b.pendingMu.Lock()
	for chatID, pendingMap := range b.pending {
		if len(pendingMap) == 0 {
			continue
		}
		cloned := make(map[int]PendingSelection, len(pendingMap))
		for messageID, pending := range pendingMap {
			if pending == nil {
				continue
			}
			value := *pending
			value.Items = append([]apputils.SearchResultItem{}, pending.Items...)
			cloned[messageID] = value
		}
		if len(cloned) > 0 {
			state.Pending[chatID] = cloned
		}
	}
	b.pendingMu.Unlock()

	b.transferMu.Lock()
	for chatID, pendingMap := range b.pendingTransfers {
		if len(pendingMap) == 0 {
			continue
		}
		cloned := make(map[int]PendingTransfer, len(pendingMap))
		for messageID, pending := range pendingMap {
			if pending == nil {
				continue
			}
			cloned[messageID] = *pending
		}
		if len(cloned) > 0 {
			state.PendingTransfers[chatID] = cloned
		}
	}
	b.transferMu.Unlock()

	b.artistModeMu.Lock()
	for chatID, pendingMap := range b.pendingArtistModes {
		if len(pendingMap) == 0 {
			continue
		}
		cloned := make(map[int]PendingArtistMode, len(pendingMap))
		for messageID, pending := range pendingMap {
			if pending == nil {
				continue
			}
			cloned[messageID] = *pending
		}
		if len(cloned) > 0 {
			state.PendingArtistModes[chatID] = cloned
		}
	}
	b.artistModeMu.Unlock()

	b.requestStateMu.Lock()
	inflightKeys := make(map[string]struct{})
	for _, request := range b.activeRequests {
		state.Requests = append(state.Requests, request)
		if key := strings.TrimSpace(request.InflightKey); key != "" {
			inflightKeys[key] = struct{}{}
		}
	}
	b.requestStateMu.Unlock()

	for key := range inflightKeys {
		state.InflightKeys = append(state.InflightKeys, key)
	}

	b.autoDeleteMu.Lock()
	for key, deleteAt := range b.autoDeleteDeadline {
		if deleteAt.IsZero() {
			continue
		}
		chatID, messageID, ok := parseAutoDeleteKey(key)
		if !ok {
			continue
		}
		state.AutoDelete = append(state.AutoDelete, telegramPersistedAutoDelete{
			ChatID:    chatID,
			MessageID: messageID,
			Sticky:    b.autoDeleteSticky[key],
			DeleteAt:  deleteAt,
		})
	}
	b.autoDeleteMu.Unlock()

	b.settingsMu.Lock()
	for chatID, settings := range b.chatSettings {
		state.ChatSettings[chatID] = settings
	}
	b.settingsMu.Unlock()

	b.accessMu.RLock()
	state.UserWhitelistEnabled = boolPtr(b.userWhitelistEnabled)
	state.UserWhitelist = snapshotIDSet(b.userWhitelist)
	state.UserBlacklist = snapshotIDSet(b.userBlacklist)
	state.ForwardEnabled = boolPtr(b.forwardEnabled)
	b.accessMu.RUnlock()

	searchMetaMu.Lock()
	for trackID, meta := range searchMetaByID {
		state.SearchMeta[trackID] = meta
	}
	searchMetaMu.Unlock()

	if len(state.Pending) == 0 {
		state.Pending = nil
	}
	if len(state.PendingTransfers) == 0 {
		state.PendingTransfers = nil
	}
	if len(state.PendingArtistModes) == 0 {
		state.PendingArtistModes = nil
	}
	if len(state.Requests) == 0 {
		state.Requests = nil
	}
	if len(state.InflightKeys) == 0 {
		state.InflightKeys = nil
	}
	if len(state.AutoDelete) == 0 {
		state.AutoDelete = nil
	}
	if len(state.ChatSettings) == 0 {
		state.ChatSettings = nil
	}
	if len(state.SearchMeta) == 0 {
		state.SearchMeta = nil
	}
	if len(state.UserWhitelist) == 0 {
		state.UserWhitelist = nil
	}
	if len(state.UserBlacklist) == 0 {
		state.UserBlacklist = nil
	}
	return state
}

func (b *TelegramBot) restoreRuntimeState() {
	if b == nil || strings.TrimSpace(b.stateFile) == "" {
		return
	}
	state, err := loadRuntimeStateFromFile(b.stateFile)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("telegram runtime state load failed: %v\n", err)
			appendRuntimeErrorLogf("telegram runtime state load failed: %v", err)
		}
		return
	}

	b.pendingMu.Lock()
	b.pending = make(map[int64]map[int]*PendingSelection)
	for chatID, pendingMap := range state.Pending {
		if len(pendingMap) == 0 {
			continue
		}
		target := make(map[int]*PendingSelection, len(pendingMap))
		for messageID, pending := range pendingMap {
			value := pending
			value.Items = append([]apputils.SearchResultItem{}, pending.Items...)
			target[messageID] = &value
		}
		if len(target) > 0 {
			b.pending[chatID] = target
		}
	}
	b.pendingMu.Unlock()

	b.transferMu.Lock()
	b.pendingTransfers = make(map[int64]map[int]*PendingTransfer)
	for chatID, pendingMap := range state.PendingTransfers {
		if len(pendingMap) == 0 {
			continue
		}
		target := make(map[int]*PendingTransfer, len(pendingMap))
		for messageID, pending := range pendingMap {
			value := pending
			target[messageID] = &value
		}
		if len(target) > 0 {
			b.pendingTransfers[chatID] = target
		}
	}
	b.transferMu.Unlock()

	b.artistModeMu.Lock()
	b.pendingArtistModes = make(map[int64]map[int]*PendingArtistMode)
	for chatID, pendingMap := range state.PendingArtistModes {
		if len(pendingMap) == 0 {
			continue
		}
		target := make(map[int]*PendingArtistMode, len(pendingMap))
		for messageID, pending := range pendingMap {
			value := pending
			target[messageID] = &value
		}
		if len(target) > 0 {
			b.pendingArtistModes[chatID] = target
		}
	}
	b.artistModeMu.Unlock()

	b.clearAllAutoDeleteMessages()
	recoveredAutoDelete := 0
	for _, entry := range state.AutoDelete {
		if entry.ChatID == 0 || entry.MessageID <= 0 {
			continue
		}
		deleteAt := entry.DeleteAt
		if deleteAt.IsZero() {
			deleteAt = time.Now().Add(telegramAutoDeleteAfter)
		}
		b.scheduleAutoDeleteMessageAt(entry.ChatID, entry.MessageID, entry.Sticky, deleteAt)
		recoveredAutoDelete++
	}

	b.settingsMu.Lock()
	if b.chatSettings == nil {
		b.chatSettings = make(map[int64]ChatDownloadSettings)
	}
	for chatID, settings := range state.ChatSettings {
		b.chatSettings[chatID] = normalizeChatSettings(settings)
	}
	b.settingsMu.Unlock()

	b.accessMu.Lock()
	if b.userWhitelist == nil {
		b.userWhitelist = make(map[int64]bool)
	} else {
		for userID := range b.userWhitelist {
			delete(b.userWhitelist, userID)
		}
	}
	if b.userBlacklist == nil {
		b.userBlacklist = make(map[int64]bool)
	} else {
		for userID := range b.userBlacklist {
			delete(b.userBlacklist, userID)
		}
	}
	if state.UserWhitelistEnabled != nil {
		b.userWhitelistEnabled = *state.UserWhitelistEnabled
	}
	if state.ForwardEnabled != nil {
		b.forwardEnabled = *state.ForwardEnabled
	}
	for _, userID := range state.UserWhitelist {
		if userID != 0 {
			b.userWhitelist[userID] = true
		}
	}
	for _, userID := range state.UserBlacklist {
		if userID != 0 {
			b.userBlacklist[userID] = true
		}
	}
	b.accessMu.Unlock()

	searchMetaMu.Lock()
	for trackID, meta := range state.SearchMeta {
		if strings.TrimSpace(trackID) == "" {
			continue
		}
		searchMetaByID[trackID] = meta
	}
	searchMetaMu.Unlock()

	b.inflightMu.Lock()
	if b.inflightDownloads == nil {
		b.inflightDownloads = make(map[string]struct{})
	}
	for key := range b.inflightDownloads {
		delete(b.inflightDownloads, key)
	}
	b.inflightMu.Unlock()

	b.requestStateMu.Lock()
	b.activeRequests = make(map[string]telegramPersistedRequest)
	requestInflightKeys := make([]string, 0, len(state.Requests))
	for _, request := range state.Requests {
		if strings.TrimSpace(request.RequestID) == "" {
			continue
		}
		b.activeRequests[request.RequestID] = request
		if strings.TrimSpace(request.InflightKey) != "" {
			requestInflightKeys = append(requestInflightKeys, strings.TrimSpace(request.InflightKey))
		}
	}
	b.requestStateMu.Unlock()
	if len(requestInflightKeys) > 0 {
		b.inflightMu.Lock()
		for _, key := range requestInflightKeys {
			b.inflightDownloads[key] = struct{}{}
		}
		b.inflightMu.Unlock()
	}

	recovered := 0
	dropped := 0
	for _, request := range state.Requests {
		req, err := b.buildRecoveredDownloadRequest(request)
		if err != nil {
			dropped++
			b.releaseInflightDownload(request.InflightKey)
			b.untrackRequest(request.RequestID)
			continue
		}
		select {
		case b.downloadQueue <- req:
			recovered++
		default:
			dropped++
			b.releaseInflightDownload(request.InflightKey)
			b.untrackRequest(request.RequestID)
		}
	}
	if recovered > 0 || dropped > 0 || recoveredAutoDelete > 0 {
		fmt.Printf("telegram runtime recovery: queued=%d dropped=%d auto_delete=%d\n", recovered, dropped, recoveredAutoDelete)
	}
	b.requestStateSave()
}

func loadRuntimeStateFromFile(path string) (telegramPersistedState, error) {
	state := telegramPersistedState{}
	target := filepath.Clean(strings.TrimSpace(path))
	if target == "" {
		return state, fmt.Errorf("state file path is empty")
	}
	payload, err := os.ReadFile(target)
	if err != nil {
		return state, err
	}
	if len(payload) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		return state, err
	}
	return state, nil
}

func (b *TelegramBot) buildRecoveredDownloadRequest(request telegramPersistedRequest) (*downloadRequest, error) {
	mediaType := strings.TrimSpace(request.MediaType)
	mediaID := strings.TrimSpace(request.MediaID)
	if mediaType == "" || mediaID == "" {
		return nil, fmt.Errorf("invalid request: missing media type/id")
	}
	storefront := strings.TrimSpace(request.Storefront)
	if storefront == "" {
		storefront = Config.Storefront
	}
	settings := normalizeChatSettings(request.Settings)
	req := &downloadRequest{
		chatID:       request.ChatID,
		replyToID:    request.ReplyToID,
		single:       request.Single,
		forceRefresh: request.ForceRefresh,
		taskType:     normalizeTelegramTaskType(request.TaskType),
		settings:     settings,
		transferMode: request.TransferMode,
		mediaType:    mediaType,
		mediaID:      mediaID,
		storefront:   storefront,
		inflightKey:  strings.TrimSpace(request.InflightKey),
		requestID:    strings.TrimSpace(request.RequestID),
	}
	if req.requestID == "" {
		req.requestID = b.nextRequestID()
	}
	if err := b.buildQueuedRequestRunner(req); err != nil {
		return nil, err
	}
	return req, nil
}

func (b *TelegramBot) buildDownloadWorkerFn(mediaType string, mediaID string, storefront string, transferMode string) (func(session *DownloadSession) error, error) {
	switch mediaType {
	case mediaTypeSong:
		return func(session *DownloadSession) error {
			return ripSong(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		}, nil
	case mediaTypeAlbum:
		return func(session *DownloadSession) error {
			return ripAlbum(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken, "")
		}, nil
	case mediaTypePlaylist:
		return func(session *DownloadSession) error {
			return ripPlaylist(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		}, nil
	case mediaTypeStation:
		return func(session *DownloadSession) error {
			return ripStation(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		}, nil
	case mediaTypeArtistSongs:
		return func(session *DownloadSession) error {
			return b.downloadArtistSongsCollection(session, mediaID, storefront)
		}, nil
	case mediaTypeArtistLPs:
		return func(session *DownloadSession) error {
			return b.stageArtistLPAlbumsCollection(session, mediaID, storefront)
		}, nil
	case mediaTypeMusicVideo:
		saveDir := strings.TrimSpace(Config.AlacSaveFolder)
		if saveDir == "" {
			saveDir = "AM-DL downloads"
		}
		return func(session *DownloadSession) error {
			return mvDownloader(session, mediaID, saveDir, b.appleToken, storefront, session.Config.MediaUserToken, nil)
		}, nil
	default:
		return nil, fmt.Errorf("unsupported media type: %s", mediaType)
	}
}
