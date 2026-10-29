package app

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
)

const (
	defaultSearchLimit                        = 8
	defaultQueueSize                          = 20
	defaultTaskWorkerLimit                    = 1
	maxTaskWorkerLimit                        = 4
	downloadWorkerPoolSize                    = 4
	defaultBackgroundTaskLimit                = 4
	artistSongsProgressNotifyStep             = 50
	pendingTTL                                = 10 * time.Minute
	defaultTelegramFormat                     = "alac"
	defaultTelegramAACType                    = "aac-lc"
	defaultTelegramMVAudioType                = "atmos"
	defaultTelegramLyricsFormat               = "lrc"
	defaultTelegramDownloadMaxGB              = 3
	defaultTelegramCleanupInterval            = 5 * time.Minute
	defaultTelegramCleanupScanInterval        = 30 * time.Minute
	defaultTelegramCleanupProtectAge          = 2 * time.Minute
	defaultTelegramHTTPTimeout                = 180 * time.Second
	defaultTelegramPollTimeout                = 75 * time.Second
	defaultTelegramStateFile                  = "telegram-state.json"
	defaultTelegramMetricsInterval            = 60 * time.Second
	defaultTelegramSubscriptionCheckInterval  = 30 * time.Minute
	defaultTelegramResourceCheck              = 30 * time.Second
	defaultTelegramCacheSaveDebounce          = 250 * time.Millisecond
	defaultTelegramMinFreeDiskMB              = 512
	defaultTelegramMinFreeTmpMB               = 256
	defaultTelegramSendGlobalInterval         = 150 * time.Millisecond
	defaultTelegramSendChatInterval           = 800 * time.Millisecond
	defaultTelegramLanguage                   = "zh"
	defaultTelegramErrorLogFile               = "telegram-error.log"
	defaultTelegramLoopRestartDelay           = 2 * time.Second
	defaultTelegramBackgroundTaskWait         = 10 * time.Second
	defaultTelegramGetUpdatesErrorSleep       = 2 * time.Second
	defaultTelegramGetUpdatesConflictSleep    = 5 * time.Second
	defaultTelegramGetUpdatesRestartThreshold = 30
	defaultTelegramDailyRestartDeferCheck     = 30 * time.Second
	defaultTelegramTemporaryReleaseWindow     = 7 * 24 * time.Hour
	telegramAutoDeleteAfter                   = 2 * time.Minute
	// 失败消息保留更久：用户能看到明确的失败原因，避免误判 bot 无响应。
	telegramFailureAutoDeleteAfter = 10 * time.Minute
	minTelegramPollTimeout         = 35 * time.Second
	telegramDialTimeout            = 20 * time.Second
	telegramTLSHandshakeTimeout    = 30 * time.Second
	uploadNoProgressTimeout        = 120 * time.Second
	uploadWatchdogInterval         = 5 * time.Second
	uploadProgressBufferSize       = 32 * 1024
)

const (
	telegramFormatAlac      = "alac"
	telegramFormatFlac      = "flac"
	telegramFormatAac       = "aac"
	telegramFormatAtmos     = "atmos"
	transferModeOneByOne    = "one"
	transferModeZip         = "zip"
	downloadStatusTrackStep = 5
)

const (
	telegramLanguageZh = "zh"
	telegramLanguageEn = "en"
)

const (
	mediaTypeSong        = "song"
	mediaTypeAlbum       = "album"
	mediaTypePlaylist    = "playlist"
	mediaTypeStation     = "station"
	mediaTypeMusicVideo  = "music-video"
	mediaTypeArtist      = "artist"
	mediaTypeCurator     = "curator"
	mediaTypeArtistSongs = "artist-songs"
	mediaTypeArtistLPs   = "artist-lp-albums"
	mediaTypeCuratorLPs  = "curator-albums"
	mediaTypeAlbumLyrics = "album-lyrics"
	mediaTypeArtistAsset = "artist-asset"
)

const (
	telegramTaskDownload      = "download"
	telegramTaskCover         = "cover"
	telegramTaskAnimatedCover = "animated-cover"
	telegramTaskSongLyrics    = "song-lyrics"
	telegramTaskAlbumLyrics   = "album-lyrics"
	telegramTaskArtistAssets  = "artist-assets"
)

type ChatDownloadSettings struct {
	Format         string
	AACType        string
	MVAudioType    string
	LyricsFormat   string
	Language       string
	EmbedLyrics    bool
	EmbedCover     bool
	EmbedInited    bool
	SongZip        bool
	AutoLyrics     bool
	AutoCover      bool
	AutoAnimated   bool
	SongComment    bool
	SettingsInited bool
}

func normalizeTransferModeForMedia(transferMode string, mediaType string, single bool) string {
	if transferMode != transferModeZip {
		transferMode = transferModeOneByOne
	}
	if mediaType == mediaTypeMusicVideo {
		return transferModeOneByOne
	}
	if single && mediaType != mediaTypeSong {
		return transferModeOneByOne
	}
	return transferMode
}

func normalizeTelegramTaskType(taskType string) string {
	switch strings.TrimSpace(taskType) {
	case "", telegramTaskDownload:
		return telegramTaskDownload
	case telegramTaskCover:
		return telegramTaskCover
	case telegramTaskAnimatedCover:
		return telegramTaskAnimatedCover
	case telegramTaskSongLyrics:
		return telegramTaskSongLyrics
	case telegramTaskAlbumLyrics:
		return telegramTaskAlbumLyrics
	case telegramTaskArtistAssets:
		return telegramTaskArtistAssets
	default:
		return strings.TrimSpace(taskType)
	}
}

func telegramMediaProducesSongAudio(mediaType string) bool {
	switch mediaType {
	case mediaTypeSong, mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation, mediaTypeArtistSongs, mediaTypeArtistLPs, mediaTypeCuratorLPs:
		return true
	default:
		return false
	}
}

func applyTelegramAudioEmbeddingPolicy(session *DownloadSession, settings ChatDownloadSettings, mediaType string) {
	if session == nil || !telegramMediaProducesSongAudio(mediaType) {
		return
	}
	normalized := normalizeChatSettings(settings)
	// 歌曲内嵌由聊天设置控制；其他标签写入不受此开关影响。
	session.Config.LrcFormat = normalized.LyricsFormat
	session.Config.EmbedLrc = normalized.EmbedLyrics
	session.Config.EmbedCover = normalized.EmbedCover
	session.Config.SaveLrcFile = normalized.AutoLyrics
	session.Config.SaveAnimatedArtwork = normalized.AutoAnimated
	// 封面文件在「封面内嵌」或「额外封面文件」任一开启时都需要下载。
	session.StaticCoverDownload = normalized.EmbedCover || normalized.AutoCover
}

type TelegramBot struct {
	token         string
	apiBase       string
	proxyInfo     string
	appleToken    string
	client        *http.Client
	pollClient    *http.Client
	allowedChats  map[int64]bool
	adminUsers    map[int64]bool
	forwardChatID int64
	searchLimit   int
	maxFileBytes  int64
	errorLogFile  string

	getUpdatesErrorDelay    time.Duration
	getUpdatesConflictDelay time.Duration
	getUpdatesRestartAfter  int

	accessMu             sync.RWMutex
	userWhitelistEnabled bool
	userWhitelist        map[int64]bool
	userBlacklist        map[int64]bool
	forwardEnabled       bool

	settingsMu   sync.Mutex
	chatSettings map[int64]ChatDownloadSettings

	pendingMu sync.Mutex
	pending   map[int64]map[int]*PendingSelection

	transferMu       sync.Mutex
	pendingTransfers map[int64]map[int]*PendingTransfer

	mvStreamMu       sync.Mutex
	pendingMVStreams map[int64]map[int]*PendingMVStream

	artistModeMu       sync.Mutex
	pendingArtistModes map[int64]map[int]*PendingArtistMode

	queueMu        sync.Mutex
	downloadQueue  chan *downloadRequest
	queueCond      *sync.Cond
	workerLimit    int
	activeWorkers  int
	workerWG       sync.WaitGroup
	queueCloseOnce sync.Once
	queueClosed    bool // guarded by queueMu; set when downloadQueue is closed

	downloadCoreMu sync.Mutex

	cacheMu    sync.Mutex
	cacheFile  string
	cache      map[string]CachedAudio
	docCache   map[string]CachedDocument
	videoCache map[string]CachedVideo
	cacheSave  chan struct{}
	cacheStop  chan struct{}
	cacheWG    sync.WaitGroup
	// cacheSnapSeq (guarded by cacheMu) numbers snapshots; cacheWriteMu
	// serializes writes and drops snapshots older than cacheWrittenSeq.
	cacheSnapSeq    uint64
	cacheWriteMu    sync.Mutex
	cacheWrittenSeq uint64

	// store is the SQLite backend; nil means the legacy JSON files are used.
	// cacheDirty (guarded by cacheMu) lists entries changed since the last
	// flush so only those rows are written.
	store      *telegramStore
	cacheDirty map[cacheEntryRef]struct{}

	inflightMu        sync.Mutex
	inflightDownloads map[string]struct{}
	sendLimiter       *telegramSendLimiter

	userLimiter *userRateLimiter
	history     *downloadHistory

	requestStateMu sync.Mutex
	activeRequests map[string]telegramPersistedRequest
	requestSeq     atomic.Uint64

	requestCancelMu    sync.Mutex
	runningRequestCtx  map[string]context.CancelFunc
	canceledRequestIDs map[string]struct{}

	stateFile string
	stateMu   sync.Mutex
	stateSave chan struct{}
	stateStop chan struct{}
	stateWG   sync.WaitGroup

	metricsStop chan struct{}
	metricsWG   sync.WaitGroup

	metricsHTTPMu     sync.Mutex
	metricsHTTPServer *http.Server
	metricsHTTPAddr   string
	metricsHTTPWG     sync.WaitGroup

	subscriptionMu          sync.Mutex
	subscriptions           map[string]telegramSubscription
	temporaryReleases       map[string]telegramTemporaryRelease
	subscriptionWatcherStop chan struct{}
	subscriptionWatcherWG   sync.WaitGroup

	bgInitOnce sync.Once
	bgSem      chan struct{}
	bgWG       sync.WaitGroup

	resourceGuard *telegramResourceGuard

	cleanupTracker *telegramCleanupTracker

	autoDeleteMu       sync.Mutex
	autoDeleteMessages map[string]*time.Timer
	autoDeleteSticky   map[string]bool
	autoDeleteDeadline map[string]time.Time

	songCommentMu    sync.Mutex
	songCommentCache map[string]songCommentCacheEntry

	adminTaskMu      sync.Mutex
	cachePushRunning bool

	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
}

type PendingSelection struct {
	Kind             string
	Query            string
	Title            string
	Storefront       string
	Offset           int
	HasNext          bool
	Items            []apputils.SearchResultItem
	CreatedAt        time.Time
	ReplyToMessageID int
	ResultsMessageID int
}

type PendingTransfer struct {
	MediaType        string
	MediaID          string
	MediaName        string
	Storefront       string
	ForceRefresh     bool
	ReplyToMessageID int
	MessageID        int
	CreatedAt        time.Time
}

type MusicVideoStreamOption struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	PlaylistURL string `json:"playlist_url"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	VideoRange  string `json:"video_range,omitempty"`
	Bandwidth   uint32 `json:"bandwidth,omitempty"`
}

type PendingMVStream struct {
	MediaID          string                   `json:"media_id"`
	MediaName        string                   `json:"media_name,omitempty"`
	Storefront       string                   `json:"storefront"`
	ForceRefresh     bool                     `json:"force_refresh,omitempty"`
	ReplyToMessageID int                      `json:"reply_to_message_id,omitempty"`
	MessageID        int                      `json:"message_id"`
	CreatedAt        time.Time                `json:"created_at"`
	Options          []MusicVideoStreamOption `json:"options,omitempty"`
}

type PendingArtistMode struct {
	ArtistID         string
	ArtistName       string
	Storefront       string
	ReplyToMessageID int
	MessageID        int
	CreatedAt        time.Time
}

type downloadRequest struct {
	chatID       int64
	replyToID    int
	single       bool
	forceRefresh bool
	taskType     string
	settings     ChatDownloadSettings
	transferMode string
	mediaType    string
	mediaID      string
	mvVariantKey string
	mvVariantURL string
	mvVariantTag string
	storefront   string
	inflightKey  string
	requestID    string
	fn           func(session *DownloadSession) error
	run          func(*TelegramBot, context.Context) error
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
	InlineQuery   *InlineQuery   `json:"inline_query,omitempty"`
}

type Message struct {
	MessageID      int       `json:"message_id"`
	From           *User     `json:"from,omitempty"`
	Chat           Chat      `json:"chat"`
	Text           string    `json:"text,omitempty"`
	Caption        string    `json:"caption,omitempty"`
	Audio          *Audio    `json:"audio,omitempty"`
	Document       *Document `json:"document,omitempty"`
	Video          *Video    `json:"video,omitempty"`
	ReplyToMessage *Message  `json:"reply_to_message,omitempty"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from,omitempty"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

type InlineQuery struct {
	ID    string `json:"id"`
	From  *User  `json:"from,omitempty"`
	Query string `json:"query"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text                         string  `json:"text"`
	CallbackData                 string  `json:"callback_data,omitempty"`
	SwitchInlineQuery            *string `json:"switch_inline_query,omitempty"`
	SwitchInlineQueryCurrentChat *string `json:"switch_inline_query_current_chat,omitempty"`
	Url                          string  `json:"url,omitempty"`
}

type ReplyKeyboardMarkup struct {
	Keyboard        [][]KeyboardButton `json:"keyboard"`
	ResizeKeyboard  bool               `json:"resize_keyboard,omitempty"`
	OneTimeKeyboard bool               `json:"one_time_keyboard,omitempty"`
}

type ReplyKeyboardRemove struct {
	RemoveKeyboard bool `json:"remove_keyboard"`
}

type KeyboardButton struct {
	Text string `json:"text"`
}

type getUpdatesResponse struct {
	OK          bool                  `json:"ok"`
	Result      []Update              `json:"result"`
	Description string                `json:"description,omitempty"`
	Parameters  apiResponseParameters `json:"parameters,omitempty"`
}

type apiResponseParameters struct {
	RetryAfter int `json:"retry_after,omitempty"`
}

type apiResponse struct {
	OK          bool                  `json:"ok"`
	Description string                `json:"description,omitempty"`
	Parameters  apiResponseParameters `json:"parameters,omitempty"`
}

type sendMessageResponse struct {
	OK          bool                  `json:"ok"`
	Result      Message               `json:"result"`
	Description string                `json:"description,omitempty"`
	Parameters  apiResponseParameters `json:"parameters,omitempty"`
}

type sendAudioResponse struct {
	OK          bool                  `json:"ok"`
	Result      AudioMessage          `json:"result"`
	Description string                `json:"description,omitempty"`
	Parameters  apiResponseParameters `json:"parameters,omitempty"`
}

type sendDocumentResponse struct {
	OK          bool                  `json:"ok"`
	Result      DocumentMessage       `json:"result"`
	Description string                `json:"description,omitempty"`
	Parameters  apiResponseParameters `json:"parameters,omitempty"`
}

type sendVideoResponse struct {
	OK          bool                  `json:"ok"`
	Result      VideoMessage          `json:"result"`
	Description string                `json:"description,omitempty"`
	Parameters  apiResponseParameters `json:"parameters,omitempty"`
}

type AudioMessage struct {
	MessageID int   `json:"message_id"`
	Audio     Audio `json:"audio"`
}

type DocumentMessage struct {
	MessageID int      `json:"message_id"`
	Document  Document `json:"document"`
}

type VideoMessage struct {
	MessageID int   `json:"message_id"`
	Video     Video `json:"video"`
}

type Audio struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	FileName     string `json:"file_name,omitempty"`
}

type Video struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
}

type InlineQueryResultCachedAudio struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	AudioFileID string `json:"audio_file_id"`
	Caption     string `json:"caption,omitempty"`
}

type InlineQueryResultArticle struct {
	Type                string              `json:"type"`
	ID                  string              `json:"id"`
	Title               string              `json:"title"`
	Description         string              `json:"description,omitempty"`
	InputMessageContent InputMessageContent `json:"input_message_content"`
}

type InputMessageContent struct {
	MessageText string `json:"message_text"`
}
