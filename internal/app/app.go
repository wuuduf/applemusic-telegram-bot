package app

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	sharedcatalog "github.com/wuuduf/applemusic-telegram-bot/internal/catalog"
	sharedstorage "github.com/wuuduf/applemusic-telegram-bot/internal/storage"
	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/cmdrunner"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"

	"github.com/fatih/color"
	"github.com/grafov/m3u8"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v2"
)

var (
	forbiddenNames          = regexp.MustCompile(`[/\\<>:"|?*]`)
	retryAfterJSONRe        = regexp.MustCompile(`(?i)"retry_after"\s*:\s*(\d+)`)
	retryAfterTextRe        = regexp.MustCompile(`(?i)retry\s+after\s+(\d+)`)
	mediaIDNumericRe        = regexp.MustCompile(`^\d+$`)
	mediaIDPlaylistRe       = regexp.MustCompile(`^pl\.[A-Za-z0-9-]+$`)
	mediaIDStationRe        = regexp.MustCompile(`^ra\.[A-Za-z0-9-]+$`)
	mediaIDGenericRe        = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	artist_select           bool
	debug_mode              bool
	alac_max                *int
	atmos_max               *int
	mv_max                  *int
	mv_audio_type           *string
	aac_type                *string
	Config                  structs.ConfigSet
	searchMetaMu            sync.Mutex
	searchMetaByID          = make(map[string]AudioMeta)
	networkHTTPClient       = &http.Client{Timeout: 45 * time.Second}
	runtimeErrorLogMu       sync.Mutex
	runtimeErrorLogPath     atomic.Value
	errNoFilesDownloaded    = errors.New("no files were downloaded")
	errTelegramSendDeferred = errors.New("telegram send deferred by limiter")
)

const maxMediaIDLength = 128
const maxTemplateValueBytes = 240

type AudioMeta struct {
	TrackID        string
	VariantKey     string
	Title          string
	Performer      string
	DurationMillis int64
	Format         string
	AlbumName      string
	ComposerName   string
	ReleaseDate    string
	GenreNames     []string
	AudioTraits    []string
	Storefront     string
	HasLyrics      bool
	ContentRating  string
}

type CachedAudio struct {
	FileID         string    `json:"file_id"`
	FileSize       int64     `json:"file_size"`
	Compressed     bool      `json:"compressed"`
	Format         string    `json:"format,omitempty"`
	SizeBytes      int64     `json:"size_bytes,omitempty"`
	BitrateKbps    float64   `json:"bitrate_kbps,omitempty"`
	DurationMillis int64     `json:"duration_millis,omitempty"`
	Title          string    `json:"title,omitempty"`
	Performer      string    `json:"performer,omitempty"`
	AlbumName      string    `json:"album_name,omitempty"`
	GenreNames     []string  `json:"genre_names,omitempty"`
	Storefront     string    `json:"storefront,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type CachedDocument struct {
	FileID    string    `json:"file_id"`
	FileSize  int64     `json:"file_size,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type CachedVideo struct {
	FileID    string    `json:"file_id"`
	FileSize  int64     `json:"file_size,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type songCommentCacheEntry struct {
	Text      string
	ExpiresAt time.Time
}

type DownloadSession struct {
	Context             context.Context
	Config              structs.ConfigSet
	DlAtmos             bool
	DlAac               bool
	DlSelect            bool
	DlSong              bool
	ForceRedownload     bool
	Counter             structs.Counter
	OkDict              map[string][]int
	LastDownloadedPaths []string
	DownloadedMeta      map[string]AudioMeta
	ActiveProgress      func(phase string, done, total int64)
	StaticCoverDownload bool

	downloadedMetaMu sync.Mutex
}

type JobContext struct {
	Session        *DownloadSession
	Token          string
	Storefront     string
	MediaUserToken string
}

func newDownloadSession(base structs.ConfigSet) *DownloadSession {
	return &DownloadSession{
		Context:             context.Background(),
		Config:              base,
		OkDict:              make(map[string][]int),
		DownloadedMeta:      make(map[string]AudioMeta),
		StaticCoverDownload: true,
	}
}

func (s *DownloadSession) resetState() {
	if s == nil {
		return
	}
	s.DlAtmos = false
	s.DlAac = false
	s.DlSelect = false
	s.DlSong = false
	s.Counter = structs.Counter{}
	s.OkDict = make(map[string][]int)
	s.clearDownloadState()
}

func (s *DownloadSession) downloadContext() context.Context {
	if s == nil || s.Context == nil {
		return context.Background()
	}
	return s.Context
}

func inferTelegramAudioFormatFromPath(path string, fallback string) string {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".flac":
		return telegramFormatFlac
	case ".aac":
		return telegramFormatAac
	case ".m4a", ".mp4":
		normalized := normalizeTelegramFormat(fallback)
		if normalized != "" {
			return normalized
		}
		return defaultTelegramFormat
	default:
		normalized := normalizeTelegramFormat(fallback)
		if normalized != "" {
			return normalized
		}
		return defaultTelegramFormat
	}
}

func (s *DownloadSession) recordDownloadedTrack(track *task.Track, format string) {
	if s == nil || track == nil || track.SavePath == "" {
		return
	}
	meta := AudioMeta{
		TrackID:        strings.TrimSpace(track.ID),
		Title:          strings.TrimSpace(track.Resp.Attributes.Name),
		Performer:      strings.TrimSpace(track.Resp.Attributes.ArtistName),
		DurationMillis: int64(track.Resp.Attributes.DurationInMillis),
		Format:         strings.TrimSpace(format),
		AlbumName:      strings.TrimSpace(track.Resp.Attributes.AlbumName),
		ComposerName:   strings.TrimSpace(track.Resp.Attributes.ComposerName),
		ReleaseDate:    strings.TrimSpace(track.Resp.Attributes.ReleaseDate),
		GenreNames:     append([]string(nil), track.Resp.Attributes.GenreNames...),
		AudioTraits:    append([]string(nil), track.Resp.Attributes.AudioTraits...),
		Storefront:     strings.TrimSpace(track.Storefront),
		HasLyrics:      track.Resp.Attributes.HasLyrics,
		ContentRating:  strings.TrimSpace(track.Resp.Attributes.ContentRating),
	}
	if meta.TrackID != "" {
		if override, ok := popSearchMeta(meta.TrackID); ok {
			if override.Title != "" {
				meta.Title = override.Title
			}
			if override.Performer != "" {
				meta.Performer = override.Performer
			}
		}
	}
	s.recordDownloadedFile(track.SavePath, meta)
}

func (s *DownloadSession) recordDownloadedFile(path string, meta AudioMeta) {
	if s == nil || strings.TrimSpace(path) == "" {
		return
	}
	if normalized := normalizeTelegramFormat(meta.Format); normalized != "" {
		meta.Format = normalized
	}
	s.LastDownloadedPaths = append(s.LastDownloadedPaths, path)
	s.downloadedMetaMu.Lock()
	s.DownloadedMeta[path] = meta
	s.downloadedMetaMu.Unlock()
}

func (s *DownloadSession) getDownloadedMeta(path string) (AudioMeta, bool) {
	if s == nil {
		return AudioMeta{}, false
	}
	s.downloadedMetaMu.Lock()
	defer s.downloadedMetaMu.Unlock()
	meta, ok := s.DownloadedMeta[path]
	return meta, ok
}

func (s *DownloadSession) clearDownloadState() {
	if s == nil {
		return
	}
	s.LastDownloadedPaths = nil
	s.downloadedMetaMu.Lock()
	s.DownloadedMeta = make(map[string]AudioMeta)
	s.downloadedMetaMu.Unlock()
}

func (s *DownloadSession) shouldDownloadStaticCover() bool {
	if s == nil {
		return true
	}
	return s.StaticCoverDownload
}

func (s *DownloadSession) shouldReuseExistingFiles() bool {
	if s == nil {
		return true
	}
	return !s.ForceRedownload
}

type telegramCacheFile struct {
	Version   int                       `json:"version"`
	Items     map[string]CachedAudio    `json:"items"`
	Documents map[string]CachedDocument `json:"documents,omitempty"`
	Videos    map[string]CachedVideo    `json:"videos,omitempty"`
}

func loadConfig() error {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(data, &Config)
	if err != nil {
		return err
	}
	if len(Config.Storefront) != 2 {
		Config.Storefront = "us"
	}
	return nil
}

func nextLocalMidnight(now time.Time) time.Time {
	current := now
	if current.IsZero() {
		current = time.Now()
	}
	loc := current.Location()
	year, month, day := current.In(loc).Date()
	return time.Date(year, month, day+1, 0, 0, 0, 0, loc)
}

func telegramDailyRestartEnabled() bool {
	if Config.TelegramDailyRestartEnabled == nil {
		return true
	}
	return *Config.TelegramDailyRestartEnabled
}

func resolveTelegramErrorLogFile(cacheFile string, stateFile string) string {
	if raw := strings.TrimSpace(os.Getenv("AMDL_TELEGRAM_ERROR_LOG_FILE")); raw != "" {
		return filepath.Clean(raw)
	}
	stateFile = strings.TrimSpace(stateFile)
	if stateFile == "" {
		stateFile = resolveTelegramStateFile(strings.TrimSpace(cacheFile))
	}
	if strings.TrimSpace(stateFile) == "" {
		return defaultTelegramErrorLogFile
	}
	dir := filepath.Dir(filepath.Clean(stateFile))
	if strings.TrimSpace(dir) == "" || dir == "." {
		return defaultTelegramErrorLogFile
	}
	return filepath.Clean(filepath.Join(dir, defaultTelegramErrorLogFile))
}

func setRuntimeErrorLogPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		runtimeErrorLogPath.Store("")
		return
	}
	runtimeErrorLogPath.Store(filepath.Clean(path))
}

func currentRuntimeErrorLogPath() string {
	value := runtimeErrorLogPath.Load()
	if value == nil {
		return ""
	}
	path, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(path)
}

func appendRuntimeErrorLog(message string) {
	path := currentRuntimeErrorLogPath()
	message = strings.TrimSpace(message)
	if path == "" || message == "" {
		return
	}
	runtimeErrorLogMu.Lock()
	defer runtimeErrorLogMu.Unlock()

	if err := ensureNonSymlinkRegularFile(path); err != nil {
		fmt.Printf("telegram error log write skipped (%s): %v\n", path, err)
		return
	}
	if _, err := ensureCacheDirectory(path); err != nil {
		fmt.Printf("telegram error log write skipped (%s): %v\n", path, err)
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, telegramCacheFilePerm)
	if err != nil {
		fmt.Printf("telegram error log write skipped (%s): %v\n", path, err)
		return
	}
	line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), message)
	if _, err := file.WriteString(line); err != nil {
		fmt.Printf("telegram error log write skipped (%s): %v\n", path, err)
	}
	if err := file.Close(); err != nil {
		fmt.Printf("telegram error log close failed (%s): %v\n", path, err)
	}
	if err := os.Chmod(path, telegramCacheFilePerm); err != nil && !os.IsPermission(err) {
		fmt.Printf("telegram error log chmod failed (%s): %v\n", path, err)
	}
}

func appendRuntimeErrorLogf(format string, args ...any) {
	appendRuntimeErrorLog(fmt.Sprintf(format, args...))
}

func setSearchMeta(trackID string, title string, performer string) {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return
	}
	meta := AudioMeta{
		TrackID:   trackID,
		Title:     strings.TrimSpace(title),
		Performer: strings.TrimSpace(performer),
	}
	if meta.Title == "" && meta.Performer == "" {
		return
	}
	searchMetaMu.Lock()
	searchMetaByID[trackID] = meta
	searchMetaMu.Unlock()
}

func popSearchMeta(trackID string) (AudioMeta, bool) {
	searchMetaMu.Lock()
	defer searchMetaMu.Unlock()
	meta, ok := searchMetaByID[trackID]
	if ok {
		delete(searchMetaByID, trackID)
	}
	return meta, ok
}

func LimitString(s string) string {
	return LimitStringWithConfig(&Config, s)
}

func LimitStringWithConfig(cfg *structs.ConfigSet, s string) string {
	if cfg == nil {
		return s
	}
	if cfg.LimitMax > 0 && len([]rune(s)) > cfg.LimitMax {
		s = string([]rune(s)[:cfg.LimitMax])
	}
	return truncateUTF8ByBytes(s, maxTemplateValueBytes)
}

func truncateUTF8ByBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	var b strings.Builder
	b.Grow(maxBytes)
	used := 0
	for _, r := range s {
		size := utf8.RuneLen(r)
		if size <= 0 {
			size = 1
		}
		if used+size > maxBytes {
			break
		}
		b.WriteRune(r)
		used += size
	}
	return b.String()
}

func isInArray(arr []int, target int) bool {
	for _, num := range arr {
		if num == target {
			return true
		}
	}
	return false
}

func fileExists(path string) (bool, error) {
	f, err := os.Stat(path)
	if err == nil {
		return !f.IsDir(), nil
	} else if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func fileExistsNonEmpty(path string) (bool, error) {
	f, err := os.Stat(path)
	if err == nil {
		return !f.IsDir() && f.Size() > 0, nil
	} else if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func checkUrl(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music|classical\.music)\.apple\.com\/(\w{2})(?:\/album|\/album\/.+))\/(?:id)?(\d[^\D]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}
func checkUrlMv(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music)\.apple\.com\/(\w{2})(?:\/music-video|\/music-video\/.+))\/(?:id)?(\d[^\D]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}
func checkUrlSong(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music|classical\.music)\.apple\.com\/(\w{2})(?:\/song|\/song\/.+))\/(?:id)?(\d[^\D]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}
func checkUrlPlaylist(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music|classical\.music)\.apple\.com\/(\w{2})(?:\/playlist|\/playlist\/.+))\/(?:id)?(pl\.[\w-]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}

func checkUrlStation(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music)\.apple\.com\/(\w{2})(?:\/station|\/station\/.+))\/(?:id)?(ra\.[\w-]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}

func checkUrlArtist(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music|classical\.music)\.apple\.com\/(\w{2})(?:\/artist|\/artist\/.+))\/(?:id)?(\d[^\D]+)(?:$|\?)`)
	matches := pat.FindAllStringSubmatch(url, -1)

	if matches == nil {
		return "", ""
	} else {
		return matches[0][1], matches[0][2]
	}
}

type AppleURLTarget struct {
	MediaType  string
	Storefront string
	ID         string
	RawURL     string
}

func parseAppleMusicURL(raw string) (*AppleURLTarget, error) {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("empty URL")
	}
	if storefront, id := checkUrlSong(cleaned); storefront != "" && id != "" {
		return &AppleURLTarget{MediaType: mediaTypeSong, Storefront: storefront, ID: id, RawURL: cleaned}, nil
	}
	if storefront, id := checkUrlMv(cleaned); storefront != "" && id != "" {
		return &AppleURLTarget{MediaType: mediaTypeMusicVideo, Storefront: storefront, ID: id, RawURL: cleaned}, nil
	}
	if storefront, id := checkUrlPlaylist(cleaned); storefront != "" && id != "" {
		return &AppleURLTarget{MediaType: mediaTypePlaylist, Storefront: storefront, ID: id, RawURL: cleaned}, nil
	}
	if storefront, id := checkUrlStation(cleaned); storefront != "" && id != "" {
		return &AppleURLTarget{MediaType: mediaTypeStation, Storefront: storefront, ID: id, RawURL: cleaned}, nil
	}
	if storefront, id := checkUrlArtist(cleaned); storefront != "" && id != "" {
		return &AppleURLTarget{MediaType: mediaTypeArtist, Storefront: storefront, ID: id, RawURL: cleaned}, nil
	}
	if storefront, id := checkUrl(cleaned); storefront != "" && id != "" {
		parseResult, err := url.Parse(cleaned)
		if err == nil {
			if songID := strings.TrimSpace(parseResult.Query().Get("i")); songID != "" {
				return &AppleURLTarget{MediaType: mediaTypeSong, Storefront: storefront, ID: songID, RawURL: cleaned}, nil
			}
		}
		return &AppleURLTarget{MediaType: mediaTypeAlbum, Storefront: storefront, ID: id, RawURL: cleaned}, nil
	}
	return nil, fmt.Errorf("unsupported Apple Music URL")
}

func extractFirstAppleMusicURL(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	tokens := strings.Fields(text)
	for _, token := range tokens {
		candidate := strings.TrimSpace(token)
		candidate = strings.Trim(candidate, "<>()[]{}\"'“”‘’")
		candidate = strings.TrimRight(candidate, ".,!?")
		if candidate == "" {
			continue
		}
		lower := strings.ToLower(candidate)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			continue
		}
		if strings.Contains(lower, "music.apple.com/") || strings.Contains(lower, "beta.music.apple.com/") || strings.Contains(lower, "classical.music.apple.com/") {
			return candidate
		}
	}
	return ""
}
func getUrlSong(songUrl string, token string) (string, error) {
	storefront, songId := checkUrlSong(songUrl)
	manifest, err := ampapi.GetSongResp(storefront, songId, Config.Language, token)
	if err != nil {
		fmt.Println("\u26A0 Failed to get manifest:", err)
		return "", err
	}
	item, err := firstSongData("main.getUrlSong", manifest)
	if err != nil {
		return "", err
	}
	albumId, err := firstSongAlbumID("main.getUrlSong", item)
	if err != nil {
		return "", err
	}
	songAlbumUrl := fmt.Sprintf("https://music.apple.com/%s/album/1/%s?i=%s", storefront, albumId, songId)
	return songAlbumUrl, nil
}

func catalogServiceForToken(token string, opPrefix string) *sharedcatalog.Service {
	return &sharedcatalog.Service{
		AppleToken:     token,
		MediaUserToken: Config.MediaUserToken,
		Language:       Config.Language,
		HTTPClient:     networkHTTPClient,
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		OpPrefix:       strings.TrimSpace(opPrefix),
	}
}

func getUrlArtistName(artistUrl string, token string) (string, string, error) {
	storefront, artistId := checkUrlArtist(artistUrl)
	name, _, err := catalogServiceForToken(token, "main.artist").FetchArtistProfile(storefront, artistId)
	if err != nil {
		return "", "", err
	}
	return name, artistId, nil
}

func checkArtist(artistUrl string, token string, relationship string) ([]string, error) {
	storefront, artistId := checkUrlArtist(artistUrl)
	var args []string
	var urls []string
	var options [][]string
	items, err := catalogServiceForToken(token, "main.artist").FetchArtistRelationshipAll(storefront, artistId, relationship)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		options = append(options, []string{item.Name, item.ReleaseDate, item.ID, item.URL})
	}
	sort.Slice(options, func(i, j int) bool {
		// 将日期字符串解析为 time.Time 类型进行比较
		dateI, _ := time.Parse("2006-01-02", options[i][1])
		dateJ, _ := time.Parse("2006-01-02", options[j][1])
		return dateI.Before(dateJ) // 返回 true 表示 i 在 j 前面
	})

	table := tablewriter.NewWriter(os.Stdout)
	if relationship == "albums" {
		table.SetHeader([]string{"", "Album Name", "Date", "Album ID"})
	} else if relationship == "music-videos" {
		table.SetHeader([]string{"", "MV Name", "Date", "MV ID"})
	}
	table.SetRowLine(false)
	table.SetHeaderColor(tablewriter.Colors{},
		tablewriter.Colors{tablewriter.FgRedColor, tablewriter.Bold},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor})

	table.SetColumnColor(tablewriter.Colors{tablewriter.FgCyanColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgRedColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor})
	for i, v := range options {
		urls = append(urls, v[3])
		options[i] = append([]string{fmt.Sprint(i + 1)}, v[:3]...)
		table.Append(options[i])
	}
	table.Render()
	if artist_select {
		fmt.Println("You have selected all options:")
		return urls, nil
	}
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Please select from the " + relationship + " options above (multiple options separated by commas, ranges supported, or type 'all' to select all)")
	cyanColor := color.New(color.FgCyan)
	cyanColor.Print("Enter your choice: ")
	input, _ := reader.ReadString('\n')

	input = strings.TrimSpace(input)
	if input == "all" {
		fmt.Println("You have selected all options:")
		return urls, nil
	}

	selectedOptions := [][]string{}
	parts := strings.Split(input, ",")
	for _, part := range parts {
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			selectedOptions = append(selectedOptions, rangeParts)
		} else {
			selectedOptions = append(selectedOptions, []string{part})
		}
	}

	fmt.Println("You have selected the following options:")
	for _, opt := range selectedOptions {
		if len(opt) == 1 {
			num, err := strconv.Atoi(opt[0])
			if err != nil {
				fmt.Println("Invalid option:", opt[0])
				continue
			}
			if num > 0 && num <= len(options) {
				fmt.Println(options[num-1])
				args = append(args, urls[num-1])
			} else {
				fmt.Println("Option out of range:", opt[0])
			}
		} else if len(opt) == 2 {
			start, err1 := strconv.Atoi(opt[0])
			end, err2 := strconv.Atoi(opt[1])
			if err1 != nil || err2 != nil {
				fmt.Println("Invalid range:", opt)
				continue
			}
			if start < 1 || end > len(options) || start > end {
				fmt.Println("Range out of range:", opt)
				continue
			}
			for i := start; i <= end; i++ {
				fmt.Println(options[i-1])
				args = append(args, urls[i-1])
			}
		} else {
			fmt.Println("Invalid option:", opt)
		}
	}
	return args, nil
}

func writeCover(sanAlbumFolder, name string, url string) (string, error) {
	return writeCoverWithConfig(sanAlbumFolder, name, url, &Config)
}

func writeCoverWithConfig(sanAlbumFolder, name string, url string, cfg *structs.ConfigSet) (string, error) {
	if cfg == nil {
		cfg = &Config
	}
	originalUrl := url
	var ext string
	var covPath string
	if cfg.CoverFormat == "original" {
		ext = strings.Split(url, "/")[len(strings.Split(url, "/"))-2]
		ext = ext[strings.LastIndex(ext, ".")+1:]
		covPath = filepath.Join(sanAlbumFolder, name+"."+ext)
	} else {
		covPath = filepath.Join(sanAlbumFolder, name+"."+cfg.CoverFormat)
	}
	if cfg.CoverFormat == "png" {
		re := regexp.MustCompile(`\{w\}x\{h\}`)
		parts := re.Split(url, 2)
		url = parts[0] + "{w}x{h}" + strings.Replace(parts[1], ".jpg", ".png", 1)
	}
	url = strings.Replace(url, "{w}x{h}", cfg.CoverSize, 1)
	if cfg.CoverFormat == "original" {
		url = strings.Replace(url, "is1-ssl.mzstatic.com/image/thumb", "a5.mzstatic.com/us/r1000/0", 1)
		url = url[:strings.LastIndex(url, "/")]
	}
	data, err := fetchCoverData(url)
	if err != nil {
		if cfg.CoverFormat == "original" {
			fmt.Println("Failed to get cover, falling back to " + ext + " url.")
			splitByDot := strings.Split(originalUrl, ".")
			last := splitByDot[len(splitByDot)-1]
			fallback := originalUrl[:len(originalUrl)-len(last)] + ext
			fallback = strings.Replace(fallback, "{w}x{h}", cfg.CoverSize, 1)
			fmt.Println("Fallback URL:", fallback)
			data, err = fetchCoverData(fallback)
			if err != nil {
				fmt.Println("Failed to get cover from fallback url.")
				return "", err
			}
		} else {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(covPath), 0755); err != nil {
		return "", err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(covPath), filepath.Base(covPath)+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := replaceFileAtomically(tmpPath, covPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return covPath, nil
}

func fetchCoverData(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	resp, err := networkHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func replaceFileAtomically(tmpPath string, targetPath string) error {
	exists, err := fileExists(targetPath)
	if err != nil {
		return err
	}
	backupPath := ""
	if exists {
		backupPath = fmt.Sprintf("%s.bak-%d", targetPath, time.Now().UnixNano())
		if err := os.Rename(targetPath, backupPath); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		if backupPath != "" {
			_ = os.Rename(backupPath, targetPath)
		}
		return err
	}
	if backupPath != "" {
		_ = os.Remove(backupPath)
	}
	return nil
}

func writeLyrics(sanAlbumFolder, filename string, lrc string) error {
	lyricspath := filepath.Join(sanAlbumFolder, filename)
	f, err := os.Create(lyricspath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(lrc)
	if err != nil {
		return err
	}
	return nil
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func setDlFlags(session *DownloadSession, quality string) {
	if session == nil {
		return
	}
	session.DlAtmos = false
	session.DlAac = false

	switch quality {
	case "atmos":
		session.DlAtmos = true
		fmt.Println("Quality set to: Dolby Atmos")
	case "aac":
		session.DlAac = true
		session.Config.AacType = "aac"
		fmt.Println("Quality set to: High-Quality (AAC)")
	case "alac":
		fmt.Println("Quality set to: Lossless (ALAC)")
	}
}

func handleSearch(session *DownloadSession, searchType string, queryParts []string, token string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("download session is nil")
	}
	selection, err := apputils.HandleSearch(searchType, queryParts, token, session.Config.Storefront, session.Config.Language)
	if err != nil {
		return "", err
	}
	if selection == nil || selection.URL == "" {
		return "", nil
	}
	if selection.IsSong {
		session.DlSong = true
	}
	if selection.Quality != "" && selection.Quality != "default" {
		setDlFlags(session, selection.Quality)
	}
	return selection.URL, nil
}

func convertIfNeeded(session *DownloadSession, track *task.Track, lrc string) {
	if session == nil {
		return
	}
	coverPath := ""
	if strings.EqualFold(session.Config.ConvertFormat, "flac") && track.SaveDir != "" {
		coverPath = findCoverFile(track.SaveDir)
	}
	apputils.ConvertIfNeededWithContext(session.downloadContext(), track, lrc, &session.Config, coverPath, session.ActiveProgress)
}

func Main() {
	err := loadConfig()
	if err != nil {
		fmt.Printf("load Config failed: %v", err)
		return
	}
	token, err := ampapi.GetToken()
	if err != nil {
		if Config.AuthorizationToken != "" && Config.AuthorizationToken != "your-authorization-token" {
			token = strings.Replace(Config.AuthorizationToken, "Bearer ", "", -1)
		} else {
			fmt.Println("Failed to get token.")
			return
		}
	}
	var search_type string
	var bot_mode bool
	var cliDlAtmos bool
	var cliDlAac bool
	var cliDlSelect bool
	var cliDlSong bool
	pflag.StringVar(&search_type, "search", "", "Search for 'album', 'song', or 'artist'. Provide query after flags.")
	pflag.BoolVar(&bot_mode, "bot", false, "Run Telegram bot mode")
	pflag.BoolVar(&cliDlAtmos, "atmos", false, "Enable atmos download mode")
	pflag.BoolVar(&cliDlAac, "aac", false, "Enable adm-aac download mode")
	pflag.BoolVar(&cliDlSelect, "select", false, "Enable selective download")
	pflag.BoolVar(&cliDlSong, "song", false, "Enable single song download mode")
	pflag.BoolVar(&artist_select, "all-album", false, "Download all artist albums")
	pflag.BoolVar(&debug_mode, "debug", false, "Enable debug mode to show audio quality information")
	alac_max = pflag.Int("alac-max", Config.AlacMax, "Specify the max quality for download alac")
	atmos_max = pflag.Int("atmos-max", Config.AtmosMax, "Specify the max quality for download atmos")
	aac_type = pflag.String("aac-type", Config.AacType, "Select AAC type, aac aac-binaural aac-downmix")
	mv_audio_type = pflag.String("mv-audio-type", Config.MVAudioType, "Select MV audio type, atmos ac3 aac")
	mv_max = pflag.Int("mv-max", Config.MVMax, "Specify the max quality for download MV")

	pflag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] [url1 url2 ...]\n", "[main | main.exe | go run main.go]")
		fmt.Fprintf(os.Stderr, "Search Usage: %s --search [album|song|artist] [query]\n", "[main | main.exe | go run main.go]")
		fmt.Println("\nOptions:")
		pflag.PrintDefaults()
	}

	pflag.Parse()
	Config.AlacMax = *alac_max
	Config.AtmosMax = *atmos_max
	Config.AacType = *aac_type
	Config.MVAudioType = *mv_audio_type
	Config.MVMax = *mv_max

	cliTemplate := newDownloadSession(Config)
	cliTemplate.DlAtmos = cliDlAtmos
	cliTemplate.DlAac = cliDlAac
	cliTemplate.DlSelect = cliDlSelect
	cliTemplate.DlSong = cliDlSong

	if bot_mode {
		runTelegramBot(token)
		return
	}

	args := pflag.Args()

	if search_type != "" {
		if len(args) == 0 {
			fmt.Println("Error: --search flag requires a query.")
			pflag.Usage()
			return
		}
		selectedUrl, err := handleSearch(cliTemplate, search_type, args, token)
		if err != nil {
			fmt.Printf("\nSearch process failed: %v\n", err)
			return
		}
		if selectedUrl == "" {
			fmt.Println("\nExiting.")
			return
		}
		os.Args = []string{selectedUrl}
	} else {
		if len(args) == 0 {
			fmt.Println("No URLs provided. Please provide at least one URL.")
			pflag.Usage()
			return
		}
		os.Args = args
	}

	if strings.Contains(os.Args[0], "/artist/") {
		urlArtistName, urlArtistID, err := getUrlArtistName(os.Args[0], token)
		if err != nil {
			fmt.Println("Failed to get artistname.")
			return
		}
		cliTemplate.Config.ArtistFolderFormat = strings.NewReplacer(
			"{UrlArtistName}", LimitStringWithConfig(&cliTemplate.Config, urlArtistName),
			"{ArtistId}", urlArtistID,
		).Replace(cliTemplate.Config.ArtistFolderFormat)
		albumArgs, err := checkArtist(os.Args[0], token, "albums")
		if err != nil {
			fmt.Println("Failed to get artist albums.")
			return
		}
		mvArgs, err := checkArtist(os.Args[0], token, "music-videos")
		if err != nil {
			fmt.Println("Failed to get artist music-videos.")
		}
		os.Args = append(albumArgs, mvArgs...)
	}
	albumTotal := len(os.Args)
	newCLISession := func() *DownloadSession {
		s := newDownloadSession(cliTemplate.Config)
		s.DlAtmos = cliTemplate.DlAtmos
		s.DlAac = cliTemplate.DlAac
		s.DlSelect = cliTemplate.DlSelect
		s.DlSong = cliTemplate.DlSong
		s.StaticCoverDownload = cliTemplate.StaticCoverDownload
		return s
	}
	session := newCLISession()
	for {
		for albumNum, urlRaw := range os.Args {
			fmt.Printf("Queue %d of %d: ", albumNum+1, albumTotal)
			var storefront, albumId string

			if strings.Contains(urlRaw, "/music-video/") {
				fmt.Println("Music Video")
				if debug_mode {
					continue
				}
				session.Counter.Total++
				if len(session.Config.MediaUserToken) <= 50 {
					fmt.Println(": meida-user-token is not set, skip MV dl")
					session.Counter.Success++
					continue
				}
				if _, err := exec.LookPath("mp4decrypt"); err != nil {
					fmt.Println(": mp4decrypt is not found, skip MV dl")
					session.Counter.Success++
					continue
				}
				mvSaveDir := strings.NewReplacer(
					"{ArtistName}", "",
					"{UrlArtistName}", "",
					"{ArtistId}", "",
				).Replace(session.Config.ArtistFolderFormat)
				if mvSaveDir != "" {
					mvSaveDir = filepath.Join(session.Config.AlacSaveFolder, forbiddenNames.ReplaceAllString(mvSaveDir, "_"))
				} else {
					mvSaveDir = session.Config.AlacSaveFolder
				}
				storefront, albumId = checkUrlMv(urlRaw)
				err := mvDownloader(session, albumId, mvSaveDir, token, storefront, session.Config.MediaUserToken, nil, "", "", "")
				if err != nil {
					fmt.Println("\u26A0 Failed to dl MV:", err)
					session.Counter.Error++
					continue
				}
				session.Counter.Success++
				continue
			}
			if strings.Contains(urlRaw, "/song/") {
				fmt.Printf("Song->")
				storefront, songId := checkUrlSong(urlRaw)
				if storefront == "" || songId == "" {
					fmt.Println("Invalid song URL format.")
					continue
				}
				err := ripSong(session, songId, token, storefront, session.Config.MediaUserToken)
				if err != nil {
					fmt.Println("Failed to rip song:", err)
				}
				continue
			}
			parse, err := url.Parse(urlRaw)
			if err != nil {
				log.Fatalf("Invalid URL: %v", err)
			}
			var urlArg_i = parse.Query().Get("i")

			if strings.Contains(urlRaw, "/album/") {
				fmt.Println("Album")
				storefront, albumId = checkUrl(urlRaw)
				err := ripAlbum(session, albumId, token, storefront, session.Config.MediaUserToken, urlArg_i)
				if err != nil {
					fmt.Println("Failed to rip album:", err)
				}
			} else if strings.Contains(urlRaw, "/playlist/") {
				fmt.Println("Playlist")
				storefront, albumId = checkUrlPlaylist(urlRaw)
				err := ripPlaylist(session, albumId, token, storefront, session.Config.MediaUserToken)
				if err != nil {
					fmt.Println("Failed to rip playlist:", err)
				}
			} else if strings.Contains(urlRaw, "/station/") {
				fmt.Printf("Station")
				storefront, albumId = checkUrlStation(urlRaw)
				if len(session.Config.MediaUserToken) <= 50 {
					fmt.Println(": meida-user-token is not set, skip station dl")
					continue
				}
				err := ripStation(session, albumId, token, storefront, session.Config.MediaUserToken)
				if err != nil {
					fmt.Println("Failed to rip station:", err)
				}
			} else {
				fmt.Println("Invalid type")
			}
		}
		fmt.Printf("=======  [\u2714 ] Completed: %d/%d  |  [\u26A0 ] Warnings: %d  |  [\u2716 ] Errors: %d  =======\n", session.Counter.Success, session.Counter.Total, session.Counter.Unavailable+session.Counter.NotSong, session.Counter.Error)
		if session.Counter.Error == 0 {
			break
		}
		fmt.Println("Error detected, press Enter to try again...")
		fmt.Scanln()
		fmt.Println("Start trying again...")
		session = newCLISession()
	}
}

func extractMvAudio(c string) (string, error) {
	return extractMvAudioWithConfig(c, Config)
}

func extractMvAudioWithConfig(c string, cfg structs.ConfigSet) (string, error) {
	MediaUrl, err := url.Parse(c)
	if err != nil {
		return "", err
	}

	resp, err := networkHTTPClient.Get(c)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", errors.New(resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	audioString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(audioString), true)
	if err != nil || listType != m3u8.MASTER {
		return "", errors.New("m3u8 not of media type")
	}

	audio := from.(*m3u8.MasterPlaylist)

	var audioPriority = []string{"audio-atmos", "audio-ac3", "audio-stereo-256"}
	if cfg.MVAudioType == "ac3" {
		audioPriority = []string{"audio-ac3", "audio-stereo-256"}
	} else if cfg.MVAudioType == "aac" {
		audioPriority = []string{"audio-stereo-256"}
	}

	re := regexp.MustCompile(`_gr(\d+)_`)

	type AudioStream struct {
		URL     string
		Rank    int
		GroupID string
	}
	var audioStreams []AudioStream

	for _, variant := range audio.Variants {
		for _, audiov := range variant.Alternatives {
			if audiov.URI != "" {
				for _, priority := range audioPriority {
					if audiov.GroupId == priority {
						matches := re.FindStringSubmatch(audiov.URI)
						if len(matches) == 2 {
							var rank int
							fmt.Sscanf(matches[1], "%d", &rank)
							streamUrl, _ := MediaUrl.Parse(audiov.URI)
							audioStreams = append(audioStreams, AudioStream{
								URL:     streamUrl.String(),
								Rank:    rank,
								GroupID: audiov.GroupId,
							})
						}
					}
				}
			}
		}
	}

	if len(audioStreams) == 0 {
		return "", errors.New("no suitable audio stream found")
	}

	sort.Slice(audioStreams, func(i, j int) bool {
		return audioStreams[i].Rank > audioStreams[j].Rank
	})
	fmt.Println("Audio: " + audioStreams[0].GroupID)
	return audioStreams[0].URL, nil
}

func checkM3u8(session *DownloadSession, b string, f string) (string, error) {
	cfg := Config
	if session != nil {
		cfg = session.Config
	}
	var EnhancedHls string
	if cfg.GetM3u8FromDevice {
		adamID := b
		conn, err := net.Dial("tcp", cfg.GetM3u8Port)
		if err != nil {
			fmt.Println("Error connecting to device:", err)
			return "none", err
		}
		defer conn.Close()
		if f == "song" {
			fmt.Println("Connected to device")
		}

		adamIDBuffer := []byte(adamID)
		lengthBuffer := []byte{byte(len(adamIDBuffer))}

		_, err = conn.Write(lengthBuffer)
		if err != nil {
			fmt.Println("Error writing length to device:", err)
			return "none", err
		}

		_, err = conn.Write(adamIDBuffer)
		if err != nil {
			fmt.Println("Error writing adamID to device:", err)
			return "none", err
		}

		response, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			fmt.Println("Error reading response from device:", err)
			return "none", err
		}

		response = bytes.TrimSpace(response)
		if len(response) > 0 {
			if f == "song" {
				fmt.Println("Received URL:", string(response))
			}
			EnhancedHls = string(response)
		} else {
			fmt.Println("Received an empty response")
		}
	}
	return EnhancedHls, nil
}

func formatAvailability(available bool, quality string) string {
	if !available {
		return "Not Available"
	}
	return quality
}

func extractMedia(session *DownloadSession, b string, more_mode bool) (string, string, error) {
	if session == nil {
		return "", "", fmt.Errorf("download session is nil")
	}
	cfg := &session.Config
	masterUrl, err := url.Parse(b)
	if err != nil {
		return "", "", err
	}
	resp, err := networkHTTPClient.Get(b)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", errors.New(resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	masterString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(masterString), true)
	if err != nil || listType != m3u8.MASTER {
		return "", "", errors.New("m3u8 not of master type")
	}
	master := from.(*m3u8.MasterPlaylist)
	var streamUrl *url.URL
	sort.Slice(master.Variants, func(i, j int) bool {
		return master.Variants[i].AverageBandwidth > master.Variants[j].AverageBandwidth
	})
	if debug_mode && more_mode {
		fmt.Println("\nDebug: All Available Variants:")
		var data [][]string
		for _, variant := range master.Variants {
			data = append(data, []string{variant.Codecs, variant.Audio, fmt.Sprint(variant.Bandwidth)})
		}
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"Codec", "Audio", "Bandwidth"})
		table.SetAutoMergeCells(true)
		table.SetRowLine(true)
		table.AppendBulk(data)
		table.Render()

		var hasAAC, hasLossless, hasHiRes, hasAtmos, hasDolbyAudio bool
		var aacQuality, losslessQuality, hiResQuality, atmosQuality, dolbyAudioQuality string

		for _, variant := range master.Variants {
			if variant.Codecs == "mp4a.40.2" { // AAC
				hasAAC = true
				split := strings.Split(variant.Audio, "-")
				if len(split) >= 3 {
					bitrate, _ := strconv.Atoi(split[2])
					currentBitrate := 0
					if aacQuality != "" {
						current := strings.Split(aacQuality, " | ")[2]
						current = strings.Split(current, " ")[0]
						currentBitrate, _ = strconv.Atoi(current)
					}
					if bitrate > currentBitrate {
						aacQuality = fmt.Sprintf("AAC | 2 Channel | %d Kbps", bitrate)
					}
				}
			} else if variant.Codecs == "ec-3" && strings.Contains(variant.Audio, "atmos") { // Dolby Atmos
				hasAtmos = true
				split := strings.Split(variant.Audio, "-")
				if len(split) > 0 {
					bitrateStr := split[len(split)-1]
					if len(bitrateStr) == 4 && bitrateStr[0] == '2' {
						bitrateStr = bitrateStr[1:]
					}
					bitrate, _ := strconv.Atoi(bitrateStr)
					currentBitrate := 0
					if atmosQuality != "" {
						current := strings.Split(strings.Split(atmosQuality, " | ")[2], " ")[0]
						currentBitrate, _ = strconv.Atoi(current)
					}
					if bitrate > currentBitrate {
						atmosQuality = fmt.Sprintf("E-AC-3 | 16 Channel | %d Kbps", bitrate)
					}
				}
			} else if variant.Codecs == "alac" { // ALAC (Lossless or Hi-Res)
				split := strings.Split(variant.Audio, "-")
				if len(split) >= 3 {
					bitDepth := split[len(split)-1]
					sampleRate := split[len(split)-2]
					sampleRateInt, _ := strconv.Atoi(sampleRate)
					if sampleRateInt > 48000 { // Hi-Res
						hasHiRes = true
						hiResQuality = fmt.Sprintf("ALAC | 2 Channel | %s-bit/%d kHz", bitDepth, sampleRateInt/1000)
					} else { // Standard Lossless
						hasLossless = true
						losslessQuality = fmt.Sprintf("ALAC | 2 Channel | %s-bit/%d kHz", bitDepth, sampleRateInt/1000)
					}
				}
			} else if variant.Codecs == "ac-3" { // Dolby Audio
				hasDolbyAudio = true
				split := strings.Split(variant.Audio, "-")
				if len(split) > 0 {
					bitrate, _ := strconv.Atoi(split[len(split)-1])
					dolbyAudioQuality = fmt.Sprintf("AC-3 |  16 Channel | %d Kbps", bitrate)
				}
			}
		}

		fmt.Println("Available Audio Formats:")
		fmt.Println("------------------------")
		fmt.Printf("AAC             : %s\n", formatAvailability(hasAAC, aacQuality))
		fmt.Printf("Lossless        : %s\n", formatAvailability(hasLossless, losslessQuality))
		fmt.Printf("Hi-Res Lossless : %s\n", formatAvailability(hasHiRes, hiResQuality))
		fmt.Printf("Dolby Atmos     : %s\n", formatAvailability(hasAtmos, atmosQuality))
		fmt.Printf("Dolby Audio     : %s\n", formatAvailability(hasDolbyAudio, dolbyAudioQuality))
		fmt.Println("------------------------")

		return "", "", nil
	}
	var Quality string
	for _, variant := range master.Variants {
		if session.DlAtmos {
			if variant.Codecs == "ec-3" && strings.Contains(variant.Audio, "atmos") {
				if debug_mode && !more_mode {
					fmt.Printf("Debug: Found Dolby Atmos variant - %s (Bitrate: %d Kbps)\n",
						variant.Audio, variant.Bandwidth/1000)
				}
				split := strings.Split(variant.Audio, "-")
				length := len(split)
				length_int, err := strconv.Atoi(split[length-1])
				if err != nil {
					return "", "", err
				}
				if length_int <= cfg.AtmosMax {
					if !debug_mode && !more_mode {
						fmt.Printf("%s\n", variant.Audio)
					}
					streamUrlTemp, err := masterUrl.Parse(variant.URI)
					if err != nil {
						return "", "", err
					}
					streamUrl = streamUrlTemp
					Quality = fmt.Sprintf("%s Kbps", split[len(split)-1])
					break
				}
			} else if variant.Codecs == "ac-3" { // Add Dolby Audio support
				if debug_mode && !more_mode {
					fmt.Printf("Debug: Found Dolby Audio variant - %s (Bitrate: %d Kbps)\n",
						variant.Audio, variant.Bandwidth/1000)
				}
				streamUrlTemp, err := masterUrl.Parse(variant.URI)
				if err != nil {
					return "", "", err
				}
				streamUrl = streamUrlTemp
				split := strings.Split(variant.Audio, "-")
				Quality = fmt.Sprintf("%s Kbps", split[len(split)-1])
				break
			}
		} else if session.DlAac {
			if variant.Codecs == "mp4a.40.2" {
				if debug_mode && !more_mode {
					fmt.Printf("Debug: Found AAC variant - %s (Bitrate: %d)\n", variant.Audio, variant.Bandwidth)
				}
				aacregex := regexp.MustCompile(`audio-stereo-\d+`)
				replaced := aacregex.ReplaceAllString(variant.Audio, "aac")
				if replaced == cfg.AacType {
					if !debug_mode && !more_mode {
						fmt.Printf("%s\n", variant.Audio)
					}
					streamUrlTemp, err := masterUrl.Parse(variant.URI)
					if err != nil {
						return "", "", err
					}
					streamUrl = streamUrlTemp
					split := strings.Split(variant.Audio, "-")
					Quality = fmt.Sprintf("%s Kbps", split[2])
					break
				}
			}
		} else {
			if variant.Codecs == "alac" {
				split := strings.Split(variant.Audio, "-")
				length := len(split)
				length_int, err := strconv.Atoi(split[length-2])
				if err != nil {
					return "", "", err
				}
				if length_int <= cfg.AlacMax {
					if !debug_mode && !more_mode {
						fmt.Printf("%s-bit / %s Hz\n", split[length-1], split[length-2])
					}
					streamUrlTemp, err := masterUrl.Parse(variant.URI)
					if err != nil {
						return "", "", err
					}
					streamUrl = streamUrlTemp
					KHZ := float64(length_int) / 1000.0
					Quality = fmt.Sprintf("%sB-%.1fkHz", split[length-1], KHZ)
					break
				}
			}
		}
	}
	if streamUrl == nil {
		return "", "", errors.New("no codec found")
	}
	return streamUrl.String(), Quality, nil
}

func extractVideo(c string) (string, error) {
	return extractVideoWithConfig(c, Config)
}

func extractVideoWithConfig(c string, cfg structs.ConfigSet) (string, error) {
	return extractVideoWithPreference(c, cfg, videoAspectAny)
}

func extractVideoWithPreference(c string, cfg structs.ConfigSet, preference videoAspectPreference) (string, error) {
	MediaUrl, err := url.Parse(c)
	if err != nil {
		return "", err
	}

	resp, err := networkHTTPClient.Get(c)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", errors.New(resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	videoString := string(body)

	from, listType, err := m3u8.DecodeFrom(strings.NewReader(videoString), true)
	if err != nil || listType != m3u8.MASTER {
		return "", errors.New("m3u8 not of media type")
	}

	video := from.(*m3u8.MasterPlaylist)

	selectedVariant, dims, err := selectVideoVariant(video.Variants, cfg.MVMax, preference)
	if err != nil {
		return "", err
	}

	streamUrl, err := MediaUrl.Parse(selectedVariant.URI)
	if err != nil {
		return "", err
	}
	videoRange := strings.TrimSpace(selectedVariant.VideoRange)
	if videoRange != "" {
		fmt.Printf("Video: %dx%d-%s\n", dims.Width, dims.Height, videoRange)
	} else {
		fmt.Printf("Video: %dx%d\n", dims.Width, dims.Height)
	}

	return streamUrl.String(), nil
}

const (
	defaultSearchLimit                        = 8
	defaultQueueSize                          = 20
	defaultTaskWorkerLimit                    = 1
	maxTaskWorkerLimit                        = 4
	downloadWorkerPoolSize                    = 4
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
	defaultTelegramResourceCheck              = 30 * time.Second
	defaultTelegramMinFreeDiskMB              = 512
	defaultTelegramMinFreeTmpMB               = 256
	defaultTelegramSendGlobalInterval         = 150 * time.Millisecond
	defaultTelegramSendChatInterval           = 800 * time.Millisecond
	defaultTelegramLanguage                   = "zh"
	defaultTelegramErrorLogFile               = "telegram-error.log"
	defaultTelegramLoopRestartDelay           = 2 * time.Second
	defaultTelegramGetUpdatesErrorSleep       = 2 * time.Second
	defaultTelegramGetUpdatesConflictSleep    = 5 * time.Second
	defaultTelegramGetUpdatesRestartThreshold = 30
	defaultTelegramDailyRestartDeferCheck     = 30 * time.Second
	telegramAutoDeleteAfter                   = 2 * time.Minute
	minTelegramPollTimeout                    = 35 * time.Second
	telegramDialTimeout                       = 20 * time.Second
	telegramTLSHandshakeTimeout               = 30 * time.Second
	uploadNoProgressTimeout                   = 120 * time.Second
	uploadWatchdogInterval                    = 5 * time.Second
	uploadProgressBufferSize                  = 32 * 1024
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
	mediaTypeArtistSongs = "artist-songs"
	mediaTypeArtistLPs   = "artist-lp-albums"
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
	case mediaTypeSong, mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation, mediaTypeArtistSongs, mediaTypeArtistLPs:
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

	downloadCoreMu sync.Mutex

	cacheMu    sync.Mutex
	cacheFile  string
	cache      map[string]CachedAudio
	docCache   map[string]CachedDocument
	videoCache map[string]CachedVideo

	inflightMu        sync.Mutex
	inflightDownloads map[string]struct{}
	sendLimiter       *telegramSendLimiter

	requestStateMu sync.Mutex
	activeRequests map[string]telegramPersistedRequest
	requestSeq     atomic.Uint64

	stateFile string
	stateMu   sync.Mutex
	stateSave chan struct{}
	stateStop chan struct{}
	stateWG   sync.WaitGroup

	metricsStop chan struct{}
	metricsWG   sync.WaitGroup

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
	run          func(*TelegramBot) error
}

type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
	InlineQuery   *InlineQuery   `json:"inline_query,omitempty"`
}

type Message struct {
	MessageID      int      `json:"message_id"`
	From           *User    `json:"from,omitempty"`
	Chat           Chat     `json:"chat"`
	Text           string   `json:"text,omitempty"`
	ReplyToMessage *Message `json:"reply_to_message,omitempty"`
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

func runTelegramBot(appleToken string) {
	botToken := strings.TrimSpace(Config.TelegramBotToken)
	if botToken == "" {
		botToken = strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	}
	if botToken == "" {
		fmt.Println("telegram-bot-token is not set. Add it to config.yaml or TELEGRAM_BOT_TOKEN.")
		return
	}
	sharedstorage.ApplyTelegramStorageOverrides(&Config)
	configureTelegramTempDir(&Config)
	signalCtx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()

	for {
		bot := newTelegramBot(botToken, appleToken)
		setRuntimeErrorLogPath(bot.errorLogFile)
		fmt.Println("Telegram bot started. Waiting for updates...")
		fmt.Printf("Telegram API base: %s (proxy=%s, api timeout=%s, poll timeout=%s)\n", bot.apiBase, bot.proxyInfo, bot.client.Timeout, bot.pollClient.Timeout)
		fmt.Printf("Telegram runtime error log: %s\n", bot.errorLogFile)

		dailyRestartEnabled := telegramDailyRestartEnabled()
		var restartTimer *time.Timer
		var restartCh <-chan time.Time
		if dailyRestartEnabled {
			restartAt := nextLocalMidnight(time.Now())
			untilRestart := time.Until(restartAt)
			if untilRestart <= 0 {
				untilRestart = time.Second
			}
			restartTimer = time.NewTimer(untilRestart)
			restartCh = restartTimer.C
			fmt.Printf("Telegram daily auto-restart scheduled at %s\n", restartAt.Format(time.RFC3339))
		} else {
			fmt.Println("Telegram daily auto-restart disabled by config.")
		}

		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			bot.loopWithAutoRestart()
		}()

		reason := "loop-exit"
		dailyRestartDeferred := false
	waitLoop:
		for {
			select {
			case <-signalCtx.Done():
				reason = "signal"
				fmt.Println("Telegram bot shutdown requested.")
				break waitLoop
			case <-restartCh:
				load := bot.dailyRestartTaskLoad()
				if load.hasPendingWork() {
					dailyRestartDeferred = true
					fmt.Printf("Telegram daily auto-restart deferred for %s because tasks are still active (%s)\n", defaultTelegramDailyRestartDeferCheck, load)
					if restartTimer != nil {
						restartTimer.Reset(defaultTelegramDailyRestartDeferCheck)
						restartCh = restartTimer.C
					}
					continue
				}
				reason = "daily-restart"
				if dailyRestartDeferred {
					fmt.Println("Telegram daily auto-restart triggered after active tasks drained.")
				} else {
					fmt.Println("Telegram daily auto-restart triggered.")
				}
				break waitLoop
			case <-loopDone:
				if bot.isShuttingDown() {
					reason = "shutdown"
				} else {
					reason = "loop-exit"
					fmt.Println("Telegram loop stopped unexpectedly. Restarting bot runtime.")
				}
				break waitLoop
			}
		}
		if restartTimer != nil {
			if !restartTimer.Stop() {
				select {
				case <-restartTimer.C:
				default:
				}
			}
		}

		bot.requestShutdown()
		closeHTTPIdleConnections(bot.pollClient)
		closeHTTPIdleConnections(bot.client)
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			fmt.Println("Telegram bot shutdown timed out waiting for polling loop to stop.")
			appendRuntimeErrorLog("telegram bot shutdown timed out waiting for polling loop to stop")
		}

		bot.stopDownloadWorkers()
		if bot.cleanupTracker != nil {
			bot.cleanupTracker.stop()
		}
		if bot.resourceGuard != nil {
			bot.resourceGuard.stop()
		}
		bot.stopMetricsReporter()
		bot.stopStateSaver()
		bot.clearAllAutoDeleteMessages()

		switch reason {
		case "signal":
			return
		case "daily-restart":
			continue
		case "shutdown":
			appendRuntimeErrorLog("telegram bot loop stopped after shutdown request; restarting runtime")
		default:
			appendRuntimeErrorLog("telegram bot loop stopped unexpectedly; restarting runtime")
		}
		select {
		case <-signalCtx.Done():
			return
		case <-time.After(defaultTelegramLoopRestartDelay):
		}
	}
}

func configureTelegramTempDir(cfg *structs.ConfigSet) {
	if cfg == nil {
		return
	}

	explicitTmp := strings.TrimSpace(os.Getenv("AMDL_TMPDIR"))
	if explicitTmp == "" {
		explicitTmp = strings.TrimSpace(os.Getenv("TMPDIR"))
	}
	if explicitTmp != "" {
		clean := filepath.Clean(explicitTmp)
		if err := os.MkdirAll(clean, 0755); err != nil {
			fmt.Printf("Failed to prepare temp dir %s: %v\n", clean, err)
			return
		}
		_ = os.Setenv("AMDL_TMPDIR", clean)
		if strings.TrimSpace(os.Getenv("TMPDIR")) == "" {
			_ = os.Setenv("TMPDIR", clean)
		}
		fmt.Printf("Telegram temp dir: %s (from environment)\n", clean)
		return
	}

	base := resolveTelegramTempBaseDir(cfg)
	if base == "" {
		return
	}
	cleanBase := filepath.Clean(base)
	if !filepath.IsAbs(cleanBase) {
		if abs, err := filepath.Abs(cleanBase); err == nil {
			cleanBase = abs
		}
	}
	tmpDir := filepath.Join(cleanBase, ".tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		fmt.Printf("Failed to create telegram temp dir %s: %v\n", tmpDir, err)
		return
	}
	_ = os.Setenv("AMDL_TMPDIR", tmpDir)
	_ = os.Setenv("TMPDIR", tmpDir)
	fmt.Printf("Telegram temp dir: %s\n", tmpDir)
}

func resolveTelegramTempBaseDir(cfg *structs.ConfigSet) string {
	if cfg == nil {
		return ""
	}
	candidates := []string{
		strings.TrimSpace(cfg.TelegramDownloadFolder),
		strings.TrimSpace(cfg.AlacSaveFolder),
		strings.TrimSpace(cfg.AtmosSaveFolder),
		strings.TrimSpace(cfg.AacSaveFolder),
	}
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func panicErrorWithStack(scope string, rec any) error {
	label := strings.TrimSpace(scope)
	if label == "" {
		label = "runtime"
	}
	return fmt.Errorf("%s panic: %v\n%s", label, rec, string(debug.Stack()))
}

func logRecoveredPanic(scope string, rec any) error {
	err := panicErrorWithStack(scope, rec)
	fmt.Println(err.Error())
	appendRuntimeErrorLog(err.Error())
	return err
}

func runWithRecovery(scope string, onPanic func(error), fn func()) (panicked bool) {
	if fn == nil {
		return false
	}
	defer func() {
		if rec := recover(); rec != nil {
			panicked = true
			err := logRecoveredPanic(scope, rec)
			if onPanic != nil {
				onPanic(err)
			}
		}
	}()
	fn()
	return false
}

func (b *TelegramBot) loopWithAutoRestart() {
	for {
		if b.isShuttingDown() {
			return
		}
		panicked := runWithRecovery("telegram loop", nil, func() {
			b.loop()
		})
		if !panicked {
			return
		}
		if b.isShuttingDown() {
			return
		}
		fmt.Printf("telegram loop recovered from panic, restarting in %s\n", defaultTelegramLoopRestartDelay)
		appendRuntimeErrorLogf("telegram loop recovered from panic; restarting in %s", defaultTelegramLoopRestartDelay)
		closeHTTPIdleConnections(b.pollClient)
		closeHTTPIdleConnections(b.client)
		select {
		case <-b.shutdownContext().Done():
			return
		case <-time.After(defaultTelegramLoopRestartDelay):
		}
	}
}

func (b *TelegramBot) getUpdatesErrorSleep() time.Duration {
	if b != nil && b.getUpdatesErrorDelay > 0 {
		return b.getUpdatesErrorDelay
	}
	return defaultTelegramGetUpdatesErrorSleep
}

func (b *TelegramBot) getUpdatesConflictSleep() time.Duration {
	if b != nil && b.getUpdatesConflictDelay > 0 {
		return b.getUpdatesConflictDelay
	}
	return defaultTelegramGetUpdatesConflictSleep
}

func (b *TelegramBot) getUpdatesRestartThreshold() int {
	if b != nil && b.getUpdatesRestartAfter > 0 {
		return b.getUpdatesRestartAfter
	}
	return defaultTelegramGetUpdatesRestartThreshold
}

func normalizeTelegramAPIBase(raw string) string {
	base := strings.TrimSpace(raw)
	if base == "" {
		return "https://api.telegram.org"
	}
	return strings.TrimRight(base, "/")
}

func sanitizeTelegramError(err error, token string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, token, "<redacted-token>")
}

func warnInsecureTelegramAPIBase(apiBase string) {
	parsed, err := url.Parse(apiBase)
	if err != nil {
		fmt.Printf("Warning: invalid telegram-api-url %q. Falling back to runtime errors if unreachable.\n", apiBase)
		return
	}
	if !strings.EqualFold(parsed.Scheme, "http") {
		return
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "localhost" {
		fmt.Printf("Warning: telegram-api-url uses plain HTTP (%s). Use only trusted local environments.\n", apiBase)
		return
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		fmt.Printf("Warning: telegram-api-url uses plain HTTP (%s). Use only trusted local environments.\n", apiBase)
		return
	}
	fmt.Printf("Warning: telegram-api-url uses plain HTTP (%s). Telegram bot token may be exposed in transit.\n", apiBase)
}

func resolveTelegramProxy() (func(*http.Request) (*url.URL, error), string) {
	if Config.TelegramNoProxy {
		return nil, "disabled"
	}
	rawProxy := strings.TrimSpace(Config.TelegramProxyURL)
	if rawProxy == "" {
		return http.ProxyFromEnvironment, "env"
	}
	parsed, err := url.Parse(rawProxy)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		fmt.Printf("Warning: invalid telegram-proxy-url %q. Falling back to environment proxy.\n", rawProxy)
		return http.ProxyFromEnvironment, "env(fallback)"
	}
	return http.ProxyURL(parsed), parsed.Redacted()
}

func newTelegramHTTPClient(timeout time.Duration, proxyFunc func(*http.Request) (*url.URL, error)) *http.Client {
	transport := &http.Transport{
		Proxy: proxyFunc,
		DialContext: (&net.Dialer{
			Timeout:   telegramDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// Force HTTP/1.1 for better compatibility with some proxy/tunnel paths.
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   telegramTLSHandshakeTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func telegramDownloadMaxBytes() int64 {
	gb := Config.TelegramDownloadMaxGB
	if gb <= 0 {
		gb = defaultTelegramDownloadMaxGB
	}
	return int64(gb) * 1024 * 1024 * 1024
}

type telegramSendLimiter struct {
	globalInterval time.Duration
	chatInterval   time.Duration

	mu           sync.Mutex
	lastAll      time.Time
	lastChat     map[int64]time.Time
	blockedUntil time.Time
	nowFn        func() time.Time
}

func newTelegramSendLimiter(globalInterval time.Duration, chatInterval time.Duration) *telegramSendLimiter {
	if globalInterval < 0 {
		globalInterval = 0
	}
	if chatInterval < 0 {
		chatInterval = 0
	}
	if globalInterval == 0 && chatInterval == 0 {
		return nil
	}
	return &telegramSendLimiter{
		globalInterval: globalInterval,
		chatInterval:   chatInterval,
		lastChat:       make(map[int64]time.Time),
		nowFn:          time.Now,
	}
}

func (l *telegramSendLimiter) wait(ctx context.Context, chatID int64) error {
	if l == nil {
		return nil
	}
	for {
		wait := l.reserve(chatID)
		if wait <= 0 {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *telegramSendLimiter) reserve(chatID int64) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.nowFn()
	wait := l.nextWaitLocked(now, chatID)
	if wait <= 0 {
		if l.globalInterval > 0 {
			l.lastAll = now
		}
		if chatID != 0 && l.chatInterval > 0 {
			l.lastChat[chatID] = now
		}
	}
	return wait
}

func (l *telegramSendLimiter) tryReserve(chatID int64) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.nowFn()
	if l.nextWaitLocked(now, chatID) > 0 {
		return false
	}
	if l.globalInterval > 0 {
		l.lastAll = now
	}
	if chatID != 0 && l.chatInterval > 0 {
		l.lastChat[chatID] = now
	}
	return true
}

func (l *telegramSendLimiter) nextWait(chatID int64) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nextWaitLocked(l.nowFn(), chatID)
}

func (l *telegramSendLimiter) nextWaitLocked(now time.Time, chatID int64) time.Duration {
	wait := time.Duration(0)
	if !l.blockedUntil.IsZero() {
		if remain := l.blockedUntil.Sub(now); remain > wait {
			wait = remain
		}
	}
	if l.globalInterval > 0 && !l.lastAll.IsZero() {
		if remain := l.globalInterval - now.Sub(l.lastAll); remain > wait {
			wait = remain
		}
	}
	if chatID != 0 && l.chatInterval > 0 {
		if last, ok := l.lastChat[chatID]; ok && !last.IsZero() {
			if remain := l.chatInterval - now.Sub(last); remain > wait {
				wait = remain
			}
		}
	}
	return wait
}

func (l *telegramSendLimiter) blockFor(duration time.Duration) {
	if l == nil || duration <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	until := l.nowFn().Add(duration)
	if until.After(l.blockedUntil) {
		l.blockedUntil = until
	}
}

func parsePositiveIntEnv(name string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func telegramSendGlobalInterval() time.Duration {
	if ms, ok := parsePositiveIntEnv("AMDL_TELEGRAM_SEND_GLOBAL_INTERVAL_MS"); ok {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultTelegramSendGlobalInterval
}

func telegramSendChatInterval() time.Duration {
	if ms, ok := parsePositiveIntEnv("AMDL_TELEGRAM_SEND_CHAT_INTERVAL_MS"); ok {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultTelegramSendChatInterval
}

func buildIDSet(ids []int64) map[int64]bool {
	result := make(map[int64]bool)
	for _, id := range ids {
		if id == 0 {
			continue
		}
		result[id] = true
	}
	return result
}

func newTelegramBot(token, appleToken string) *TelegramBot {
	allowed := buildIDSet(Config.TelegramAllowedChatIDs)
	admins := buildIDSet(Config.TelegramAdminUserIDs)
	searchLimit := Config.TelegramSearchLimit
	if searchLimit <= 0 {
		searchLimit = defaultSearchLimit
	}
	maxFileBytes := int64(Config.TelegramMaxFileMB) * 1024 * 1024
	if maxFileBytes <= 0 {
		maxFileBytes = 50 * 1024 * 1024
	}
	cacheFile := strings.TrimSpace(Config.TelegramCacheFile)
	if cacheFile == "" {
		cacheFile = "telegram-cache.json"
	}
	stateFile := resolveTelegramStateFile(cacheFile)
	queueSize := defaultQueueSize
	if queueSize <= 0 {
		queueSize = 1
	}
	apiBase := normalizeTelegramAPIBase(Config.TelegramAPIURL)
	warnInsecureTelegramAPIBase(apiBase)
	apiTimeout := time.Duration(Config.TelegramHTTPTimeoutSec) * time.Second
	if apiTimeout <= 0 {
		apiTimeout = defaultTelegramHTTPTimeout
	}
	pollTimeout := time.Duration(Config.TelegramPollTimeoutSec) * time.Second
	if pollTimeout <= 0 {
		pollTimeout = defaultTelegramPollTimeout
	}
	if pollTimeout < minTelegramPollTimeout {
		pollTimeout = minTelegramPollTimeout
	}
	proxyFunc, proxyInfo := resolveTelegramProxy()
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	bot := &TelegramBot{
		token:                   token,
		apiBase:                 apiBase,
		proxyInfo:               proxyInfo,
		appleToken:              appleToken,
		client:                  newTelegramHTTPClient(apiTimeout, proxyFunc),
		pollClient:              newTelegramHTTPClient(pollTimeout, proxyFunc),
		allowedChats:            allowed,
		adminUsers:              admins,
		forwardChatID:           Config.TelegramForwardChatID,
		searchLimit:             searchLimit,
		maxFileBytes:            maxFileBytes,
		errorLogFile:            resolveTelegramErrorLogFile(cacheFile, stateFile),
		getUpdatesErrorDelay:    defaultTelegramGetUpdatesErrorSleep,
		getUpdatesConflictDelay: defaultTelegramGetUpdatesConflictSleep,
		getUpdatesRestartAfter:  defaultTelegramGetUpdatesRestartThreshold,
		userWhitelistEnabled:    Config.TelegramUserWhitelistEnabled,
		userWhitelist:           make(map[int64]bool),
		userBlacklist:           make(map[int64]bool),
		forwardEnabled:          Config.TelegramForwardEnabled,
		chatSettings:            make(map[int64]ChatDownloadSettings),
		pending:                 make(map[int64]map[int]*PendingSelection),
		pendingTransfers:        make(map[int64]map[int]*PendingTransfer),
		pendingMVStreams:        make(map[int64]map[int]*PendingMVStream),
		pendingArtistModes:      make(map[int64]map[int]*PendingArtistMode),
		downloadQueue:           make(chan *downloadRequest, queueSize),
		workerLimit:             defaultTaskWorkerLimit,
		cacheFile:               cacheFile,
		stateFile:               stateFile,
		cache:                   make(map[string]CachedAudio),
		docCache:                make(map[string]CachedDocument),
		videoCache:              make(map[string]CachedVideo),
		inflightDownloads:       make(map[string]struct{}),
		activeRequests:          make(map[string]telegramPersistedRequest),
		autoDeleteMessages:      make(map[string]*time.Timer),
		autoDeleteSticky:        make(map[string]bool),
		autoDeleteDeadline:      make(map[string]time.Time),
		songCommentCache:        make(map[string]songCommentCacheEntry),
		sendLimiter:             newTelegramSendLimiter(telegramSendGlobalInterval(), telegramSendChatInterval()),
		shutdownCtx:             shutdownCtx,
		shutdownCancel:          shutdownCancel,
	}
	bot.queueCond = sync.NewCond(&bot.queueMu)
	bot.loadCache()
	bot.startStateSaver()
	bot.startDownloadWorker()
	bot.restoreRuntimeState()
	bot.startMetricsReporter()
	bot.resourceGuard = newTelegramResourceGuard(telegramCleanupRootPaths())
	if bot.resourceGuard != nil {
		bot.resourceGuard.start()
	}
	bot.cleanupTracker = newTelegramCleanupTracker(
		telegramCleanupRoots(),
		cacheFile,
		telegramDownloadMaxBytes(),
		telegramCleanupInterval(),
		telegramCleanupScanInterval(),
		telegramCleanupProtectAge(),
	)
	bot.cleanupTracker.onDelete = func(path string, size int64) {
		appRuntimeMetrics.recordCleanupRemoval(size)
	}
	bot.cleanupTracker.start()
	bot.requestStateSave()
	return bot
}

func (b *TelegramBot) loop() {
	if err := b.dropPendingUpdatesOnStart(); err != nil {
		if b.isShuttingDown() {
			return
		}
		msg := sanitizeTelegramError(err, b.token)
		fmt.Println("startup drop-pending-updates failed:", msg)
		appendRuntimeErrorLogf("startup drop-pending-updates failed: %s", msg)
	}
	offset := b.consumePendingUpdatesOnStart()
	lastConflictHint := time.Time{}
	consecutiveErrors := 0
	for {
		if b.isShuttingDown() {
			return
		}
		updates, err := b.getUpdates(offset)
		if err != nil {
			if b.isShuttingDown() {
				return
			}
			consecutiveErrors++
			msg := sanitizeTelegramError(err, b.token)
			fmt.Println("getUpdates error:", msg)
			appendRuntimeErrorLogf("getUpdates error (consecutive=%d): %s", consecutiveErrors, msg)
			if threshold := b.getUpdatesRestartThreshold(); threshold > 0 && consecutiveErrors >= threshold {
				fmt.Printf("getUpdates hit %d consecutive errors, restarting bot runtime.\n", consecutiveErrors)
				appendRuntimeErrorLogf("getUpdates hit %d consecutive errors; restarting bot runtime", consecutiveErrors)
				return
			}
			backoff, reason := b.getUpdatesBackoff(err)
			if reason == "conflict" {
				if lastConflictHint.IsZero() || time.Since(lastConflictHint) > 30*time.Second {
					fmt.Println("Hint: 409 Conflict means another getUpdates consumer is active (another bot process) or webhook is set. Keep only one bot instance and clear webhook if needed.")
					lastConflictHint = time.Now()
				}
			} else if reason == "retry_after" {
				fmt.Printf("getUpdates rate limited, retry after %s\n", backoff.Round(time.Second))
				appendRuntimeErrorLogf("getUpdates rate limited; retry after %s", backoff.Round(time.Second))
			}
			if backoff <= 0 {
				backoff = b.getUpdatesErrorSleep()
			}
			if err := sleepWithContext(b.shutdownContext(), backoff); err != nil {
				return
			}
			continue
		}
		consecutiveErrors = 0
		for _, upd := range updates {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
			}
			b.dispatchUpdateSafely(upd)
		}
	}
}

func updateReplyContext(upd Update) (int64, int) {
	if upd.Message != nil {
		return upd.Message.Chat.ID, upd.Message.MessageID
	}
	if upd.CallbackQuery != nil && upd.CallbackQuery.Message != nil {
		return upd.CallbackQuery.Message.Chat.ID, upd.CallbackQuery.Message.MessageID
	}
	return 0, 0
}

func (b *TelegramBot) dispatchUpdate(upd Update) {
	if upd.Message != nil {
		b.handleMessage(upd.Message)
		return
	}
	if upd.CallbackQuery != nil {
		b.handleCallback(upd.CallbackQuery)
		return
	}
	if upd.InlineQuery != nil {
		b.handleInlineQuery(upd.InlineQuery)
	}
}

func (b *TelegramBot) dispatchUpdateSafely(upd Update) {
	scope := fmt.Sprintf("telegram update handler update_id=%d", upd.UpdateID)
	runWithRecovery(scope, func(_ error) {
		chatID, replyToID := updateReplyContext(upd)
		if chatID != 0 {
			_ = b.sendMessageWithReply(chatID, "任务异常已记录，已自动跳过并继续后续任务。", nil, replyToID)
		}
	}, func() {
		b.dispatchUpdate(upd)
	})
}

func (b *TelegramBot) dropPendingUpdatesOnStart() error {
	payload := map[string]any{
		"drop_pending_updates": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", b.apiURL("deleteWebhook"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("deleteWebhook failed: %s", strings.TrimSpace(string(responseBody)))
	}
	apiResp := apiResponse{}
	if err := json.Unmarshal(responseBody, &apiResp); err == nil && !apiResp.OK {
		return fmt.Errorf("deleteWebhook error: %s", apiResp.Description)
	}
	return nil
}

func (b *TelegramBot) consumePendingUpdatesOnStart() int {
	offset := 0
	skipped := 0
	for {
		updates, err := b.getUpdatesWithOptions(offset, 0, 100)
		if err != nil {
			msg := sanitizeTelegramError(err, b.token)
			fmt.Println("startup pending-update check failed:", msg)
			appendRuntimeErrorLogf("startup pending-update check failed: %s", msg)
			return offset
		}
		if len(updates) == 0 {
			break
		}
		for _, upd := range updates {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
				skipped++
			}
		}
		if len(updates) < 100 {
			break
		}
	}
	if skipped > 0 {
		fmt.Printf("Skipped %d pending updates on startup.\n", skipped)
	}
	return offset
}

func (b *TelegramBot) startDownloadWorker() {
	for i := 0; i < downloadWorkerPoolSize; i++ {
		b.workerWG.Add(1)
		go func() {
			defer b.workerWG.Done()
			for req := range b.downloadQueue {
				if req == nil {
					continue
				}
				func() {
					b.queueMu.Lock()
					for b.activeWorkers >= b.workerLimit {
						b.queueCond.Wait()
					}
					b.activeWorkers++
					b.queueMu.Unlock()

					taskType := ""
					if req != nil {
						taskType = normalizeTelegramTaskType(req.taskType)
					}
					panicked := false
					defer func() {
						if panicked {
							appRuntimeMetrics.recordTaskPanic(taskType)
							return
						}
						appRuntimeMetrics.recordTaskFinished(taskType)
					}()

					defer func() {
						if rec := recover(); rec != nil {
							panicked = true
							reqID := ""
							mediaType := ""
							mediaID := ""
							chatID := int64(0)
							replyToID := 0
							if req != nil {
								reqID = strings.TrimSpace(req.requestID)
								mediaType = req.mediaType
								mediaID = req.mediaID
								chatID = req.chatID
								replyToID = req.replyToID
							}
							scope := fmt.Sprintf("telegram download worker request=%s media=%s:%s", reqID, mediaType, mediaID)
							_ = logRecoveredPanic(scope, rec)
							if chatID != 0 {
								_ = b.sendMessageWithReply(chatID, "任务发生异常，已自动跳过并继续后续任务。", nil, replyToID)
							}
						}
					}()

					defer func() {
						if strings.TrimSpace(req.requestID) != "" {
							b.untrackRequest(req.requestID)
						}
						if strings.TrimSpace(req.inflightKey) != "" {
							b.releaseInflightDownload(req.inflightKey)
						}
						b.queueMu.Lock()
						if b.activeWorkers > 0 {
							b.activeWorkers--
						}
						b.queueCond.Broadcast()
						b.queueMu.Unlock()
					}()

					if strings.TrimSpace(req.requestID) != "" {
						b.markRequestRunning(req.requestID)
					}
					b.runQueuedRequest(req)
				}()
			}
		}()
	}
}

func (b *TelegramBot) stopDownloadWorkers() {
	if b == nil {
		return
	}
	b.queueCloseOnce.Do(func() {
		if b.downloadQueue != nil {
			close(b.downloadQueue)
		}
	})
	b.workerWG.Wait()
}

func normalizeTaskWorkerLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > maxTaskWorkerLimit {
		return maxTaskWorkerLimit
	}
	return limit
}

func parseTaskWorkerLimit(raw string) (int, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return 0, false
	}
	for _, prefix := range []string{"workers", "worker", "threads", "thread"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimPrefix(value, prefix)
			break
		}
	}
	value = strings.TrimSpace(strings.Trim(value, "=:_-x"))
	if value == "" {
		return 0, false
	}
	num, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return normalizeTaskWorkerLimit(num), true
}

func (b *TelegramBot) getTaskWorkerLimit() int {
	b.queueMu.Lock()
	defer b.queueMu.Unlock()
	return b.workerLimit
}

func (b *TelegramBot) setTaskWorkerLimit(limit int) int {
	normalized := normalizeTaskWorkerLimit(limit)
	b.queueMu.Lock()
	b.workerLimit = normalized
	if b.queueCond != nil {
		b.queueCond.Broadcast()
	}
	b.queueMu.Unlock()
	return normalized
}

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

func (b *TelegramBot) handleMessage(msg *Message) {
	if msg.Text == "" {
		return
	}
	text := strings.TrimSpace(msg.Text)
	if cmd, args, ok := parseCommand(text); ok {
		cmd = normalizeTelegramBotCommand(cmd)
		if !b.isAllowedChat(msg.Chat.ID) {
			// Allow querying chat_id even when not in allowlist.
			if cmd == "id" && len(args) == 0 {
				_ = b.sendMessage(msg.Chat.ID, b.formatChatIDTextForChat(msg.Chat.ID), nil)
			} else if cmd == "amwhoami" && len(args) == 0 {
				_ = b.sendMessage(msg.Chat.ID, formatWhoAmIText(messageUserID(msg), msg.Chat.ID), nil)
			} else {
				_ = b.sendMessage(msg.Chat.ID, b.localizeOutgoingText(msg.Chat.ID, "Not authorized for this bot."), nil)
			}
			return
		}
		if !b.isAllowedUser(messageUserID(msg)) {
			if cmd == "amwhoami" && len(args) == 0 {
				_ = b.sendMessage(msg.Chat.ID, formatWhoAmIText(messageUserID(msg), msg.Chat.ID), nil)
			} else {
				_ = b.sendMessage(msg.Chat.ID, b.localizeOutgoingText(msg.Chat.ID, "Not authorized for this bot."), nil)
			}
			return
		}
		b.handleCommandWithContext(msg.Chat.ID, msg.Chat.Type, messageUserID(msg), cmd, args, msg.MessageID, msg.ReplyToMessage)
		return
	}
	if !b.isAllowedChat(msg.Chat.ID) {
		_ = b.sendMessage(msg.Chat.ID, b.localizeOutgoingText(msg.Chat.ID, "Not authorized for this bot."), nil)
		return
	}
	if !b.isAllowedUser(messageUserID(msg)) {
		_ = b.sendMessage(msg.Chat.ID, b.localizeOutgoingText(msg.Chat.ID, "Not authorized for this bot."), nil)
		return
	}
	urlText := extractFirstAppleMusicURL(text)
	if urlText == "" {
		return
	}
	target, err := parseAppleMusicURL(urlText)
	if err != nil {
		_ = b.sendMessageWithReply(msg.Chat.ID, b.localizeOutgoingText(msg.Chat.ID, fmt.Sprintf("Unsupported Apple Music URL: %s", urlText)), nil, msg.MessageID)
		return
	}
	b.handleURLTarget(msg.Chat.ID, msg.MessageID, target)
}

func (b *TelegramBot) handleCallback(cb *CallbackQuery) {
	if cb == nil || cb.Message == nil {
		return
	}
	if !b.isAllowedChat(cb.Message.Chat.ID) || !b.isAllowedUser(callbackUserID(cb)) {
		_ = b.answerCallbackQuery(cb.ID)
		return
	}
	b.markMessageInteraction(cb.Message.Chat.ID, cb.Message.MessageID)
	data := strings.TrimSpace(cb.Data)
	if strings.HasPrefix(data, "admin:") {
		b.handleAdminCallback(cb)
		_ = b.answerCallbackQuery(cb.ID)
		return
	}
	if data == "panel_cancel" || data == "setting_exit" || data == "setting_close" {
		b.cancelPanelAndDelete(cb.Message.Chat.ID, cb.Message.MessageID)
		_ = b.answerCallbackQuery(cb.ID)
		return
	}
	if strings.HasPrefix(data, "sel:") {
		numStr := strings.TrimPrefix(data, "sel:")
		if n, err := strconv.Atoi(numStr); err == nil {
			b.handleSelection(cb.Message.Chat.ID, cb.Message.MessageID, n)
		}
	} else if strings.HasPrefix(data, "mvstream:") {
		numStr := strings.TrimPrefix(data, "mvstream:")
		if n, err := strconv.Atoi(numStr); err == nil {
			b.handleMusicVideoStreamSelection(cb.Message.Chat.ID, cb.Message.MessageID, n)
		}
	} else if strings.HasPrefix(data, "setting_format:") {
		format := strings.TrimPrefix(data, "setting_format:")
		settings := b.setChatFormat(cb.Message.Chat.ID, format)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_aac:") {
		aacType := strings.TrimPrefix(data, "setting_aac:")
		settings := b.setChatAACType(cb.Message.Chat.ID, aacType)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_mv_audio:") {
		mvAudioType := strings.TrimPrefix(data, "setting_mv_audio:")
		settings := b.setChatMVAudioType(cb.Message.Chat.ID, mvAudioType)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_lyrics_format:") {
		lyricsFormat := strings.TrimPrefix(data, "setting_lyrics_format:")
		settings := b.setChatLyricsFormat(cb.Message.Chat.ID, lyricsFormat)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_embed:lyrics" {
		settings := b.toggleChatEmbedLyrics(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_embed:cover" {
		settings := b.toggleChatEmbedCover(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_lang:") {
		language := strings.TrimPrefix(data, "setting_lang:")
		settings := b.setChatLanguage(cb.Message.Chat.ID, language)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_auto:lyrics" {
		settings := b.toggleChatAutoLyrics(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_auto:cover" {
		settings := b.toggleChatAutoCover(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_auto:animated" {
		settings := b.toggleChatAutoAnimated(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if data == "setting_auto:comment" {
		settings := b.toggleChatSongComment(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
		b.maybeNotifySongCommentConfig(cb.Message.Chat.ID, cb.Message.MessageID, settings)
	} else if data == "setting_song_zip" {
		settings := b.toggleChatSongZip(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting_worker:") {
		workerRaw := strings.TrimPrefix(data, "setting_worker:")
		if limit, ok := parseTaskWorkerLimit(workerRaw); ok {
			_ = b.setTaskWorkerLimit(limit)
		}
		settings := b.getChatSettings(cb.Message.Chat.ID)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "setting:") {
		// Backward compatibility for old callbacks.
		format := strings.TrimPrefix(data, "setting:")
		settings := b.setChatFormat(cb.Message.Chat.ID, format)
		_ = b.editMessageText(cb.Message.Chat.ID, cb.Message.MessageID, b.formatSettingsText(settings), b.buildSettingsKeyboard(settings))
	} else if strings.HasPrefix(data, "transfer:") {
		mode := strings.TrimPrefix(data, "transfer:")
		b.handleMediaTransfer(cb.Message.Chat.ID, cb.Message.MessageID, mode)
	} else if strings.HasPrefix(data, "album_transfer:") {
		// Backward compatibility for old callbacks.
		mode := strings.TrimPrefix(data, "album_transfer:")
		b.handleMediaTransfer(cb.Message.Chat.ID, cb.Message.MessageID, mode)
	} else if strings.HasPrefix(data, "artist_rel:") {
		relationship := strings.TrimPrefix(data, "artist_rel:")
		b.handleArtistModeSelection(cb.Message.Chat.ID, cb.Message.MessageID, relationship)
	} else if strings.HasPrefix(data, "page:") {
		deltaStr := strings.TrimPrefix(data, "page:")
		if delta, err := strconv.Atoi(deltaStr); err == nil {
			b.handlePage(cb.Message.Chat.ID, cb.Message.MessageID, delta)
		}
	}
	_ = b.answerCallbackQuery(cb.ID)
}

func (b *TelegramBot) handleAdminCallback(cb *CallbackQuery) {
	if b == nil || cb == nil || cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	messageID := cb.Message.MessageID
	userID := callbackUserID(cb)
	if !b.isAdminUser(userID) {
		_ = b.sendMessageWithReply(chatID, "只有管理员可以操作管理员面板。", nil, messageID)
		return
	}
	data := strings.TrimSpace(cb.Data)
	switch data {
	case "admin:close":
		_ = b.deleteMessage(chatID, messageID)
	case "admin:refresh":
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false))
	case "admin:whitelist:toggle":
		b.setUserWhitelistEnabled(!b.isUserWhitelistEnabled())
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false))
	case "admin:forward:toggle":
		if b.forwardChatID == 0 && !b.isForwardEnabled() {
			_ = b.sendMessageWithReply(chatID, "未配置归档群 chat_id，无法开启归档转发。", nil, messageID)
			return
		}
		b.setForwardEnabled(!b.isForwardEnabled())
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false))
	case "admin:cachepush:confirm":
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(true))
	case "admin:cachepush:run":
		_ = b.editMessageText(chatID, messageID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false))
		b.triggerAdminCachePush(chatID, messageID)
	}
}

func (b *TelegramBot) triggerAdminCachePush(chatID int64, replyToID int) {
	if b == nil {
		return
	}
	targetChatID := b.forwardChatID
	if targetChatID == 0 {
		_ = b.sendMessageWithReply(chatID, "未配置归档群 chat_id，无法执行缓存转存。", nil, replyToID)
		return
	}
	items := b.listCachedAudioItems()
	if len(items) == 0 {
		_ = b.sendMessageWithReply(chatID, "当前没有可转存的缓存音频。", nil, replyToID)
		return
	}
	if !b.beginCachePush() {
		_ = b.sendMessageWithReply(chatID, "缓存转存任务已经在运行中，请稍后再试。", nil, replyToID)
		return
	}
	total := len(items)
	_ = b.sendMessageWithReply(chatID, fmt.Sprintf("已开始缓存转存，共 %d 条音频，目标群：%d", total, targetChatID), nil, replyToID)
	go func() {
		runWithRecovery("telegram admin cache push", nil, func() {
			defer b.endCachePush()
			success := 0
			failed := 0
			retriedSuccess := 0
			rateLimitedItems := make([]cachedAudioItem, 0)
			for idx, item := range items {
				if err := b.sendAudioByFileIDWithoutSongComment(targetChatID, item.Entry, 0, item.TrackID); err != nil {
					failed++
					if isTelegramRateLimitError(err) {
						rateLimitedItems = append(rateLimitedItems, item)
					}
					fmt.Printf("admin cache push failed idx=%d track=%s err=%v\n", idx+1, item.TrackID, err)
					appendRuntimeErrorLogf("admin cache push failed idx=%d track=%s err=%v", idx+1, item.TrackID, err)
				} else {
					success++
				}
				if idx+1 < total && (idx+1)%25 == 0 {
					_ = b.sendMessageWithReply(chatID, fmt.Sprintf("缓存转存进度：%d/%d（成功 %d，失败 %d）", idx+1, total, success, failed), nil, 0)
				}
			}
			const retryRounds = 3
			pendingRateLimited := rateLimitedItems
			for round := 1; round <= retryRounds && len(pendingRateLimited) > 0; round++ {
				_ = b.sendMessageWithReply(chatID, fmt.Sprintf("检测到 %d 条触发 429，开始第 %d/%d 轮重试。", len(pendingRateLimited), round, retryRounds), nil, 0)
				nextRound := make([]cachedAudioItem, 0, len(pendingRateLimited))
				for idx, item := range pendingRateLimited {
					if err := b.sendAudioByFileIDWithoutSongComment(targetChatID, item.Entry, 0, item.TrackID); err != nil {
						fmt.Printf("admin cache push retry failed round=%d idx=%d track=%s err=%v\n", round, idx+1, item.TrackID, err)
						appendRuntimeErrorLogf("admin cache push retry failed round=%d idx=%d track=%s err=%v", round, idx+1, item.TrackID, err)
						if isTelegramRateLimitError(err) {
							nextRound = append(nextRound, item)
						}
						continue
					}
					retriedSuccess++
					success++
					failed--
				}
				pendingRateLimited = nextRound
			}
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("缓存转存完成：成功 %d，最终失败 %d，429重试补回 %d，目标群：%d", success, failed, retriedSuccess, targetChatID), nil, 0)
		})
	}()
}

func (b *TelegramBot) cancelPanelAndDelete(chatID int64, messageID int) {
	b.clearPanelStateByMessage(chatID, messageID)
	if err := b.deleteMessage(chatID, messageID); err != nil {
		_ = b.editMessageText(chatID, messageID, b.localizeOutgoingText(chatID, "已取消。"), nil)
	}
}

func (b *TelegramBot) clearPanelStateByMessage(chatID int64, messageID int) {
	b.clearPendingByMessage(chatID, messageID)
	b.clearPendingTransferByMessage(chatID, messageID)
	b.clearPendingMVStreamByMessage(chatID, messageID)
	b.clearPendingArtistModeByMessage(chatID, messageID)
}

func (b *TelegramBot) handleInlineQuery(q *InlineQuery) {
	if q == nil || q.ID == "" {
		return
	}
	if !b.isAllowedUser(inlineQueryUserID(q)) {
		_ = b.answerInlineQuery(q.ID, []any{}, true)
		return
	}
	query := strings.TrimSpace(q.Query)
	if query == "" {
		_ = b.answerInlineQuery(q.ID, []any{}, true)
		return
	}
	trackID := extractInlineTrackID(query)
	if trackID == "" {
		_ = b.answerInlineQuery(q.ID, []any{}, true)
		return
	}
	entry, ok := b.getCachedAudio(trackID, b.maxFileBytes, "")
	results := []any{}
	if ok {
		entry = b.enrichCachedAudio(trackID, entry)
		format := normalizeTelegramFormat(entry.Format)
		if format == "" {
			format = defaultTelegramFormat
		}
		results = append(results, InlineQueryResultCachedAudio{
			Type:        "audio",
			ID:          fmt.Sprintf("song_%s", trackID),
			AudioFileID: entry.FileID,
			Caption:     formatTelegramCaption(entry.SizeBytes, entry.BitrateKbps, format),
		})
	} else {
		meta, err := b.fetchTrackMeta(trackID)
		title := "Send /songid " + trackID
		description := ""
		if err == nil {
			if meta.Title != "" && meta.Performer != "" {
				title = meta.Performer + " - " + meta.Title
				description = "Send /songid " + trackID
			} else if meta.Title != "" {
				title = meta.Title
				description = "Send /songid " + trackID
			}
		}
		results = append(results, InlineQueryResultArticle{
			Type:        "article",
			ID:          fmt.Sprintf("songcmd_%s", trackID),
			Title:       title,
			Description: description,
			InputMessageContent: InputMessageContent{
				MessageText: "/songid " + trackID,
			},
		})
	}
	_ = b.answerInlineQuery(q.ID, results, true)
}

func (b *TelegramBot) handleCommand(chatID int64, chatType string, cmd string, args []string, replyToID int) {
	b.handleCommandWithContext(chatID, chatType, 0, cmd, args, replyToID, nil)
}

func (b *TelegramBot) handleCommandWithContext(chatID int64, chatType string, userID int64, cmd string, args []string, replyToID int, replyTo *Message) {
	cmd = normalizeTelegramBotCommand(cmd)
	switch cmd {
	case "start", "help":
		_ = b.sendMessage(chatID, b.botHelpTextForChat(chatID), nil)
	case "amwhoami":
		_ = b.sendMessageWithReply(chatID, formatWhoAmIText(userID, chatID), nil, replyToID)
	case "search_song":
		b.handleSearch(chatID, "song", strings.Join(args, " "), replyToID)
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
			_ = b.sendMessageWithReply(chatID, "Usage: /url <apple-music-url>", nil, replyToID)
			return
		}
		raw := extractFirstAppleMusicURL(strings.Join(args, " "))
		if raw == "" {
			raw = args[0]
		}
		target, err := parseAppleMusicURL(raw)
		if err != nil {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Unsupported Apple Music URL: %s", raw), nil, replyToID)
			return
		}
		b.handleURLTarget(chatID, replyToID, target)
	case "refresh":
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
			if target, err := parseAppleMusicURL(args[0]); err == nil {
				b.handleURLTarget(chatID, replyToID, target)
				return
			}
			b.queueDownloadSong(chatID, args[0])
			return
		}
		kind := strings.ToLower(args[0])
		switch kind {
		case "song":
			b.queueDownloadSong(chatID, args[1])
		case "album":
			b.queueDownloadAlbum(chatID, args[1])
		case "playlist":
			b.queueDownloadPlaylist(chatID, args[1])
		case "station":
			b.queueDownloadStation(chatID, args[1])
		case "mv", "music-video", "musicvideo":
			b.queueDownloadMusicVideo(chatID, args[1])
		case "artist":
			b.startArtistSelection(chatID, args[1], "", Config.Storefront, replyToID)
		default:
			_ = b.sendMessage(chatID, "Usage: /id <song|album|playlist|station|mv|artist> <id>", nil)
		}
	case "songid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /songid <id>", nil)
			return
		}
		b.queueDownloadSong(chatID, args[0])
	case "albumid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /albumid <id>", nil)
			return
		}
		b.queueDownloadAlbum(chatID, args[0])
	case "playlistid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /playlistid <id>", nil)
			return
		}
		b.queueDownloadPlaylist(chatID, args[0])
	case "stationid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /stationid <id>", nil)
			return
		}
		b.queueDownloadStation(chatID, args[0])
	case "mvid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /mvid <id>", nil)
			return
		}
		b.queueDownloadMusicVideo(chatID, args[0])
	case "artistid":
		if len(args) == 0 {
			_ = b.sendMessage(chatID, "Usage: /artistid <id>", nil)
			return
		}
		b.startArtistSelection(chatID, args[0], "", Config.Storefront, replyToID)
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

func normalizeTelegramBotCommand(cmd string) string {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "h":
		return "help"
	case "i":
		return "id"
	case "sg":
		return "search_song"
	case "sa":
		return "search_album"
	case "sr":
		return "search_artist"
	case "s":
		return "search"
	case "u":
		return "url"
	case "ap":
		return "artistphoto"
	case "cv":
		return "cover"
	case "ac":
		return "animatedcover"
	case "ly":
		return "lyrics"
	case "st":
		return "settings"
	case "rf":
		return "refresh"
	default:
		return cmd
	}
}

func (b *TelegramBot) handleURLTarget(chatID int64, replyToID int, target *AppleURLTarget) {
	b.handleURLTargetWithOptions(chatID, replyToID, target, false)
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
	default:
		_ = b.sendMessageWithReply(chatID, "Unsupported Apple Music URL type.", nil, replyToID)
	}
}

type artworkFetchResult struct {
	DisplayName string
	CoverURL    string
	MotionURL   string
}

type artistProfileResponse struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Name    string `json:"name"`
			Artwork struct {
				URL string `json:"url"`
			} `json:"artwork"`
		} `json:"attributes"`
	} `json:"data"`
}

func normalizeCommandMediaType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case mediaTypeSong, "songs":
		return mediaTypeSong
	case mediaTypeAlbum, "albums":
		return mediaTypeAlbum
	case mediaTypePlaylist, "playlists":
		return mediaTypePlaylist
	case mediaTypeStation, "stations":
		return mediaTypeStation
	case "mv", "mvs", "video", "videos", mediaTypeMusicVideo, "musicvideo", "musicvideos":
		return mediaTypeMusicVideo
	case mediaTypeArtist, "artists":
		return mediaTypeArtist
	default:
		return ""
	}
}

func normalizeMediaIdentifier(mediaType, rawID string) (string, error) {
	normalizedType := normalizeCommandMediaType(mediaType)
	if normalizedType == "" {
		normalizedType = strings.TrimSpace(mediaType)
	}
	id := strings.TrimSpace(rawID)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	if len(id) > maxMediaIDLength {
		return "", fmt.Errorf("invalid id")
	}
	if strings.ContainsAny(id, "/\\\r\n\t\x00") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid id")
	}
	switch normalizedType {
	case mediaTypeSong, mediaTypeAlbum, mediaTypeArtist, mediaTypeMusicVideo:
		if !mediaIDNumericRe.MatchString(id) {
			return "", fmt.Errorf("invalid id")
		}
	case mediaTypePlaylist:
		if !mediaIDPlaylistRe.MatchString(id) {
			return "", fmt.Errorf("invalid id")
		}
	case mediaTypeStation:
		if !mediaIDStationRe.MatchString(id) {
			return "", fmt.Errorf("invalid id")
		}
	default:
		if !mediaIDGenericRe.MatchString(id) {
			return "", fmt.Errorf("invalid id")
		}
	}
	return id, nil
}

func normalizeAppleURLTarget(target *AppleURLTarget) (*AppleURLTarget, error) {
	if target == nil {
		return nil, fmt.Errorf("invalid target")
	}
	mediaType := normalizeCommandMediaType(target.MediaType)
	if mediaType == "" {
		return nil, fmt.Errorf("invalid media_type")
	}
	mediaID, err := normalizeMediaIdentifier(mediaType, target.ID)
	if err != nil {
		return nil, err
	}
	return &AppleURLTarget{
		MediaType:  mediaType,
		Storefront: resolveStorefront(target),
		ID:         mediaID,
		RawURL:     strings.TrimSpace(target.RawURL),
	}, nil
}

func joinFileWithinRoot(root string, fileName string) (string, error) {
	cleanRoot := filepath.Clean(strings.TrimSpace(root))
	if cleanRoot == "" {
		return "", fmt.Errorf("invalid root")
	}
	candidate := strings.TrimSpace(fileName)
	if candidate == "" {
		return "", fmt.Errorf("invalid filename")
	}
	if filepath.IsAbs(candidate) {
		return "", fmt.Errorf("invalid filename")
	}
	cleanName := filepath.Clean(candidate)
	if cleanName == "." || cleanName == ".." {
		return "", fmt.Errorf("invalid filename")
	}
	if strings.Contains(cleanName, "/") || strings.Contains(cleanName, `\`) {
		return "", fmt.Errorf("invalid filename")
	}
	target := filepath.Join(cleanRoot, cleanName)
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return target, nil
}

func resolveCommandTarget(args []string, defaultType string) (*AppleURLTarget, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("empty args")
	}
	joined := strings.TrimSpace(strings.Join(args, " "))
	if urlText := extractFirstAppleMusicURL(joined); urlText != "" {
		target, err := parseAppleMusicURL(urlText)
		if err != nil {
			return nil, err
		}
		return normalizeAppleURLTarget(target)
	}
	first := strings.TrimSpace(args[0])
	lowerFirst := strings.ToLower(first)
	if strings.HasPrefix(lowerFirst, "http://") || strings.HasPrefix(lowerFirst, "https://") {
		target, err := parseAppleMusicURL(first)
		if err != nil {
			return nil, err
		}
		return normalizeAppleURLTarget(target)
	}
	if len(args) >= 2 {
		mediaType := normalizeCommandMediaType(args[0])
		mediaID := strings.TrimSpace(args[1])
		if mediaType != "" && mediaID != "" {
			normalizedID, err := normalizeMediaIdentifier(mediaType, mediaID)
			if err != nil {
				return nil, err
			}
			return &AppleURLTarget{
				MediaType:  mediaType,
				Storefront: Config.Storefront,
				ID:         normalizedID,
			}, nil
		}
	}
	defaultType = normalizeCommandMediaType(defaultType)
	if defaultType != "" {
		mediaID := strings.TrimSpace(args[0])
		if mediaID == "" {
			return nil, fmt.Errorf("empty media id")
		}
		normalizedID, err := normalizeMediaIdentifier(defaultType, mediaID)
		if err != nil {
			return nil, err
		}
		return &AppleURLTarget{
			MediaType:  defaultType,
			Storefront: Config.Storefront,
			ID:         normalizedID,
		}, nil
	}
	return nil, fmt.Errorf("unable to resolve target")
}

func resolveRefreshURLTarget(args []string) (*AppleURLTarget, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("empty args")
	}
	trimmedArgs := args
	if len(trimmedArgs) > 0 {
		switch strings.ToLower(strings.TrimSpace(trimmedArgs[0])) {
		case "url", "ulr":
			trimmedArgs = trimmedArgs[1:]
		}
	}
	if len(trimmedArgs) == 0 {
		return nil, fmt.Errorf("missing url")
	}
	joined := strings.TrimSpace(strings.Join(trimmedArgs, " "))
	raw := extractFirstAppleMusicURL(joined)
	if raw == "" {
		raw = strings.TrimSpace(trimmedArgs[0])
	}
	return parseAppleMusicURL(raw)
}

func normalizeLyricsOutputFormat(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "lrc":
		return "lrc"
	case "ttml":
		return "ttml"
	default:
		return ""
	}
}

func sanitizeFileBaseName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "apple-music"
	}
	name = forbiddenNames.ReplaceAllString(name, "_")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.Join(strings.Fields(name), " ")
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "apple-music"
	}
	return name
}

func resolveStorefront(target *AppleURLTarget) string {
	storefront := strings.TrimSpace(Config.Storefront)
	if storefront != "" {
		return storefront
	}
	if target != nil && strings.TrimSpace(target.Storefront) != "" {
		return strings.TrimSpace(target.Storefront)
	}
	return "us"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func composeArtistTitle(artistName, title string) string {
	artistName = strings.TrimSpace(artistName)
	title = strings.TrimSpace(title)
	if artistName != "" && title != "" {
		return artistName + " - " + title
	}
	if title != "" {
		return title
	}
	return artistName
}

func firstSongData(op string, resp *ampapi.SongResp) (*ampapi.SongRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "song.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "song.data", resp.Data)
}

func firstAlbumData(op string, resp *ampapi.AlbumResp) (*ampapi.AlbumRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "album.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "album.data", resp.Data)
}

func firstPlaylistData(op string, resp *ampapi.PlaylistResp) (*ampapi.PlaylistRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "playlist.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "playlist.data", resp.Data)
}

func firstStationData(op string, resp *ampapi.StationResp) (*ampapi.StationRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "station.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "station.data", resp.Data)
}

func firstMusicVideoData(op string, resp *ampapi.MusicVideoResp) (*ampapi.MusicVideoRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "music_video.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "music_video.data", resp.Data)
}

func firstSongAlbumID(op string, item *ampapi.SongRespData) (string, error) {
	if item == nil {
		return "", &safe.AccessError{Op: op, Path: "song.data[0]", Reason: "nil track item"}
	}
	albumRef, err := safe.FirstRef(op, "song.data[0].relationships.albums.data", item.Relationships.Albums.Data)
	if err != nil {
		return "", err
	}
	albumID := strings.TrimSpace(albumRef.ID)
	if albumID == "" {
		return "", &safe.AccessError{Op: op, Path: "song.data[0].relationships.albums.data[0].id", Reason: "empty album id"}
	}
	return albumID, nil
}

func firstGenreName(op string, genres []string) (string, error) {
	return safe.FirstString(op, "attributes.genreNames", genres)
}

func runExternalCommand(ctx context.Context, name string, args ...string) (cmdrunner.Result, error) {
	result, err := cmdrunner.RunWithOptions(ctx, name, args, cmdrunner.RunOptions{})
	if cmdrunner.IsTimeout(err) {
		appRuntimeMetrics.recordExternalCommandTimeout()
	}
	return result, err
}

func runExternalCommandInDir(ctx context.Context, dir string, name string, args ...string) (cmdrunner.Result, error) {
	result, err := cmdrunner.RunWithOptions(ctx, name, args, cmdrunner.RunOptions{Dir: dir})
	if cmdrunner.IsTimeout(err) {
		appRuntimeMetrics.recordExternalCommandTimeout()
	}
	return result, err
}

func runMP4BoxWithTags(ctx context.Context, tags []string, args ...string) (cmdrunner.Result, error) {
	if len(tags) == 0 {
		return runExternalCommand(ctx, "MP4Box", args...)
	}
	tagFile, err := os.CreateTemp("", "mp4box-itags-*.txt")
	if err != nil {
		return cmdrunner.Result{}, err
	}
	tagPath := tagFile.Name()
	defer os.Remove(tagPath)
	if _, err := tagFile.WriteString(strings.Join(tags, "\n") + "\n"); err != nil {
		_ = tagFile.Close()
		return cmdrunner.Result{}, err
	}
	if err := tagFile.Close(); err != nil {
		return cmdrunner.Result{}, err
	}
	mp4Args := append([]string{"-itags", tagPath}, args...)
	return runExternalCommand(ctx, "MP4Box", mp4Args...)
}

func (b *TelegramBot) catalogService() *sharedcatalog.Service {
	return &sharedcatalog.Service{
		AppleToken:     b.appleToken,
		MediaUserToken: Config.MediaUserToken,
		Language:       Config.Language,
		HTTPClient:     b.client,
		UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		OpPrefix:       "main.telegram",
	}
}

func (b *TelegramBot) fetchArtistProfile(storefront string, artistID string) (string, string, error) {
	return b.catalogService().FetchArtistProfile(storefront, artistID)
}

func collectUniqueArtistSongIDs(items []sharedcatalog.ArtistRelationshipItem) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		trackID := strings.TrimSpace(item.ID)
		if trackID == "" {
			continue
		}
		if _, ok := seen[trackID]; ok {
			continue
		}
		seen[trackID] = struct{}{}
		ids = append(ids, trackID)
	}
	return ids
}

func collectUniqueArtistCollectionItems(items []sharedcatalog.ArtistRelationshipItem) []sharedcatalog.ArtistRelationshipItem {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	filtered := make([]sharedcatalog.ArtistRelationshipItem, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, item)
	}
	return filtered
}

func (b *TelegramBot) fetchArtistSongIDs(storefront string, artistID string) ([]string, error) {
	artistID = strings.TrimSpace(artistID)
	if artistID == "" {
		return nil, fmt.Errorf("artist id is empty")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	related, err := b.catalogService().FetchArtistRelationshipAll(storefront, artistID, "songs")
	if err != nil {
		return nil, err
	}
	return collectUniqueArtistSongIDs(related), nil
}

func (b *TelegramBot) fetchArtistLPAlbums(storefront string, artistID string) ([]sharedcatalog.ArtistRelationshipItem, error) {
	artistID = strings.TrimSpace(artistID)
	if artistID == "" {
		return nil, fmt.Errorf("artist id is empty")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	service := b.catalogService()
	service.Language = b.searchLanguage()
	related, err := service.FetchArtistViewAll(storefront, artistID, "full-albums")
	if err != nil {
		return nil, err
	}
	return collectUniqueArtistCollectionItems(related), nil
}

func buildArtistSongTrackStubs(trackIDs []string) []task.Track {
	if len(trackIDs) == 0 {
		return nil
	}
	total := len(trackIDs)
	tracks := make([]task.Track, 0, total)
	for idx, trackID := range trackIDs {
		tracks = append(tracks, task.Track{
			ID:        trackID,
			TaskNum:   idx + 1,
			TaskTotal: total,
		})
	}
	return tracks
}

func (b *TelegramBot) stageArtistLPAlbumsCollection(session *DownloadSession, artistID string, storefront string) error {
	if session == nil {
		return fmt.Errorf("download session is nil")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	albums, err := b.fetchArtistLPAlbums(storefront, artistID)
	if err != nil {
		return err
	}
	for _, album := range albums {
		if err := session.downloadContext().Err(); err != nil {
			return err
		}
		albumID := strings.TrimSpace(album.ID)
		if albumID == "" {
			continue
		}
		if err := ripAlbum(session, albumID, b.appleToken, storefront, session.Config.MediaUserToken, ""); err != nil {
			if ctxErr := session.downloadContext().Err(); ctxErr != nil {
				return ctxErr
			}
			fmt.Printf("Failed to rip artist LP album %s: %v\n", albumID, err)
			session.Counter.Error++
		}
	}
	return nil
}

func (b *TelegramBot) downloadArtistSongsCollection(session *DownloadSession, artistID string, storefront string) error {
	if session == nil {
		return fmt.Errorf("download session is nil")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	trackIDs, err := b.fetchArtistSongIDs(storefront, artistID)
	if err != nil {
		return err
	}
	for _, trackID := range trackIDs {
		if err := session.downloadContext().Err(); err != nil {
			return err
		}
		if err := ripSong(session, trackID, b.appleToken, storefront, session.Config.MediaUserToken); err != nil {
			if ctxErr := session.downloadContext().Err(); ctxErr != nil {
				return ctxErr
			}
			fmt.Printf("Failed to rip artist song %s: %v\n", trackID, err)
			session.Counter.Error++
		}
	}
	return nil
}

type artistLPDownloadResult struct {
	Total     int
	Succeeded int
	Failed    int
	SentAny   bool
}

func formatArtistLPAlbumProgress(index int, total int, album sharedcatalog.ArtistRelationshipItem) string {
	albumName := strings.TrimSpace(album.Name)
	if albumName == "" {
		albumName = "album " + strings.TrimSpace(album.ID)
	}
	return fmt.Sprintf("Album %d/%d · %s", index+1, total, albumName)
}

func isContextCancellationError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (b *TelegramBot) sendPreparedDownloadPaths(session *DownloadSession, chatID int64, replyToID int, status *DownloadStatus, settings ChatDownloadSettings, paths []string, primaryCount int) bool {
	if len(paths) == 0 {
		return false
	}
	sentAny := false
	for idx, path := range paths {
		if idx >= primaryCount && !sentAny {
			if status != nil {
				status.UpdateSync("Primary media upload failed. Skip extra files (lyrics/cover/animated).", 0, 0)
			}
			break
		}
		if err := b.sendDownloadedPathWithRetry(session, chatID, path, replyToID, status, settings); err != nil {
			sanitized := sanitizeTelegramError(err, b.token)
			fmt.Printf("send file error (%s): %s\n", path, sanitized)
			appendRuntimeErrorLogf("send file error (%s): %s", path, sanitized)
			if status != nil {
				status.Update(fmt.Sprintf("Failed to send %s: %v", filepath.Base(path), err), 0, 0)
			}
			continue
		}
		sentAny = true
	}
	return sentAny
}

func (b *TelegramBot) runArtistLPAlbumZipTransfer(chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, albumID string, storefront string, status *DownloadStatus, progress func(phase string, done, total int64)) (bool, error) {
	if !forceRefresh && b.trySendCachedBundleZip(chatID, mediaTypeAlbum, albumID, replyToID, settings) {
		return true, nil
	}

	var (
		session      *DownloadSession
		paths        []string
		primaryCount int
	)
	coreErr := func() error {
		b.downloadCoreMu.Lock()
		defer b.downloadCoreMu.Unlock()

		var err error
		session, err = b.prepareTelegramDownloadSession(false, forceRefresh, settings, mediaTypeAlbum, progress)
		if err != nil {
			return err
		}
		if status != nil {
			status.Update("Downloading", 0, 0)
		}
		if err := ripAlbum(session, albumID, b.appleToken, storefront, session.Config.MediaUserToken, ""); err != nil {
			return err
		}

		paths = append([]string{}, session.LastDownloadedPaths...)
		primaryCount = len(paths)
		if primaryCount == 0 {
			return errNoFilesDownloaded
		}
		paths = b.augmentDownloadedPaths(paths, settings)
		fmt.Printf("telegram artist LP album ready chat=%d album=%s primary=%d total=%d transfer=%s\n", chatID, albumID, primaryCount, len(paths), transferModeZip)
		return nil
	}()
	if coreErr != nil {
		return false, coreErr
	}
	if forceRefresh && primaryCount > 0 {
		b.purgeTargetCaches(&AppleURLTarget{MediaType: mediaTypeAlbum, ID: albumID})
	}
	if b.cleanupTracker != nil {
		b.cleanupTracker.RecordPaths(paths)
	}

	if status != nil {
		status.Update("Zipping", 0, 0)
	}
	zipPath, displayName, err := createZipFromPaths(paths)
	if err != nil {
		return false, err
	}
	defer os.Remove(zipPath)

	cacheKey := b.bundleZipCacheKey(mediaTypeAlbum, albumID, settings)
	if err := b.sendDocumentFile(chatID, zipPath, displayName, replyToID, status, cacheKey); err != nil {
		sanitized := sanitizeTelegramError(err, b.token)
		fmt.Printf("send artist LP ZIP error (album:%s): %s\n", albumID, sanitized)
		appendRuntimeErrorLogf("send artist LP ZIP error (album:%s): %s", albumID, sanitized)
		if strings.Contains(strings.ToLower(err.Error()), "zip exceeds telegram limit") {
			if status != nil {
				status.UpdateSync("ZIP exceeds Telegram limit, fallback to one-by-one transfer.", 0, 0)
			}
			return b.sendPreparedDownloadPaths(session, chatID, replyToID, status, settings, paths, primaryCount), nil
		}
		return false, err
	}
	return true, nil
}

func (b *TelegramBot) runArtistLPAlbumTransfer(chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, transferMode string, albumID string, storefront string, status *DownloadStatus, statusLabelPrefix string, progress func(phase string, done, total int64)) (bool, error) {
	switch transferMode {
	case transferModeZip:
		return b.runArtistLPAlbumZipTransfer(chatID, replyToID, forceRefresh, settings, albumID, storefront, status, progress)
	default:
		handled, sentAny, err := b.runCollectionOneByOneSequential(chatID, replyToID, forceRefresh, settings, mediaTypeAlbum, albumID, storefront, status, statusLabelPrefix, progress)
		if !handled {
			return false, fmt.Errorf("album one-by-one handler unavailable")
		}
		return sentAny, err
	}
}

func (b *TelegramBot) downloadArtistLPAlbumsCollection(chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, artistID string, storefront string, transferMode string, status *DownloadStatus) (artistLPDownloadResult, error) {
	result := artistLPDownloadResult{}
	if storefront == "" {
		storefront = Config.Storefront
	}
	albums, err := b.fetchArtistLPAlbums(storefront, artistID)
	if err != nil {
		return result, err
	}
	result.Total = len(albums)
	if len(albums) == 0 {
		return result, errNoFilesDownloaded
	}

	for idx, album := range albums {
		if err := b.operationContext().Err(); err != nil {
			return result, err
		}
		albumID := strings.TrimSpace(album.ID)
		if albumID == "" {
			result.Failed++
			continue
		}
		albumProgressLabel := formatArtistLPAlbumProgress(idx, len(albums), album)
		if status != nil {
			status.UpdateSync(albumProgressLabel, int64(idx), int64(len(albums)))
		}
		albumProgress := func(phase string, done, total int64) {
			if status == nil {
				return
			}
			phase = strings.TrimSpace(phase)
			if phase == "" {
				phase = "Working"
			}
			status.Update(albumProgressLabel+" · "+phase, done, total)
		}
		sentAny, albumErr := b.runArtistLPAlbumTransfer(chatID, replyToID, forceRefresh, settings, transferMode, albumID, storefront, status, albumProgressLabel, albumProgress)
		if albumErr != nil {
			if isContextCancellationError(albumErr) {
				return result, albumErr
			}
			result.Failed++
			sanitized := sanitizeTelegramError(albumErr, b.token)
			fmt.Printf("artist LP album failed artist=%s album=%s transfer=%s: %s\n", artistID, albumID, transferMode, sanitized)
			appendRuntimeErrorLogf("artist LP album failed artist=%s album=%s transfer=%s: %s", artistID, albumID, transferMode, sanitized)
			if status != nil {
				status.UpdateSync(fmt.Sprintf("%s failed: %v", albumProgressLabel, albumErr), int64(idx+1), int64(len(albums)))
			}
			continue
		}
		if sentAny {
			result.SentAny = true
			result.Succeeded++
		} else {
			result.Failed++
			if status != nil {
				status.UpdateSync(albumProgressLabel+" (no upload success)", int64(idx+1), int64(len(albums)))
			}
			continue
		}
		if status != nil {
			status.UpdateSync(albumProgressLabel, int64(idx+1), int64(len(albums)))
		}
	}
	return result, nil
}

func (b *TelegramBot) fetchArtwork(target *AppleURLTarget) (artworkFetchResult, error) {
	if target == nil {
		return artworkFetchResult{}, fmt.Errorf("invalid target")
	}
	info, err := b.catalogService().FetchArtwork(sharedcatalog.ArtworkTarget{
		MediaType:  target.MediaType,
		ID:         target.ID,
		Storefront: resolveStorefront(target),
	})
	if err != nil {
		return artworkFetchResult{}, err
	}
	return artworkFetchResult{
		DisplayName: info.DisplayName,
		CoverURL:    info.CoverURL,
		MotionURL:   info.MotionURL,
	}, nil
}

func renderCoverToTemp(coverURL string) (string, string, error) {
	tmpDir, err := os.MkdirTemp("", "amdl-cover-*")
	if err != nil {
		return "", "", err
	}
	coverPath, err := writeCover(tmpDir, "cover", coverURL)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", err
	}
	return coverPath, tmpDir, nil
}

func copyTempFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func (b *TelegramBot) saveAnimatedCover(motionURL string, savePath string) error {
	if strings.TrimSpace(motionURL) == "" {
		return fmt.Errorf("motion url is empty")
	}
	videoURL, err := extractVideo(motionURL)
	if err != nil {
		return err
	}
	result, err := runExternalCommand(b.operationContext(), "ffmpeg", "-loglevel", "error", "-y", "-i", videoURL, "-c", "copy", savePath)
	if err != nil {
		outText := strings.TrimSpace(result.Combined)
		if outText == "" {
			outText = err.Error()
		}
		return fmt.Errorf("%s", outText)
	}
	return nil
}

func (b *TelegramBot) exportArtistAssets(chatID int64, replyToID int, artistID string, storefront string, transferMode string) {
	if strings.TrimSpace(artistID) == "" {
		_ = b.sendMessageWithReply(chatID, "Artist ID is empty.", nil, replyToID)
		return
	}
	storefront = resolveStorefront(&AppleURLTarget{Storefront: storefront})
	status, err := newDownloadStatus(b, chatID, replyToID)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to create status message: %v", err), nil, replyToID)
		return
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		status.finishFailure()
	}()

	tmpDir, err := os.MkdirTemp("", "amdl-artist-assets-*")
	if err != nil {
		status.UpdateSync(fmt.Sprintf("Failed to create temp directory: %v", err), 0, 0)
		return
	}
	defer os.RemoveAll(tmpDir)

	usedNames := make(map[string]struct{})
	assetPaths := []string{}
	coverCount := 0
	motionCount := 0
	failedAlbums := 0
	ffmpegOK := true
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		ffmpegOK = false
	}

	status.Update("Loading artist profile", 0, 0)
	artistInfo, artistErr := b.fetchArtwork(&AppleURLTarget{
		MediaType:  mediaTypeArtist,
		Storefront: storefront,
		ID:         artistID,
	})
	artistName := sanitizeFileBaseName(firstNonEmpty(artistInfo.DisplayName, "artist-"+artistID))
	if artistErr == nil && strings.TrimSpace(artistInfo.CoverURL) != "" {
		profilePath, profileTmp, err := renderCoverToTemp(artistInfo.CoverURL)
		if err == nil {
			profileName := uniqueName(usedNames, artistName+"-artist-photo"+strings.ToLower(filepath.Ext(profilePath)))
			profileDst := filepath.Join(tmpDir, profileName)
			if err := copyTempFile(profilePath, profileDst); err == nil {
				assetPaths = append(assetPaths, profileDst)
				coverCount++
			}
		}
		if strings.TrimSpace(profileTmp) != "" {
			_ = os.RemoveAll(profileTmp)
		}
	}

	status.Update("Loading artist albums", 0, 0)
	albums, _, err := apputils.FetchArtistAlbums(storefront, artistID, b.appleToken, 0, 0, b.searchLanguage())
	if err != nil {
		status.UpdateSync(fmt.Sprintf("Failed to load artist albums: %v", err), 0, 0)
		return
	}
	if strings.HasPrefix(artistName, "artist-") && len(albums) > 0 {
		if resolvedName := sanitizeFileBaseName(albums[0].Artist); resolvedName != "" {
			artistName = resolvedName
		}
	}
	totalAlbums := len(albums)
	for i, album := range albums {
		status.Update("Collecting artist assets", int64(i), int64(totalAlbums))
		if strings.TrimSpace(album.ID) == "" {
			continue
		}
		info, err := b.fetchArtwork(&AppleURLTarget{
			MediaType:  mediaTypeAlbum,
			Storefront: storefront,
			ID:         album.ID,
		})
		if err != nil {
			failedAlbums++
			continue
		}
		albumBase := sanitizeFileBaseName(firstNonEmpty(info.DisplayName, album.Name, "album-"+album.ID))

		if strings.TrimSpace(info.CoverURL) != "" {
			coverPath, coverTmp, err := renderCoverToTemp(info.CoverURL)
			if err == nil {
				coverName := uniqueName(usedNames, fmt.Sprintf("%03d-%s-cover%s", i+1, albumBase, strings.ToLower(filepath.Ext(coverPath))))
				coverDst := filepath.Join(tmpDir, coverName)
				if err := copyTempFile(coverPath, coverDst); err == nil {
					assetPaths = append(assetPaths, coverDst)
					coverCount++
				}
			}
			if strings.TrimSpace(coverTmp) != "" {
				_ = os.RemoveAll(coverTmp)
			}
		}

		if ffmpegOK && strings.TrimSpace(info.MotionURL) != "" {
			motionName := uniqueName(usedNames, fmt.Sprintf("%03d-%s-animated-cover.mp4", i+1, albumBase))
			motionDst := filepath.Join(tmpDir, motionName)
			if err := b.saveAnimatedCover(info.MotionURL, motionDst); err == nil {
				assetPaths = append(assetPaths, motionDst)
				motionCount++
			}
		}
	}

	if !ffmpegOK {
		_ = b.sendMessageWithReply(chatID, "ffmpeg 不可用，已跳过动态封面导出。", nil, replyToID)
	}
	if len(assetPaths) == 0 {
		status.UpdateSync("No artist assets exported.", 0, 0)
		return
	}

	if transferMode == transferModeZip {
		status.Update("Building ZIP", int64(len(assetPaths)), int64(len(assetPaths)))
		zipPath, displayName, err := createZipFromPaths(assetPaths)
		if err != nil {
			status.UpdateSync(fmt.Sprintf("Failed to build ZIP: %v", err), 0, 0)
			return
		}
		defer os.Remove(zipPath)
		if artistName != "" {
			displayName = artistName + ".artist-assets.zip"
		}
		if err := b.sendDocumentFile(chatID, zipPath, displayName, replyToID, status, ""); err == nil {
			completed = true
			status.finishSuccess()
			return
		} else if strings.Contains(strings.ToLower(err.Error()), "zip exceeds telegram limit") {
			status.UpdateSync("ZIP exceeds Telegram size limit, fallback to one-by-one.", 0, 0)
			transferMode = transferModeOneByOne
		} else {
			status.UpdateSync(fmt.Sprintf("Failed to send ZIP: %v", err), 0, 0)
			return
		}
	}

	if transferMode == transferModeOneByOne {
		sentCount := 0
		for idx, path := range assetPaths {
			status.Update("Sending artist assets", int64(idx), int64(len(assetPaths)))
			ext := strings.ToLower(filepath.Ext(path))
			var err error
			if ext == ".mp4" {
				err = b.sendVideoFile(chatID, path, replyToID, "", status, "")
				if err != nil {
					err = b.sendDocumentFile(chatID, path, filepath.Base(path), replyToID, status, "")
				}
			} else {
				err = b.sendDocumentFile(chatID, path, filepath.Base(path), replyToID, status, "")
			}
			if err == nil {
				sentCount++
			}
		}
		completed = true
		status.finishSuccess()
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Artist assets done: sent=%d, covers=%d, animated=%d, failed_albums=%d.", sentCount, coverCount, motionCount, failedAlbums), nil, replyToID)
		return
	}
}

func (b *TelegramBot) handleCoverOnly(chatID int64, replyToID int, target *AppleURLTarget, artistOnly bool) {
	info, err := b.fetchArtwork(target)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to fetch cover info: %v", err), nil, replyToID)
		return
	}
	coverPath, tmpDir, err := renderCoverToTemp(info.CoverURL)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to download cover: %v", err), nil, replyToID)
		return
	}
	defer os.RemoveAll(tmpDir)
	base := sanitizeFileBaseName(info.DisplayName)
	suffix := "-cover"
	if artistOnly {
		suffix = "-artist-photo"
	}
	displayName := base + suffix + strings.ToLower(filepath.Ext(coverPath))
	if err := b.sendDocumentFile(chatID, coverPath, displayName, replyToID, nil, ""); err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to send cover: %v", err), nil, replyToID)
		return
	}
}

func (b *TelegramBot) enqueueCoverTask(chatID int64, replyToID int, target *AppleURLTarget) {
	if target == nil || strings.TrimSpace(target.MediaType) == "" || strings.TrimSpace(target.ID) == "" {
		_ = b.sendMessageWithReply(chatID, "Invalid Apple Music URL.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       true,
		taskType:     telegramTaskCover,
		settings:     settings,
		transferMode: transferModeOneByOne,
		mediaType:    target.MediaType,
		mediaID:      target.ID,
		storefront:   resolveStorefront(target),
		requestID:    b.nextRequestID(),
	})
}

func (b *TelegramBot) handleAnimatedCoverOnly(chatID int64, replyToID int, target *AppleURLTarget) {
	if target == nil {
		_ = b.sendMessageWithReply(chatID, "Invalid target.", nil, replyToID)
		return
	}
	switch target.MediaType {
	case mediaTypeSong, mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation:
	default:
		_ = b.sendMessageWithReply(chatID, "animatedcover only supports song/album/playlist/station.", nil, replyToID)
		return
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		_ = b.sendMessageWithReply(chatID, "animatedcover requires ffmpeg in PATH.", nil, replyToID)
		return
	}
	info, err := b.fetchArtwork(target)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to fetch artwork info: %v", err), nil, replyToID)
		return
	}
	if strings.TrimSpace(info.MotionURL) == "" {
		_ = b.sendMessageWithReply(chatID, "No animated cover found for this item.", nil, replyToID)
		return
	}
	videoURL, err := extractVideo(info.MotionURL)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Animated cover not available: %v", err), nil, replyToID)
		return
	}
	tmp, err := os.CreateTemp("", "amdl-animated-cover-*.mp4")
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to create temp file: %v", err), nil, replyToID)
		return
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)
	result, err := runExternalCommand(b.operationContext(), "ffmpeg", "-loglevel", "error", "-y", "-i", videoURL, "-c", "copy", tmpPath)
	if err != nil {
		errText := strings.TrimSpace(result.Combined)
		if errText == "" {
			errText = err.Error()
		}
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to download animated cover: %s", errText), nil, replyToID)
		return
	}
	displayName := sanitizeFileBaseName(info.DisplayName) + "-animated-cover.mp4"
	if err := b.sendVideoFile(chatID, tmpPath, replyToID, "", nil, ""); err == nil {
		return
	} else {
		if docErr := b.sendDocumentFile(chatID, tmpPath, displayName, replyToID, nil, ""); docErr != nil {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to send animated cover: %v; fallback failed: %v", err, docErr), nil, replyToID)
			return
		}
	}
}

func (b *TelegramBot) enqueueAnimatedCoverTask(chatID int64, replyToID int, target *AppleURLTarget) {
	if target == nil || strings.TrimSpace(target.MediaType) == "" || strings.TrimSpace(target.ID) == "" {
		_ = b.sendMessageWithReply(chatID, "Invalid target.", nil, replyToID)
		return
	}
	switch target.MediaType {
	case mediaTypeSong, mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation:
	default:
		_ = b.sendMessageWithReply(chatID, "animatedcover only supports song/album/playlist/station.", nil, replyToID)
		return
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		_ = b.sendMessageWithReply(chatID, "animatedcover requires ffmpeg in PATH.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       true,
		taskType:     telegramTaskAnimatedCover,
		settings:     settings,
		transferMode: transferModeOneByOne,
		mediaType:    target.MediaType,
		mediaID:      target.ID,
		storefront:   resolveStorefront(target),
		requestID:    b.nextRequestID(),
	})
}

func (b *TelegramBot) fetchLyricsOnly(songID string, storefront string, outputFormat string) (string, string, error) {
	return b.catalogService().FetchLyricsOnly(songID, storefront, outputFormat)
}

func (b *TelegramBot) sendTextAsDocument(chatID int64, replyToID int, displayName string, ext string, content string) error {
	ext = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")
	if ext == "" {
		ext = "txt"
	}
	tmp, err := os.CreateTemp("", "amdl-doc-*."+ext)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)
	if displayName == "" {
		displayName = "apple-music." + ext
	}
	return b.sendDocumentFile(chatID, tmpPath, displayName, replyToID, nil, "")
}

func uniqueName(existing map[string]struct{}, candidate string) string {
	if _, ok := existing[candidate]; !ok {
		existing[candidate] = struct{}{}
		return candidate
	}
	ext := filepath.Ext(candidate)
	base := strings.TrimSuffix(candidate, ext)
	for i := 2; i <= 9999; i++ {
		name := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, ok := existing[name]; !ok {
			existing[name] = struct{}{}
			return name
		}
	}
	existing[candidate] = struct{}{}
	return candidate
}

func (b *TelegramBot) exportAlbumLyrics(chatID int64, replyToID int, albumID string, storefront string, transferMode string) {
	b.exportAlbumLyricsWithSettings(chatID, replyToID, albumID, storefront, transferMode, b.getChatSettings(chatID))
}

func (b *TelegramBot) exportAlbumLyricsWithSettings(chatID int64, replyToID int, albumID string, storefront string, transferMode string, settings ChatDownloadSettings) {
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Lyrics export requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	if strings.TrimSpace(albumID) == "" {
		_ = b.sendMessageWithReply(chatID, "Album ID is empty.", nil, replyToID)
		return
	}
	storefront = resolveStorefront(&AppleURLTarget{Storefront: storefront})
	settings = normalizeChatSettings(settings)
	lyricsFormat := normalizeLyricsOutputFormat(settings.LyricsFormat)
	if lyricsFormat == "" {
		lyricsFormat = defaultTelegramLyricsFormat
	}
	status, err := newDownloadStatus(b, chatID, replyToID)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to create status message: %v", err), nil, replyToID)
		return
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		status.finishFailure()
	}()
	status.Update("Loading album metadata", 0, 0)
	exported, err := b.catalogService().ExportAlbumLyrics("", albumID, storefront, lyricsFormat)
	if err != nil {
		switch {
		case errors.Is(err, sharedcatalog.ErrAlbumNotFound):
			status.UpdateSync("Album not found.", 0, 0)
		case errors.Is(err, sharedcatalog.ErrNoLyricsExported):
			status.UpdateSync("No lyrics files could be exported for this album.", 0, 0)
		default:
			var accessErr *safe.AccessError
			if errors.As(err, &accessErr) {
				status.UpdateSync(fmt.Sprintf("Album metadata is invalid: %v", err), 0, 0)
			} else {
				status.UpdateSync(fmt.Sprintf("Failed to load album: %v", err), 0, 0)
			}
		}
		return
	}
	lyricPaths := exported.Paths
	failedCount := exported.FailedCount
	if len(lyricPaths) == 0 {
		status.UpdateSync("No lyrics files could be exported for this album.", 0, 0)
		return
	}
	status.Update("Preparing output", int64(len(lyricPaths)), int64(len(lyricPaths)+failedCount))
	if transferMode == transferModeZip {
		zipPath, displayName, err := createZipFromPaths(lyricPaths)
		if err != nil {
			status.UpdateSync(fmt.Sprintf("Failed to build ZIP: %v", err), 0, 0)
			return
		}
		defer os.Remove(zipPath)
		safeAlbumName := sanitizeFileBaseName(exported.AlbumName)
		if safeAlbumName != "" {
			displayName = safeAlbumName + ".lyrics.zip"
		}
		if err := b.sendDocumentFile(chatID, zipPath, displayName, replyToID, status, ""); err == nil {
			completed = true
			status.finishSuccess()
		} else if strings.Contains(strings.ToLower(err.Error()), "zip exceeds telegram limit") {
			status.UpdateSync("ZIP exceeds Telegram size limit, fallback to one-by-one.", 0, 0)
			transferMode = transferModeOneByOne
		} else {
			status.UpdateSync(fmt.Sprintf("Failed to send ZIP: %v", err), 0, 0)
			return
		}
	}
	if transferMode == transferModeOneByOne {
		for idx, lyricPath := range lyricPaths {
			status.Update("Sending lyrics files", int64(idx), int64(len(lyricPaths)))
			if err := b.sendDocumentFile(chatID, lyricPath, filepath.Base(lyricPath), replyToID, status, ""); err != nil {
				failedCount++
			}
		}
		completed = true
		status.finishSuccess()
	}
	if failedCount > 0 {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Lyrics export completed with %d failed tracks.", failedCount), nil, replyToID)
	}
}

func (b *TelegramBot) handleLyricsOnly(chatID int64, replyToID int, target *AppleURLTarget) {
	b.handleLyricsOnlyWithSettings(chatID, replyToID, target, b.getChatSettings(chatID))
}

func (b *TelegramBot) handleLyricsOnlyWithSettings(chatID int64, replyToID int, target *AppleURLTarget, settings ChatDownloadSettings) {
	if target == nil || strings.TrimSpace(target.ID) == "" {
		_ = b.sendMessageWithReply(chatID, "Invalid song target.", nil, replyToID)
		return
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Lyrics export requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	settings = normalizeChatSettings(settings)
	outputFormat := normalizeLyricsOutputFormat(settings.LyricsFormat)
	if outputFormat == "" {
		outputFormat = defaultTelegramLyricsFormat
	}
	storefront := resolveStorefront(target)
	songID := strings.TrimSpace(target.ID)
	content, lyricType, err := b.fetchLyricsOnly(songID, storefront, outputFormat)
	if err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to fetch lyrics: %v", err), nil, replyToID)
		return
	}
	baseName := "song-" + songID
	if songResp, err := ampapi.GetSongResp(storefront, songID, Config.Language, b.appleToken); err == nil {
		item, dataErr := firstSongData("main.telegram.handleLyricsOnly", songResp)
		if dataErr == nil {
			if title := composeArtistTitle(item.Attributes.ArtistName, item.Attributes.Name); strings.TrimSpace(title) != "" {
				baseName = title
			}
		}
	}
	displayName := sanitizeFileBaseName(baseName) + ".lyrics." + outputFormat
	if err := b.sendTextAsDocument(chatID, replyToID, displayName, outputFormat, content); err != nil {
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to send lyrics file: %v", err), nil, replyToID)
		return
	}
	if outputFormat == "lrc" {
		if lyricType == "syllable-lyrics" {
			_ = b.sendMessageWithReply(chatID, "LRC exported. Translation lines are included when Apple provides them.", nil, replyToID)
		} else {
			_ = b.sendMessageWithReply(chatID, "LRC exported from fallback source (translation may be unavailable).", nil, replyToID)
		}
		return
	}
	if lyricType == "syllable-lyrics" {
		_ = b.sendMessageWithReply(chatID, "TTML exported. Translation and transliteration are included when Apple provides them.", nil, replyToID)
	} else {
		_ = b.sendMessageWithReply(chatID, "TTML exported from fallback lyrics source (translation/transliteration may be unavailable).", nil, replyToID)
	}
}

func (b *TelegramBot) enqueueSongLyricsTask(chatID int64, replyToID int, target *AppleURLTarget) {
	if target == nil || strings.TrimSpace(target.ID) == "" || target.MediaType != mediaTypeSong {
		_ = b.sendMessageWithReply(chatID, "Invalid song target.", nil, replyToID)
		return
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Lyrics export requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       true,
		taskType:     telegramTaskSongLyrics,
		settings:     settings,
		transferMode: transferModeOneByOne,
		mediaType:    target.MediaType,
		mediaID:      target.ID,
		storefront:   resolveStorefront(target),
		requestID:    b.nextRequestID(),
	})
}

func (b *TelegramBot) enqueueAlbumLyricsTask(chatID int64, replyToID int, albumID string, storefront string, transferMode string) {
	if strings.TrimSpace(albumID) == "" {
		_ = b.sendMessageWithReply(chatID, "Album ID is empty.", nil, replyToID)
		return
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Lyrics export requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       false,
		taskType:     telegramTaskAlbumLyrics,
		settings:     settings,
		transferMode: transferMode,
		mediaType:    mediaTypeAlbum,
		mediaID:      albumID,
		storefront:   resolveStorefront(&AppleURLTarget{Storefront: storefront}),
		requestID:    b.nextRequestID(),
	})
}

func (b *TelegramBot) enqueueArtistAssetsTask(chatID int64, replyToID int, artistID string, storefront string, transferMode string) {
	if strings.TrimSpace(artistID) == "" {
		_ = b.sendMessageWithReply(chatID, "Artist ID is empty.", nil, replyToID)
		return
	}
	settings := b.getChatSettings(chatID)
	_ = b.enqueueTelegramTask(&downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       false,
		taskType:     telegramTaskArtistAssets,
		settings:     settings,
		transferMode: transferMode,
		mediaType:    mediaTypeArtist,
		mediaID:      artistID,
		storefront:   resolveStorefront(&AppleURLTarget{Storefront: storefront}),
		requestID:    b.nextRequestID(),
	})
}

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
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to load artist content: %v", err), nil, replyToID)
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
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Search failed: %v", err), nil, replyToID)
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
		allowCollectionZipCache := mediaType != mediaTypeArtistLPs
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
			_ = b.editMessageText(chatID, messageID, fmt.Sprintf("Search failed: %v", err), nil)
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

func makeDownloadInflightKey(chatID int64, mediaType string, mediaID string, storefront string, transferMode string, settings ChatDownloadSettings) string {
	normalized := normalizeChatSettings(settings)
	normalizedTransfer := normalizeTransferModeForMedia(transferMode, mediaType, mediaType == mediaTypeSong || mediaType == mediaTypeMusicVideo)
	return strings.Join([]string{
		strconv.FormatInt(chatID, 10),
		strings.ToLower(strings.TrimSpace(mediaType)),
		strings.TrimSpace(mediaID),
		strings.TrimSpace(storefront),
		normalizedTransfer,
		normalized.Format,
		normalized.AACType,
		normalized.MVAudioType,
		strconv.FormatBool(normalized.SongZip),
		strconv.FormatBool(normalized.AutoLyrics),
		strconv.FormatBool(normalized.AutoCover),
		strconv.FormatBool(normalized.AutoAnimated),
		normalized.LyricsFormat,
	}, "|")
}

func (b *TelegramBot) acquireInflightDownload(key string) bool {
	if b == nil {
		return false
	}
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return true
	}
	b.inflightMu.Lock()
	defer b.inflightMu.Unlock()
	if _, exists := b.inflightDownloads[trimmed]; exists {
		return false
	}
	b.inflightDownloads[trimmed] = struct{}{}
	return true
}

func (b *TelegramBot) releaseInflightDownload(key string) {
	if b == nil {
		return
	}
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return
	}
	b.inflightMu.Lock()
	delete(b.inflightDownloads, trimmed)
	b.inflightMu.Unlock()
}

func (b *TelegramBot) waitTelegramSend(ctx context.Context, chatID int64) error {
	if b == nil || b.sendLimiter == nil {
		return nil
	}
	err := b.sendLimiter.wait(ctx, chatID)
	if err != nil {
		fmt.Printf("telegram send wait canceled chat=%d: %v\n", chatID, err)
	}
	return err
}

func (b *TelegramBot) tryTelegramSend(chatID int64) bool {
	if b == nil || b.sendLimiter == nil {
		return true
	}
	return b.sendLimiter.tryReserve(chatID)
}

func (b *TelegramBot) operationContext() context.Context {
	if b != nil && b.shutdownCtx != nil {
		return b.shutdownCtx
	}
	return context.Background()
}

func (b *TelegramBot) uploadContext(session *DownloadSession) context.Context {
	if session != nil && session.Context != nil {
		return session.downloadContext()
	}
	return b.operationContext()
}

func (b *TelegramBot) newBotDownloadSession(base structs.ConfigSet) *DownloadSession {
	session := newDownloadSession(base)
	session.Context = b.operationContext()
	return session
}

func (b *TelegramBot) applyTelegramRetryAfter(duration time.Duration) {
	if b == nil || duration <= 0 {
		return
	}
	if b.sendLimiter != nil {
		b.sendLimiter.blockFor(duration)
	}
	fmt.Printf("telegram retry_after applied: blocking sends for %s\n", duration.Round(time.Second))
	appRuntimeMetrics.recordTelegramRetryAfter()
}

func (b *TelegramBot) noteTelegramRateLimit(err error) {
	if err == nil {
		return
	}
	retryAfter, ok := parseTelegramRetryAfter(err)
	if !ok {
		return
	}
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	b.applyTelegramRetryAfter(retryAfter)
}

func (b *TelegramBot) getUpdatesBackoff(err error) (time.Duration, string) {
	if err == nil {
		return 0, ""
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "409") || strings.Contains(msg, "conflict") {
		return b.getUpdatesConflictSleep(), "conflict"
	}
	if retryAfter, ok := parseTelegramRetryAfter(err); ok {
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return retryAfter, "retry_after"
	}
	return b.getUpdatesErrorSleep(), "default"
}

func (b *TelegramBot) queueDownloadSong(chatID int64, songID string) {
	b.queueDownloadSongWithStorefrontOptions(chatID, songID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadSongWithReply(chatID int64, songID string, replyToID int) {
	b.queueDownloadSongWithStorefrontOptions(chatID, songID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadSongWithStorefront(chatID int64, songID string, storefront string, replyToID int) {
	b.queueDownloadSongWithStorefrontOptions(chatID, songID, storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadSongWithStorefrontOptions(chatID int64, songID string, storefront string, replyToID int, forceRefresh bool) {
	if songID == "" {
		_ = b.sendMessage(chatID, "Song ID is empty.", nil)
		return
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	settings := b.getChatSettings(chatID)
	mode := transferModeOneByOne
	if settings.SongZip {
		mode = transferModeZip
	}
	b.enqueueSongDownload(chatID, songID, storefront, replyToID, mode, forceRefresh)
}

func (b *TelegramBot) enqueueSongDownload(chatID int64, songID string, storefront string, replyToID int, transferMode string, forceRefresh bool) {
	if songID == "" {
		_ = b.sendMessage(chatID, "Song ID is empty.", nil)
		return
	}
	settings := b.getChatSettings(chatID)
	transferMode = normalizeTransferModeForMedia(transferMode, mediaTypeSong, true)
	if !forceRefresh && transferMode == transferModeZip && b.trySendCachedBundleZip(chatID, mediaTypeSong, songID, replyToID, settings) {
		return
	}
	if !forceRefresh && transferMode == transferModeOneByOne && b.trySendCachedTrack(chatID, replyToID, songID, settings) {
		b.sendCachedSongAutoExtras(chatID, replyToID, songID, storefront, settings)
		return
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	inflightKey := makeDownloadInflightKey(chatID, mediaTypeSong, songID, storefront, transferMode, settings)
	if !b.acquireInflightDownload(inflightKey) {
		_ = b.sendMessageWithReply(chatID, "Same song task is already running for this chat. Please wait.", nil, replyToID)
		return
	}
	if queued := b.enqueueDownload(chatID, replyToID, true, forceRefresh, settings, transferMode, mediaTypeSong, songID, storefront, inflightKey, func(session *DownloadSession) error {
		return ripSong(session, songID, b.appleToken, storefront, session.Config.MediaUserToken)
	}); !queued {
		b.releaseInflightDownload(inflightKey)
	}
}

func hasSongAutoExtras(settings ChatDownloadSettings) bool {
	normalized := normalizeChatSettings(settings)
	return normalized.AutoLyrics || normalized.AutoCover || normalized.AutoAnimated
}

func (b *TelegramBot) sendCachedSongAutoExtras(chatID int64, replyToID int, songID string, storefront string, settings ChatDownloadSettings) {
	normalized := normalizeChatSettings(settings)
	if !hasSongAutoExtras(normalized) {
		return
	}
	if storefront == "" {
		storefront = Config.Storefront
	}

	baseName := "song-" + songID
	if meta, err := b.fetchTrackMeta(songID); err == nil {
		if title := composeArtistTitle(meta.Performer, meta.Title); strings.TrimSpace(title) != "" {
			baseName = title
		}
	}

	if normalized.AutoLyrics {
		outputFormat := normalizeLyricsOutputFormat(normalized.LyricsFormat)
		if outputFormat == "" {
			outputFormat = defaultTelegramLyricsFormat
		}
		content, _, err := b.fetchLyricsOnly(songID, storefront, outputFormat)
		if err != nil {
			fmt.Printf("cached song extra lyrics skipped (%s): %v\n", songID, err)
		} else if strings.TrimSpace(content) != "" {
			displayName := sanitizeFileBaseName(baseName) + ".lyrics." + outputFormat
			if err := b.sendTextAsDocument(chatID, replyToID, displayName, outputFormat, content); err != nil {
				fmt.Printf("send cached lyrics error (%s): %v\n", songID, err)
			}
		}
	}

	needArtwork := normalized.AutoCover || normalized.AutoAnimated
	if !needArtwork {
		return
	}
	info, err := b.fetchArtwork(&AppleURLTarget{
		MediaType:  mediaTypeSong,
		ID:         songID,
		Storefront: storefront,
	})
	if err != nil {
		fmt.Printf("cached song extra artwork skipped (%s): %v\n", songID, err)
		return
	}
	displayBase := sanitizeFileBaseName(firstNonEmpty(info.DisplayName, baseName))

	if normalized.AutoCover && strings.TrimSpace(info.CoverURL) != "" {
		coverPath, tmpDir, err := renderCoverToTemp(info.CoverURL)
		if err != nil {
			fmt.Printf("cached song extra cover skipped (%s): %v\n", songID, err)
		} else {
			displayName := displayBase + "-cover" + strings.ToLower(filepath.Ext(coverPath))
			if err := b.sendDocumentFile(chatID, coverPath, displayName, replyToID, nil, ""); err != nil {
				fmt.Printf("send cached cover error (%s): %v\n", songID, err)
			}
			_ = os.RemoveAll(tmpDir)
		}
	}

	if normalized.AutoAnimated && strings.TrimSpace(info.MotionURL) != "" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			fmt.Printf("cached song extra animated skipped (%s): ffmpeg not found\n", songID)
			return
		}
		tmp, err := os.CreateTemp("", "amdl-cached-song-animated-*.mp4")
		if err != nil {
			fmt.Printf("cached song extra animated skipped (%s): %v\n", songID, err)
			return
		}
		tmpPath := tmp.Name()
		_ = tmp.Close()
		defer os.Remove(tmpPath)
		if err := b.saveAnimatedCover(info.MotionURL, tmpPath); err != nil {
			fmt.Printf("cached song extra animated skipped (%s): %v\n", songID, err)
			return
		}
		if err := b.sendVideoFile(chatID, tmpPath, replyToID, "", nil, ""); err != nil {
			displayName := displayBase + "-animated-cover.mp4"
			if docErr := b.sendDocumentFile(chatID, tmpPath, displayName, replyToID, nil, ""); docErr != nil {
				fmt.Printf("send cached animated cover error (%s): %v; fallback: %v\n", songID, err, docErr)
			}
		}
	}
}

func (b *TelegramBot) queueDownloadAlbum(chatID int64, albumID string) {
	b.queueDownloadAlbumWithStorefrontOptions(chatID, albumID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadAlbumWithReply(chatID int64, albumID string, replyToID int) {
	b.queueDownloadAlbumWithStorefrontOptions(chatID, albumID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadAlbumWithStorefront(chatID int64, albumID string, storefront string, replyToID int) {
	b.queueDownloadAlbumWithStorefrontOptions(chatID, albumID, storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadAlbumWithStorefrontOptions(chatID int64, albumID string, storefront string, replyToID int, forceRefresh bool) {
	if albumID == "" {
		_ = b.sendMessage(chatID, "Album ID is empty.", nil)
		return
	}
	b.promptMediaTransfer(chatID, mediaTypeAlbum, albumID, storefront, "", replyToID, forceRefresh)
}

func (b *TelegramBot) queueDownloadPlaylist(chatID int64, playlistID string) {
	b.queueDownloadPlaylistWithStorefrontOptions(chatID, playlistID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadPlaylistWithReply(chatID int64, playlistID string, replyToID int) {
	b.queueDownloadPlaylistWithStorefrontOptions(chatID, playlistID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadPlaylistWithStorefront(chatID int64, playlistID string, storefront string, replyToID int) {
	b.queueDownloadPlaylistWithStorefrontOptions(chatID, playlistID, storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadPlaylistWithStorefrontOptions(chatID int64, playlistID string, storefront string, replyToID int, forceRefresh bool) {
	if playlistID == "" {
		_ = b.sendMessage(chatID, "Playlist ID is empty.", nil)
		return
	}
	b.promptMediaTransfer(chatID, mediaTypePlaylist, playlistID, storefront, "", replyToID, forceRefresh)
}

func (b *TelegramBot) queueDownloadStation(chatID int64, stationID string) {
	b.queueDownloadStationWithStorefront(chatID, stationID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadStationWithReply(chatID int64, stationID string, replyToID int) {
	b.queueDownloadStationWithStorefront(chatID, stationID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadStationWithStorefront(chatID int64, stationID string, storefront string, replyToID int, forceRefresh bool) {
	if stationID == "" {
		_ = b.sendMessage(chatID, "Station ID is empty.", nil)
		return
	}
	if len(strings.TrimSpace(Config.MediaUserToken)) <= 50 {
		_ = b.sendMessageWithReply(chatID, "Station download requires media-user-token in config.yaml.", nil, replyToID)
		return
	}
	b.promptMediaTransfer(chatID, mediaTypeStation, stationID, storefront, "", replyToID, forceRefresh)
}

func (b *TelegramBot) queueDownloadMusicVideo(chatID int64, mvID string) {
	b.queueDownloadMusicVideoWithStorefront(chatID, mvID, Config.Storefront, 0, false)
}

func (b *TelegramBot) queueDownloadMusicVideoWithReply(chatID int64, mvID string, replyToID int) {
	b.queueDownloadMusicVideoWithStorefront(chatID, mvID, Config.Storefront, replyToID, false)
}

func (b *TelegramBot) queueDownloadMusicVideoWithStorefront(chatID int64, mvID string, storefront string, replyToID int, forceRefresh bool) {
	normalizedID, storefront, ok := b.validateMusicVideoRequest(chatID, mvID, storefront, replyToID)
	if !ok {
		return
	}
	b.promptMusicVideoStreamSelection(chatID, normalizedID, storefront, replyToID, forceRefresh)
}

func (b *TelegramBot) promptMediaTransfer(chatID int64, mediaType string, mediaID string, storefront string, mediaName string, replyToID int, forceRefresh bool) {
	if mediaID == "" {
		_ = b.sendMessage(chatID, "Media ID is empty.", nil)
		return
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	message := "Choose transfer method:"
	if mediaType == mediaTypeAlbumLyrics {
		message = "Choose lyrics export method:"
	} else if mediaType == mediaTypeArtistAsset {
		message = "Choose artist assets export method:"
	}
	messageID, err := b.sendMessageWithReplyReturn(chatID, message, buildTransferKeyboard(b.getChatLanguage(chatID)), replyToID)
	if err != nil {
		return
	}
	b.setPendingTransfer(chatID, mediaType, mediaID, mediaName, storefront, replyToID, messageID, forceRefresh)
}

func (b *TelegramBot) enqueueCollectionDownload(chatID int64, mediaType string, mediaID string, storefront string, replyToID int, transferMode string, forceRefresh bool) {
	if mediaID == "" {
		_ = b.sendMessage(chatID, "Media ID is empty.", nil)
		return
	}
	settings := b.getChatSettings(chatID)
	if storefront == "" {
		storefront = Config.Storefront
	}
	transferMode = normalizeTransferModeForMedia(transferMode, mediaType, false)
	inflightKey := makeDownloadInflightKey(chatID, mediaType, mediaID, storefront, transferMode, settings)
	if !b.acquireInflightDownload(inflightKey) {
		_ = b.sendMessageWithReply(chatID, "Same download task is already running for this chat. Please wait.", nil, replyToID)
		return
	}
	enqueueWithRollback := func(fn func(session *DownloadSession) error) {
		if queued := b.enqueueDownload(chatID, replyToID, false, forceRefresh, settings, transferMode, mediaType, mediaID, storefront, inflightKey, fn); !queued {
			b.releaseInflightDownload(inflightKey)
		}
	}
	switch mediaType {
	case mediaTypeAlbum:
		enqueueWithRollback(func(session *DownloadSession) error {
			return ripAlbum(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken, "")
		})
	case mediaTypePlaylist:
		enqueueWithRollback(func(session *DownloadSession) error {
			return ripPlaylist(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		})
	case mediaTypeStation:
		enqueueWithRollback(func(session *DownloadSession) error {
			return ripStation(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		})
	case mediaTypeArtistSongs:
		enqueueWithRollback(func(session *DownloadSession) error {
			return b.downloadArtistSongsCollection(session, mediaID, storefront)
		})
	case mediaTypeArtistLPs:
		enqueueWithRollback(func(session *DownloadSession) error {
			return b.stageArtistLPAlbumsCollection(session, mediaID, storefront)
		})
	default:
		b.releaseInflightDownload(inflightKey)
		_ = b.sendMessageWithReply(chatID, "Unsupported collection type for transfer.", nil, replyToID)
	}
}

func (b *TelegramBot) buildQueuedRequestRunner(req *downloadRequest) error {
	if b == nil || req == nil {
		return fmt.Errorf("invalid request")
	}
	req.taskType = normalizeTelegramTaskType(req.taskType)
	req.settings = normalizeChatSettings(req.settings)
	if strings.TrimSpace(req.storefront) == "" {
		req.storefront = Config.Storefront
	}
	if strings.TrimSpace(req.mediaType) == "" || strings.TrimSpace(req.mediaID) == "" {
		return fmt.Errorf("invalid request: missing media type/id")
	}
	if req.run != nil {
		return nil
	}
	switch req.taskType {
	case telegramTaskDownload:
		req.transferMode = normalizeTransferModeForMedia(req.transferMode, req.mediaType, req.single)
		if req.fn == nil {
			fn, err := b.buildDownloadWorkerFn(req.mediaType, req.mediaID, req.storefront, req.transferMode, req.mvVariantURL, req.mvVariantKey, req.mvVariantTag)
			if err != nil {
				return err
			}
			req.fn = fn
		}
		req.run = func(bot *TelegramBot) error {
			bot.runDownload(req.chatID, req.fn, req.single, req.forceRefresh, req.replyToID, req.settings, req.transferMode, req.mediaType, req.mediaID, req.storefront)
			return nil
		}
	case telegramTaskCover:
		req.single = true
		req.transferMode = transferModeOneByOne
		target := &AppleURLTarget{MediaType: req.mediaType, ID: req.mediaID, Storefront: req.storefront}
		req.run = func(bot *TelegramBot) error {
			bot.handleCoverOnly(req.chatID, req.replyToID, target, false)
			return nil
		}
	case telegramTaskAnimatedCover:
		req.single = true
		req.transferMode = transferModeOneByOne
		target := &AppleURLTarget{MediaType: req.mediaType, ID: req.mediaID, Storefront: req.storefront}
		req.run = func(bot *TelegramBot) error {
			bot.handleAnimatedCoverOnly(req.chatID, req.replyToID, target)
			return nil
		}
	case telegramTaskSongLyrics:
		req.single = true
		req.transferMode = transferModeOneByOne
		target := &AppleURLTarget{MediaType: req.mediaType, ID: req.mediaID, Storefront: req.storefront}
		settings := req.settings
		req.run = func(bot *TelegramBot) error {
			bot.handleLyricsOnlyWithSettings(req.chatID, req.replyToID, target, settings)
			return nil
		}
	case telegramTaskAlbumLyrics:
		req.single = false
		if req.transferMode != transferModeZip {
			req.transferMode = transferModeOneByOne
		}
		settings := req.settings
		req.run = func(bot *TelegramBot) error {
			bot.exportAlbumLyricsWithSettings(req.chatID, req.replyToID, req.mediaID, req.storefront, req.transferMode, settings)
			return nil
		}
	case telegramTaskArtistAssets:
		req.single = false
		if req.transferMode != transferModeZip {
			req.transferMode = transferModeOneByOne
		}
		req.run = func(bot *TelegramBot) error {
			bot.exportArtistAssets(req.chatID, req.replyToID, req.mediaID, req.storefront, req.transferMode)
			return nil
		}
	default:
		return fmt.Errorf("unsupported task type: %s", req.taskType)
	}
	return nil
}

func (b *TelegramBot) enqueueTaskRequest(req *downloadRequest, blockedMessage string, queueFullMessage string) bool {
	if req == nil {
		return false
	}
	if err := b.buildQueuedRequestRunner(req); err != nil {
		if req.chatID != 0 {
			_ = b.sendMessageWithReply(req.chatID, fmt.Sprintf("Failed: %v", err), nil, req.replyToID)
		}
		return false
	}
	if b.resourceGuard != nil {
		if ok, reason := b.resourceGuard.allow(); !ok {
			msg := strings.TrimSpace(blockedMessage)
			if msg == "" {
				msg = "Resource pressure detected. New tasks are temporarily blocked."
			}
			if strings.TrimSpace(reason) != "" {
				msg = msg + " " + reason
			}
			_ = b.sendMessageWithReply(req.chatID, msg, nil, req.replyToID)
			return false
		}
	}
	b.queueMu.Lock()
	queueLen := len(b.downloadQueue)
	queueCap := cap(b.downloadQueue)
	activeWorkers := b.activeWorkers
	workerLimit := b.workerLimit
	queueFull := queueLen >= queueCap
	b.queueMu.Unlock()

	if queueFull {
		msg := strings.TrimSpace(queueFullMessage)
		if msg == "" {
			msg = "Task queue is full. Please try again later."
		}
		_ = b.sendMessageWithReply(req.chatID, msg, nil, req.replyToID)
		return false
	}
	b.trackQueuedRequest(req)
	select {
	case b.downloadQueue <- req:
	default:
		b.untrackRequest(req.requestID)
		msg := strings.TrimSpace(queueFullMessage)
		if msg == "" {
			msg = "Task queue is full. Please try again later."
		}
		_ = b.sendMessageWithReply(req.chatID, msg, nil, req.replyToID)
		return false
	}
	waitingRequests := b.queuedRequestCount()
	if queueLen > 0 || activeWorkers >= workerLimit {
		if waitingRequests <= 0 {
			waitingRequests = 1
		}
		_ = b.sendMessageWithReply(req.chatID, fmt.Sprintf("Queued. Waiting: %d, running: %d/%d", waitingRequests, activeWorkers, workerLimit), nil, req.replyToID)
	}
	return true
}

func (b *TelegramBot) queuedRequestCount() int {
	if b == nil {
		return 0
	}
	b.requestStateMu.Lock()
	defer b.requestStateMu.Unlock()
	if len(b.activeRequests) == 0 {
		return 0
	}
	count := 0
	for _, request := range b.activeRequests {
		if strings.TrimSpace(request.State) != "running" {
			count++
		}
	}
	return count
}

func (b *TelegramBot) enqueueDownload(chatID int64, replyToID int, single bool, forceRefresh bool, settings ChatDownloadSettings, transferMode string, mediaType string, mediaID string, storefront string, inflightKey string, fn func(session *DownloadSession) error) bool {
	req := &downloadRequest{
		chatID:       chatID,
		replyToID:    replyToID,
		single:       single,
		forceRefresh: forceRefresh,
		taskType:     telegramTaskDownload,
		settings:     settings,
		transferMode: transferMode,
		mediaType:    mediaType,
		mediaID:      mediaID,
		storefront:   storefront,
		inflightKey:  inflightKey,
		requestID:    b.nextRequestID(),
		fn:           fn,
	}
	return b.enqueueTaskRequest(req, "Resource pressure detected. New download tasks are temporarily blocked.", "Download queue is full. Please try again later.")
}

func (b *TelegramBot) enqueueTelegramTask(req *downloadRequest) bool {
	return b.enqueueTaskRequest(req, "Resource pressure detected. New tasks are temporarily blocked.", "Task queue is full. Please try again later.")
}

func (b *TelegramBot) runQueuedRequest(req *downloadRequest) {
	if req == nil {
		return
	}
	if err := b.buildQueuedRequestRunner(req); err != nil {
		if req.chatID != 0 {
			_ = b.sendMessageWithReply(req.chatID, fmt.Sprintf("Failed: %v", err), nil, req.replyToID)
		}
		return
	}
	if req.run == nil {
		if req.chatID != 0 {
			_ = b.sendMessageWithReply(req.chatID, "Failed: task runner is unavailable", nil, req.replyToID)
		}
		return
	}
	if err := req.run(b); err != nil && req.chatID != 0 {
		_ = b.sendMessageWithReply(req.chatID, fmt.Sprintf("Failed: %v", err), nil, req.replyToID)
	}
}

func (b *TelegramBot) trySendCachedTrack(chatID int64, replyToID int, trackID string, settings ChatDownloadSettings) bool {
	normalized := normalizeChatSettings(settings)
	// 仅复用“歌词+封面均内嵌”的缓存音频，避免和关闭内嵌的设置混淆。
	if !normalized.EmbedLyrics || !normalized.EmbedCover {
		return false
	}
	entry, ok := b.getCachedAudio(trackID, b.maxFileBytes, normalized.Format)
	if !ok {
		return false
	}
	if err := b.sendAudioByFileID(chatID, entry, replyToID, trackID); err != nil {
		b.deleteCachedAudio(trackID, entry.Format, entry.Compressed)
		return false
	}
	return true
}

func (b *TelegramBot) trySendCachedBundleZip(chatID int64, mediaType string, mediaID string, replyToID int, settings ChatDownloadSettings) bool {
	if mediaID == "" || mediaType == "" {
		return false
	}
	key := b.bundleZipCacheKey(mediaType, mediaID, settings)
	entry, ok := b.getCachedDocument(key)
	if !ok {
		return false
	}
	if err := b.sendDocumentByFileID(chatID, entry, replyToID); err != nil {
		b.deleteCachedDocument(key)
		return false
	}
	return true
}

func (b *TelegramBot) trySendCachedMusicVideo(chatID int64, replyToID int, mvID string, settings ChatDownloadSettings, variantKey string) bool {
	if mvID == "" {
		return false
	}
	videoKey := b.mvCacheKey(mvID, settings, variantKey, "video")
	if entry, ok := b.getCachedVideo(videoKey); ok {
		if err := b.sendVideoByFileID(chatID, entry, replyToID); err == nil {
			return true
		}
		b.deleteCachedVideo(videoKey)
	}
	documentKey := b.mvCacheKey(mvID, settings, variantKey, "document")
	if entry, ok := b.getCachedDocument(documentKey); ok {
		if err := b.sendDocumentByFileID(chatID, entry, replyToID); err == nil {
			return true
		}
		b.deleteCachedDocument(documentKey)
	}
	return false
}

func shouldUseTelegramCollectionSequentialOneByOne(single bool, transferMode string, mediaType string) bool {
	if single || transferMode != transferModeOneByOne {
		return false
	}
	switch mediaType {
	case mediaTypeAlbum, mediaTypePlaylist, mediaTypeStation, mediaTypeArtistSongs:
		return true
	default:
		return false
	}
}

func (b *TelegramBot) prepareTelegramDownloadSession(single bool, forceRefresh bool, settings ChatDownloadSettings, mediaType string, progress func(phase string, done, total int64)) (*DownloadSession, error) {
	session := b.newBotDownloadSession(Config)
	session.resetState()
	session.DlSelect = false
	session.ForceRedownload = forceRefresh
	if single {
		session.DlSong = true
	}
	session.Config.AacType = settings.AACType
	session.Config.MVAudioType = settings.MVAudioType
	applyTelegramAudioEmbeddingPolicy(session, settings, mediaType)

	switch settings.Format {
	case telegramFormatAtmos:
		session.DlAtmos = true
	case telegramFormatAac:
		session.DlAac = true
	}

	session.Config.ConvertAfterDownload = false
	if settings.Format == telegramFormatFlac {
		session.Config.ConvertAfterDownload = true
		session.Config.ConvertFormat = telegramFormatFlac
		session.Config.ConvertKeepOriginal = false
		session.Config.ConvertSkipLossyToLossless = false
		if _, err := exec.LookPath(session.Config.FFmpegPath); err != nil {
			return nil, fmt.Errorf("ffmpeg not found at '%s'", session.Config.FFmpegPath)
		}
	} else {
		session.Config.ConvertFormat = ""
	}

	session.ActiveProgress = progress
	return session, nil
}

func (b *TelegramBot) resolveCollectionTracksForSequential(session *DownloadSession, mediaType string, mediaID string, storefront string) ([]task.Track, bool, error) {
	if session == nil {
		return nil, false, fmt.Errorf("download session is nil")
	}
	if storefront == "" {
		storefront = Config.Storefront
	}
	switch mediaType {
	case mediaTypeAlbum:
		ctx, err := resolveAlbumMediaStage(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken, "")
		if err != nil {
			return nil, false, err
		}
		prepareAlbumWorkspaceStage(ctx)
		if ctx == nil || ctx.album == nil {
			return nil, false, nil
		}
		return ctx.album.Tracks, false, nil
	case mediaTypePlaylist:
		ctx, err := resolvePlaylistMediaStage(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		if err != nil {
			return nil, false, err
		}
		preparePlaylistWorkspaceStage(ctx)
		if ctx == nil || ctx.playlist == nil {
			return nil, false, nil
		}
		return ctx.playlist.Tracks, false, nil
	case mediaTypeStation:
		ctx, err := resolveStationMediaStage(session, mediaID, b.appleToken, storefront, session.Config.MediaUserToken)
		if err != nil {
			return nil, false, err
		}
		if ctx != nil && ctx.station != nil && ctx.station.Type == "stream" {
			return nil, true, nil
		}
		prepareStationWorkspaceStage(ctx)
		if ctx == nil || ctx.station == nil {
			return nil, false, nil
		}
		return ctx.station.Tracks, false, nil
	case mediaTypeArtistSongs:
		trackIDs, err := b.fetchArtistSongIDs(storefront, mediaID)
		if err != nil {
			return nil, false, err
		}
		return buildArtistSongTrackStubs(trackIDs), false, nil
	default:
		return nil, false, fmt.Errorf("unsupported collection type for sequential handling: %s", mediaType)
	}
}

func shouldReportCollectionTrackProgress(index int, total int) bool {
	if total <= 0 || index < 0 {
		return false
	}
	completed := index + 1
	if completed <= 1 || completed >= total {
		return true
	}
	return completed%downloadStatusTrackStep == 0
}

func (b *TelegramBot) sendArtistSongsProgressUpdate(chatID int64, replyToID int, mediaType string, completed int, total int) {
	if b == nil || b.client == nil {
		return
	}
	if mediaType != mediaTypeArtistSongs || completed <= 0 || total <= 0 {
		return
	}
	if completed%artistSongsProgressNotifyStep != 0 {
		return
	}
	_ = b.sendMessageWithReply(chatID, fmt.Sprintf("%d/%d", completed, total), nil, replyToID)
}

func (b *TelegramBot) runCollectionOneByOneSequential(chatID int64, replyToID int, forceRefresh bool, settings ChatDownloadSettings, mediaType string, mediaID string, storefront string, status *DownloadStatus, statusLabelPrefix string, progress func(phase string, done, total int64)) (bool, bool, error) {
	if !shouldUseTelegramCollectionSequentialOneByOne(false, transferModeOneByOne, mediaType) {
		return false, false, nil
	}
	if storefront == "" {
		storefront = Config.Storefront
	}

	var session *DownloadSession
	var tracks []task.Track
	stationStream := false
	resolveErr := func() error {
		// Keep core serialized to avoid heavy external tool contention.
		b.downloadCoreMu.Lock()
		defer b.downloadCoreMu.Unlock()

		var err error
		session, err = b.prepareTelegramDownloadSession(false, forceRefresh, settings, mediaType, progress)
		if err != nil {
			return err
		}
		tracks, stationStream, err = b.resolveCollectionTracksForSequential(session, mediaType, mediaID, storefront)
		return err
	}()
	if resolveErr != nil {
		return true, false, resolveErr
	}
	if stationStream {
		// Stream station should keep legacy behavior.
		return false, false, nil
	}
	if len(tracks) == 0 {
		return true, false, errNoFilesDownloaded
	}

	sentAny := false
	downloadedAny := false
	sentExtraPaths := make(map[string]struct{})
	totalTracks := len(tracks)

	for idx := range tracks {
		if err := session.downloadContext().Err(); err != nil {
			return true, sentAny, err
		}
		track := &tracks[idx]
		trackLabel := fmt.Sprintf("Track %d/%d", idx+1, totalTracks)
		if strings.TrimSpace(statusLabelPrefix) != "" {
			trackLabel = strings.TrimSpace(statusLabelPrefix) + " · " + trackLabel
		}
		reportTrackProgress := shouldReportCollectionTrackProgress(idx, totalTracks)
		if status != nil && reportTrackProgress {
			status.Update(trackLabel, int64(idx), int64(totalTracks))
		}

		trackID := strings.TrimSpace(track.ID)
		if !forceRefresh && trackID != "" && b.trySendCachedTrack(chatID, replyToID, trackID, settings) {
			sentAny = true
			if status != nil && reportTrackProgress {
				status.Update(trackLabel+" (cached)", int64(idx+1), int64(totalTracks))
			}
			b.sendArtistSongsProgressUpdate(chatID, replyToID, mediaType, idx+1, totalTracks)
			continue
		}

		downloadFailed := false
		// Keep core serialized to avoid heavy external tool contention.
		func() {
			b.downloadCoreMu.Lock()
			defer b.downloadCoreMu.Unlock()
			session.clearDownloadState()
			if mediaType == mediaTypeArtistSongs {
				if err := ripSong(session, trackID, b.appleToken, storefront, session.Config.MediaUserToken); err != nil {
					if ctxErr := session.downloadContext().Err(); ctxErr != nil {
						downloadFailed = true
						return
					}
					fmt.Printf("Failed to rip artist song %s: %v\n", trackID, err)
					session.Counter.Error++
				}
				return
			}
			ripTrack(session, track, b.appleToken, session.Config.MediaUserToken)
		}()
		if downloadFailed {
			return true, sentAny, session.downloadContext().Err()
		}

		downloadedPaths := append([]string{}, session.LastDownloadedPaths...)
		if len(downloadedPaths) == 0 {
			if status != nil && reportTrackProgress {
				status.Update(trackLabel+" (no output)", int64(idx+1), int64(totalTracks))
			}
			b.sendArtistSongsProgressUpdate(chatID, replyToID, mediaType, idx+1, totalTracks)
			continue
		}
		downloadedAny = true

		uploadPaths := downloadedPaths
		if mediaType == mediaTypeAlbum || mediaType == mediaTypeArtistSongs {
			uploadPaths = b.augmentDownloadedPaths(uploadPaths, settings)
		}

		filteredPaths := make([]string, 0, len(uploadPaths))
		primaryCount := len(downloadedPaths)
		filteredPrimaryCount := 0
		for pathIdx, path := range uploadPaths {
			trimmed := strings.TrimSpace(path)
			if trimmed == "" {
				continue
			}
			if pathIdx >= primaryCount {
				if _, exists := sentExtraPaths[trimmed]; exists {
					continue
				}
			} else {
				filteredPrimaryCount++
			}
			filteredPaths = append(filteredPaths, trimmed)
		}
		if len(filteredPaths) == 0 {
			if status != nil && reportTrackProgress {
				status.Update(trackLabel+" (no upload path)", int64(idx+1), int64(totalTracks))
			}
			b.sendArtistSongsProgressUpdate(chatID, replyToID, mediaType, idx+1, totalTracks)
			continue
		}
		if b.cleanupTracker != nil {
			b.cleanupTracker.RecordPaths(filteredPaths)
		}

		sentPrimary := false
		for pathIdx, path := range filteredPaths {
			if pathIdx >= filteredPrimaryCount && !sentPrimary {
				if status != nil {
					status.UpdateSync("Primary media upload failed. Skip extra files (lyrics/cover/animated).", 0, 0)
				}
				break
			}
			if err := b.sendDownloadedPathWithRetry(session, chatID, path, replyToID, status, settings); err != nil {
				sanitized := sanitizeTelegramError(err, b.token)
				fmt.Printf("send file error (%s): %s\n", path, sanitized)
				appendRuntimeErrorLogf("send file error (%s): %s", path, sanitized)
				if status != nil && reportTrackProgress {
					status.Update(fmt.Sprintf("Failed to send %s: %v", filepath.Base(path), err), 0, 0)
				}
				continue
			}
			if pathIdx < filteredPrimaryCount {
				sentPrimary = true
				sentAny = true
			} else {
				sentExtraPaths[path] = struct{}{}
			}
		}
		if status != nil && reportTrackProgress {
			status.Update(trackLabel, int64(idx+1), int64(totalTracks))
		}
		b.sendArtistSongsProgressUpdate(chatID, replyToID, mediaType, idx+1, totalTracks)
	}

	if forceRefresh && downloadedAny && mediaType != "" && mediaID != "" {
		// Refresh should only drop stale Telegram file_id caches after fresh output exists.
		b.purgeTargetCaches(&AppleURLTarget{MediaType: mediaType, ID: mediaID})
	}
	if !sentAny && !downloadedAny {
		return true, false, errNoFilesDownloaded
	}
	return true, sentAny, nil
}

func (b *TelegramBot) runDownload(chatID int64, fn func(session *DownloadSession) error, single bool, forceRefresh bool, replyToID int, settings ChatDownloadSettings, transferMode string, mediaType string, mediaID string, storefront string) {
	var status *DownloadStatus
	startedAt := time.Now()
	taskResult := "unknown"
	fmt.Printf("telegram task start chat=%d media=%s:%s storefront=%s transfer=%s single=%t refresh=%t\n", chatID, mediaType, mediaID, storefront, transferMode, single, forceRefresh)
	defer func() {
		if rec := recover(); rec != nil {
			taskResult = "panic"
			scope := fmt.Sprintf("telegram runDownload chat=%d media=%s:%s", chatID, mediaType, mediaID)
			_ = logRecoveredPanic(scope, rec)
			if status != nil {
				status.UpdateSync("任务异常已记录，已自动跳过当前任务。", 0, 0)
				status.Stop()
			} else {
				_ = b.sendMessageWithReply(chatID, "任务异常已记录，已自动跳过当前任务。", nil, replyToID)
			}
		}
		fmt.Printf("telegram task end chat=%d media=%s:%s result=%s elapsed=%s\n", chatID, mediaType, mediaID, taskResult, time.Since(startedAt).Round(time.Millisecond))
	}()

	settings = normalizeChatSettings(settings)
	transferMode = normalizeTransferModeForMedia(transferMode, mediaType, single)

	var err error
	status, err = newDownloadStatus(b, chatID, replyToID)
	if err != nil {
		taskResult = fmt.Sprintf("status-create-failed: %v", err)
		_ = b.sendMessageWithReply(chatID, fmt.Sprintf("Failed to create status message: %v", err), nil, replyToID)
		return
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		status.finishFailure()
	}()

	progress := func(phase string, done, total int64) {
		status.Update(phase, done, total)
	}
	if mediaType == mediaTypeArtistLPs {
		result, lpErr := b.downloadArtistLPAlbumsCollection(chatID, replyToID, forceRefresh, settings, mediaID, storefront, transferMode, status)
		if lpErr != nil {
			if errors.Is(lpErr, errNoFilesDownloaded) {
				taskResult = "no-files(lp)"
				status.UpdateSync("No files were downloaded.", 0, 0)
				return
			}
			taskResult = fmt.Sprintf("failed(lp): %s", sanitizeTelegramError(lpErr, b.token))
			status.UpdateSync(fmt.Sprintf("Failed: %v", lpErr), 0, 0)
			return
		}
		if result.Failed > 0 {
			_ = b.sendMessageWithReply(chatID, fmt.Sprintf("All LPs finished: success=%d/%d, failed=%d.", result.Succeeded, result.Total, result.Failed), nil, replyToID)
		}
		if result.SentAny {
			taskResult = "success(lp)"
			completed = true
			status.finishSuccess()
		} else {
			taskResult = "no-upload-success(lp)"
			status.UpdateSync("No LP was uploaded successfully.", 0, 0)
		}
		return
	}
	if shouldUseTelegramCollectionSequentialOneByOne(single, transferMode, mediaType) {
		handled, sentAny, seqErr := b.runCollectionOneByOneSequential(chatID, replyToID, forceRefresh, settings, mediaType, mediaID, storefront, status, "", progress)
		if handled {
			if seqErr != nil {
				if errors.Is(seqErr, errNoFilesDownloaded) {
					taskResult = "no-files(seq)"
					status.UpdateSync("No files were downloaded.", 0, 0)
					return
				}
				taskResult = fmt.Sprintf("failed(seq): %s", sanitizeTelegramError(seqErr, b.token))
				status.UpdateSync(fmt.Sprintf("Failed: %v", seqErr), 0, 0)
				return
			}
			if sentAny {
				taskResult = "success(seq)"
				completed = true
				status.finishSuccess()
			} else {
				taskResult = "no-upload-success(seq)"
			}
			return
		}
	}

	paths := []string{}
	primaryCount := 0
	var session *DownloadSession
	coreErr := func() error {
		// Keep core serialized to avoid heavy external tool contention.
		b.downloadCoreMu.Lock()
		defer b.downloadCoreMu.Unlock()

		session, err = b.prepareTelegramDownloadSession(single, forceRefresh, settings, mediaType, progress)
		if err != nil {
			return err
		}

		status.Update("Downloading", 0, 0)
		if err := fn(session); err != nil {
			return err
		}

		paths = append([]string{}, session.LastDownloadedPaths...)
		primaryCount = len(paths)
		if len(paths) == 0 {
			return errNoFilesDownloaded
		}
		if mediaType == mediaTypeSong || mediaType == mediaTypeAlbum || mediaType == mediaTypeArtistSongs {
			paths = b.augmentDownloadedPaths(paths, settings)
		}
		fmt.Printf("telegram task ready chat=%d media=%s:%s primary=%d total=%d transfer=%s\n", chatID, mediaType, mediaID, primaryCount, len(paths), transferMode)
		return nil
	}()
	if coreErr != nil {
		if errors.Is(coreErr, errNoFilesDownloaded) {
			taskResult = "no-files"
			status.UpdateSync("No files were downloaded.", 0, 0)
			return
		}
		taskResult = fmt.Sprintf("failed: %s", sanitizeTelegramError(coreErr, b.token))
		status.UpdateSync(fmt.Sprintf("Failed: %v", coreErr), 0, 0)
		return
	}
	if forceRefresh && mediaType != "" && mediaID != "" {
		// Refresh should only drop stale Telegram file_id caches after fresh output exists.
		b.purgeTargetCaches(&AppleURLTarget{MediaType: mediaType, ID: mediaID})
	}
	if b.cleanupTracker != nil {
		b.cleanupTracker.RecordPaths(paths)
	}

	if transferMode == transferModeZip {
		if status != nil {
			status.Update("Zipping", 0, 0)
		}
		zipPath, displayName, err := createZipFromPaths(paths)
		if err != nil {
			taskResult = fmt.Sprintf("zip-create-failed: %v", err)
			status.UpdateSync(fmt.Sprintf("Failed to create ZIP: %v", err), 0, 0)
			return
		}
		defer os.Remove(zipPath)
		cacheKey := ""
		if mediaID != "" && mediaType != "" {
			cacheKey = b.bundleZipCacheKey(mediaType, mediaID, settings)
		}
		if err := b.sendDocumentFile(chatID, zipPath, displayName, replyToID, status, cacheKey); err != nil {
			sanitized := sanitizeTelegramError(err, b.token)
			fmt.Println("send ZIP error:", sanitized)
			appendRuntimeErrorLogf("send ZIP error (%s:%s): %s", mediaType, mediaID, sanitized)
			if strings.Contains(strings.ToLower(err.Error()), "zip exceeds telegram limit") {
				fmt.Printf("telegram zip fallback chat=%d media=%s:%s reason=%s\n", chatID, mediaType, mediaID, sanitized)
				status.UpdateSync("ZIP exceeds Telegram limit, fallback to one-by-one transfer.", 0, 0)
			} else {
				taskResult = fmt.Sprintf("zip-send-failed: %s", sanitized)
				status.UpdateSync(fmt.Sprintf("Failed to send ZIP: %v", err), 0, 0)
				return
			}
		} else {
			taskResult = "success(zip)"
			completed = true
			status.finishSuccess()
			return
		}
	}
	sentAny := b.sendPreparedDownloadPaths(session, chatID, replyToID, status, settings, paths, primaryCount)
	if sentAny {
		taskResult = "success"
		completed = true
		status.finishSuccess()
	} else if taskResult == "unknown" {
		taskResult = "no-upload-success"
	}
}

type downloadFileEntry struct {
	path    string
	size    int64
	modTime time.Time
	owner   string
	mode    string
}

func (b *TelegramBot) cleanupDownloadsIfNeeded() {
	if b.cleanupTracker == nil {
		return
	}
	b.cleanupTracker.cleanupOnce(false)
}

func telegramCleanupRoots() []sharedstorage.CleanupRoot {
	return sharedstorage.TelegramCleanupRoots(&Config)
}

func telegramCleanupRootPaths() []string {
	return sharedstorage.CleanupRootPaths(telegramCleanupRoots())
}

func scanDownloadFolder(root string, cacheFile string) (int64, []downloadFileEntry, error) {
	var totalSize int64
	entries := []downloadFileEntry{}
	cachePath := ""
	if cacheFile != "" {
		cachePath = filepath.Clean(cacheFile)
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if cachePath != "" && filepath.Clean(path) == cachePath {
			return nil
		}
		size := info.Size()
		totalSize += size
		entries = append(entries, downloadFileEntry{
			path:    path,
			size:    size,
			modTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return totalSize, entries, err
	}
	return totalSize, entries, nil
}

func createZipFromPaths(paths []string) (string, string, error) {
	if len(paths) == 0 {
		return "", "", fmt.Errorf("no files to zip")
	}
	displayName := zipDisplayName(paths)
	tmpDir := chooseZipTempDir(paths)
	tmp, err := os.CreateTemp(tmpDir, "amdl-*.zip")
	if err != nil && tmpDir != "" {
		tmp, err = os.CreateTemp("", "amdl-*.zip")
	}
	if err != nil {
		return "", "", err
	}
	tmpPath := tmp.Name()
	zipWriter := zip.NewWriter(tmp)
	fail := func(err error) (string, string, error) {
		_ = zipWriter.Close()
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", "", err
	}
	rootDir := commonZipRoot(paths)
	added := 0
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return fail(err)
		}
		if info.IsDir() {
			continue
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return fail(err)
		}
		relName := filepath.Base(path)
		if rootDir != "" {
			if rel, err := filepath.Rel(rootDir, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				relName = rel
			}
		}
		header.Name = filepath.ToSlash(relName)
		header.Method = zip.Deflate
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return fail(err)
		}
		file, err := os.Open(path)
		if err != nil {
			return fail(err)
		}
		_, err = io.Copy(writer, file)
		file.Close()
		if err != nil {
			return fail(err)
		}
		added++
	}
	if err := zipWriter.Close(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", "", err
	}
	if added == 0 {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("no files to zip")
	}
	return tmpPath, displayName, nil
}

func chooseZipTempDir(paths []string) string {
	candidates := []string{}
	if envDir := strings.TrimSpace(os.Getenv("AMDL_TMPDIR")); envDir != "" {
		candidates = append(candidates, envDir)
	}
	if root := commonZipRoot(paths); root != "" {
		candidates = append(candidates, root)
	}
	candidates = append(candidates,
		strings.TrimSpace(Config.TelegramDownloadFolder),
		strings.TrimSpace(Config.AlacSaveFolder),
		strings.TrimSpace(Config.AtmosSaveFolder),
		strings.TrimSpace(Config.AacSaveFolder),
	)
	seen := make(map[string]struct{}, len(candidates))
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		clean := filepath.Clean(dir)
		if clean == "." || clean == string(filepath.Separator) {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		info, err := os.Stat(clean)
		if err == nil && info.IsDir() {
			return clean
		}
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(clean, 0755); mkErr == nil {
				return clean
			}
		}
	}
	return ""
}

func zipDisplayName(paths []string) string {
	root := commonZipRoot(paths)
	if root == "" {
		return "album.zip"
	}
	base := filepath.Base(root)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "album.zip"
	}
	return base + ".zip"
}

func commonZipRoot(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	root := filepath.Dir(paths[0])
	for _, path := range paths[1:] {
		dir := filepath.Dir(path)
		for !isParentDir(root, dir) {
			parent := filepath.Dir(root)
			if parent == root {
				return root
			}
			root = parent
		}
	}
	return root
}

func isParentDir(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..")
}

func fileExistsRegular(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func (b *TelegramBot) augmentDownloadedPaths(paths []string, settings ChatDownloadSettings) []string {
	if len(paths) == 0 {
		return paths
	}
	normalized := normalizeChatSettings(settings)
	if !normalized.AutoLyrics && !normalized.AutoCover && !normalized.AutoAnimated {
		return paths
	}
	result := append([]string{}, paths...)
	seen := make(map[string]struct{}, len(result))
	for _, path := range result {
		seen[path] = struct{}{}
	}
	coverDone := make(map[string]struct{})
	animatedDone := make(map[string]struct{})
	appendFile := func(path string) {
		if !fileExistsRegular(path) {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	isAudioPath := func(path string) bool {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".m4a", ".flac", ".mp3", ".aac", ".wav", ".opus":
			return true
		default:
			return false
		}
	}
	for _, path := range paths {
		dir := filepath.Dir(path)
		if normalized.AutoCover {
			if _, ok := coverDone[dir]; !ok {
				coverDone[dir] = struct{}{}
				appendFile(findCoverFile(dir))
			}
		}
		if normalized.AutoAnimated {
			if _, ok := animatedDone[dir]; !ok {
				animatedDone[dir] = struct{}{}
				appendFile(preferredAnimatedArtworkPath(dir))
			}
		}
		if normalized.AutoLyrics && isAudioPath(path) {
			ext := strings.ToLower(filepath.Ext(path))
			lyricsPath := strings.TrimSuffix(path, ext) + "." + normalized.LyricsFormat
			appendFile(lyricsPath)
		}
	}
	return result
}

func isRetryableUploadError(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := parseTelegramRetryAfter(err); ok {
		return true
	}
	lower := strings.ToLower(err.Error())
	retryableHints := []string{
		"context deadline exceeded",
		"client.timeout exceeded",
		"tls handshake timeout",
		"connection reset by peer",
		"use of closed network connection",
		"broken pipe",
		"read/write on closed pipe",
		"i/o timeout",
		"eof",
		"unexpected eof",
		"bad gateway",
		"temporarily unavailable",
		"too many requests",
		"file_parts_invalid",
	}
	for _, hint := range retryableHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func parseTelegramRetryAfter(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	msg := err.Error()
	for _, re := range []*regexp.Regexp{retryAfterJSONRe, retryAfterTextRe} {
		matches := re.FindStringSubmatch(msg)
		if len(matches) < 2 {
			continue
		}
		sec, convErr := strconv.Atoi(matches[1])
		if convErr != nil || sec <= 0 {
			continue
		}
		return time.Duration(sec) * time.Second, true
	}
	return 0, false
}

func isTelegramRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errTelegramSendDeferred) {
		return true
	}
	if _, ok := parseTelegramRetryAfter(err); ok {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "429") && strings.Contains(lower, "too many requests")
}

func telegramAPIHTTPError(operation string, status string, responseBody []byte) error {
	bodyText := strings.TrimSpace(string(bytes.TrimSpace(responseBody)))
	if bodyText == "" {
		return fmt.Errorf("%s failed: %s", operation, status)
	}
	return fmt.Errorf("%s failed: %s %s", operation, status, bodyText)
}

func telegramAPIResponseError(operation string, description string, params apiResponseParameters) error {
	description = strings.TrimSpace(description)
	if params.RetryAfter > 0 {
		if description == "" {
			description = fmt.Sprintf(`{"parameters":{"retry_after":%d}}`, params.RetryAfter)
		} else {
			description = fmt.Sprintf(`%s {"parameters":{"retry_after":%d}}`, description, params.RetryAfter)
		}
	}
	if description == "" {
		return fmt.Errorf("%s error", operation)
	}
	return fmt.Errorf("%s error: %s", operation, description)
}

func isPipeClosedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "read/write on closed pipe")
}

func (b *TelegramBot) answerCallbackQuery(callbackID string) error {
	if callbackID == "" {
		return nil
	}
	payload := map[string]any{
		"callback_query_id": callbackID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", b.apiURL("answerCallbackQuery"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (b *TelegramBot) answerInlineQuery(inlineQueryID string, results any, personal bool) error {
	if inlineQueryID == "" {
		return nil
	}
	payload := map[string]any{
		"inline_query_id": inlineQueryID,
		"results":         results,
		"is_personal":     personal,
		"cache_time":      0,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", b.apiURL("answerInlineQuery"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (b *TelegramBot) editMessageText(chatID int64, messageID int, text string, markup any) error {
	return b.editMessageTextWithMode(chatID, messageID, text, markup, false)
}

func (b *TelegramBot) editMessageTextBestEffort(chatID int64, messageID int, text string, markup any) error {
	return b.editMessageTextWithMode(chatID, messageID, text, markup, true)
}

func (b *TelegramBot) editMessageTextWithMode(chatID int64, messageID int, text string, markup any, bestEffort bool) error {
	if messageID == 0 {
		return nil
	}
	ctx := b.operationContext()
	if bestEffort {
		if !b.tryTelegramSend(chatID) {
			return errTelegramSendDeferred
		}
	} else {
		if err := b.waitTelegramSend(ctx, chatID); err != nil {
			return err
		}
	}
	text = b.localizeOutgoingText(chatID, text)
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.apiURL("editMessageText"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		apiResp := apiResponse{}
		if err := json.Unmarshal(responseBody, &apiResp); err == nil {
			if strings.Contains(apiResp.Description, "message is not modified") {
				return nil
			}
			if apiResp.Description != "" {
				err = fmt.Errorf("telegram editMessageText error: %s", apiResp.Description)
				b.noteTelegramRateLimit(err)
				return err
			}
		}
		err = fmt.Errorf("telegram editMessageText failed: %s", strings.TrimSpace(string(responseBody)))
		b.noteTelegramRateLimit(err)
		return err
	}
	apiResp := apiResponse{}
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		if strings.Contains(apiResp.Description, "message is not modified") {
			return nil
		}
		err = fmt.Errorf("telegram editMessageText error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	return nil
}

func (b *TelegramBot) deleteMessage(chatID int64, messageID int) error {
	if messageID == 0 {
		return nil
	}
	b.clearAutoDeleteMessage(chatID, messageID)
	b.clearPanelStateByMessage(chatID, messageID)
	ctx := b.operationContext()
	if err := b.waitTelegramSend(ctx, chatID); err != nil {
		return err
	}
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.apiURL("deleteMessage"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		apiResp := apiResponse{}
		if err := json.Unmarshal(responseBody, &apiResp); err == nil && strings.TrimSpace(apiResp.Description) != "" {
			if isIgnorableDeleteMessageError(apiResp.Description) {
				return nil
			}
			err = fmt.Errorf("telegram deleteMessage failed: %s", strings.TrimSpace(apiResp.Description))
			b.noteTelegramRateLimit(err)
			return err
		}
		err = fmt.Errorf("telegram deleteMessage failed: %s", strings.TrimSpace(string(responseBody)))
		b.noteTelegramRateLimit(err)
		return err
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return nil
	}
	apiResp := apiResponse{}
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		return err
	}
	if !apiResp.OK {
		if isIgnorableDeleteMessageError(apiResp.Description) {
			return nil
		}
		err = fmt.Errorf("telegram deleteMessage error: %s", apiResp.Description)
		b.noteTelegramRateLimit(err)
		return err
	}
	return nil
}

func isIgnorableDeleteMessageError(description string) bool {
	desc := strings.ToLower(strings.TrimSpace(description))
	return strings.Contains(desc, "message to delete not found")
}

func (b *TelegramBot) getUpdates(offset int) ([]Update, error) {
	return b.getUpdatesWithOptions(offset, 30, 0)
}

func (b *TelegramBot) getUpdatesWithOptions(offset int, timeoutSec int, limit int) ([]Update, error) {
	req, err := http.NewRequestWithContext(b.shutdownContext(), "GET", b.apiURL("getUpdates"), nil)
	if err != nil {
		return nil, err
	}
	query := req.URL.Query()
	if timeoutSec < 0 {
		timeoutSec = 0
	}
	query.Set("timeout", strconv.Itoa(timeoutSec))
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}
	req.URL.RawQuery = query.Encode()
	pollClient := b.pollClient
	if pollClient == nil {
		pollClient = b.client
	}
	resp, err := pollClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(resp.Body)
		apiResp := apiResponse{}
		if err := json.Unmarshal(responseBody, &apiResp); err == nil && strings.TrimSpace(apiResp.Description) != "" {
			return nil, fmt.Errorf("getUpdates failed: %s (%s)", resp.Status, apiResp.Description)
		}
		return nil, fmt.Errorf("getUpdates failed: %s", resp.Status)
	}
	var data getUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if !data.OK {
		return nil, fmt.Errorf("getUpdates error: %s", data.Description)
	}
	return data.Result, nil
}

func (b *TelegramBot) shutdownContext() context.Context {
	if b == nil || b.shutdownCtx == nil {
		return context.Background()
	}
	return b.shutdownCtx
}

func (b *TelegramBot) requestShutdown() {
	if b == nil || b.shutdownCancel == nil {
		return
	}
	b.shutdownCancel()
}

func (b *TelegramBot) isShuttingDown() bool {
	if b == nil || b.shutdownCtx == nil {
		return false
	}
	select {
	case <-b.shutdownCtx.Done():
		return true
	default:
		return false
	}
}

func (b *TelegramBot) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", b.apiBase, b.token, method)
}

func messageUserID(msg *Message) int64 {
	if msg == nil || msg.From == nil {
		return 0
	}
	return msg.From.ID
}

func callbackUserID(cb *CallbackQuery) int64 {
	if cb == nil || cb.From == nil {
		return 0
	}
	return cb.From.ID
}

func inlineQueryUserID(q *InlineQuery) int64 {
	if q == nil || q.From == nil {
		return 0
	}
	return q.From.ID
}

func parseTelegramUserID(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("user_id is empty")
	}
	userID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || userID <= 0 {
		return 0, fmt.Errorf("invalid user_id")
	}
	return userID, nil
}

func resolveAdminTargetUserID(args []string, replyTo *Message) (int64, error) {
	if len(args) > 0 {
		return parseTelegramUserID(args[0])
	}
	if replyTo != nil && replyTo.From != nil && replyTo.From.ID > 0 {
		return replyTo.From.ID, nil
	}
	return 0, fmt.Errorf("target user is required")
}

func boolPtr(v bool) *bool {
	value := v
	return &value
}

func snapshotIDSet(items map[int64]bool) []int64 {
	if len(items) == 0 {
		return nil
	}
	out := make([]int64, 0, len(items))
	for id, enabled := range items {
		if !enabled || id == 0 {
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (b *TelegramBot) isAdminUser(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	return b.adminUsers[userID]
}

func (b *TelegramBot) isAllowedChat(chatID int64) bool {
	if len(b.allowedChats) == 0 {
		return true
	}
	return b.allowedChats[chatID]
}

func (b *TelegramBot) isUserWhitelisted(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return b.userWhitelist[userID]
}

func (b *TelegramBot) isUserBlacklisted(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return b.userBlacklist[userID]
}

func (b *TelegramBot) isUserWhitelistEnabled() bool {
	if b == nil {
		return false
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return b.userWhitelistEnabled
}

func (b *TelegramBot) isAllowedUser(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	if b.isAdminUser(userID) {
		return true
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	if b.userBlacklist[userID] {
		return false
	}
	if !b.userWhitelistEnabled {
		return true
	}
	return b.userWhitelist[userID]
}

func (b *TelegramBot) setUserWhitelistEnabled(enabled bool) bool {
	if b == nil {
		return false
	}
	b.accessMu.Lock()
	changed := b.userWhitelistEnabled != enabled
	b.userWhitelistEnabled = enabled
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) isForwardEnabled() bool {
	if b == nil {
		return false
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return b.forwardEnabled
}

func (b *TelegramBot) setForwardEnabled(enabled bool) bool {
	if b == nil {
		return false
	}
	b.accessMu.Lock()
	changed := b.forwardEnabled != enabled
	b.forwardEnabled = enabled
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) addUserWhitelist(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.Lock()
	if b.userWhitelist == nil {
		b.userWhitelist = make(map[int64]bool)
	}
	changed := !b.userWhitelist[userID]
	b.userWhitelist[userID] = true
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) removeUserWhitelist(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.Lock()
	changed := b.userWhitelist[userID]
	delete(b.userWhitelist, userID)
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) addUserBlacklist(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.Lock()
	if b.userBlacklist == nil {
		b.userBlacklist = make(map[int64]bool)
	}
	changed := !b.userBlacklist[userID]
	b.userBlacklist[userID] = true
	delete(b.userWhitelist, userID)
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) removeUserBlacklist(userID int64) bool {
	if b == nil || userID == 0 {
		return false
	}
	b.accessMu.Lock()
	changed := b.userBlacklist[userID]
	delete(b.userBlacklist, userID)
	b.accessMu.Unlock()
	if changed {
		b.requestStateSave()
	}
	return changed
}

func (b *TelegramBot) userWhitelistCount() int {
	if b == nil {
		return 0
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return len(b.userWhitelist)
}

func (b *TelegramBot) userBlacklistCount() int {
	if b == nil {
		return 0
	}
	b.accessMu.RLock()
	defer b.accessMu.RUnlock()
	return len(b.userBlacklist)
}

func (b *TelegramBot) adminUserCount() int {
	if b == nil {
		return 0
	}
	return len(b.adminUsers)
}

func (b *TelegramBot) isCachePushRunning() bool {
	if b == nil {
		return false
	}
	b.adminTaskMu.Lock()
	defer b.adminTaskMu.Unlock()
	return b.cachePushRunning
}

func (b *TelegramBot) beginCachePush() bool {
	if b == nil {
		return false
	}
	b.adminTaskMu.Lock()
	defer b.adminTaskMu.Unlock()
	if b.cachePushRunning {
		return false
	}
	b.cachePushRunning = true
	return true
}

func (b *TelegramBot) endCachePush() {
	if b == nil {
		return
	}
	b.adminTaskMu.Lock()
	b.cachePushRunning = false
	b.adminTaskMu.Unlock()
}

type cachedAudioItem struct {
	Key     string
	TrackID string
	Entry   CachedAudio
}

func cachedAudioSortTime(entry CachedAudio) time.Time {
	if !entry.CreatedAt.IsZero() {
		return entry.CreatedAt
	}
	return entry.UpdatedAt
}

func (b *TelegramBot) listCachedAudioItems() []cachedAudioItem {
	if b == nil {
		return nil
	}
	b.cacheMu.Lock()
	items := make([]cachedAudioItem, 0, len(b.cache))
	for key, entry := range b.cache {
		parts := strings.Split(key, "|")
		if len(parts) < 3 {
			continue
		}
		items = append(items, cachedAudioItem{
			Key:     key,
			TrackID: strings.TrimSpace(parts[0]),
			Entry:   entry,
		})
	}
	b.cacheMu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		left := cachedAudioSortTime(items[i].Entry)
		right := cachedAudioSortTime(items[j].Entry)
		switch {
		case left.IsZero() && !right.IsZero():
			return false
		case !left.IsZero() && right.IsZero():
			return true
		case !left.Equal(right):
			return left.Before(right)
		default:
			return items[i].Key < items[j].Key
		}
	})
	return items
}

func (b *TelegramBot) formatAdminPanelText(chatID int64) string {
	whitelistMode := "关闭"
	if b.isUserWhitelistEnabled() {
		whitelistMode = "开启"
	}
	forwardStatus := "关闭"
	if b.isForwardEnabled() {
		forwardStatus = "开启"
	}
	forwardChatLabel := "未配置"
	if b != nil && b.forwardChatID != 0 {
		forwardChatLabel = strconv.FormatInt(b.forwardChatID, 10)
	}
	cachePushStatus := "空闲"
	if b.isCachePushRunning() {
		cachePushStatus = "运行中"
	}
	if b.getChatLanguage(chatID) == telegramLanguageEn {
		return fmt.Sprintf("Admin panel\n- Whitelist mode: %s\n- Archive forward: %s\n- Archive chat_id: %s\n- Cache push task: %s\n- Admin users: %d\n- Whitelisted users: %d\n- Blacklisted users: %d",
			map[bool]string{true: "ON", false: "OFF"}[b.isUserWhitelistEnabled()],
			map[bool]string{true: "ON", false: "OFF"}[b.isForwardEnabled()],
			forwardChatLabel,
			map[bool]string{true: "RUNNING", false: "IDLE"}[b.isCachePushRunning()],
			b.adminUserCount(),
			b.userWhitelistCount(),
			b.userBlacklistCount(),
		)
	}
	return fmt.Sprintf("管理员面板\n- 白名单模式：%s\n- 归档转发：%s\n- 归档群 chat_id：%s\n- 缓存转存任务：%s\n- 管理员数量：%d\n- 白名单人数：%d\n- 黑名单人数：%d",
		whitelistMode,
		forwardStatus,
		forwardChatLabel,
		cachePushStatus,
		b.adminUserCount(),
		b.userWhitelistCount(),
		b.userBlacklistCount(),
	)
}

func (b *TelegramBot) buildAdminPanelKeyboard(confirmCachePush bool) InlineKeyboardMarkup {
	whitelistLabel := "白名单：关闭"
	if b.isUserWhitelistEnabled() {
		whitelistLabel = "白名单：开启"
	}
	forwardLabel := "归档转发：关闭"
	if b.isForwardEnabled() {
		forwardLabel = "归档转发：开启"
	}
	rows := [][]InlineKeyboardButton{
		{{Text: whitelistLabel, CallbackData: "admin:whitelist:toggle"}},
		{{Text: forwardLabel, CallbackData: "admin:forward:toggle"}},
	}
	if confirmCachePush {
		rows = append(rows,
			[]InlineKeyboardButton{
				{Text: "确认转存", CallbackData: "admin:cachepush:run"},
				{Text: "返回", CallbackData: "admin:refresh"},
			},
		)
	} else {
		rows = append(rows,
			[]InlineKeyboardButton{
				{Text: "缓存转存到归档群", CallbackData: "admin:cachepush:confirm"},
			},
		)
	}
	rows = append(rows,
		[]InlineKeyboardButton{
			{Text: "刷新", CallbackData: "admin:refresh"},
			{Text: "关闭", CallbackData: "admin:close"},
		},
	)
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *TelegramBot) openAdminPanel(chatID int64, replyToID int) {
	_ = b.sendMessageWithReply(chatID, b.formatAdminPanelText(chatID), b.buildAdminPanelKeyboard(false), replyToID)
}

func formatWhoAmIText(userID int64, chatID int64) string {
	return fmt.Sprintf("用户ID: %d\n会话ID(chat_id): %d", userID, chatID)
}

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

func botHelpText() string {
	return strings.TrimSpace(`
命令列表（短命令）：
/h 帮助
/i 查看当前会话ID（chat_id）；也可按资源ID下载
/sg <关键词> 搜索歌曲
/sa <关键词> 搜索专辑
/sr <关键词> 搜索艺人
/s <类型> <关键词> 统一搜索
/u <Apple Music 链接> 解析并下载链接
/rf <Apple Music 链接> 强制重下并重传（清缓存，跳过本地复用）
/ap <艺人-url|艺人-id> 导出艺人主页图+专辑封面+动态封面（逐个/ZIP）
/cv <url|type id> 仅下载封面
/ac <url|type id> 仅下载动态封面
/ly <song|album> 导出歌词文件（格式由设置决定）
/st [值] 查看或修改下载设置（音质/AAC/MV/歌词/歌曲ZIP/任务线程/内嵌开关/自动附加/歌曲赏析）
/amadmin 管理员面板（仅管理员）
/amwlon 开启用户白名单模式（仅管理员）
/amwloff 关闭用户白名单模式（仅管理员）
/amwladd <user_id> 添加白名单用户（仅管理员）
/amwldel <user_id> 移除白名单用户（仅管理员）
/amban <user_id> 封禁用户（仅管理员）
/amunban <user_id> 解除封禁（仅管理员）
/amcachepush 批量转存缓存音频到归档群（仅管理员）
/amwhoami 查看当前 user_id / chat_id

参数说明：
- /s 的 <类型>：song | album | artist
- /cv 的 type：song | album | playlist | station | mv | artist
- /ac 的 type：song | album | playlist | station
- /st 任务线程：worker1 | worker2 | worker3 | worker4（默认 worker1）
- /st 语言：zh | en
- /st 赏析：comment | comment_on | comment_off

也支持直接发送 Apple Music 链接（自动识别）：
song | album | playlist | artist | station | music-video
`)
}

func (b *TelegramBot) botHelpTextForChat(chatID int64) string {
	if b.getChatLanguage(chatID) == telegramLanguageEn {
		return strings.TrimSpace(`
Command list (short aliases):
/h Help
/i Show current chat_id; also supports resource-id downloads
/sg <keywords> Search songs
/sa <keywords> Search albums
/sr <keywords> Search artists
/s <type> <keywords> Unified search
/u <Apple Music URL> Parse and download URL
/rf <Apple Music URL> Force refresh and resend (clear cache, skip local reuse)
/ap <artist-url|artist-id> Export artist photo + album covers + animated covers (one-by-one/ZIP)
/cv <url|type id> Download static cover only
/ac <url|type id> Download animated cover only
/ly <song|album> Export lyrics files (format depends on settings)
/st [value] View or update settings (quality/AAC/MV/lyrics/song ZIP/workers/embed toggles/auto extras/song comment/language)
/amadmin Admin panel (admin only)
/amwlon Enable user whitelist mode (admin only)
/amwloff Disable user whitelist mode (admin only)
/amwladd <user_id> Add a whitelisted user (admin only)
/amwldel <user_id> Remove a whitelisted user (admin only)
/amban <user_id> Ban a user (admin only)
/amunban <user_id> Unban a user (admin only)
/amcachepush Push cached audio items to archive chat (admin only)
/amwhoami Show current user_id / chat_id

Parameters:
- /s <type>: song | album | artist
- /cv type: song | album | playlist | station | mv | artist
- /ac type: song | album | playlist | station
- /st workers: worker1 | worker2 | worker3 | worker4 (default worker1)
- /st language: zh | en
- /st song comment: comment | comment_on | comment_off

You can also send Apple Music URLs directly (auto recognized):
song | album | playlist | artist | station | music-video
`)
	}
	return botHelpText()
}

func formatChatIDText(chatID int64) string {
	return fmt.Sprintf(
		"会话ID (chat_id): %d\n"+
			"把这个值写进 config.yaml 白名单：\n"+
			"telegram-allowed-chat-ids:\n"+
			"  - %d",
		chatID,
		chatID,
	)
}

func (b *TelegramBot) formatChatIDTextForChat(chatID int64) string {
	if b.getChatLanguage(chatID) == telegramLanguageEn {
		return fmt.Sprintf(
			"Session ID (chat_id): %d\n"+
				"Use this value in config.yaml whitelist:\n"+
				"telegram-allowed-chat-ids:\n"+
				"  - %d",
			chatID,
			chatID,
		)
	}
	return formatChatIDText(chatID)
}

func (b *TelegramBot) localizeOutgoingText(chatID int64, text string) string {
	lang := b.getChatLanguage(chatID)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return text
	}
	if lang == telegramLanguageEn {
		switch trimmed {
		case "Not authorized for this bot.":
			return "Not authorized for this bot."
		case "已取消。":
			return "Canceled."
		case "该会话未被授权使用此机器人。":
			return "Not authorized for this bot."
		case "请选择发送方式：":
			return "Choose transfer method:"
		case "请选择歌词导出方式：":
			return "Choose lyrics export method:"
		case "请选择艺人素材导出方式：":
			return "Choose artist assets export method:"
		case "无效的 Apple Music 链接。":
			return "Invalid Apple Music URL."
		case "无效的目标。":
			return "Invalid target."
		case "未知命令。发送 /help 查看用法。":
			return "Unknown command. Send /help for usage."
		}
		if strings.HasPrefix(trimmed, "用法：") {
			return "Usage: " + strings.TrimPrefix(trimmed, "用法：")
		}
		if strings.HasPrefix(trimmed, "失败：") {
			return "Failed: " + strings.TrimPrefix(trimmed, "失败：")
		}
		if strings.HasPrefix(trimmed, "搜索失败：") {
			return "Search failed: " + strings.TrimPrefix(trimmed, "搜索失败：")
		}
		return text
	}

	switch trimmed {
	case "Not authorized for this bot.":
		return "该会话未被授权使用此机器人。"
	case "Unknown command. Send /help for usage.":
		return "未知命令。发送 /help 查看用法。"
	case "Choose transfer method:":
		return "请选择发送方式："
	case "Choose lyrics export method:":
		return "请选择歌词导出方式："
	case "Choose artist assets export method:":
		return "请选择艺人素材导出方式："
	case "Selection expired. Please search again.":
		return "选择已过期，请重新搜索。"
	case "Selection out of range.":
		return "选择超出范围。"
	case "No results found.":
		return "没有搜索结果。"
	case "No content found for this artist.":
		return "该艺人暂无可用内容。"
	case "Invalid Apple Music URL.":
		return "无效的 Apple Music 链接。"
	case "Invalid target.":
		return "无效的目标。"
	case "Please provide a search query.":
		return "请提供搜索关键词。"
	case "Search type must be song, album, or artist.":
		return "搜索类型必须是 song、album 或 artist。"
	case "Music Video ID is invalid.":
		return "Music Video ID 无效。"
	case "MV download requires media-user-token in config.yaml.":
		return "MV 下载需要在 config.yaml 中配置 media-user-token。"
	case "MV download requires mp4decrypt in PATH.":
		return "MV 下载需要在 PATH 中提供 mp4decrypt。"
	case "Station download requires media-user-token in config.yaml.":
		return "电台下载需要在 config.yaml 中配置 media-user-token。"
	case "Same MV task is already running for this chat. Please wait.":
		return "该会话已有相同 MV 任务在运行，请稍候。"
	case "Same download task is already running for this chat. Please wait.":
		return "该会话已有相同下载任务在运行，请稍候。"
	case "animatedcover only supports song/album/playlist/station.":
		return "animatedcover 仅支持 song/album/playlist/station。"
	case "animatedcover requires ffmpeg in PATH.":
		return "animatedcover 需要在 PATH 中提供 ffmpeg。"
	case "Invalid song target.":
		return "无效的歌曲目标。"
	case "Album ID is empty.":
		return "专辑 ID 不能为空。"
	case "Artist ID is empty.":
		return "艺人 ID 不能为空。"
	case "Playlist ID is empty.":
		return "歌单 ID 不能为空。"
	case "Station ID is empty.":
		return "电台 ID 不能为空。"
	case "Music Video ID is empty.":
		return "MV ID 不能为空。"
	case "Lyrics export requires media-user-token in config.yaml.":
		return "歌词导出需要在 config.yaml 中配置 media-user-token。"
	case "Selection expired. Please request the artist again.":
		return "选择已过期，请重新请求艺人内容。"
	case "Selection expired. Please request it again.":
		return "选择已过期，请重新请求。"
	case "Unknown artist view.":
		return "未知的艺人视图。"
	case "Artist assets export mode: one by one.":
		return "艺人素材导出方式：逐个发送。"
	case "Artist assets export mode: ZIP.":
		return "艺人素材导出方式：ZIP。"
	case "Unknown artist assets export mode.":
		return "未知的艺人素材导出方式。"
	case "Lyrics export mode: one by one.":
		return "歌词导出方式：逐个发送。"
	case "Lyrics export mode: ZIP.":
		return "歌词导出方式：ZIP。"
	case "Unknown lyrics export mode.":
		return "未知的歌词导出方式。"
	case "Transfer mode: one by one (refresh).":
		return "发送方式：逐个发送（刷新）。"
	case "Transfer mode: one by one.":
		return "发送方式：逐个发送。"
	case "Transfer mode: ZIP (cached).":
		return "发送方式：ZIP（命中缓存）。"
	case "Transfer mode: ZIP (refresh).":
		return "发送方式：ZIP（刷新）。"
	case "Transfer mode: ZIP.":
		return "发送方式：ZIP。"
	case "Unknown transfer mode.":
		return "未知的发送方式。"
	}
	if strings.HasPrefix(trimmed, "Usage: ") {
		return "用法：" + strings.TrimPrefix(trimmed, "Usage: ")
	}
	if strings.HasPrefix(trimmed, "Failed: ") {
		return "失败：" + strings.TrimPrefix(trimmed, "Failed: ")
	}
	if strings.HasPrefix(trimmed, "Failed to ") {
		return "失败：" + strings.TrimPrefix(trimmed, "Failed to ")
	}
	if strings.HasPrefix(trimmed, "Search failed: ") {
		return "搜索失败：" + strings.TrimPrefix(trimmed, "Search failed: ")
	}
	if strings.HasPrefix(trimmed, "Unsupported Apple Music URL: ") {
		return "不支持的 Apple Music 链接：" + strings.TrimPrefix(trimmed, "Unsupported Apple Music URL: ")
	}
	return text
}

func (b *TelegramBot) shouldMarkAutoDeleteSticky(markup any) bool {
	if markup == nil {
		return false
	}
	keyboard, ok := markup.(InlineKeyboardMarkup)
	if !ok {
		if ptr, okPtr := markup.(*InlineKeyboardMarkup); okPtr && ptr != nil {
			keyboard = *ptr
			ok = true
		}
	}
	if !ok {
		return false
	}
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			if strings.TrimSpace(button.CallbackData) != "" {
				return true
			}
		}
	}
	return false
}

func autoDeleteKey(chatID int64, messageID int) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}

func parseAutoDeleteKey(key string) (int64, int, bool) {
	parts := strings.Split(strings.TrimSpace(key), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || chatID == 0 {
		return 0, 0, false
	}
	messageID, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || messageID <= 0 {
		return 0, 0, false
	}
	return chatID, messageID, true
}

func (b *TelegramBot) scheduleAutoDeleteMessage(chatID int64, messageID int, sticky bool) {
	b.scheduleAutoDeleteMessageAt(chatID, messageID, sticky, time.Now().Add(telegramAutoDeleteAfter))
}

func (b *TelegramBot) scheduleAutoDeleteMessageAt(chatID int64, messageID int, sticky bool, deleteAt time.Time) {
	if b == nil || messageID <= 0 || chatID == 0 {
		return
	}
	if deleteAt.IsZero() {
		deleteAt = time.Now().Add(telegramAutoDeleteAfter)
	}
	delay := time.Until(deleteAt)
	if delay < 0 {
		delay = 0
	}
	key := autoDeleteKey(chatID, messageID)
	var timer *time.Timer
	b.autoDeleteMu.Lock()
	if b.autoDeleteMessages == nil {
		b.autoDeleteMessages = make(map[string]*time.Timer)
	}
	if b.autoDeleteSticky == nil {
		b.autoDeleteSticky = make(map[string]bool)
	}
	if b.autoDeleteDeadline == nil {
		b.autoDeleteDeadline = make(map[string]time.Time)
	}
	if existing := b.autoDeleteMessages[key]; existing != nil {
		existing.Stop()
	}
	b.autoDeleteSticky[key] = sticky
	b.autoDeleteDeadline[key] = deleteAt
	timer = time.AfterFunc(delay, func() {
		b.autoDeleteMu.Lock()
		if b.autoDeleteMessages[key] != timer {
			b.autoDeleteMu.Unlock()
			return
		}
		delete(b.autoDeleteMessages, key)
		delete(b.autoDeleteSticky, key)
		delete(b.autoDeleteDeadline, key)
		b.autoDeleteMu.Unlock()
		b.requestStateSave()
		_ = b.deleteMessage(chatID, messageID)
	})
	b.autoDeleteMessages[key] = timer
	b.autoDeleteMu.Unlock()
	b.requestStateSave()
}

func (b *TelegramBot) markMessageInteraction(chatID int64, messageID int) {
	key := autoDeleteKey(chatID, messageID)
	b.autoDeleteMu.Lock()
	sticky := b.autoDeleteSticky[key]
	b.autoDeleteMu.Unlock()
	if sticky {
		// 交互面板每次操作都刷新自动删除计时，避免“点击过一次后永久不删”。
		b.scheduleAutoDeleteMessage(chatID, messageID, true)
	}
}

func (b *TelegramBot) clearAutoDeleteMessage(chatID int64, messageID int) {
	if b == nil || messageID <= 0 || chatID == 0 {
		return
	}
	key := autoDeleteKey(chatID, messageID)
	changed := false
	b.autoDeleteMu.Lock()
	if timer := b.autoDeleteMessages[key]; timer != nil {
		timer.Stop()
		changed = true
	}
	if _, ok := b.autoDeleteMessages[key]; ok {
		delete(b.autoDeleteMessages, key)
		changed = true
	}
	if _, ok := b.autoDeleteSticky[key]; ok {
		delete(b.autoDeleteSticky, key)
		changed = true
	}
	if _, ok := b.autoDeleteDeadline[key]; ok {
		delete(b.autoDeleteDeadline, key)
		changed = true
	}
	b.autoDeleteMu.Unlock()
	if changed {
		b.requestStateSave()
	}
}

func (b *TelegramBot) clearAllAutoDeleteMessages() {
	if b == nil {
		return
	}
	changed := false
	b.autoDeleteMu.Lock()
	for key, timer := range b.autoDeleteMessages {
		if timer != nil {
			timer.Stop()
		}
		delete(b.autoDeleteMessages, key)
		changed = true
	}
	for key := range b.autoDeleteSticky {
		delete(b.autoDeleteSticky, key)
		changed = true
	}
	for key := range b.autoDeleteDeadline {
		delete(b.autoDeleteDeadline, key)
		changed = true
	}
	b.autoDeleteMu.Unlock()
	if changed {
		b.requestStateSave()
	}
}

func formatChatIDTextLegacy(chatID int64) string {
	return fmt.Sprintf(
		"Session ID (chat_id): %d\n"+
			"Use this value in config.yaml whitelist:\n"+
			"telegram-allowed-chat-ids:\n"+
			"  - %d",
		chatID,
		chatID,
	)
}
