package app

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apputils "github.com/wuuduf/applemusic-telegram-bot/utils"
	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"

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
	searchMetaAt            = make(map[string]time.Time)
	searchMetaLastPrune     time.Time
	networkHTTPClient       = nethttp.Client()
	runtimeErrorLogMu       sync.Mutex
	runtimeErrorLogPath     atomic.Value
	errNoFilesDownloaded    = errors.New("no files were downloaded")
	errTelegramSendDeferred = errors.New("telegram send deferred by limiter")
)

const maxMediaIDLength = 128

// Search metadata is only consumed when a selected track is downloaded, so
// selections that never download must expire instead of piling up in memory
// and in the persisted state file.
const (
	searchMetaTTL        = 24 * time.Hour
	searchMetaMaxEntries = 5000
	searchMetaPruneEvery = 10 * time.Minute
)
const maxTemplateValueBytes = 240

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
