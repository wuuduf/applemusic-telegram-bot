package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	sharedcatalog "github.com/wuuduf/applemusic-telegram-bot/internal/catalog"
	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
)

var stdoutCaptureMu sync.Mutex

func captureStdoutForTest(t *testing.T, fn func()) string {
	t.Helper()
	stdoutCaptureMu.Lock()
	defer stdoutCaptureMu.Unlock()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe failed: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = original
		_ = w.Close()
		_ = r.Close()
	}()
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	_ = w.Close()
	os.Stdout = original
	out := <-done
	_ = r.Close()
	return out
}

func TestWriteMP4TagsMissingGenreReturnsAccessError(t *testing.T) {
	track := &task.Track{}
	cfg := &structs.ConfigSet{}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("writeMP4Tags should not panic: %v", rec)
		}
	}()
	err := writeMP4Tags(track, "", cfg)
	if err == nil {
		t.Fatalf("expected error")
	}
	var accessErr *safe.AccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected AccessError, got %T", err)
	}
}

func TestTelegramMediaProducesSongAudio(t *testing.T) {
	tests := []struct {
		mediaType string
		want      bool
	}{
		{mediaType: mediaTypeSong, want: true},
		{mediaType: mediaTypeAlbum, want: true},
		{mediaType: mediaTypePlaylist, want: true},
		{mediaType: mediaTypeStation, want: true},
		{mediaType: mediaTypeArtistSongs, want: true},
		{mediaType: mediaTypeMusicVideo, want: false},
		{mediaType: mediaTypeArtist, want: false},
		{mediaType: mediaTypeAlbumLyrics, want: false},
	}

	for _, tt := range tests {
		got := telegramMediaProducesSongAudio(tt.mediaType)
		if got != tt.want {
			t.Fatalf("mediaType=%s got=%v want=%v", tt.mediaType, got, tt.want)
		}
	}
}

func TestShouldUseTelegramCollectionSequentialOneByOne(t *testing.T) {
	tests := []struct {
		name         string
		single       bool
		transferMode string
		mediaType    string
		want         bool
	}{
		{name: "album one-by-one", single: false, transferMode: transferModeOneByOne, mediaType: mediaTypeAlbum, want: true},
		{name: "playlist one-by-one", single: false, transferMode: transferModeOneByOne, mediaType: mediaTypePlaylist, want: true},
		{name: "station one-by-one", single: false, transferMode: transferModeOneByOne, mediaType: mediaTypeStation, want: true},
		{name: "artist songs one-by-one", single: false, transferMode: transferModeOneByOne, mediaType: mediaTypeArtistSongs, want: true},
		{name: "zip should not use sequential", single: false, transferMode: transferModeZip, mediaType: mediaTypeAlbum, want: false},
		{name: "single song should not use collection sequential", single: true, transferMode: transferModeOneByOne, mediaType: mediaTypeSong, want: false},
		{name: "mv should not use collection sequential", single: false, transferMode: transferModeOneByOne, mediaType: mediaTypeMusicVideo, want: false},
	}

	for _, tt := range tests {
		got := shouldUseTelegramCollectionSequentialOneByOne(tt.single, tt.transferMode, tt.mediaType)
		if got != tt.want {
			t.Fatalf("%s: got=%v want=%v", tt.name, got, tt.want)
		}
	}
}

func TestResolveStorefrontPrefersConfigOverURL(t *testing.T) {
	original := Config.Storefront
	t.Cleanup(func() {
		Config.Storefront = original
	})

	Config.Storefront = "cn"
	if got := resolveStorefront(&AppleURLTarget{Storefront: "us"}); got != "cn" {
		t.Fatalf("expected config storefront first, got %q", got)
	}

	Config.Storefront = ""
	if got := resolveStorefront(&AppleURLTarget{Storefront: "us"}); got != "us" {
		t.Fatalf("expected target storefront fallback, got %q", got)
	}

	if got := resolveStorefront(&AppleURLTarget{}); got != "us" {
		t.Fatalf("expected default us fallback, got %q", got)
	}
}

func TestHandleURLTargetWithOptionsUsesConfigStorefront(t *testing.T) {
	original := Config.Storefront
	t.Cleanup(func() {
		Config.Storefront = original
	})
	Config.Storefront = "cn"

	bot := &TelegramBot{
		downloadQueue:     make(chan *downloadRequest, 2),
		workerLimit:       1,
		chatSettings:      make(map[int64]ChatDownloadSettings),
		inflightDownloads: make(map[string]struct{}),
	}
	bot.queueCond = sync.NewCond(&bot.queueMu)

	bot.handleURLTargetWithOptions(42, 7, &AppleURLTarget{
		MediaType:  mediaTypeSong,
		ID:         "12345",
		Storefront: "us",
	}, false)

	if len(bot.downloadQueue) != 1 {
		t.Fatalf("expected queued request")
	}
	req := <-bot.downloadQueue
	if req == nil {
		t.Fatalf("expected non-nil request")
	}
	if req.storefront != "cn" {
		t.Fatalf("expected config storefront cn, got %q", req.storefront)
	}
}

func TestSendArtistSongsProgressUpdateSendsEveryFifty(t *testing.T) {
	var sentTexts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		payload := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		text, _ := payload["text"].(string)
		sentTexts = append(sentTexts, text)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:   "test-token",
		apiBase: server.URL,
		client:  server.Client(),
	}

	bot.sendArtistSongsProgressUpdate(42, 7, mediaTypeArtistSongs, 49, 120)
	bot.sendArtistSongsProgressUpdate(42, 7, mediaTypeArtistSongs, 50, 120)
	bot.sendArtistSongsProgressUpdate(42, 7, mediaTypeAlbum, 50, 120)

	if got := len(sentTexts); got != 1 {
		t.Fatalf("expected exactly 1 progress message, got %d", got)
	}
	if sentTexts[0] != "50/120" {
		t.Fatalf("unexpected progress text: %q", sentTexts[0])
	}
}

func TestLimitStringWithConfigHonorsRuneAndByteLimits(t *testing.T) {
	cfg := &structs.ConfigSet{LimitMax: 200}
	input := strings.Repeat("阿", 120) // 360 bytes
	got := LimitStringWithConfig(cfg, input)
	if !utf8.ValidString(got) {
		t.Fatalf("expected valid utf-8, got invalid string")
	}
	if len([]byte(got)) > maxTemplateValueBytes {
		t.Fatalf("expected byte length <= %d, got %d", maxTemplateValueBytes, len([]byte(got)))
	}
	if len([]rune(got)) >= len([]rune(input)) {
		t.Fatalf("expected truncation, got runes=%d input=%d", len([]rune(got)), len([]rune(input)))
	}
}

func TestLimitStringWithConfigHonorsConfigRuneLimit(t *testing.T) {
	cfg := &structs.ConfigSet{LimitMax: 5}
	got := LimitStringWithConfig(cfg, "abcdef")
	if got != "abcde" {
		t.Fatalf("unexpected truncate result: %q", got)
	}
}

func TestDownloadSessionShouldReuseExistingFiles(t *testing.T) {
	session := newDownloadSession(structs.ConfigSet{})
	if !session.shouldReuseExistingFiles() {
		t.Fatalf("expected local file reuse by default")
	}
	session.ForceRedownload = true
	if session.shouldReuseExistingFiles() {
		t.Fatalf("expected force redownload to disable local file reuse")
	}
}

func TestApplyTelegramAudioEmbeddingPolicy(t *testing.T) {
	base := structs.ConfigSet{
		LrcFormat:           "lrc",
		SaveLrcFile:         false,
		EmbedLrc:            false,
		EmbedCover:          false,
		SaveAnimatedArtwork: false,
	}
	settings := ChatDownloadSettings{
		LyricsFormat: "ttml",
		AutoLyrics:   false,
		AutoCover:    false,
		AutoAnimated: false,
	}

	for _, mediaType := range []string{mediaTypeSong, mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation} {
		session := newDownloadSession(base)
		session.StaticCoverDownload = false

		applyTelegramAudioEmbeddingPolicy(session, settings, mediaType)

		if !session.Config.EmbedLrc {
			t.Fatalf("mediaType=%s expected EmbedLrc=true", mediaType)
		}
		if !session.Config.EmbedCover {
			t.Fatalf("mediaType=%s expected EmbedCover=true", mediaType)
		}
		if session.Config.LrcFormat != "ttml" {
			t.Fatalf("mediaType=%s expected LyricsFormat=ttml got=%s", mediaType, session.Config.LrcFormat)
		}
		if session.Config.SaveLrcFile {
			t.Fatalf("mediaType=%s expected SaveLrcFile=false when AutoLyrics=false", mediaType)
		}
		if session.Config.SaveAnimatedArtwork {
			t.Fatalf("mediaType=%s expected SaveAnimatedArtwork=false when AutoAnimated=false", mediaType)
		}
		if !session.StaticCoverDownload {
			t.Fatalf("mediaType=%s expected StaticCoverDownload=true for cover embedding", mediaType)
		}
	}
}

func TestApplyTelegramAudioEmbeddingPolicyRespectsEmbedSwitches(t *testing.T) {
	session := newDownloadSession(structs.ConfigSet{})
	session.StaticCoverDownload = false
	settings := ChatDownloadSettings{
		LyricsFormat:   "lrc",
		EmbedLyrics:    false,
		EmbedCover:     false,
		EmbedInited:    true,
		AutoCover:      true,
		SettingsInited: true,
	}
	applyTelegramAudioEmbeddingPolicy(session, settings, mediaTypeSong)
	if session.Config.EmbedLrc {
		t.Fatalf("expected embed lyrics disabled")
	}
	if session.Config.EmbedCover {
		t.Fatalf("expected embed cover disabled")
	}
	// 虽然封面不内嵌，但开启了额外封面，仍需要下载静态封面文件。
	if !session.StaticCoverDownload {
		t.Fatalf("expected static cover download enabled when auto cover is on")
	}
}

func TestParseTelegramRetryAfterFromJSONBody(t *testing.T) {
	err := errors.New(`telegram sendDocument failed: {"ok":false,"error_code":429,"description":"Too Many Requests: retry after 13","parameters":{"retry_after":13}}`)
	got, ok := parseTelegramRetryAfter(err)
	if !ok {
		t.Fatalf("expected retry-after parse success")
	}
	if got != 13*time.Second {
		t.Fatalf("expected 13s, got %s", got)
	}
}

func TestParseTelegramRetryAfterFromDescription(t *testing.T) {
	err := errors.New("telegram sendAudio failed: 429 Too Many Requests: retry after 7")
	got, ok := parseTelegramRetryAfter(err)
	if !ok {
		t.Fatalf("expected retry-after parse success")
	}
	if got != 7*time.Second {
		t.Fatalf("expected 7s, got %s", got)
	}
}

func TestPendingSelectionIsolatedByMessageID(t *testing.T) {
	chatID := int64(1001)
	b := &TelegramBot{
		pending: make(map[int64]map[int]*PendingSelection),
	}

	b.setPending(chatID, "song", "q1", "us", 0, []apputils.SearchResultItem{{ID: "s1"}}, false, 11, 101, "")
	b.setPending(chatID, "song", "q2", "us", 0, []apputils.SearchResultItem{{ID: "s2"}}, false, 12, 102, "")

	pending1, ok := b.getPending(chatID, 101)
	if !ok {
		t.Fatalf("expected pending for message 101")
	}
	if pending1.Query != "q1" || pending1.ReplyToMessageID != 11 {
		t.Fatalf("unexpected pending1: %+v", pending1)
	}

	pending2, ok := b.getPending(chatID, 102)
	if !ok {
		t.Fatalf("expected pending for message 102")
	}
	if pending2.Query != "q2" || pending2.ReplyToMessageID != 12 {
		t.Fatalf("unexpected pending2: %+v", pending2)
	}

	b.clearPendingByMessage(chatID, 101)
	if _, ok := b.getPending(chatID, 101); ok {
		t.Fatalf("message 101 pending should be cleared")
	}
	if _, ok := b.getPending(chatID, 102); !ok {
		t.Fatalf("message 102 pending should remain")
	}
}

func TestPendingTransferIsolatedByMessageID(t *testing.T) {
	chatID := int64(2001)
	b := &TelegramBot{
		pendingTransfers: make(map[int64]map[int]*PendingTransfer),
	}

	b.setPendingTransfer(chatID, mediaTypeAlbum, "a1", "Album 1", "us", 21, 201, false)
	b.setPendingTransfer(chatID, mediaTypePlaylist, "p1", "Playlist 1", "us", 22, 202, false)

	pending1, ok := b.getPendingTransfer(chatID, 201)
	if !ok {
		t.Fatalf("expected pending transfer for message 201")
	}
	if pending1.MediaID != "a1" || pending1.ReplyToMessageID != 21 {
		t.Fatalf("unexpected pending transfer 201: %+v", pending1)
	}

	pending2, ok := b.getPendingTransfer(chatID, 202)
	if !ok {
		t.Fatalf("expected pending transfer for message 202")
	}
	if pending2.MediaID != "p1" || pending2.ReplyToMessageID != 22 {
		t.Fatalf("unexpected pending transfer 202: %+v", pending2)
	}

	b.clearPendingTransferByMessage(chatID, 201)
	if _, ok := b.getPendingTransfer(chatID, 201); ok {
		t.Fatalf("message 201 transfer should be cleared")
	}
	if _, ok := b.getPendingTransfer(chatID, 202); !ok {
		t.Fatalf("message 202 transfer should remain")
	}
}

func TestPendingArtistModeIsolatedByMessageID(t *testing.T) {
	chatID := int64(3001)
	b := &TelegramBot{
		pendingArtistModes: make(map[int64]map[int]*PendingArtistMode),
	}

	b.setPendingArtistMode(chatID, "artist-a", "Artist A", "us", 31, 301)
	b.setPendingArtistMode(chatID, "artist-b", "Artist B", "us", 32, 302)

	pending1, ok := b.getPendingArtistMode(chatID, 301)
	if !ok {
		t.Fatalf("expected pending artist mode for message 301")
	}
	if pending1.ArtistID != "artist-a" || pending1.ReplyToMessageID != 31 {
		t.Fatalf("unexpected pending artist mode 301: %+v", pending1)
	}

	pending2, ok := b.getPendingArtistMode(chatID, 302)
	if !ok {
		t.Fatalf("expected pending artist mode for message 302")
	}
	if pending2.ArtistID != "artist-b" || pending2.ReplyToMessageID != 32 {
		t.Fatalf("unexpected pending artist mode 302: %+v", pending2)
	}

	b.clearPendingArtistModeByMessage(chatID, 301)
	if _, ok := b.getPendingArtistMode(chatID, 301); ok {
		t.Fatalf("message 301 artist mode should be cleared")
	}
	if _, ok := b.getPendingArtistMode(chatID, 302); !ok {
		t.Fatalf("message 302 artist mode should remain")
	}
}

func TestCollectUniqueArtistSongIDs(t *testing.T) {
	items := []sharedcatalog.ArtistRelationshipItem{
		{ID: "song-1", Name: "Song 1"},
		{ID: "song-2", Name: "Song 2"},
		{ID: "song-1", Name: "Song 1 duplicate"},
		{ID: ""},
		{ID: "song-3", Name: "Song 3"},
	}
	got := collectUniqueArtistSongIDs(items)
	want := []string{"song-1", "song-2", "song-3"}
	if len(got) != len(want) {
		t.Fatalf("unexpected unique id count: got=%d want=%d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected unique id order: got=%v want=%v", got, want)
		}
	}
}

func TestHandleArtistModeSelectionSongsPromptsTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":901}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	chatID := int64(3002)
	panelMessageID := 301
	bot := &TelegramBot{
		token:              "test-token",
		apiBase:            server.URL,
		client:             server.Client(),
		pendingArtistModes: make(map[int64]map[int]*PendingArtistMode),
		chatSettings:       make(map[int64]ChatDownloadSettings),
	}
	bot.setPendingArtistMode(chatID, "artist-123", "Artist Name", "us", 77, panelMessageID)

	bot.handleArtistModeSelection(chatID, panelMessageID, "songs")

	if _, ok := bot.getPendingArtistMode(chatID, panelMessageID); ok {
		t.Fatalf("artist mode should be cleared after selecting songs")
	}
	pendingTransfer, ok := bot.getPendingTransfer(chatID, 901)
	if !ok {
		t.Fatalf("expected transfer prompt for artist songs")
	}
	if pendingTransfer.MediaType != mediaTypeArtistSongs || pendingTransfer.MediaID != "artist-123" {
		t.Fatalf("unexpected pending transfer: %+v", pendingTransfer)
	}
}

func TestHasSongAutoExtras(t *testing.T) {
	if hasSongAutoExtras(ChatDownloadSettings{}) {
		t.Fatalf("expected false for empty settings")
	}
	if !hasSongAutoExtras(ChatDownloadSettings{AutoLyrics: true, SettingsInited: true}) {
		t.Fatalf("expected true when AutoLyrics enabled")
	}
	if !hasSongAutoExtras(ChatDownloadSettings{AutoCover: true, SettingsInited: true}) {
		t.Fatalf("expected true when AutoCover enabled")
	}
	if !hasSongAutoExtras(ChatDownloadSettings{AutoAnimated: true, SettingsInited: true}) {
		t.Fatalf("expected true when AutoAnimated enabled")
	}
}

func TestAcquireReleaseInflightDownload(t *testing.T) {
	b := &TelegramBot{
		inflightDownloads: make(map[string]struct{}),
	}
	key := "chat|song|123"
	if !b.acquireInflightDownload(key) {
		t.Fatalf("expected first acquire success")
	}
	if b.acquireInflightDownload(key) {
		t.Fatalf("expected second acquire to be blocked")
	}
	b.releaseInflightDownload(key)
	if !b.acquireInflightDownload(key) {
		t.Fatalf("expected acquire after release to succeed")
	}
}

func TestMakeDownloadInflightKeyIncludesSettings(t *testing.T) {
	base := ChatDownloadSettings{
		Format:         telegramFormatAlac,
		AACType:        "aac",
		MVAudioType:    "atmos",
		LyricsFormat:   "lrc",
		AutoLyrics:     false,
		AutoCover:      false,
		AutoAnimated:   false,
		SettingsInited: true,
	}
	keyA := makeDownloadInflightKey(100, mediaTypeSong, "123", "us", transferModeOneByOne, base)
	base.AutoLyrics = true
	keyB := makeDownloadInflightKey(100, mediaTypeSong, "123", "us", transferModeOneByOne, base)
	if keyA == keyB {
		t.Fatalf("expected different keys when settings differ")
	}
}

func TestNormalizeTelegramBotCommandRefreshAlias(t *testing.T) {
	if got := normalizeTelegramBotCommand("rf"); got != "refresh" {
		t.Fatalf("expected rf alias to map to refresh, got %q", got)
	}
}

func TestResolveRefreshURLTargetSupportsURLPrefixes(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "direct",
			args: []string{"https://music.apple.com/us/song/example/123456789"},
		},
		{
			name: "url-prefix",
			args: []string{"url", "https://music.apple.com/us/song/example/123456789"},
		},
		{
			name: "ulr-prefix",
			args: []string{"ulr", "https://music.apple.com/us/song/example/123456789"},
		},
	}
	for _, tt := range tests {
		target, err := resolveRefreshURLTarget(tt.args)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tt.name, err)
		}
		if target.MediaType != mediaTypeSong || target.ID != "123456789" || target.Storefront != "us" {
			t.Fatalf("%s: unexpected target: %+v", tt.name, target)
		}
	}
}

func TestPurgeTargetCachesSongClearsAudioAndBundleZip(t *testing.T) {
	b := &TelegramBot{
		cache: map[string]CachedAudio{
			"song-1|alac|false": {FileID: "a1"},
			"song-1|flac|true":  {FileID: "a2"},
			"song-2|alac|false": {FileID: "a3"},
		},
		docCache: map[string]CachedDocument{
			"song:song-1|profile-a|zip":   {FileID: "d1"},
			"song:song-1|profile-b|zip":   {FileID: "d2"},
			"album:album-1|profile-a|zip": {FileID: "d3"},
		},
		videoCache: map[string]CachedVideo{},
	}

	removed := b.purgeTargetCaches(&AppleURLTarget{MediaType: mediaTypeSong, ID: "song-1"})
	if removed != 4 {
		t.Fatalf("expected 4 removed cache entries, got %d", removed)
	}
	if len(b.cache) != 1 {
		t.Fatalf("expected unrelated song cache to remain, got %#v", b.cache)
	}
	if _, ok := b.cache["song-2|alac|false"]; !ok {
		t.Fatalf("expected unrelated audio cache to remain")
	}
	if len(b.docCache) != 1 {
		t.Fatalf("expected unrelated bundle cache to remain, got %#v", b.docCache)
	}
	if _, ok := b.docCache["album:album-1|profile-a|zip"]; !ok {
		t.Fatalf("expected unrelated album zip cache to remain")
	}
}

func TestPurgeTargetCachesMusicVideoClearsVideoAndDocument(t *testing.T) {
	b := &TelegramBot{
		cache: map[string]CachedAudio{},
		docCache: map[string]CachedDocument{
			"music-video:mv-1|profile-a|document": {FileID: "d1"},
			"song:song-1|profile-a|zip":           {FileID: "d2"},
		},
		videoCache: map[string]CachedVideo{
			"music-video:mv-1|profile-a|video": {FileID: "v1"},
			"music-video:mv-2|profile-a|video": {FileID: "v2"},
		},
	}

	removed := b.purgeTargetCaches(&AppleURLTarget{MediaType: mediaTypeMusicVideo, ID: "mv-1"})
	if removed != 2 {
		t.Fatalf("expected 2 removed cache entries, got %d", removed)
	}
	if _, ok := b.docCache["song:song-1|profile-a|zip"]; !ok {
		t.Fatalf("expected unrelated document cache to remain")
	}
	if _, ok := b.videoCache["music-video:mv-2|profile-a|video"]; !ok {
		t.Fatalf("expected unrelated video cache to remain")
	}
}

func TestTelegramSendLimiterNextWait(t *testing.T) {
	limiter := newTelegramSendLimiter(2*time.Second, 4*time.Second)
	if limiter == nil {
		t.Fatalf("expected limiter")
	}
	now := time.Unix(1000, 0)
	limiter.lastAll = now.Add(-1 * time.Second)
	limiter.lastChat[42] = now.Add(-500 * time.Millisecond)
	wait := limiter.nextWaitLocked(now, 42)
	expected := 3500 * time.Millisecond
	if wait != expected {
		t.Fatalf("wait mismatch: got %s want %s", wait, expected)
	}
}

func TestEnqueueSongDownloadForceRefreshDoesNotPurgeCachesWhenQueueIsFull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:             "test-token",
		apiBase:           server.URL,
		client:            server.Client(),
		downloadQueue:     make(chan *downloadRequest, 1),
		workerLimit:       1,
		cache:             map[string]CachedAudio{"song-1|alac|false": {FileID: "audio-1"}},
		docCache:          map[string]CachedDocument{"song:song-1|profile-a|zip": {FileID: "doc-1"}},
		videoCache:        map[string]CachedVideo{},
		inflightDownloads: make(map[string]struct{}),
	}
	bot.queueCond = sync.NewCond(&bot.queueMu)
	bot.downloadQueue <- &downloadRequest{requestID: "busy"}

	bot.enqueueSongDownload(42, "song-1", "us", 0, transferModeOneByOne, true)

	if _, ok := bot.cache["song-1|alac|false"]; !ok {
		t.Fatalf("force refresh should not purge audio cache before the task is accepted")
	}
	if _, ok := bot.docCache["song:song-1|profile-a|zip"]; !ok {
		t.Fatalf("force refresh should not purge bundle cache before the task is accepted")
	}
	if got := len(bot.inflightDownloads); got != 0 {
		t.Fatalf("expected inflight lock rollback when queue is full, got %d entries", got)
	}
}

func TestHandleCommandCoverQueuesHeavyTask(t *testing.T) {
	bot := &TelegramBot{
		downloadQueue: make(chan *downloadRequest, 2),
		workerLimit:   1,
		chatSettings:  make(map[int64]ChatDownloadSettings),
	}
	bot.queueCond = sync.NewCond(&bot.queueMu)

	bot.handleCommand(42, "private", "cover", []string{"song", "12345"}, 7)

	if len(bot.downloadQueue) != 1 {
		t.Fatalf("expected cover task to be queued, got %d", len(bot.downloadQueue))
	}
	req := <-bot.downloadQueue
	if req == nil {
		t.Fatalf("expected non-nil queued request")
	}
	if req.taskType != telegramTaskCover {
		t.Fatalf("expected cover task type, got %q", req.taskType)
	}
	if req.mediaType != mediaTypeSong || req.mediaID != "12345" {
		t.Fatalf("unexpected queued cover request: %+v", req)
	}
}

func TestResolveAdminTargetUserIDPrefersExplicitArgument(t *testing.T) {
	replyTo := &Message{From: &User{ID: 2002}}
	got, err := resolveAdminTargetUserID([]string{"1001"}, replyTo)
	if err != nil {
		t.Fatalf("resolveAdminTargetUserID failed: %v", err)
	}
	if got != 1001 {
		t.Fatalf("expected explicit user id to win, got %d", got)
	}
}

func TestResolveAdminTargetUserIDFallsBackToReply(t *testing.T) {
	got, err := resolveAdminTargetUserID(nil, &Message{From: &User{ID: 2002}})
	if err != nil {
		t.Fatalf("resolveAdminTargetUserID failed: %v", err)
	}
	if got != 2002 {
		t.Fatalf("expected reply target user id, got %d", got)
	}
}

func TestTelegramBotUserAccessRules(t *testing.T) {
	bot := &TelegramBot{
		adminUsers:           map[int64]bool{1: true},
		userWhitelistEnabled: true,
		userWhitelist:        map[int64]bool{2: true},
		userBlacklist:        map[int64]bool{3: true, 1: true},
	}
	if !bot.isAllowedUser(1) {
		t.Fatalf("expected admin user to bypass user access checks")
	}
	if !bot.isAllowedUser(2) {
		t.Fatalf("expected whitelisted user to pass when whitelist mode is enabled")
	}
	if bot.isAllowedUser(3) {
		t.Fatalf("expected blacklisted user to be denied")
	}
	if bot.isAllowedUser(4) {
		t.Fatalf("expected non-whitelisted user to be denied when whitelist mode is enabled")
	}
	bot.setUserWhitelistEnabled(false)
	if !bot.isAllowedUser(4) {
		t.Fatalf("expected regular user to pass when whitelist mode is disabled")
	}
}

func TestHandleCommandWithContextSupportsReplyBasedBan(t *testing.T) {
	var messages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		payload := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		if text, _ := payload["text"].(string); text != "" {
			messages = append(messages, text)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:         "test-token",
		apiBase:       server.URL,
		client:        server.Client(),
		adminUsers:    map[int64]bool{9001: true},
		userWhitelist: make(map[int64]bool),
		userBlacklist: make(map[int64]bool),
	}
	bot.handleCommandWithContext(42, "private", 9001, "amban", nil, 7, &Message{From: &User{ID: 12345}})

	if !bot.isUserBlacklisted(12345) {
		t.Fatalf("expected reply target user to be blacklisted")
	}
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "已封禁用户：12345") {
		t.Fatalf("expected success message, got %+v", messages)
	}
}

func TestFormatAdminPanelTextIncludesStatsAndForwardChat(t *testing.T) {
	bot := &TelegramBot{
		adminUsers:           map[int64]bool{9001: true, 9002: true},
		userWhitelistEnabled: true,
		userWhitelist:        map[int64]bool{1001: true, 1002: true, 1003: true},
		userBlacklist:        map[int64]bool{2001: true},
		forwardChatID:        -1001234567890,
		forwardEnabled:       true,
	}

	text := bot.formatAdminPanelText(42)
	for _, want := range []string{
		"管理员面板",
		"白名单模式：开启",
		"归档转发：开启",
		"归档群 chat_id：-1001234567890",
		"管理员数量：2",
		"白名单人数：3",
		"黑名单人数：1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected admin panel text to contain %q, got %q", want, text)
		}
	}
}

func TestBuildAdminPanelKeyboardIncludesExpectedCallbacks(t *testing.T) {
	bot := &TelegramBot{
		userWhitelistEnabled: true,
		forwardEnabled:       true,
	}

	keyboard := bot.buildAdminPanelKeyboard(false)
	got := make([]string, 0, 8)
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			got = append(got, button.CallbackData)
		}
	}
	for _, want := range []string{
		"admin:whitelist:toggle",
		"admin:forward:toggle",
		"admin:cachepush:confirm",
		"admin:refresh",
		"admin:close",
	} {
		if !containsString(got, want) {
			t.Fatalf("expected callback %q in %+v", want, got)
		}
	}

	confirmKeyboard := bot.buildAdminPanelKeyboard(true)
	confirmCallbacks := make([]string, 0, 8)
	for _, row := range confirmKeyboard.InlineKeyboard {
		for _, button := range row {
			confirmCallbacks = append(confirmCallbacks, button.CallbackData)
		}
	}
	for _, want := range []string{"admin:cachepush:run", "admin:refresh"} {
		if !containsString(confirmCallbacks, want) {
			t.Fatalf("expected confirm callback %q in %+v", want, confirmCallbacks)
		}
	}
}

func TestBotHelpTextIncludesAdminCommands(t *testing.T) {
	text := botHelpText()
	for _, want := range []string{
		"/amadmin",
		"/amwlon",
		"/amwloff",
		"/amwladd <user_id>",
		"/amwldel <user_id>",
		"/amban <user_id>",
		"/amunban <user_id>",
		"/amcachepush",
		"/amwhoami",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected help text to contain %q, got %q", want, text)
		}
	}
}

func TestHandleCommandWithContextAmadminRequiresAdminAndOpensPanel(t *testing.T) {
	var sentTexts []string
	var sentMarkups []InlineKeyboardMarkup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		payload := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		if text, _ := payload["text"].(string); text != "" {
			sentTexts = append(sentTexts, text)
		}
		if rawMarkup, ok := payload["reply_markup"]; ok && rawMarkup != nil {
			markupJSON, err := json.Marshal(rawMarkup)
			if err != nil {
				t.Fatalf("marshal markup failed: %v", err)
			}
			var markup InlineKeyboardMarkup
			if err := json.Unmarshal(markupJSON, &markup); err != nil {
				t.Fatalf("unmarshal markup failed: %v", err)
			}
			sentMarkups = append(sentMarkups, markup)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:      "test-token",
		apiBase:    server.URL,
		client:     server.Client(),
		adminUsers: map[int64]bool{9001: true},
	}

	bot.handleCommandWithContext(42, "private", 8001, "amadmin", nil, 7, nil)
	if len(sentTexts) == 0 || !strings.Contains(sentTexts[len(sentTexts)-1], "只有管理员可以打开管理员面板") {
		t.Fatalf("expected non-admin rejection, got %+v", sentTexts)
	}

	bot.handleCommandWithContext(42, "private", 9001, "amadmin", nil, 8, nil)
	if len(sentTexts) < 2 || !strings.Contains(sentTexts[len(sentTexts)-1], "管理员面板") {
		t.Fatalf("expected admin panel message, got %+v", sentTexts)
	}
	if len(sentMarkups) == 0 {
		t.Fatalf("expected inline keyboard markup for admin panel")
	}
	lastMarkup := sentMarkups[len(sentMarkups)-1]
	callbacks := make([]string, 0, 8)
	for _, row := range lastMarkup.InlineKeyboard {
		for _, button := range row {
			callbacks = append(callbacks, button.CallbackData)
		}
	}
	if !containsString(callbacks, "admin:whitelist:toggle") || !containsString(callbacks, "admin:forward:toggle") {
		t.Fatalf("expected admin callbacks in markup, got %+v", callbacks)
	}
}

func TestHandleCommandWithContextAmcachepushRejectsNonAdmin(t *testing.T) {
	var messages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		payload := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		if text, _ := payload["text"].(string); text != "" {
			messages = append(messages, text)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:         "test-token",
		apiBase:       server.URL,
		client:        server.Client(),
		adminUsers:    map[int64]bool{9001: true},
		forwardChatID: -1001234567890,
	}

	bot.handleCommandWithContext(42, "private", 8001, "amcachepush", nil, 7, nil)
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "只有管理员可以执行缓存转存") {
		t.Fatalf("expected non-admin rejection, got %+v", messages)
	}
}

func TestHandleCallbackAdminPanelTogglesModes(t *testing.T) {
	var editedTexts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch {
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload failed: %v", err)
			}
			if text, _ := payload["text"].(string); text != "" {
				editedTexts = append(editedTexts, text)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
		}
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:                "test-token",
		apiBase:              server.URL,
		client:               server.Client(),
		allowedChats:         map[int64]bool{42: true},
		adminUsers:           map[int64]bool{9001: true},
		userWhitelistEnabled: false,
		userWhitelist:        make(map[int64]bool),
		userBlacklist:        make(map[int64]bool),
		forwardChatID:        -1001234567890,
		forwardEnabled:       false,
		stateSave:            make(chan struct{}, 4),
		autoDeleteMessages:   make(map[string]*time.Timer),
		autoDeleteSticky:     make(map[string]bool),
		autoDeleteDeadline:   make(map[string]time.Time),
	}

	bot.handleCallback(&CallbackQuery{
		ID:   "cb-whitelist",
		From: &User{ID: 9001},
		Message: &Message{
			MessageID: 11,
			Chat:      Chat{ID: 42, Type: "private"},
		},
		Data: "admin:whitelist:toggle",
	})
	if !bot.isUserWhitelistEnabled() {
		t.Fatalf("expected whitelist mode toggled on")
	}

	bot.handleCallback(&CallbackQuery{
		ID:   "cb-forward",
		From: &User{ID: 9001},
		Message: &Message{
			MessageID: 11,
			Chat:      Chat{ID: 42, Type: "private"},
		},
		Data: "admin:forward:toggle",
	})
	if !bot.isForwardEnabled() {
		t.Fatalf("expected forward mode toggled on")
	}
	if len(editedTexts) < 2 {
		t.Fatalf("expected editMessageText calls, got %+v", editedTexts)
	}
	if !strings.Contains(editedTexts[0], "白名单模式：开启") {
		t.Fatalf("expected whitelist edit text, got %+v", editedTexts)
	}
	if !strings.Contains(editedTexts[len(editedTexts)-1], "归档转发：开启") {
		t.Fatalf("expected forward edit text, got %+v", editedTexts)
	}
}

func TestHandleCallbackAdminForwardToggleRequiresConfiguredChat(t *testing.T) {
	var messages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload failed: %v", err)
			}
			if text, _ := payload["text"].(string); text != "" {
				messages = append(messages, text)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:          "test-token",
		apiBase:        server.URL,
		client:         server.Client(),
		allowedChats:   map[int64]bool{42: true},
		adminUsers:     map[int64]bool{9001: true},
		forwardEnabled: false,
	}

	bot.handleCallback(&CallbackQuery{
		ID:   "cb-forward-missing-chat",
		From: &User{ID: 9001},
		Message: &Message{
			MessageID: 11,
			Chat:      Chat{ID: 42, Type: "private"},
		},
		Data: "admin:forward:toggle",
	})

	if bot.isForwardEnabled() {
		t.Fatalf("expected forward mode to remain disabled when forward chat_id is missing")
	}
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "未配置归档群 chat_id") {
		t.Fatalf("expected missing forward chat warning, got %+v", messages)
	}
}

func TestHandleInlineQueryRejectsUnauthorizedUser(t *testing.T) {
	var gotResults []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if !strings.HasSuffix(r.URL.Path, "/answerInlineQuery") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		payload := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		if results, ok := payload["results"].([]any); ok {
			gotResults = results
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:                "test-token",
		apiBase:              server.URL,
		client:               server.Client(),
		userWhitelistEnabled: true,
		userWhitelist:        map[int64]bool{},
		userBlacklist:        map[int64]bool{},
	}

	bot.handleInlineQuery(&InlineQuery{
		ID:    "inline-1",
		From:  &User{ID: 12345},
		Query: "track:123",
	})

	if len(gotResults) != 0 {
		t.Fatalf("expected unauthorized inline query to receive empty results, got %+v", gotResults)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func mustReceiveSendAudioCall[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case call := <-ch:
		return call
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for sendAudio call")
		var zero T
		return zero
	}
}

func waitForMessageText(t *testing.T, ch <-chan string, match func(string) bool) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case text := <-ch:
			if match(text) {
				return text
			}
		case <-deadline:
			t.Fatalf("timed out waiting for matching message text")
			return ""
		}
	}
}

func TestHandleMediaTransferQueuesArtistAssetsTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:         "test-token",
		apiBase:       server.URL,
		client:        server.Client(),
		downloadQueue: make(chan *downloadRequest, 2),
		workerLimit:   1,
		chatSettings:  make(map[int64]ChatDownloadSettings),
		pendingTransfers: map[int64]map[int]*PendingTransfer{
			42: {
				100: {
					MediaType:        mediaTypeArtistAsset,
					MediaID:          "artist-123",
					Storefront:       "us",
					ReplyToMessageID: 7,
					MessageID:        100,
					CreatedAt:        time.Now(),
				},
			},
		},
	}
	bot.queueCond = sync.NewCond(&bot.queueMu)

	bot.handleMediaTransfer(42, 100, transferModeZip)

	if len(bot.downloadQueue) != 1 {
		t.Fatalf("expected artist assets task to be queued, got %d", len(bot.downloadQueue))
	}
	req := <-bot.downloadQueue
	if req == nil {
		t.Fatalf("expected non-nil queued request")
	}
	if req.taskType != telegramTaskArtistAssets {
		t.Fatalf("expected artist assets task type, got %q", req.taskType)
	}
	if req.mediaType != mediaTypeArtist || req.mediaID != "artist-123" {
		t.Fatalf("unexpected queued artist assets request: %+v", req)
	}
	if req.transferMode != transferModeZip {
		t.Fatalf("expected zip transfer mode, got %q", req.transferMode)
	}
}

func TestSendAudioFileCleansCompressedAndThumbTemps(t *testing.T) {
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "track.flac")
	if err := os.WriteFile(audioPath, []byte(strings.Repeat("A", 64)), 0644); err != nil {
		t.Fatalf("write audio fixture: %v", err)
	}
	coverPath := filepath.Join(tmpDir, "cover.jpg")
	if err := os.WriteFile(coverPath, []byte("cover"), 0644); err != nil {
		t.Fatalf("write cover fixture: %v", err)
	}

	logPath := filepath.Join(tmpDir, "ffmpeg.log")
	ffmpegPath := filepath.Join(tmpDir, "fake-ffmpeg")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
log=%q
last=""
for arg in "$@"; do
  last="$arg"
done
printf '%%s\n' "$last" >> "$log"
: > "$last"
`, logPath)
	if err := os.WriteFile(ffmpegPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	oldFFmpegPath := Config.FFmpegPath
	Config.FFmpegPath = ffmpegPath
	defer func() {
		Config.FFmpegPath = oldFFmpegPath
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"audio-file","file_size":4}}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:        "test-token",
		apiBase:      server.URL,
		client:       server.Client(),
		maxFileBytes: 8,
	}
	session := newDownloadSession(Config)
	session.Config.EmbedLrc = true
	session.Config.EmbedCover = true
	session.recordDownloadedFile(audioPath, AudioMeta{
		TrackID:        "track-1",
		Title:          "Song",
		Performer:      "Artist",
		DurationMillis: 1000,
	})

	if err := bot.sendAudioFile(session, 42, audioPath, 0, nil, telegramFormatFlac); err != nil {
		t.Fatalf("sendAudioFile failed: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read ffmpeg log: %v", err)
	}
	seen := make(map[string]struct{})
	paths := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			paths = append(paths, line)
		}
	}
	hasCompressed := false
	hasThumb := false
	for _, path := range paths {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".flac":
			hasCompressed = true
		case ".jpg":
			hasThumb = true
		}
	}
	if !hasCompressed || !hasThumb {
		t.Fatalf("expected fake ffmpeg to create compressed and thumb outputs, got %q", string(data))
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected temp file %s to be removed, stat err=%v", path, err)
		}
	}
}

func TestWriteCoverWithConfigPreservesExistingFileOnRefreshFailure(t *testing.T) {
	tmpDir := t.TempDir()
	coverPath := filepath.Join(tmpDir, "cover.jpg")
	if err := os.WriteFile(coverPath, []byte("old-cover"), 0644); err != nil {
		t.Fatalf("write cover fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	oldClient := networkHTTPClient
	networkHTTPClient = server.Client()
	defer func() {
		networkHTTPClient = oldClient
	}()

	cfg := &structs.ConfigSet{
		CoverFormat: "jpg",
		CoverSize:   "1000x1000",
	}
	_, err := writeCoverWithConfig(tmpDir, "cover", server.URL+"/art/{w}x{h}.jpg", cfg)
	if err == nil {
		t.Fatalf("expected cover download to fail")
	}

	data, err := os.ReadFile(coverPath)
	if err != nil {
		t.Fatalf("read preserved cover: %v", err)
	}
	if string(data) != "old-cover" {
		t.Fatalf("expected existing cover to be preserved, got %q", string(data))
	}
}

func TestRunMP4BoxWithTagsUsesTagFile(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "mp4box-tags.log")
	mp4boxPath := filepath.Join(tmpDir, "MP4Box")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
log=%q
tagfile=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-itags" ]; then
    tagfile="$arg"
    break
  fi
  prev="$arg"
done
if [ -z "$tagfile" ]; then
  echo "missing tag file" >&2
  exit 1
fi
cat "$tagfile" > "$log"
`, logPath)
	if err := os.WriteFile(mp4boxPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake MP4Box: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	defer func() {
		_ = os.Setenv("PATH", oldPath)
	}()

	tags := []string{
		"title=Song:Name",
		"artist=Artist",
		"cover=/tmp/cover:art.jpg",
	}
	if _, err := runMP4BoxWithTags(context.Background(), tags, "dummy.m4a"); err != nil {
		t.Fatalf("runMP4BoxWithTags failed: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read MP4Box log: %v", err)
	}
	got := string(data)
	for _, tag := range tags {
		if !strings.Contains(got, tag+"\n") {
			t.Fatalf("expected tag %q in tag file, got %q", tag, got)
		}
	}
}

func TestTelegramCacheSaveLogsFailure(t *testing.T) {
	b := &TelegramBot{
		cacheFile:  "/dev/null/telegram-cache.json",
		cache:      map[string]CachedAudio{},
		docCache:   map[string]CachedDocument{},
		videoCache: map[string]CachedVideo{},
	}

	out := captureStdoutForTest(t, func() {
		b.saveCacheLocked()
	})

	if !strings.Contains(out, "telegram cache save failed") {
		t.Fatalf("expected cache save failure log, got %q", out)
	}
}

func TestTelegramStateSaverLogsFailure(t *testing.T) {
	b := &TelegramBot{
		stateFile: "/dev/null/telegram-state.json",
		pending:   make(map[int64]map[int]*PendingSelection),
	}

	out := captureStdoutForTest(t, func() {
		b.startStateSaver()
		defer b.stopStateSaver()
		b.requestStateSave()
		time.Sleep(100 * time.Millisecond)
	})

	if !strings.Contains(out, "telegram runtime state save failed") {
		t.Fatalf("expected state save failure log, got %q", out)
	}
}

func TestDownloadStationStreamStageRecordsReusedFile(t *testing.T) {
	tmpDir := t.TempDir()
	session := newDownloadSession(structs.ConfigSet{
		SongFileFormat: "{SongName}",
	})
	ctx := &stationDownloadContext{
		session:      session,
		cfg:          &session.Config,
		station:      &task.Station{ID: "ra.123", Name: "Test Station"},
		playlistPath: tmpDir,
	}

	songName := strings.NewReplacer(
		"{SongId}", ctx.station.ID,
		"{SongNumer}", "01",
		"{SongName}", LimitStringWithConfig(ctx.cfg, ctx.station.Name),
		"{ArtistName}", "Apple Music Station",
		"{DiscNumber}", "1",
		"{TrackNumber}", "1",
		"{Quality}", "256Kbps",
		"{Tag}", "",
		"{Codec}", "AAC",
	).Replace(ctx.cfg.SongFileFormat)
	trackPath := filepath.Join(tmpDir, fmt.Sprintf("%s.m4a", forbiddenNames.ReplaceAllString(songName, "_")))
	if err := os.WriteFile(trackPath, []byte("station"), 0644); err != nil {
		t.Fatalf("write station track: %v", err)
	}
	session.OkDict[ctx.station.ID] = []int{1}

	if err := downloadStationStreamStage(ctx); err != nil {
		t.Fatalf("downloadStationStreamStage failed: %v", err)
	}
	if len(session.LastDownloadedPaths) != 1 || session.LastDownloadedPaths[0] != trackPath {
		t.Fatalf("expected reused station file to be recorded, got %v", session.LastDownloadedPaths)
	}
	meta, ok := session.getDownloadedMeta(trackPath)
	if !ok {
		t.Fatalf("expected downloaded meta for reused station file")
	}
	if meta.Format != telegramFormatAac {
		t.Fatalf("expected station stream format=%s, got %s", telegramFormatAac, meta.Format)
	}
}

func TestSendAudioFileUsesActualFormatFromMeta(t *testing.T) {
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "track.m4a")
	if err := os.WriteFile(audioPath, []byte("audio"), 0644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"audio-aac","file_size":5}}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:        "test-token",
		apiBase:      server.URL,
		client:       server.Client(),
		maxFileBytes: 1024,
	}
	session := newDownloadSession(Config)
	session.Config.EmbedLrc = true
	session.Config.EmbedCover = true
	session.recordDownloadedFile(audioPath, AudioMeta{
		TrackID:        "track-1",
		Title:          "Song",
		Performer:      "Artist",
		DurationMillis: 1000,
		Format:         telegramFormatAac,
	})

	if err := bot.sendAudioFile(session, 42, audioPath, 0, nil, telegramFormatAlac); err != nil {
		t.Fatalf("sendAudioFile failed: %v", err)
	}
	if _, ok := bot.getCachedAudio("track-1", bot.maxFileBytes, telegramFormatAac); !ok {
		t.Fatalf("expected AAC cache entry")
	}
	if _, ok := bot.getCachedAudio("track-1", bot.maxFileBytes, telegramFormatAlac); ok {
		t.Fatalf("did not expect ALAC cache entry for AAC fallback output")
	}
}

func TestFormatTelegramAudioCaptionIncludesMetadataLines(t *testing.T) {
	got := formatTelegramAudioCaption(25_460_000, 852.74, telegramFormatAlac, AudioMeta{
		Performer:  "Taylor Swift",
		AlbumName:  "1989 (Taylor’s Version)",
		GenreNames: []string{"Pop", "Country Pop", "Music"},
	})
	if !strings.Contains(got, "#AppleMusic #alac 文件大小") {
		t.Fatalf("expected base caption header, got %q", got)
	}
	if !strings.Contains(got, "\n歌手：Taylor Swift\n") {
		t.Fatalf("expected performer line, got %q", got)
	}
	if !strings.Contains(got, "\n专辑：1989 (Taylor’s Version)\n") {
		t.Fatalf("expected album line, got %q", got)
	}
	if !strings.Contains(got, "\n风格：Pop / Country Pop\n") {
		t.Fatalf("expected genre line, got %q", got)
	}
	if strings.Count(got, "via @jellyamdl_bot") != 1 {
		t.Fatalf("expected single via line, got %q", got)
	}
}

func TestSendAudioFileIncludesEnhancedCaptionAndCachesMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "track.m4a")
	if err := os.WriteFile(audioPath, []byte("audio"), 0644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	var gotCaption string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm failed: %v", err)
		}
		gotCaption = r.FormValue("caption")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"audio-file","file_size":5}}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:        "test-token",
		apiBase:      server.URL,
		client:       server.Client(),
		maxFileBytes: 1024,
		cache:        map[string]CachedAudio{},
	}
	session := newDownloadSession(Config)
	session.Config.EmbedLrc = true
	session.Config.EmbedCover = true
	session.recordDownloadedFile(audioPath, AudioMeta{
		TrackID:        "track-1",
		Title:          "Blank Space",
		Performer:      "Taylor Swift",
		DurationMillis: 1000,
		Format:         telegramFormatAlac,
		AlbumName:      "1989 (Taylor’s Version)",
		GenreNames:     []string{"Pop", "Music"},
		Storefront:     "us",
	})

	if err := bot.sendAudioFile(session, 42, audioPath, 0, nil, telegramFormatAlac); err != nil {
		t.Fatalf("sendAudioFile failed: %v", err)
	}

	if !strings.Contains(gotCaption, "\n歌手：Taylor Swift\n") {
		t.Fatalf("expected performer line in caption, got %q", gotCaption)
	}
	if !strings.Contains(gotCaption, "\n专辑：1989 (Taylor’s Version)\n") {
		t.Fatalf("expected album line in caption, got %q", gotCaption)
	}
	if !strings.Contains(gotCaption, "\n风格：Pop\n") {
		t.Fatalf("expected genre line in caption, got %q", gotCaption)
	}

	entry, ok := bot.getCachedAudio("track-1", bot.maxFileBytes, telegramFormatAlac)
	if !ok {
		t.Fatalf("expected cached audio entry")
	}
	if entry.AlbumName != "1989 (Taylor’s Version)" {
		t.Fatalf("expected album cached, got %+v", entry)
	}
	if len(entry.GenreNames) != 1 || entry.GenreNames[0] != "Pop" {
		t.Fatalf("expected sanitized cached genres, got %+v", entry.GenreNames)
	}
}

func TestTelegramCacheLoadKeepsOldAudioEntryCompatibility(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "telegram-cache.json")
	content := `{"version":4,"items":{"track-1|alac|false":{"file_id":"audio-file","file_size":123,"title":"Song","performer":"Artist"}}}`
	if err := os.WriteFile(cachePath, []byte(content), 0o600); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	bot := &TelegramBot{cacheFile: cachePath}
	bot.loadCache()

	entry, ok := bot.getCachedAudio("track-1", 0, telegramFormatAlac)
	if !ok {
		t.Fatalf("expected cache entry to load")
	}
	if entry.AlbumName != "" {
		t.Fatalf("expected old cache entry album to remain empty, got %q", entry.AlbumName)
	}
	if len(entry.GenreNames) != 0 {
		t.Fatalf("expected old cache entry genres to remain empty, got %+v", entry.GenreNames)
	}
}

func TestSendAudioByFileIDIncludesEnhancedCaptionFromCacheEntry(t *testing.T) {
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		gotPayload = map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"audio-file","file_size":123}}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:        "test-token",
		apiBase:      server.URL,
		client:       server.Client(),
		maxFileBytes: 1024,
	}
	err := bot.sendAudioByFileID(42, CachedAudio{
		FileID:         "audio-file",
		FileSize:       123,
		Format:         telegramFormatAlac,
		BitrateKbps:    852.74,
		DurationMillis: 1000,
		Title:          "Blank Space",
		Performer:      "Taylor Swift",
		AlbumName:      "1989 (Taylor’s Version)",
		GenreNames:     []string{"Pop", "Country Pop"},
	}, 0, "track-1")
	if err != nil {
		t.Fatalf("sendAudioByFileID failed: %v", err)
	}

	caption, _ := gotPayload["caption"].(string)
	if !strings.Contains(caption, "\n歌手：Taylor Swift\n") {
		t.Fatalf("expected performer line in caption, got %q", caption)
	}
	if !strings.Contains(caption, "\n专辑：1989 (Taylor’s Version)\n") {
		t.Fatalf("expected album line in caption, got %q", caption)
	}
	if !strings.Contains(caption, "\n风格：Pop / Country Pop\n") {
		t.Fatalf("expected genre line in caption, got %q", caption)
	}
}

func TestListCachedAudioItemsPrefersCreatedAtThenUpdatedAt(t *testing.T) {
	base := time.Now()
	bot := &TelegramBot{
		cache: map[string]CachedAudio{
			"track-middle|alac|false": {
				FileID:    "file-middle",
				CreatedAt: base.Add(-30 * time.Minute),
				UpdatedAt: base.Add(10 * time.Minute),
			},
			"track-oldest|alac|false": {
				FileID:    "file-oldest",
				CreatedAt: base.Add(-90 * time.Minute),
				UpdatedAt: base.Add(20 * time.Minute),
			},
			"track-legacy|alac|false": {
				FileID:    "file-legacy",
				UpdatedAt: base.Add(-10 * time.Minute),
			},
		},
	}

	items := bot.listCachedAudioItems()
	if len(items) != 3 {
		t.Fatalf("expected 3 cached items, got %d", len(items))
	}
	got := []string{items[0].TrackID, items[1].TrackID, items[2].TrackID}
	want := []string{"track-oldest", "track-middle", "track-legacy"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expected sorted track order %v, got %v", want, got)
	}
}

func TestSendAudioFileForwardsSongAudioToArchiveChat(t *testing.T) {
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "track.m4a")
	if err := os.WriteFile(audioPath, []byte("audio"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	type sendAudioCall struct {
		ChatID    int64
		ReplyToID int
		Audio     string
	}
	calls := make(chan sendAudioCall, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendAudio"):
			call := sendAudioCall{}
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatalf("ParseMultipartForm failed: %v", err)
				}
				call.ChatID, _ = strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
				call.ReplyToID, _ = strconv.Atoi(r.FormValue("reply_to_message_id"))
			} else {
				payload := map[string]any{}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatalf("decode payload failed: %v", err)
				}
				call.ChatID = int64(payload["chat_id"].(float64))
				call.Audio, _ = payload["audio"].(string)
				if rawReply, ok := payload["reply_to_message_id"].(float64); ok {
					call.ReplyToID = int(rawReply)
				}
			}
			calls <- call
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"audio-file","file_size":5}}}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:          "test-token",
		apiBase:        server.URL,
		client:         server.Client(),
		maxFileBytes:   1024,
		forwardChatID:  -1001234567890,
		forwardEnabled: true,
		cache:          map[string]CachedAudio{},
	}
	session := newDownloadSession(Config)
	session.recordDownloadedFile(audioPath, AudioMeta{
		TrackID:        "track-1",
		Title:          "Blank Space",
		Performer:      "Taylor Swift",
		DurationMillis: 1000,
		Format:         telegramFormatAlac,
		AlbumName:      "1989 (Taylor’s Version)",
		GenreNames:     []string{"Pop"},
		Storefront:     "us",
	})

	if err := bot.sendAudioFile(session, 42, audioPath, 9, nil, telegramFormatAlac); err != nil {
		t.Fatalf("sendAudioFile failed: %v", err)
	}

	first := mustReceiveSendAudioCall(t, calls)
	second := mustReceiveSendAudioCall(t, calls)
	if first.ChatID != 42 {
		t.Fatalf("expected first audio send to user chat, got %+v", first)
	}
	if first.ReplyToID != 9 {
		t.Fatalf("expected original audio send to keep reply id, got %+v", first)
	}
	if second.ChatID != -1001234567890 {
		t.Fatalf("expected second audio send to archive chat, got %+v", second)
	}
	if second.ReplyToID != 0 {
		t.Fatalf("expected archive forward to omit reply id, got %+v", second)
	}
	if second.Audio != "audio-file" {
		t.Fatalf("expected archive forward to reuse returned file_id, got %+v", second)
	}
}

func TestSendAudioByFileIDForwardsSongAudioToArchiveChat(t *testing.T) {
	type sendAudioCall struct {
		ChatID    int64
		ReplyToID int
		Audio     string
	}
	calls := make(chan sendAudioCall, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendAudio"):
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload failed: %v", err)
			}
			call := sendAudioCall{
				ChatID: int64(payload["chat_id"].(float64)),
				Audio:  payload["audio"].(string),
			}
			if rawReply, ok := payload["reply_to_message_id"].(float64); ok {
				call.ReplyToID = int(rawReply)
			}
			calls <- call
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"cached-file","file_size":123}}}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:          "test-token",
		apiBase:        server.URL,
		client:         server.Client(),
		forwardChatID:  -1001234567890,
		forwardEnabled: true,
		maxFileBytes:   1024,
	}
	err := bot.sendAudioByFileID(42, CachedAudio{
		FileID:         "cached-file",
		FileSize:       123,
		Format:         telegramFormatAlac,
		BitrateKbps:    852.74,
		DurationMillis: 1000,
		Title:          "Blank Space",
		Performer:      "Taylor Swift",
		AlbumName:      "1989 (Taylor’s Version)",
		GenreNames:     []string{"Pop"},
		Storefront:     "us",
		CreatedAt:      time.Now().Add(-time.Minute),
	}, 7, "track-1")
	if err != nil {
		t.Fatalf("sendAudioByFileID failed: %v", err)
	}

	first := mustReceiveSendAudioCall(t, calls)
	second := mustReceiveSendAudioCall(t, calls)
	if first.ChatID != 42 || first.ReplyToID != 7 {
		t.Fatalf("expected first send to user chat with reply id, got %+v", first)
	}
	if second.ChatID != -1001234567890 || second.ReplyToID != 0 {
		t.Fatalf("expected archive forward without reply id, got %+v", second)
	}
	if second.Audio != "cached-file" {
		t.Fatalf("expected archive forward to reuse cache file_id, got %+v", second)
	}
}

func TestSendAudioByFileIDWithoutSongCommentDoesNotArchiveForward(t *testing.T) {
	var sendAudioCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendAudio"):
			sendAudioCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"cached-file","file_size":123}}}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:          "test-token",
		apiBase:        server.URL,
		client:         server.Client(),
		forwardChatID:  -1001234567890,
		forwardEnabled: true,
		maxFileBytes:   1024,
	}
	err := bot.sendAudioByFileIDWithoutSongComment(42, CachedAudio{
		FileID:         "cached-file",
		FileSize:       123,
		Format:         telegramFormatAlac,
		BitrateKbps:    852.74,
		DurationMillis: 1000,
		Title:          "Blank Space",
		Performer:      "Taylor Swift",
		AlbumName:      "1989 (Taylor’s Version)",
		GenreNames:     []string{"Pop"},
		Storefront:     "us",
	}, 0, "track-1")
	if err != nil {
		t.Fatalf("sendAudioByFileIDWithoutSongComment failed: %v", err)
	}
	if got := sendAudioCount.Load(); got != 1 {
		t.Fatalf("expected internal helper to send exactly one audio without recursive forward, got %d", got)
	}
}

func TestSendAudioByFileIDWithoutSongCommentAddsArchiveMetadataCaption(t *testing.T) {
	var gotCaption string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendAudio"):
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload failed: %v", err)
			}
			gotCaption, _ = payload["caption"].(string)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"cached-file","file_size":123}}}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:         "test-token",
		apiBase:       server.URL,
		client:        server.Client(),
		forwardChatID: -1001234567890,
		maxFileBytes:  1024,
	}
	err := bot.sendAudioByFileIDWithoutSongComment(bot.forwardChatID, CachedAudio{
		FileID:         "cached-file",
		FileSize:       123,
		Format:         telegramFormatAlac,
		BitrateKbps:    852.74,
		DurationMillis: 1000,
		Title:          "Blank Space",
		Performer:      "Taylor Swift",
		AlbumName:      "1989 (Taylor’s Version)",
		GenreNames:     []string{"Pop"},
		Storefront:     "us",
	}, 0, "track-1")
	if err != nil {
		t.Fatalf("sendAudioByFileIDWithoutSongComment failed: %v", err)
	}
	if !strings.Contains(gotCaption, "#amdlcache tid=track-1 fmt=alac sf=us") {
		t.Fatalf("expected archive caption metadata, got %q", gotCaption)
	}
}

func TestHandleMessageIngestsArchiveAudioIntoCache(t *testing.T) {
	bot := &TelegramBot{
		forwardChatID: -1001234567890,
		cache:         map[string]CachedAudio{},
	}
	bot.handleMessage(&Message{
		MessageID: 77,
		Chat:      Chat{ID: bot.forwardChatID, Type: "supergroup"},
		Caption:   "#AppleMusic #alac 文件大小1.23MB 456.78kbps\n#amdlcache tid=track-1 fmt=alac sf=us",
		Audio: &Audio{
			FileID:   "archive-file-id",
			FileSize: 123,
		},
	})

	entry, ok := bot.getCachedAudio("track-1", 0, telegramFormatAlac)
	if !ok {
		t.Fatalf("expected archive audio to be cached")
	}
	if entry.FileID != "archive-file-id" {
		t.Fatalf("expected cached archive file_id, got %+v", entry)
	}
	if entry.Storefront != "us" {
		t.Fatalf("expected cached storefront from archive caption, got %+v", entry)
	}
}

func TestHandleCommandWithContextAmcachepushPushesCachedAudioInOrder(t *testing.T) {
	type sendAudioCall struct {
		ChatID int64
		Audio  string
	}
	calls := make(chan sendAudioCall, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendAudio"):
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload failed: %v", err)
			}
			calls <- sendAudioCall{
				ChatID: int64(payload["chat_id"].(float64)),
				Audio:  payload["audio"].(string),
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"cache-file","file_size":123}}}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	base := time.Now()
	bot := &TelegramBot{
		token:         "test-token",
		apiBase:       server.URL,
		client:        server.Client(),
		adminUsers:    map[int64]bool{9001: true},
		forwardChatID: -1001234567890,
		cache: map[string]CachedAudio{
			"track-2|alac|false": {
				FileID:         "file-2",
				FileSize:       222,
				Format:         telegramFormatAlac,
				BitrateKbps:    320,
				DurationMillis: 1000,
				Title:          "Song 2",
				Performer:      "Artist 2",
				AlbumName:      "Album 2",
				GenreNames:     []string{"Pop"},
				Storefront:     "us",
				CreatedAt:      base.Add(2 * time.Minute),
			},
			"track-1|alac|false": {
				FileID:         "file-1",
				FileSize:       111,
				Format:         telegramFormatAlac,
				BitrateKbps:    320,
				DurationMillis: 1000,
				Title:          "Song 1",
				Performer:      "Artist 1",
				AlbumName:      "Album 1",
				GenreNames:     []string{"Rock"},
				Storefront:     "us",
				CreatedAt:      base.Add(-2 * time.Minute),
			},
		},
	}

	bot.handleCommandWithContext(42, "private", 9001, "amcachepush", nil, 0, nil)

	first := mustReceiveSendAudioCall(t, calls)
	second := mustReceiveSendAudioCall(t, calls)
	if first.ChatID != -1001234567890 || second.ChatID != -1001234567890 {
		t.Fatalf("expected cache push target chat to be archive group, got first=%+v second=%+v", first, second)
	}
	if first.Audio != "file-1" || second.Audio != "file-2" {
		t.Fatalf("expected cache push to follow CreatedAt order, got first=%+v second=%+v", first, second)
	}
}

func TestTriggerAdminCachePushRetriesRateLimitedItems(t *testing.T) {
	type sendAudioCall struct {
		ChatID int64
		Audio  string
	}
	audioCalls := make(chan sendAudioCall, 8)
	messageTexts := make(chan string, 8)
	var rateLimitedCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendAudio"):
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload failed: %v", err)
			}
			call := sendAudioCall{
				ChatID: int64(payload["chat_id"].(float64)),
				Audio:  payload["audio"].(string),
			}
			audioCalls <- call
			if call.Audio == "file-1" && rateLimitedCount.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":false,"description":"Too Many Requests","parameters":{"retry_after":1}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"audio":{"file_id":"cache-file","file_size":123}}}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			payload := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload failed: %v", err)
			}
			if text, _ := payload["text"].(string); text != "" {
				messageTexts <- text
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:         "test-token",
		apiBase:       server.URL,
		client:        server.Client(),
		forwardChatID: -1001234567890,
		cache: map[string]CachedAudio{
			"track-1|alac|false": {
				FileID:         "file-1",
				FileSize:       111,
				Format:         telegramFormatAlac,
				BitrateKbps:    320,
				DurationMillis: 1000,
				Title:          "Song 1",
				Performer:      "Artist 1",
				AlbumName:      "Album 1",
				GenreNames:     []string{"Rock"},
				Storefront:     "us",
				CreatedAt:      time.Now(),
			},
		},
	}

	bot.triggerAdminCachePush(42, 0)

	first := mustReceiveSendAudioCall(t, audioCalls)
	second := mustReceiveSendAudioCall(t, audioCalls)
	if first.Audio != "file-1" || second.Audio != "file-1" {
		t.Fatalf("expected the same rate-limited audio to be retried, got first=%+v second=%+v", first, second)
	}

	summary := waitForMessageText(t, messageTexts, func(text string) bool {
		return strings.Contains(text, "缓存转存完成")
	})
	if !strings.Contains(summary, "成功 1") || !strings.Contains(summary, "最终失败 0") || !strings.Contains(summary, "429重试补回 1") {
		t.Fatalf("expected retry summary, got %q", summary)
	}
}

func TestTelegramSendWithRetryRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bot := &TelegramBot{}
	attempts := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := bot.sendWithRetry(ctx, nil, "Upload", 3, func() error {
		attempts++
		return fmt.Errorf("context deadline exceeded")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected retry loop to stop before second attempt, got %d attempts", attempts)
	}
}

func TestTelegramBotNewBotDownloadSessionUsesShutdownContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bot := &TelegramBot{shutdownCtx: ctx}
	session := bot.newBotDownloadSession(Config)
	cancel()

	select {
	case <-session.downloadContext().Done():
	case <-time.After(time.Second):
		t.Fatalf("expected bot download session context to be canceled")
	}
}

func TestSendAudioFileRespectsSessionContextLimiterWait(t *testing.T) {
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "track.m4a")
	if err := os.WriteFile(audioPath, []byte("audio"), 0644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	limiter := newTelegramSendLimiter(time.Hour, 0)
	if limiter == nil {
		t.Fatalf("expected limiter")
	}
	limiter.lastAll = time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bot := &TelegramBot{
		sendLimiter:  limiter,
		maxFileBytes: 1024,
	}
	session := newDownloadSession(Config)
	session.Context = ctx

	err := bot.sendAudioFile(session, 42, audioPath, 0, nil, telegramFormatAlac)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestSendMessageWithReplyReturnRespectsLimiterWait(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	limiter := newTelegramSendLimiter(time.Hour, 0)
	if limiter == nil {
		t.Fatalf("expected limiter")
	}
	limiter.lastAll = time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bot := &TelegramBot{
		token:       "test-token",
		apiBase:     server.URL,
		client:      server.Client(),
		sendLimiter: limiter,
		shutdownCtx: ctx,
	}

	_, err := bot.sendMessageWithReplyReturn(42, "hello", nil, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("expected no HTTP request when limiter wait is canceled, got %d", got)
	}
}

func TestSendMessageWithReplyReturnIncludesRetryAfterFromHTTPBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 5","parameters":{"retry_after":5}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:   "test-token",
		apiBase: server.URL,
		client:  server.Client(),
	}

	_, err := bot.sendMessageWithReplyReturn(42, "hello", nil, 0)
	if err == nil {
		t.Fatalf("expected error")
	}
	retryAfter, ok := parseTelegramRetryAfter(err)
	if !ok {
		t.Fatalf("expected retry_after to be parsed from %v", err)
	}
	if retryAfter != 5*time.Second {
		t.Fatalf("expected retry_after=5s, got %s", retryAfter)
	}
}

func TestNewDownloadStatusFallsBackOnRateLimit(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 4","parameters":{"retry_after":4}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:   "test-token",
		apiBase: server.URL,
		client:  server.Client(),
	}

	output := captureStdoutForTest(t, func() {
		status, err := newDownloadStatus(bot, 42, 0)
		if err != nil {
			t.Fatalf("expected fallback status, got err=%v", err)
		}
		if status == nil {
			t.Fatalf("expected status")
		}
		if !status.disabled {
			t.Fatalf("expected disabled fallback status")
		}
		if status.messageID != 0 {
			t.Fatalf("expected no message id, got %d", status.messageID)
		}
		status.Update("Downloading", 1, 2)
		status.UpdateSync("Done", 2, 2)
		status.finishFailure()
		status.finishSuccess()
	})
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected exactly one sendMessage attempt, got %d", got)
	}
	if !strings.Contains(output, "continuing without status message") {
		t.Fatalf("expected fallback log, got %q", output)
	}
}

func TestDownloadStatusFlushRespectsMinEditInterval(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:   "test-token",
		apiBase: server.URL,
		client:  server.Client(),
	}
	status := &DownloadStatus{
		bot:        bot,
		chatID:     42,
		messageID:  7,
		lastUpdate: time.Now(),
		updateCh:   make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
	}

	status.Update("Downloading", 1, 10)
	status.flush(false)

	if got := hits.Load(); got != 0 {
		t.Fatalf("expected no edit during hard cooldown, got %d", got)
	}
	status.mu.Lock()
	dirty := status.dirty
	status.mu.Unlock()
	if !dirty {
		t.Fatalf("expected status to remain dirty after cooldown deferral")
	}
}

func TestDownloadStatusMutesAfterEditRateLimit(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 5","parameters":{"retry_after":5}}`))
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:   "test-token",
		apiBase: server.URL,
		client:  server.Client(),
	}
	status := &DownloadStatus{
		bot:        bot,
		chatID:     42,
		messageID:  7,
		lastUpdate: time.Now().Add(-downloadStatusMinEditInterval),
		updateCh:   make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
	}

	status.Update("Downloading", 1, 10)
	status.flush(false)
	if !status.isMuted() {
		t.Fatalf("expected status to mute after edit rate limit")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected one edit attempt before mute, got %d", got)
	}

	status.Update("Still downloading", 2, 10)
	status.flush(false)
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected no further edit attempts after mute, got %d", got)
	}
}

func TestDownloadStatusFinishSuccessSchedulesAutoDelete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/editMessageText") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	b := &TelegramBot{
		token:              "test-token",
		apiBase:            server.URL,
		client:             server.Client(),
		autoDeleteMessages: make(map[string]*time.Timer),
		autoDeleteSticky:   make(map[string]bool),
		autoDeleteDeadline: make(map[string]time.Time),
	}
	status := &DownloadStatus{
		bot:       b,
		chatID:    1001,
		messageID: 42,
		stopCh:    make(chan struct{}),
	}
	status.finishSuccess()
	key := autoDeleteKey(1001, 42)
	defer b.clearAutoDeleteMessage(1001, 42)
	b.autoDeleteMu.Lock()
	_, exists := b.autoDeleteMessages[key]
	b.autoDeleteMu.Unlock()
	if !exists {
		t.Fatalf("expected success status to schedule auto-delete")
	}
}

func TestShouldReportCollectionTrackProgress(t *testing.T) {
	tests := []struct {
		index int
		total int
		want  bool
	}{
		{index: 0, total: 12, want: true},
		{index: 1, total: 12, want: false},
		{index: 4, total: 12, want: true},
		{index: 9, total: 12, want: true},
		{index: 10, total: 12, want: false},
		{index: 11, total: 12, want: true},
	}
	for _, tc := range tests {
		if got := shouldReportCollectionTrackProgress(tc.index, tc.total); got != tc.want {
			t.Fatalf("index=%d total=%d want=%t got=%t", tc.index, tc.total, tc.want, got)
		}
	}
}

func TestRunDownloadContinuesWithoutStatusMessageOnRateLimit(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 3","parameters":{"retry_after":3}}`))
		default:
			t.Fatalf("unexpected telegram API call to %s", r.URL.Path)
		}
	}))
	defer server.Close()

	bot := &TelegramBot{
		token:   "test-token",
		apiBase: server.URL,
		client:  server.Client(),
	}
	var executed atomic.Bool

	output := captureStdoutForTest(t, func() {
		bot.runDownload(42, func(session *DownloadSession) error {
			executed.Store(true)
			return errNoFilesDownloaded
		}, true, false, 0, ChatDownloadSettings{}, transferModeOneByOne, mediaTypeSong, "song-1", "us")
	})

	if !executed.Load() {
		t.Fatalf("expected download function to execute despite status rate limit")
	}
	if strings.Contains(output, "status-create-failed") {
		t.Fatalf("expected task to continue without status message, got %q", output)
	}
	if !strings.Contains(output, "result=no-files") {
		t.Fatalf("expected no-files task result, got %q", output)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected only the initial status sendMessage attempt, got %d", got)
	}
}

func TestEditMessageTextRespectsLimiterWait(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	limiter := newTelegramSendLimiter(time.Hour, 0)
	if limiter == nil {
		t.Fatalf("expected limiter")
	}
	limiter.lastAll = time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bot := &TelegramBot{
		token:       "test-token",
		apiBase:     server.URL,
		client:      server.Client(),
		sendLimiter: limiter,
		shutdownCtx: ctx,
	}

	err := bot.editMessageText(42, 7, "hello", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("expected no HTTP request when limiter wait is canceled, got %d", got)
	}
}

func TestEditMessageTextBestEffortDeferredByLimiter(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	limiter := newTelegramSendLimiter(time.Hour, 0)
	if limiter == nil {
		t.Fatalf("expected limiter")
	}
	limiter.lastAll = time.Now()

	bot := &TelegramBot{
		token:       "test-token",
		apiBase:     server.URL,
		client:      server.Client(),
		sendLimiter: limiter,
		shutdownCtx: context.Background(),
	}

	err := bot.editMessageTextBestEffort(42, 7, "hello", nil)
	if !errors.Is(err, errTelegramSendDeferred) {
		t.Fatalf("expected deferred send error, got %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("expected no HTTP request when best-effort edit is deferred, got %d", got)
	}
}

func TestHandleTrackReuseStageRecordsSourceFormatWhenConversionFails(t *testing.T) {
	tmpDir := t.TempDir()
	trackPath := filepath.Join(tmpDir, "song.m4a")
	if err := os.WriteFile(trackPath, []byte("audio"), 0644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	session := newDownloadSession(structs.ConfigSet{
		ConvertAfterDownload:       true,
		ConvertFormat:              telegramFormatFlac,
		ConvertKeepOriginal:        false,
		ConvertSkipLossyToLossless: false,
		ConvertWarnLossyToLossless: false,
		FFmpegPath:                 "definitely-missing-ffmpeg-binary",
	})
	track := &task.Track{
		SaveDir: tmpDir,
		PreID:   "album-1",
		TaskNum: 1,
	}
	ctx := &trackDownloadContext{
		session:           session,
		cfg:               &session.Config,
		track:             track,
		trackPath:         trackPath,
		convertedPath:     filepath.Join(tmpDir, "song.flac"),
		conversionEnabled: true,
		considerConverted: true,
		actualFormat:      telegramFormatFlac,
	}

	if !handleTrackReuseStage(ctx) {
		t.Fatalf("expected reuse stage to succeed")
	}
	if track.SavePath != trackPath {
		t.Fatalf("expected original path to remain selected, got %q", track.SavePath)
	}
	meta, ok := session.getDownloadedMeta(trackPath)
	if !ok {
		t.Fatalf("expected downloaded metadata for reused track")
	}
	if meta.Format != telegramFormatAlac {
		t.Fatalf("expected ALAC format after failed conversion, got %s", meta.Format)
	}
}

func TestHandleTrackReuseStageRecordsConvertedFormatWhenConvertedExists(t *testing.T) {
	tmpDir := t.TempDir()
	convertedPath := filepath.Join(tmpDir, "song.flac")
	if err := os.WriteFile(convertedPath, []byte("audio"), 0644); err != nil {
		t.Fatalf("write converted audio: %v", err)
	}

	session := newDownloadSession(structs.ConfigSet{
		ConvertAfterDownload: true,
		ConvertFormat:        telegramFormatFlac,
		ConvertKeepOriginal:  false,
	})
	track := &task.Track{
		SaveDir: tmpDir,
		PreID:   "album-1",
		TaskNum: 1,
	}
	ctx := &trackDownloadContext{
		session:           session,
		cfg:               &session.Config,
		track:             track,
		trackPath:         filepath.Join(tmpDir, "song.m4a"),
		convertedPath:     convertedPath,
		conversionEnabled: true,
		considerConverted: true,
		actualFormat:      telegramFormatFlac,
	}

	if !handleTrackReuseStage(ctx) {
		t.Fatalf("expected converted reuse stage to succeed")
	}
	if track.SavePath != convertedPath {
		t.Fatalf("expected converted path to be selected, got %q", track.SavePath)
	}
	meta, ok := session.getDownloadedMeta(convertedPath)
	if !ok {
		t.Fatalf("expected downloaded metadata for converted track")
	}
	if meta.Format != telegramFormatFlac {
		t.Fatalf("expected FLAC format for converted output, got %s", meta.Format)
	}
}

func TestBuildDirectSongTrackStage(t *testing.T) {
	songData := &ampapi.SongRespData{
		ID:   "song-1",
		Type: "songs",
		Href: "/v1/catalog/us/songs/song-1",
	}
	songData.Attributes.Name = "Song Name"
	songData.Attributes.ArtistName = "Artist Name"
	songData.Attributes.TrackNumber = 3
	songData.Attributes.ExtendedAssetUrls.EnhancedHls = "https://example.com/song.m3u8"

	track, err := buildDirectSongTrackStage(songData, "us", "en-US", "album-1")
	if err != nil {
		t.Fatalf("buildDirectSongTrackStage failed: %v", err)
	}
	if track.ID != "song-1" || track.PreID != "album-1" || track.PreType != "albums" {
		t.Fatalf("unexpected track identity: %+v", track)
	}
	if track.TaskNum != 3 {
		t.Fatalf("expected task num from song track number, got %d", track.TaskNum)
	}
	if track.WebM3u8 != "https://example.com/song.m3u8" {
		t.Fatalf("unexpected m3u8: %q", track.WebM3u8)
	}
	if track.Resp.Attributes.Name != "Song Name" || track.Resp.Attributes.ArtistName != "Artist Name" {
		t.Fatalf("unexpected track resp data: %+v", track.Resp.Attributes)
	}
}

func TestAssignTrackWorkspaceStage(t *testing.T) {
	tracks := []task.Track{
		{ID: "1"},
		{ID: "2"},
	}

	assignTrackWorkspaceStage(tracks, "/tmp/music", "/tmp/music/cover.jpg", "ALAC")

	for _, track := range tracks {
		if track.SaveDir != "/tmp/music" {
			t.Fatalf("unexpected save dir for track %s: %q", track.ID, track.SaveDir)
		}
		if track.CoverPath != "/tmp/music/cover.jpg" {
			t.Fatalf("unexpected cover path for track %s: %q", track.ID, track.CoverPath)
		}
		if track.Codec != "ALAC" {
			t.Fatalf("unexpected codec for track %s: %q", track.ID, track.Codec)
		}
	}
}

func TestBuildTrackSelectionStage(t *testing.T) {
	all := buildTrackSelectionStage(3, false, nil)
	if want := []int{1, 2, 3}; len(all) != len(want) || all[0] != 1 || all[2] != 3 {
		t.Fatalf("unexpected default selection: %#v", all)
	}

	custom := buildTrackSelectionStage(4, true, func() []int { return []int{2, 4} })
	if len(custom) != 2 || custom[0] != 2 || custom[1] != 4 {
		t.Fatalf("unexpected custom selection: %#v", custom)
	}
}

func TestNormalizeChatSettingsDefaultLanguage(t *testing.T) {
	normalized := normalizeChatSettings(ChatDownloadSettings{})
	if normalized.Language != telegramLanguageZh {
		t.Fatalf("expected default language zh, got %q", normalized.Language)
	}
	if !normalized.EmbedLyrics || !normalized.EmbedCover {
		t.Fatalf("expected default embed toggles on, got lyrics=%t cover=%t", normalized.EmbedLyrics, normalized.EmbedCover)
	}
	if normalized.SongComment {
		t.Fatalf("expected song comment disabled by default")
	}
}

func TestNormalizeChatSettingsLegacyKeepsEmbedDefaultOn(t *testing.T) {
	normalized := normalizeChatSettings(ChatDownloadSettings{
		SettingsInited: true,
		// EmbedInited 默认 false，模拟老版本状态文件未包含新字段。
	})
	if !normalized.EmbedLyrics || !normalized.EmbedCover {
		t.Fatalf("expected legacy settings to default embed toggles on")
	}
}

func TestSetChatLanguage(t *testing.T) {
	b := &TelegramBot{chatSettings: make(map[int64]ChatDownloadSettings)}
	settings := b.setChatLanguage(1001, telegramLanguageEn)
	if settings.Language != telegramLanguageEn {
		t.Fatalf("expected language to be en, got %q", settings.Language)
	}
}

func TestToggleChatSongComment(t *testing.T) {
	b := &TelegramBot{chatSettings: make(map[int64]ChatDownloadSettings)}
	first := b.toggleChatSongComment(1001)
	if !first.SongComment {
		t.Fatalf("expected song comment enabled after first toggle")
	}
	second := b.toggleChatSongComment(1001)
	if second.SongComment {
		t.Fatalf("expected song comment disabled after second toggle")
	}
}

func TestRenderSongCommentTemplateZHSections(t *testing.T) {
	meta := AudioMeta{
		Title:          "Love Song",
		Performer:      "方大同",
		DurationMillis: 269000,
		AlbumName:      "未来",
		ComposerName:   "方大同",
		ReleaseDate:    "2007-12-28",
		HasLyrics:      true,
	}
	got := renderSongCommentTemplateZH(
		meta,
		"这首作品以情绪递进和细节表达见长。",
		[]string{"R&B", "Soul", "华语流行"},
		10748,
		102813,
	)
	for _, must := range []string{
		"🎧 歌曲赏析",
		"风格：",
		"结构：",
		"编曲：",
		"人声：",
		"主题：",
		"总结：",
	} {
		if !strings.Contains(got, must) {
			t.Fatalf("expected rendered comment to contain %q, got: %s", must, got)
		}
	}
	if len([]rune(got)) < 120 {
		t.Fatalf("expected richer comment output, got too short: %s", got)
	}
}

func TestBuildTransferKeyboardLocalizedAndCross(t *testing.T) {
	zh := buildTransferKeyboard(telegramLanguageZh)
	if got := zh.InlineKeyboard[0][0].Text; got != "逐个发送" {
		t.Fatalf("expected zh one-by-one label, got %q", got)
	}
	if got := zh.InlineKeyboard[1][0].Text; got != "❌" {
		t.Fatalf("expected cross cancel button, got %q", got)
	}

	en := buildTransferKeyboard(telegramLanguageEn)
	if got := en.InlineKeyboard[0][0].Text; got != "Transfer one by one" {
		t.Fatalf("expected en one-by-one label, got %q", got)
	}
	if got := en.InlineKeyboard[1][0].Text; got != "❌" {
		t.Fatalf("expected cross cancel button, got %q", got)
	}
}

func TestBuildArtistModeKeyboardIncludesAllSongs(t *testing.T) {
	zh := buildArtistModeKeyboard(telegramLanguageZh)
	findButton := func(rows [][]InlineKeyboardButton, callback string) *InlineKeyboardButton {
		for i := range rows {
			for j := range rows[i] {
				if rows[i][j].CallbackData == callback {
					return &rows[i][j]
				}
			}
		}
		return nil
	}
	if button := findButton(zh.InlineKeyboard, "artist_rel:songs"); button == nil || button.Text != "全部歌曲" {
		if button == nil {
			t.Fatalf("expected zh all songs button")
		}
		t.Fatalf("expected zh all songs label, got %q", button.Text)
	}
	if button := findButton(zh.InlineKeyboard, "artist_rel:full-albums"); button == nil || button.Text != "全部LP" {
		if button == nil {
			t.Fatalf("expected zh all LPs button")
		}
		t.Fatalf("expected zh all LPs label, got %q", button.Text)
	}

	en := buildArtistModeKeyboard(telegramLanguageEn)
	if button := findButton(en.InlineKeyboard, "artist_rel:songs"); button == nil || button.Text != "All Songs" {
		if button == nil {
			t.Fatalf("expected en all songs button")
		}
		t.Fatalf("expected en all songs label, got %q", button.Text)
	}
	if button := findButton(en.InlineKeyboard, "artist_rel:full-albums"); button == nil || button.Text != "All LPs" {
		if button == nil {
			t.Fatalf("expected en all LPs button")
		}
		t.Fatalf("expected en all LPs label, got %q", button.Text)
	}
}

func TestNormalizeArtistRelationshipIncludesSongs(t *testing.T) {
	tests := map[string]string{
		"albums":      "albums",
		"mv":          "music-videos",
		"song":        "songs",
		"allsongs":    "songs",
		"full-albums": "full-albums",
		"all-lps":     "full-albums",
		"unknown":     "",
	}
	for input, want := range tests {
		if got := normalizeArtistRelationship(input); got != want {
			t.Fatalf("input=%q got=%q want=%q", input, got, want)
		}
	}
}

func TestBuildInlineKeyboardLocalizedAndCross(t *testing.T) {
	zh := buildInlineKeyboard(1, true, true, telegramLanguageZh)
	if got := zh.InlineKeyboard[1][0].Text; got != "上一页" {
		t.Fatalf("expected zh prev label, got %q", got)
	}
	if got := zh.InlineKeyboard[2][0].Text; got != "❌" {
		t.Fatalf("expected cross cancel button, got %q", got)
	}

	en := buildInlineKeyboard(1, true, true, telegramLanguageEn)
	if got := en.InlineKeyboard[1][0].Text; got != "Prev" {
		t.Fatalf("expected en prev label, got %q", got)
	}
	if got := en.InlineKeyboard[2][0].Text; got != "❌" {
		t.Fatalf("expected cross cancel button, got %q", got)
	}
}

func TestLocalizeOutgoingTextUsagePrefix(t *testing.T) {
	b := &TelegramBot{
		chatSettings: map[int64]ChatDownloadSettings{
			1001: {Language: telegramLanguageZh, SettingsInited: true},
		},
	}
	got := b.localizeOutgoingText(1001, "Usage: /settings <value>")
	if got != "用法：/settings <value>" {
		t.Fatalf("unexpected localized usage text: %q", got)
	}
}

func TestAutoDeleteStickyInteractionReschedulesTimer(t *testing.T) {
	b := &TelegramBot{
		autoDeleteMessages: make(map[string]*time.Timer),
		autoDeleteSticky:   make(map[string]bool),
		autoDeleteDeadline: make(map[string]time.Time),
	}
	b.scheduleAutoDeleteMessage(1001, 42, true)
	key := autoDeleteKey(1001, 42)
	b.autoDeleteMu.Lock()
	initialDeadline := b.autoDeleteDeadline[key]
	_, existsBefore := b.autoDeleteMessages[key]
	b.autoDeleteMu.Unlock()
	if !existsBefore {
		t.Fatalf("expected scheduled auto-delete timer")
	}
	b.markMessageInteraction(1001, 42)
	b.autoDeleteMu.Lock()
	updatedDeadline := b.autoDeleteDeadline[key]
	sticky := b.autoDeleteSticky[key]
	_, existsAfter := b.autoDeleteMessages[key]
	b.autoDeleteMu.Unlock()
	if !existsAfter {
		t.Fatalf("expected sticky timer to remain tracked after interaction")
	}
	if !sticky {
		t.Fatalf("expected sticky marker to be kept after interaction")
	}
	if !updatedDeadline.After(initialDeadline) {
		t.Fatalf("expected interaction to extend auto-delete deadline, before=%s after=%s", initialDeadline, updatedDeadline)
	}
}

func TestDownloadStatusFinishFailureSchedulesAutoDelete(t *testing.T) {
	b := &TelegramBot{
		autoDeleteMessages: make(map[string]*time.Timer),
		autoDeleteSticky:   make(map[string]bool),
		autoDeleteDeadline: make(map[string]time.Time),
	}
	status := &DownloadStatus{
		bot:       b,
		chatID:    1001,
		messageID: 42,
		stopCh:    make(chan struct{}),
	}
	status.finishFailure()
	key := autoDeleteKey(1001, 42)
	defer b.clearAutoDeleteMessage(1001, 42)
	b.autoDeleteMu.Lock()
	_, exists := b.autoDeleteMessages[key]
	b.autoDeleteMu.Unlock()
	if !exists {
		t.Fatalf("expected failure status to schedule auto-delete")
	}
}

func TestDeleteMessageReturnsTelegramAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: message can't be deleted"}`))
	}))
	defer server.Close()

	b := &TelegramBot{
		token:   "test-token",
		apiBase: server.URL,
		client:  server.Client(),
	}
	err := b.deleteMessage(1001, 42)
	if err == nil {
		t.Fatalf("expected deleteMessage error when telegram rejects delete")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "deletemessage") {
		t.Fatalf("unexpected deleteMessage error: %v", err)
	}
}

func TestDeleteMessageIgnoresNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: message to delete not found"}`))
	}))
	defer server.Close()

	b := &TelegramBot{
		token:   "test-token",
		apiBase: server.URL,
		client:  server.Client(),
	}
	if err := b.deleteMessage(1001, 42); err != nil {
		t.Fatalf("expected not-found delete to be ignored, got: %v", err)
	}
}

func TestNormalizeMediaIdentifierRejectsTraversal(t *testing.T) {
	t.Parallel()
	if _, err := normalizeMediaIdentifier(mediaTypeMusicVideo, "../12345"); err == nil {
		t.Fatalf("expected traversal media id to be rejected")
	}
	if _, err := normalizeMediaIdentifier(mediaTypeMusicVideo, `12\34`); err == nil {
		t.Fatalf("expected backslash media id to be rejected")
	}
}

func TestNormalizeMediaIdentifierAcceptsKnownPatterns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mediaType string
		id        string
	}{
		{mediaTypeMusicVideo, "123456789"},
		{mediaTypeSong, "987654321"},
		{mediaTypePlaylist, "pl.u-123ABC-def"},
		{mediaTypeStation, "ra.9876abcd"},
	}
	for _, tc := range cases {
		if _, err := normalizeMediaIdentifier(tc.mediaType, tc.id); err != nil {
			t.Fatalf("expected %s id %q to pass, got error: %v", tc.mediaType, tc.id, err)
		}
	}
}

func TestResolveCommandTargetRejectsTraversalMediaID(t *testing.T) {
	if _, err := resolveCommandTarget([]string{"mv", "../escape"}, ""); err == nil {
		t.Fatalf("expected traversal ID to be rejected")
	}
}

func TestParseAppleMusicURLCurator(t *testing.T) {
	t.Parallel()
	target, err := parseAppleMusicURL("https://music.apple.com/us/curator/100-best-albums/1702073195?l=zh-Hans-CN")
	if err != nil {
		t.Fatalf("parseAppleMusicURL curator failed: %v", err)
	}
	if target == nil {
		t.Fatalf("expected curator target")
	}
	if target.MediaType != mediaTypeCurator {
		t.Fatalf("unexpected media type: %q", target.MediaType)
	}
	if target.Storefront != "us" {
		t.Fatalf("unexpected storefront: %q", target.Storefront)
	}
	if target.ID != "1702073195" {
		t.Fatalf("unexpected curator id: %q", target.ID)
	}
}

func TestExtractAppleMusicURLsReturnsAllMatchesInOrder(t *testing.T) {
	t.Parallel()
	text := strings.Join([]string{
		"先看这个：https://music.apple.com/us/album/foo/1234567890?i=1234567891,",
		"再看这个 <https://music.apple.com/us/curator/100-best-albums/1702073195?l=zh-Hans-CN>",
		"以及非 Apple 链接 https://example.com/test",
		"最后一个 https://music.apple.com/us/playlist/foo/pl.u-123ABC-def!",
	}, " ")
	got := extractAppleMusicURLs(text)
	want := []string{
		"https://music.apple.com/us/album/foo/1234567890?i=1234567891",
		"https://music.apple.com/us/curator/100-best-albums/1702073195?l=zh-Hans-CN",
		"https://music.apple.com/us/playlist/foo/pl.u-123ABC-def",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected urls:\n got=%v\nwant=%v", got, want)
	}
	if first := extractFirstAppleMusicURL(text); first != want[0] {
		t.Fatalf("unexpected first url: got=%q want=%q", first, want[0])
	}
}

func TestSplitBatchArgsSupportsCommonSeparators(t *testing.T) {
	t.Parallel()
	got := splitBatchArgs([]string{
		"123,456",
		"789，101112",
		"pl.u-123ABC-def;ra.9876abcd",
		"1702073195；222",
		"",
		"  333  ",
	})
	want := []string{
		"123",
		"456",
		"789",
		"101112",
		"pl.u-123ABC-def",
		"ra.9876abcd",
		"1702073195",
		"222",
		"333",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected batch args: got=%v want=%v", got, want)
	}
}

func TestJoinFileWithinRootRejectsEscapingPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := joinFileWithinRoot(root, "../oops.mp4"); err == nil {
		t.Fatalf("expected escaping filename to be rejected")
	}
	if _, err := joinFileWithinRoot(root, "/tmp/oops.mp4"); err == nil {
		t.Fatalf("expected absolute filename to be rejected")
	}
	path, err := joinFileWithinRoot(root, "safe.mp4")
	if err != nil {
		t.Fatalf("expected safe filename to pass, got %v", err)
	}
	if got, want := filepath.Dir(path), filepath.Clean(root); got != want {
		t.Fatalf("expected output under root, got dir=%s want=%s", got, want)
	}
}

func TestTelegramCacheSaveUsesSecureFileMode(t *testing.T) {
	t.Parallel()
	cachePath := filepath.Join(t.TempDir(), "telegram-cache.json")
	b := &TelegramBot{
		cacheFile:  cachePath,
		cache:      map[string]CachedAudio{"1|alac|false": {FileID: "audio-file-1"}},
		docCache:   map[string]CachedDocument{},
		videoCache: map[string]CachedVideo{},
	}

	b.saveCacheLocked()

	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("expected cache file to be written: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(telegramCacheFilePerm); got != want {
		t.Fatalf("unexpected cache file mode: got %o want %o", got, want)
	}
	if _, err := os.Stat(cachePath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("expected fixed tmp file not to be used, err=%v", err)
	}
}

func TestTelegramCacheSaveRejectsSymlinkTarget(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "real-cache.json")
	if err := os.WriteFile(realPath, []byte(`{"keep":"original"}`), 0o600); err != nil {
		t.Fatalf("write real cache file: %v", err)
	}
	linkPath := filepath.Join(tmpDir, "cache-link.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	b := &TelegramBot{
		cacheFile:  linkPath,
		cache:      map[string]CachedAudio{"1|alac|false": {FileID: "audio-file-1"}},
		docCache:   map[string]CachedDocument{},
		videoCache: map[string]CachedVideo{},
	}
	out := captureStdoutForTest(t, func() {
		b.saveCacheLocked()
	})

	raw, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("read real cache file: %v", err)
	}
	if string(raw) != `{"keep":"original"}` {
		t.Fatalf("expected symlink target not to be overwritten, got %q", string(raw))
	}
	if !strings.Contains(strings.ToLower(out), "symlink") {
		t.Fatalf("expected symlink rejection log, got %q", out)
	}
}

func TestTelegramCacheLoadSkipsSymlinkFile(t *testing.T) {
	tmpDir := t.TempDir()
	realPath := filepath.Join(tmpDir, "real-cache.json")
	content := `{"version":4,"items":{"1|alac|false":{"file_id":"audio-file-1"}}}`
	if err := os.WriteFile(realPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write real cache file: %v", err)
	}
	linkPath := filepath.Join(tmpDir, "cache-link.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	b := &TelegramBot{cacheFile: linkPath}
	b.loadCache()
	if len(b.cache) != 0 || len(b.docCache) != 0 || len(b.videoCache) != 0 {
		t.Fatalf("expected cache load to skip symlink source, got cache=%d doc=%d video=%d", len(b.cache), len(b.docCache), len(b.videoCache))
	}
}
