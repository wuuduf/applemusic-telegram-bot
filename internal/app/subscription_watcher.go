package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
)

const (
	telegramSubscriptionKindArtist = "artist"
)

type telegramSubscription struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	TargetID       string    `json:"target_id"`
	Storefront     string    `json:"storefront"`
	Title          string    `json:"title"`
	CreatedBy      int64     `json:"created_by"`
	DeliveryChatID int64     `json:"delivery_chat_id"`
	CreatedAt      time.Time `json:"created_at"`
	LastCheckAt    time.Time `json:"last_check_at"`
	LastSeenAlbum  string    `json:"last_seen_album,omitempty"`
	Enabled        bool      `json:"enabled"`
}

type telegramTemporaryRelease struct {
	ID                string    `json:"id"`
	SubscriptionID    string    `json:"subscription_id"`
	AlbumID           string    `json:"album_id"`
	Storefront        string    `json:"storefront"`
	Title             string    `json:"title"`
	ArtistName        string    `json:"artist_name,omitempty"`
	DeliveryChatID    int64     `json:"delivery_chat_id"`
	ArchiveChatID     int64     `json:"archive_chat_id,omitempty"`
	DiscoveredAt      time.Time `json:"discovered_at"`
	TemporarySentAt   time.Time `json:"temporary_sent_at,omitempty"`
	RefreshEligibleAt time.Time `json:"refresh_eligible_at,omitempty"`
	Refreshed         bool      `json:"refreshed,omitempty"`
	RefreshedAt       time.Time `json:"refreshed_at,omitempty"`
}

type temporaryReleaseFilter string

type subscriptionListFilter string

const (
	temporaryReleaseFilterAll       temporaryReleaseFilter = "all"
	temporaryReleaseFilterPending   temporaryReleaseFilter = "pending"
	temporaryReleaseFilterReady     temporaryReleaseFilter = "ready"
	temporaryReleaseFilterRefreshed temporaryReleaseFilter = "refreshed"

	subscriptionListFilterAll     subscriptionListFilter = "all"
	subscriptionListFilterEnabled subscriptionListFilter = "enabled"
	subscriptionListFilterPaused  subscriptionListFilter = "paused"
)

func telegramSubscriptionCheckInterval() time.Duration {
	sec := Config.TelegramSubscriptionCheckIntervalSec
	if sec <= 0 {
		return defaultTelegramSubscriptionCheckInterval
	}
	return time.Duration(sec) * time.Second
}

func normalizeSubscriptionStorefront(storefront string) string {
	storefront = strings.TrimSpace(storefront)
	if storefront == "" {
		storefront = Config.Storefront
	}
	if storefront == "" {
		storefront = "us"
	}
	return storefront
}

func (b *TelegramBot) nextSubscriptionID() string {
	if b == nil {
		return ""
	}
	return fmt.Sprintf("sub-%d-%d", time.Now().UnixNano(), b.requestSeq.Add(1))
}

func (b *TelegramBot) nextTemporaryReleaseID() string {
	if b == nil {
		return ""
	}
	return fmt.Sprintf("subtmp-%d-%d", time.Now().UnixNano(), b.requestSeq.Add(1))
}

func (b *TelegramBot) snapshotSubscriptions() map[string]telegramSubscription {
	if b == nil {
		return nil
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	if len(b.subscriptions) == 0 {
		return nil
	}
	cloned := make(map[string]telegramSubscription, len(b.subscriptions))
	for id, item := range b.subscriptions {
		cloned[id] = item
	}
	return cloned
}

func (b *TelegramBot) snapshotTemporaryReleases() map[string]telegramTemporaryRelease {
	if b == nil {
		return nil
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	if len(b.temporaryReleases) == 0 {
		return nil
	}
	cloned := make(map[string]telegramTemporaryRelease, len(b.temporaryReleases))
	for id, item := range b.temporaryReleases {
		cloned[id] = item
	}
	return cloned
}

func (b *TelegramBot) restoreSubscriptions(subscriptions map[string]telegramSubscription, releases map[string]telegramTemporaryRelease) {
	if b == nil {
		return
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	if b.subscriptions == nil {
		b.subscriptions = make(map[string]telegramSubscription)
	} else {
		for id := range b.subscriptions {
			delete(b.subscriptions, id)
		}
	}
	if b.temporaryReleases == nil {
		b.temporaryReleases = make(map[string]telegramTemporaryRelease)
	} else {
		for id := range b.temporaryReleases {
			delete(b.temporaryReleases, id)
		}
	}
	for id, item := range subscriptions {
		if strings.TrimSpace(id) == "" {
			continue
		}
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = id
		}
		item.Kind = strings.TrimSpace(item.Kind)
		if item.Kind == "" {
			item.Kind = telegramSubscriptionKindArtist
		}
		item.TargetID = strings.TrimSpace(item.TargetID)
		item.Storefront = normalizeSubscriptionStorefront(item.Storefront)
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now()
		}
		b.subscriptions[item.ID] = item
	}
	for id, item := range releases {
		if strings.TrimSpace(id) == "" {
			continue
		}
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = id
		}
		item.SubscriptionID = strings.TrimSpace(item.SubscriptionID)
		item.AlbumID = strings.TrimSpace(item.AlbumID)
		item.Storefront = normalizeSubscriptionStorefront(item.Storefront)
		b.temporaryReleases[item.ID] = item
	}
}

func (b *TelegramBot) addArtistSubscription(chatID int64, userID int64, target *AppleURLTarget) (telegramSubscription, apputils.SearchResultItem, bool, error) {
	if b == nil {
		return telegramSubscription{}, apputils.SearchResultItem{}, false, fmt.Errorf("telegram bot is nil")
	}
	if target == nil {
		return telegramSubscription{}, apputils.SearchResultItem{}, false, fmt.Errorf("artist target is nil")
	}
	if target.MediaType != mediaTypeArtist {
		return telegramSubscription{}, apputils.SearchResultItem{}, false, fmt.Errorf("subscription only supports artist target")
	}
	storefront := normalizeSubscriptionStorefront(resolveStorefront(target))
	artistID := strings.TrimSpace(target.ID)
	if artistID == "" {
		return telegramSubscription{}, apputils.SearchResultItem{}, false, fmt.Errorf("artist id is empty")
	}
	artistName, _, err := b.fetchArtistProfile(storefront, artistID)
	if err != nil {
		return telegramSubscription{}, apputils.SearchResultItem{}, false, err
	}
	latestAlbum, latestErr := b.fetchLatestArtistAlbum(storefront, artistID)
	if latestErr != nil && !strings.Contains(strings.ToLower(latestErr.Error()), "no albums found") {
		return telegramSubscription{}, apputils.SearchResultItem{}, false, latestErr
	}

	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	for _, existing := range b.subscriptions {
		if existing.Kind == telegramSubscriptionKindArtist &&
			existing.TargetID == artistID &&
			normalizeSubscriptionStorefront(existing.Storefront) == storefront &&
			existing.DeliveryChatID == chatID {
			return existing, latestAlbum, false, nil
		}
	}

	subscription := telegramSubscription{
		ID:             b.nextSubscriptionID(),
		Kind:           telegramSubscriptionKindArtist,
		TargetID:       artistID,
		Storefront:     storefront,
		Title:          strings.TrimSpace(artistName),
		CreatedBy:      userID,
		DeliveryChatID: chatID,
		CreatedAt:      time.Now(),
		LastSeenAlbum:  strings.TrimSpace(latestAlbum.ID),
		Enabled:        true,
	}
	if b.subscriptions == nil {
		b.subscriptions = make(map[string]telegramSubscription)
	}
	b.subscriptions[subscription.ID] = subscription
	b.requestStateSave()
	return subscription, latestAlbum, true, nil
}

func (b *TelegramBot) listSubscriptions() []telegramSubscription {
	if b == nil {
		return nil
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	items := make([]telegramSubscription, 0, len(b.subscriptions))
	for _, item := range b.subscriptions {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items
}

func (b *TelegramBot) deleteSubscription(subscriptionID string) (telegramSubscription, bool) {
	if b == nil {
		return telegramSubscription{}, false
	}
	subscriptionID = strings.TrimSpace(subscriptionID)
	if subscriptionID == "" {
		return telegramSubscription{}, false
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	item, ok := b.subscriptions[subscriptionID]
	if !ok {
		return telegramSubscription{}, false
	}
	delete(b.subscriptions, subscriptionID)
	for releaseID, release := range b.temporaryReleases {
		if strings.TrimSpace(release.SubscriptionID) == subscriptionID {
			delete(b.temporaryReleases, releaseID)
		}
	}
	b.requestStateSave()
	return item, true
}

func (b *TelegramBot) setSubscriptionEnabled(subscriptionID string, enabled bool) (telegramSubscription, bool, bool) {
	if b == nil {
		return telegramSubscription{}, false, false
	}
	subscriptionID = strings.TrimSpace(subscriptionID)
	if subscriptionID == "" {
		return telegramSubscription{}, false, false
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	item, ok := b.subscriptions[subscriptionID]
	if !ok {
		return telegramSubscription{}, false, false
	}
	if item.Enabled == enabled {
		return item, false, true
	}
	item.Enabled = enabled
	b.subscriptions[subscriptionID] = item
	b.requestStateSave()
	return item, true, true
}

func (b *TelegramBot) fetchLatestArtistAlbum(storefront, artistID string) (apputils.SearchResultItem, error) {
	storefront = normalizeSubscriptionStorefront(storefront)
	artistID = strings.TrimSpace(artistID)
	if artistID == "" {
		return apputils.SearchResultItem{}, fmt.Errorf("artist id is empty")
	}
	items, _, err := apputils.FetchArtistAlbums(storefront, artistID, b.appleToken, 1, 0, b.searchLanguage())
	if err != nil {
		return apputils.SearchResultItem{}, err
	}
	if len(items) == 0 {
		return apputils.SearchResultItem{}, fmt.Errorf("no albums found for artist %s", artistID)
	}
	return items[0], nil
}

func (b *TelegramBot) startSubscriptionWatcher() {
	if b == nil {
		return
	}
	interval := telegramSubscriptionCheckInterval()
	if interval <= 0 || b.subscriptionWatcherStop != nil {
		return
	}
	stopCh := make(chan struct{})
	b.subscriptionWatcherStop = stopCh
	b.subscriptionWatcherWG.Add(1)
	go func() {
		defer b.subscriptionWatcherWG.Done()
		runWithRecovery("telegram subscription watcher", nil, func() {
			b.checkSubscriptionsOnce()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					b.checkSubscriptionsOnce()
				case <-stopCh:
					return
				case <-b.shutdownContext().Done():
					return
				}
			}
		})
	}()
}

func (b *TelegramBot) stopSubscriptionWatcher() {
	if b == nil || b.subscriptionWatcherStop == nil {
		return
	}
	stopCh := b.subscriptionWatcherStop
	close(stopCh)
	b.subscriptionWatcherWG.Wait()
	b.subscriptionWatcherStop = nil
}

func (b *TelegramBot) checkSubscriptionsOnce() {
	if b == nil || b.isShuttingDown() {
		return
	}
	items := b.listSubscriptions()
	for _, item := range items {
		if b.isShuttingDown() {
			return
		}
		if !item.Enabled || item.Kind != telegramSubscriptionKindArtist {
			continue
		}
		b.checkArtistSubscription(item)
	}
}

func (b *TelegramBot) checkArtistSubscription(item telegramSubscription) {
	latestAlbum, err := b.fetchLatestArtistAlbum(item.Storefront, item.TargetID)
	b.subscriptionMu.Lock()
	current, ok := b.subscriptions[item.ID]
	if ok {
		current.LastCheckAt = time.Now()
		b.subscriptions[item.ID] = current
	}
	b.subscriptionMu.Unlock()
	if !ok {
		return
	}
	b.requestStateSave()
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no albums found") {
			return
		}
		b.logTelegramPrintf("subscription check failed id=%s artist=%s err=%v\n", item.ID, item.TargetID, err)
		b.appendTelegramRuntimeErrorLogf("subscription check failed id=%s artist=%s err=%v", item.ID, item.TargetID, err)
		return
	}
	latestAlbumID := strings.TrimSpace(latestAlbum.ID)
	if latestAlbumID == "" {
		return
	}

	b.subscriptionMu.Lock()
	current, ok = b.subscriptions[item.ID]
	if !ok {
		b.subscriptionMu.Unlock()
		return
	}
	if strings.TrimSpace(current.LastSeenAlbum) == "" {
		current.LastSeenAlbum = latestAlbumID
		current.LastCheckAt = time.Now()
		b.subscriptions[current.ID] = current
		b.subscriptionMu.Unlock()
		b.requestStateSave()
		return
	}
	if strings.TrimSpace(current.LastSeenAlbum) == latestAlbumID {
		b.subscriptionMu.Unlock()
		return
	}
	b.subscriptionMu.Unlock()

	if err := b.triggerSubscriptionTemporaryAlbum(current, latestAlbum); err != nil {
		b.logTelegramPrintf("subscription trigger failed id=%s album=%s err=%v\n", current.ID, latestAlbumID, err)
		b.appendTelegramRuntimeErrorLogf("subscription trigger failed id=%s album=%s err=%v", current.ID, latestAlbumID, err)
		return
	}

	b.subscriptionMu.Lock()
	current, ok = b.subscriptions[item.ID]
	if ok {
		current.LastSeenAlbum = latestAlbumID
		current.LastCheckAt = time.Now()
		b.subscriptions[current.ID] = current
	}
	b.subscriptionMu.Unlock()
	if ok {
		b.requestStateSave()
	}
}

func (b *TelegramBot) triggerSubscriptionTemporaryAlbum(item telegramSubscription, album apputils.SearchResultItem) error {
	if b == nil {
		return fmt.Errorf("telegram bot is nil")
	}
	albumID := strings.TrimSpace(album.ID)
	if albumID == "" {
		return fmt.Errorf("album id is empty")
	}

	release := telegramTemporaryRelease{
		ID:             b.nextTemporaryReleaseID(),
		SubscriptionID: item.ID,
		AlbumID:        albumID,
		Storefront:     normalizeSubscriptionStorefront(item.Storefront),
		Title:          strings.TrimSpace(album.Name),
		ArtistName:     firstNonEmpty(strings.TrimSpace(album.Artist), item.Title),
		DeliveryChatID: item.DeliveryChatID,
		ArchiveChatID:  b.forwardChatID,
		DiscoveredAt:   time.Now(),
	}
	b.subscriptionMu.Lock()
	if b.temporaryReleases == nil {
		b.temporaryReleases = make(map[string]telegramTemporaryRelease)
	}
	for _, existing := range b.temporaryReleases {
		if existing.SubscriptionID == release.SubscriptionID && existing.AlbumID == release.AlbumID {
			b.subscriptionMu.Unlock()
			return nil
		}
	}
	b.temporaryReleases[release.ID] = release
	b.subscriptionMu.Unlock()
	b.requestStateSave()

	queuedAny := false
	if item.DeliveryChatID != 0 {
		if b.enqueueSubscriptionAlbumDownload(item.DeliveryChatID, item, release) {
			queuedAny = true
		}
	}
	archiveChatID := b.forwardChatID
	if archiveChatID != 0 && archiveChatID != item.DeliveryChatID && b.shouldDeliverTemporaryReleaseToArchive(release) {
		archiveSubscription := item
		archiveSubscription.DeliveryChatID = archiveChatID
		if b.enqueueSubscriptionAlbumDownload(archiveChatID, archiveSubscription, release) {
			queuedAny = true
		}
	}
	if !queuedAny {
		b.subscriptionMu.Lock()
		delete(b.temporaryReleases, release.ID)
		b.subscriptionMu.Unlock()
		b.requestStateSave()
		return fmt.Errorf("subscription album %s was not queued for any delivery target", albumID)
	}
	return nil
}

func (b *TelegramBot) shouldDeliverTemporaryReleaseToArchive(release telegramTemporaryRelease) bool {
	if b == nil {
		return false
	}
	if release.ArchiveChatID == 0 || strings.TrimSpace(release.AlbumID) == "" {
		return false
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	canonical := release
	found := false
	for _, item := range b.temporaryReleases {
		if item.ArchiveChatID != release.ArchiveChatID {
			continue
		}
		if strings.TrimSpace(item.Storefront) != strings.TrimSpace(release.Storefront) {
			continue
		}
		if strings.TrimSpace(item.AlbumID) != strings.TrimSpace(release.AlbumID) {
			continue
		}
		if !found {
			canonical = item
			found = true
			continue
		}
		if item.DiscoveredAt.Before(canonical.DiscoveredAt) || (item.DiscoveredAt.Equal(canonical.DiscoveredAt) && item.ID < canonical.ID) {
			canonical = item
		}
	}
	if !found {
		return true
	}
	return canonical.ID == release.ID
}

func (b *TelegramBot) enqueueSubscriptionAlbumDownload(chatID int64, subscription telegramSubscription, release telegramTemporaryRelease) bool {
	if b == nil || chatID == 0 {
		return false
	}
	settings := b.getChatSettings(chatID)
	inflightKey := makeDownloadInflightKey(chatID, mediaTypeAlbum, release.AlbumID, release.Storefront, transferModeOneByOne, settings)
	if !b.acquireInflightDownload(inflightKey) {
		return false
	}
	req := &downloadRequest{
		chatID:       chatID,
		replyToID:    0,
		single:       false,
		forceRefresh: false,
		taskType:     telegramTaskDownload,
		settings:     settings,
		transferMode: transferModeOneByOne,
		mediaType:    mediaTypeAlbum,
		mediaID:      release.AlbumID,
		storefront:   release.Storefront,
		inflightKey:  inflightKey,
		requestID:    b.nextRequestID(),
	}
	req.run = func(bot *TelegramBot, ctx context.Context) error {
		success := bot.runDownloadWithContextResult(ctx, chatID, func(session *DownloadSession) error {
			return ripAlbum(session, release.AlbumID, bot.appleToken, release.Storefront, session.Config.MediaUserToken, "")
		}, false, false, 0, settings, transferModeOneByOne, mediaTypeAlbum, release.AlbumID, release.Storefront)
		if success {
			bot.markTemporaryReleaseSent(release.ID)
		}
		return nil
	}
	if queued := b.enqueueTelegramTask(req); !queued {
		b.releaseInflightDownload(inflightKey)
		return false
	}
	message := fmt.Sprintf("订阅命中：%s 发布了新专辑《%s》，已加入临时下载队列。", firstNonEmpty(subscription.Title, subscription.TargetID), firstNonEmpty(release.Title, release.AlbumID))
	_ = b.sendMessage(chatID, message, nil)
	return true
}

func (b *TelegramBot) markTemporaryReleaseSent(releaseID string) {
	if b == nil {
		return
	}
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return
	}
	b.subscriptionMu.Lock()
	release, ok := b.temporaryReleases[releaseID]
	if ok && release.TemporarySentAt.IsZero() {
		release.TemporarySentAt = time.Now()
		release.RefreshEligibleAt = release.TemporarySentAt.Add(defaultTelegramTemporaryReleaseWindow)
		b.temporaryReleases[releaseID] = release
	}
	b.subscriptionMu.Unlock()
	if ok {
		b.requestStateSave()
	}
}

func (b *TelegramBot) markTemporaryReleaseRefreshed(releaseID string) {
	if b == nil {
		return
	}
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return
	}
	b.subscriptionMu.Lock()
	release, ok := b.temporaryReleases[releaseID]
	if ok && !release.Refreshed {
		release.Refreshed = true
		release.RefreshedAt = time.Now()
		b.temporaryReleases[releaseID] = release
	}
	b.subscriptionMu.Unlock()
	if ok {
		b.requestStateSave()
	}
}

func (b *TelegramBot) listTemporaryReleases() []telegramTemporaryRelease {
	if b == nil {
		return nil
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	items := make([]telegramTemporaryRelease, 0, len(b.temporaryReleases))
	for _, item := range b.temporaryReleases {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DiscoveredAt.Equal(items[j].DiscoveredAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].DiscoveredAt.Before(items[j].DiscoveredAt)
	})
	return items
}

func (b *TelegramBot) getTemporaryRelease(releaseID string) (telegramTemporaryRelease, bool) {
	if b == nil {
		return telegramTemporaryRelease{}, false
	}
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return telegramTemporaryRelease{}, false
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	item, ok := b.temporaryReleases[releaseID]
	return item, ok
}

func temporaryReleaseRefreshState(item telegramTemporaryRelease, now time.Time) string {
	if item.Refreshed {
		return "refreshed"
	}
	if item.TemporarySentAt.IsZero() {
		return "temporary-pending"
	}
	if !item.RefreshEligibleAt.IsZero() && now.Before(item.RefreshEligibleAt) {
		return "waiting-window"
	}
	return "refresh-ready"
}

func formatSubscriptionRelativeTime(value time.Time, now time.Time) string {
	if value.IsZero() {
		return "-"
	}
	if now.IsZero() {
		now = time.Now()
	}
	diff := now.Sub(value)
	if diff < 0 {
		diff = -diff
	}
	if diff < time.Minute {
		return "just now"
	}
	if diff < time.Hour {
		return fmt.Sprintf("%dm ago", int(diff/time.Minute))
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(diff/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(diff/(24*time.Hour)))
}

func formatSubscriptionRemaining(value time.Duration) string {
	if value <= 0 {
		return "0m"
	}
	days := int(value / (24 * time.Hour))
	value -= time.Duration(days) * 24 * time.Hour
	hours := int(value / time.Hour)
	value -= time.Duration(hours) * time.Hour
	minutes := int(value / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

func summarizeTemporaryReleaseStates(items []telegramTemporaryRelease, now time.Time) (pending int, ready int, refreshed int) {
	for _, item := range items {
		switch temporaryReleaseRefreshState(item, now) {
		case "refreshed":
			refreshed++
		case "refresh-ready":
			ready++
		default:
			pending++
		}
	}
	return pending, ready, refreshed
}

func normalizeSubscriptionListFilter(raw string) (subscriptionListFilter, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all", "list":
		return subscriptionListFilterAll, true
	case "enabled", "active":
		return subscriptionListFilterEnabled, true
	case "paused", "pause":
		return subscriptionListFilterPaused, true
	default:
		return "", false
	}
}

func parseTemporaryReleaseListArgs(args []string) (temporaryReleaseFilter, string, string, bool) {
	if len(args) == 0 {
		return temporaryReleaseFilterAll, "", "", true
	}
	first := strings.ToLower(strings.TrimSpace(args[0]))
	switch first {
	case "", "list", "all":
		return temporaryReleaseFilterAll, "", "", true
	case "pending":
		return temporaryReleaseFilterPending, "", "", true
	case "ready":
		return temporaryReleaseFilterReady, "", "", true
	case "refreshed":
		return temporaryReleaseFilterRefreshed, "", "", true
	case "artist":
		if len(args) < 2 {
			return "", "", "", false
		}
		return temporaryReleaseFilterAll, "artist", strings.TrimSpace(strings.Join(args[1:], " ")), true
	case "album":
		if len(args) != 2 {
			return "", "", "", false
		}
		return temporaryReleaseFilterAll, "album", strings.TrimSpace(args[1]), true
	default:
		return "", "", "", false
	}
}

func filterSubscriptionsByState(items []telegramSubscription, filter subscriptionListFilter) []telegramSubscription {
	if filter == subscriptionListFilterAll {
		return items
	}
	filtered := make([]telegramSubscription, 0, len(items))
	for _, item := range items {
		if filter == subscriptionListFilterEnabled && item.Enabled {
			filtered = append(filtered, item)
		}
		if filter == subscriptionListFilterPaused && !item.Enabled {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterTemporaryReleases(items []telegramTemporaryRelease, filter temporaryReleaseFilter, matchKind, matchValue string, now time.Time) []telegramTemporaryRelease {
	matchValue = strings.TrimSpace(strings.ToLower(matchValue))
	filtered := make([]telegramTemporaryRelease, 0, len(items))
	for _, item := range items {
		status := temporaryReleaseRefreshState(item, now)
		if !temporaryReleaseMatchesFilter(status, filter) {
			continue
		}
		switch matchKind {
		case "artist":
			artist := strings.ToLower(strings.TrimSpace(item.ArtistName))
			subscriptionID := strings.ToLower(strings.TrimSpace(item.SubscriptionID))
			if matchValue != "" && !strings.Contains(artist, matchValue) && !strings.Contains(subscriptionID, matchValue) {
				continue
			}
		case "album":
			albumID := strings.ToLower(strings.TrimSpace(item.AlbumID))
			title := strings.ToLower(strings.TrimSpace(item.Title))
			if matchValue != "" && matchValue != albumID && !strings.Contains(title, matchValue) {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func (b *TelegramBot) formatTemporaryReleaseListText(filter temporaryReleaseFilter, matchKind, matchValue string) string {
	allItems := b.listTemporaryReleases()
	if len(allItems) == 0 {
		return "当前没有临时订阅发布记录。"
	}
	now := time.Now()
	pendingTotal, readyTotal, refreshedTotal := summarizeTemporaryReleaseStates(allItems, now)
	items := filterTemporaryReleases(allItems, filter, matchKind, matchValue, now)
	title := "临时订阅发布记录："
	switch filter {
	case temporaryReleaseFilterPending:
		title = "临时订阅发布记录（pending）："
	case temporaryReleaseFilterReady:
		title = "临时订阅发布记录（ready）："
	case temporaryReleaseFilterRefreshed:
		title = "临时订阅发布记录（refreshed）："
	}
	if strings.TrimSpace(matchValue) != "" {
		title = fmt.Sprintf("%s match_%s=%s", title, matchKind, strings.TrimSpace(matchValue))
	}
	lines := []string{
		title,
		fmt.Sprintf("summary: total=%d pending=%d ready=%d refreshed=%d matched=%d", len(allItems), pendingTotal, readyTotal, refreshedTotal, len(items)),
	}
	for _, item := range items {
		status := temporaryReleaseRefreshState(item, now)
		sentAt := "-"
		if !item.TemporarySentAt.IsZero() {
			sentAt = item.TemporarySentAt.Format("2006-01-02 15:04")
		}
		eligibleAt := "-"
		if !item.RefreshEligibleAt.IsZero() {
			eligibleAt = item.RefreshEligibleAt.Format("2006-01-02 15:04")
		}
		extra := ""
		switch status {
		case "waiting-window":
			if !item.RefreshEligibleAt.IsZero() {
				extra = " | remaining=" + formatSubscriptionRemaining(item.RefreshEligibleAt.Sub(now))
			}
		case "refresh-ready":
			if !item.RefreshEligibleAt.IsZero() {
				extra = " | ready_for=" + formatSubscriptionRemaining(now.Sub(item.RefreshEligibleAt))
			}
		case "refreshed":
			if !item.RefreshedAt.IsZero() {
				extra = " | refreshed_at=" + item.RefreshedAt.Format("2006-01-02 15:04")
			}
		}
		lines = append(lines, fmt.Sprintf(
			"- %s | %s | artist=%s | album=%s | status=%s | sent=%s | refresh_after=%s%s",
			item.ID,
			firstNonEmpty(strings.TrimSpace(item.Title), item.AlbumID),
			firstNonEmpty(strings.TrimSpace(item.ArtistName), "-"),
			item.AlbumID,
			status,
			sentAt,
			eligibleAt,
			extra,
		))
	}
	if len(lines) == 2 {
		return "当前没有符合条件的临时订阅发布记录。"
	}
	return strings.Join(lines, "\n")
}

func temporaryReleaseMatchesFilter(status string, filter temporaryReleaseFilter) bool {
	switch filter {
	case temporaryReleaseFilterPending:
		return status == "temporary-pending" || status == "waiting-window"
	case temporaryReleaseFilterReady:
		return status == "refresh-ready"
	case temporaryReleaseFilterRefreshed:
		return status == "refreshed"
	default:
		return true
	}
}

func (b *TelegramBot) enqueueSubscriptionAlbumRefresh(chatID int64, subscription telegramSubscription, release telegramTemporaryRelease) bool {
	if b == nil || chatID == 0 {
		return false
	}
	settings := b.getChatSettings(chatID)
	inflightKey := makeDownloadInflightKey(chatID, mediaTypeAlbum, release.AlbumID, release.Storefront, transferModeOneByOne, settings)
	if !b.acquireInflightDownload(inflightKey) {
		return false
	}
	req := &downloadRequest{
		chatID:       chatID,
		replyToID:    0,
		single:       false,
		forceRefresh: true,
		taskType:     telegramTaskDownload,
		settings:     settings,
		transferMode: transferModeOneByOne,
		mediaType:    mediaTypeAlbum,
		mediaID:      release.AlbumID,
		storefront:   release.Storefront,
		inflightKey:  inflightKey,
		requestID:    b.nextRequestID(),
	}
	req.run = func(bot *TelegramBot, ctx context.Context) error {
		success := bot.runDownloadWithContextResult(ctx, chatID, func(session *DownloadSession) error {
			return ripAlbum(session, release.AlbumID, bot.appleToken, release.Storefront, session.Config.MediaUserToken, "")
		}, false, true, 0, settings, transferModeOneByOne, mediaTypeAlbum, release.AlbumID, release.Storefront)
		if success {
			bot.markTemporaryReleaseRefreshed(release.ID)
		}
		return nil
	}
	if queued := b.enqueueTelegramTask(req); !queued {
		b.releaseInflightDownload(inflightKey)
		return false
	}
	message := fmt.Sprintf("订阅刷新：%s《%s》已加入正式重新下载队列。", firstNonEmpty(subscription.Title, subscription.TargetID), firstNonEmpty(release.Title, release.AlbumID))
	_ = b.sendMessage(chatID, message, nil)
	return true
}

func (b *TelegramBot) triggerTemporaryReleaseRefresh(release telegramTemporaryRelease) (int, error) {
	if b == nil {
		return 0, fmt.Errorf("telegram bot is nil")
	}
	now := time.Now()
	if release.Refreshed {
		return 0, fmt.Errorf("temporary release already refreshed")
	}
	if release.TemporarySentAt.IsZero() {
		return 0, fmt.Errorf("temporary release has not completed temporary delivery yet")
	}
	if !release.RefreshEligibleAt.IsZero() && now.Before(release.RefreshEligibleAt) {
		return 0, fmt.Errorf("temporary release is not eligible until %s", release.RefreshEligibleAt.Format(time.RFC3339))
	}

	b.subscriptionMu.Lock()
	subscription, ok := b.subscriptions[release.SubscriptionID]
	b.subscriptionMu.Unlock()
	if !ok {
		return 0, fmt.Errorf("subscription not found for release %s", release.ID)
	}

	queued := 0
	if subscription.DeliveryChatID != 0 && b.enqueueSubscriptionAlbumRefresh(subscription.DeliveryChatID, subscription, release) {
		queued++
	}
	archiveChatID := release.ArchiveChatID
	if archiveChatID == 0 {
		archiveChatID = b.forwardChatID
	}
	if archiveChatID != 0 && archiveChatID != subscription.DeliveryChatID && b.shouldDeliverTemporaryReleaseToArchive(release) {
		archiveSubscription := subscription
		archiveSubscription.DeliveryChatID = archiveChatID
		if b.enqueueSubscriptionAlbumRefresh(archiveChatID, archiveSubscription, release) {
			queued++
		}
	}
	if queued == 0 {
		return 0, fmt.Errorf("refresh task could not be queued")
	}
	return queued, nil
}

func (b *TelegramBot) refreshEligibleTemporaryReleases() (queuedReleases int, skipped int, messages []string) {
	items := b.listTemporaryReleases()
	now := time.Now()
	for _, item := range items {
		switch temporaryReleaseRefreshState(item, now) {
		case "refreshed":
			skipped++
			continue
		case "temporary-pending", "waiting-window":
			skipped++
			continue
		}
		queuedTargets, err := b.triggerTemporaryReleaseRefresh(item)
		if err != nil {
			messages = append(messages, fmt.Sprintf("%s: %s", item.ID, err.Error()))
			continue
		}
		if queuedTargets > 0 {
			queuedReleases++
		}
	}
	return queuedReleases, skipped, messages
}

func (b *TelegramBot) formatSubscriptionListText(filter subscriptionListFilter) string {
	items := filterSubscriptionsByState(b.listSubscriptions(), filter)
	if len(items) == 0 {
		return "当前没有订阅。"
	}
	now := time.Now()
	enabledCount := 0
	pausedCount := 0
	allItems := b.listSubscriptions()
	for _, item := range allItems {
		if item.Enabled {
			enabledCount++
		} else {
			pausedCount++
		}
	}
	title := "当前订阅："
	if filter == subscriptionListFilterEnabled {
		title = "当前订阅（enabled）："
	}
	if filter == subscriptionListFilterPaused {
		title = "当前订阅（paused）："
	}
	lines := []string{
		title,
		fmt.Sprintf("summary: total=%d enabled=%d paused=%d matched=%d", len(allItems), enabledCount, pausedCount, len(items)),
	}
	for _, item := range items {
		title := firstNonEmpty(strings.TrimSpace(item.Title), item.TargetID)
		lastSeen := firstNonEmpty(strings.TrimSpace(item.LastSeenAlbum), "-")
		state := "enabled"
		if !item.Enabled {
			state = "paused"
		}
		lastCheck := formatSubscriptionRelativeTime(item.LastCheckAt, now)
		lines = append(lines, fmt.Sprintf(
			"- %s | %s | artist=%s | storefront=%s | chat=%d | latest=%s | state=%s | checked=%s",
			item.ID,
			title,
			item.TargetID,
			item.Storefront,
			item.DeliveryChatID,
			lastSeen,
			state,
			lastCheck,
		))
	}
	return strings.Join(lines, "\n")
}

func (b *TelegramBot) handleSubscriptionCommand(chatID int64, userID int64, args []string, replyToID int) {
	if b == nil {
		return
	}
	if !b.isAdminUser(userID) {
		_ = b.sendMessageWithReply(chatID, "只有管理员可以使用订阅功能。", nil, replyToID)
		return
	}
	if len(args) == 0 {
		_ = b.sendMessageWithReply(chatID, "Usage: /sub artist <artist-url|artist-id> | /sub list | /sub del <subscription_id> | /sub pause <subscription_id> | /sub resume <subscription_id>", nil, replyToID)
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "list":
		filter := subscriptionListFilterAll
		if len(args) > 1 {
			parsed, ok := normalizeSubscriptionListFilter(args[1])
			if !ok {
				_ = b.sendMessageWithReply(chatID, "Usage: /sub list [enabled|paused]", nil, replyToID)
				return
			}
			filter = parsed
		}
		_ = b.sendMessageWithReply(chatID, b.formatSubscriptionListText(filter), nil, replyToID)
	case "pause":
		if len(args) != 2 {
			_ = b.sendMessageWithReply(chatID, "Usage: /sub pause <subscription_id>", nil, replyToID)
			return
		}
		item, changed, ok := b.setSubscriptionEnabled(args[1], false)
		if !ok {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("未找到订阅：%s", strings.TrimSpace(args[1])), nil, replyToID)
			return
		}
		if !changed {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("订阅已处于暂停状态：%s", item.ID), nil, replyToID)
			return
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已暂停订阅：%s (%s)", item.ID, firstNonEmpty(item.Title, item.TargetID)), nil, replyToID)
	case "resume":
		if len(args) != 2 {
			_ = b.sendMessageWithReply(chatID, "Usage: /sub resume <subscription_id>", nil, replyToID)
			return
		}
		item, changed, ok := b.setSubscriptionEnabled(args[1], true)
		if !ok {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("未找到订阅：%s", strings.TrimSpace(args[1])), nil, replyToID)
			return
		}
		if !changed {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("订阅已处于启用状态：%s", item.ID), nil, replyToID)
			return
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已恢复订阅：%s (%s)", item.ID, firstNonEmpty(item.Title, item.TargetID)), nil, replyToID)
	case "del", "delete", "rm":
		if len(args) != 2 {
			_ = b.sendMessageWithReply(chatID, "Usage: /sub del <subscription_id>", nil, replyToID)
			return
		}
		item, ok := b.deleteSubscription(args[1])
		if !ok {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("未找到订阅：%s", strings.TrimSpace(args[1])), nil, replyToID)
			return
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已删除订阅：%s (%s)", item.ID, firstNonEmpty(item.Title, item.TargetID)), nil, replyToID)
	case "artist":
		if len(args) < 2 {
			_ = b.sendMessageWithReply(chatID, "Usage: /sub artist <artist-url|artist-id>", nil, replyToID)
			return
		}
		target, err := resolveCommandTarget(args[1:], mediaTypeArtist)
		if err != nil || target == nil || target.MediaType != mediaTypeArtist {
			_ = b.sendMessageWithReply(chatID, "Usage: /sub artist <artist-url|artist-id>", nil, replyToID)
			return
		}
		subscription, latestAlbum, created, addErr := b.addArtistSubscription(chatID, userID, target)
		if addErr != nil {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("创建订阅失败：%s", b.sanitizeTelegramErr(addErr)), nil, replyToID)
			return
		}
		if !created {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("订阅已存在：%s | %s", subscription.ID, firstNonEmpty(subscription.Title, subscription.TargetID)), nil, replyToID)
			return
		}
		latestText := "无"
		if strings.TrimSpace(latestAlbum.ID) != "" {
			latestText = fmt.Sprintf("%s (%s)", firstNonEmpty(strings.TrimSpace(latestAlbum.Name), latestAlbum.ID), latestAlbum.ID)
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已创建订阅：%s\n艺人：%s\n当前基线专辑：%s\n后续检测到新专辑时，会自动临时下载并发送到当前 chat 和归档群。", subscription.ID, firstNonEmpty(subscription.Title, subscription.TargetID), latestText), nil, replyToID)
	default:
		_ = b.sendMessageWithReply(chatID, "Usage: /sub artist <artist-url|artist-id> | /sub list | /sub del <subscription_id> | /sub pause <subscription_id> | /sub resume <subscription_id>", nil, replyToID)
	}
}

func (b *TelegramBot) handleSubscriptionTempCommand(chatID int64, userID int64, args []string, replyToID int) {
	if b == nil {
		return
	}
	if !b.isAdminUser(userID) {
		_ = b.sendMessageWithReply(chatID, "只有管理员可以使用订阅临时发布功能。", nil, replyToID)
		return
	}
	filter, matchKind, matchValue, ok := parseTemporaryReleaseListArgs(args)
	if !ok {
		_ = b.sendMessageWithReply(chatID, "Usage: /subtemp [list|all|pending|ready|refreshed|artist <keyword>|album <album-id|keyword>]", nil, replyToID)
		return
	}
	_ = b.sendMessageWithReply(chatID, b.formatTemporaryReleaseListText(filter, matchKind, matchValue), nil, replyToID)
}

func (b *TelegramBot) handleSubscriptionRefreshCommand(chatID int64, userID int64, args []string, replyToID int) {
	if b == nil {
		return
	}
	if !b.isAdminUser(userID) {
		_ = b.sendMessageWithReply(chatID, "只有管理员可以执行订阅刷新。", nil, replyToID)
		return
	}
	if len(args) != 1 {
		_ = b.sendMessageWithReply(chatID, "Usage: /subrefresh <temporary_release_id>", nil, replyToID)
		return
	}
	item, ok := b.getTemporaryRelease(args[0])
	if !ok {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("未找到临时发布记录：%s", strings.TrimSpace(args[0])), nil, replyToID)
		return
	}
	queuedTargets, err := b.triggerTemporaryReleaseRefresh(item)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("刷新失败：%s", b.sanitizeTelegramErr(err)), nil, replyToID)
		return
	}
	_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已触发刷新：%s（投递目标 %d 个）", item.ID, queuedTargets), nil, replyToID)
}

func (b *TelegramBot) handleSubscriptionRefreshAllCommand(chatID int64, userID int64, replyToID int) {
	if b == nil {
		return
	}
	if !b.isAdminUser(userID) {
		_ = b.sendMessageWithReply(chatID, "只有管理员可以执行订阅批量刷新。", nil, replyToID)
		return
	}
	queuedReleases, skipped, messages := b.refreshEligibleTemporaryReleases()
	lines := []string{fmt.Sprintf("批量刷新完成（仅 ready 项）：queued=%d skipped=%d", queuedReleases, skipped)}
	if len(messages) > 0 {
		if len(messages) > 5 {
			messages = messages[:5]
		}
		lines = append(lines, "errors:")
		lines = append(lines, messages...)
	}
	_ = b.sendMessageWithReply(chatID, strings.Join(lines, "\n"), nil, replyToID)
}
