package app

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sharedcatalog "github.com/wuuduf/applemusic-telegram-bot/internal/catalog"
	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
)

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

func checkUrlCurator(url string) (string, string) {
	pat := regexp.MustCompile(`^(?:https:\/\/(?:beta\.music|music|classical\.music)\.apple\.com\/(\w{2})(?:\/curator|\/curator\/.+))\/(?:id)?([A-Za-z0-9._-]+)(?:$|\?)`)
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
	if storefront, id := checkUrlCurator(cleaned); storefront != "" && id != "" {
		return &AppleURLTarget{MediaType: mediaTypeCurator, Storefront: storefront, ID: id, RawURL: cleaned}, nil
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

func extractAppleMusicURLs(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	tokens := strings.Fields(text)
	urls := make([]string, 0)
	for _, token := range tokens {
		candidate := strings.TrimSpace(token)
		candidate = strings.Trim(candidate, "<>()[]{}\"'“”‘’")
		candidate = strings.TrimRight(candidate, ".,!?")
		if candidate == "" {
			continue
		}
		lower := strings.ToLower(candidate)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			httpIdx := strings.Index(lower, "http://")
			httpsIdx := strings.Index(lower, "https://")
			start := -1
			switch {
			case httpIdx >= 0 && httpsIdx >= 0:
				if httpIdx < httpsIdx {
					start = httpIdx
				} else {
					start = httpsIdx
				}
			case httpIdx >= 0:
				start = httpIdx
			case httpsIdx >= 0:
				start = httpsIdx
			}
			if start < 0 {
				continue
			}
			candidate = candidate[start:]
			candidate = strings.Trim(candidate, "<>()[]{}\"'“”‘’")
			candidate = strings.TrimRight(candidate, ".,!?")
			lower = strings.ToLower(candidate)
		}
		if strings.Contains(lower, "music.apple.com/") || strings.Contains(lower, "beta.music.apple.com/") || strings.Contains(lower, "classical.music.apple.com/") {
			urls = append(urls, candidate)
		}
	}
	return urls
}

func extractFirstAppleMusicURL(text string) string {
	urls := extractAppleMusicURLs(text)
	if len(urls) == 0 {
		return ""
	}
	return urls[0]
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

func getUrlCuratorName(curatorURL string, token string) (string, string, error) {
	storefront, curatorID := checkUrlCurator(curatorURL)
	if storefront == "" || curatorID == "" {
		return "", "", fmt.Errorf("invalid curator url")
	}
	service := catalogServiceForToken(token, "main.curator")
	service.Language = Config.Language
	name, err := service.FetchCuratorName(storefront, curatorID)
	if err != nil {
		return "", "", err
	}
	return name, curatorID, nil
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
