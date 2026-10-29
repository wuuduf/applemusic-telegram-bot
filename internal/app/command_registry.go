package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// helpSection groups commands in /help. Commands with helpHidden are still
// registered (aliases, rate limiting) but not listed, e.g. admin commands,
// which live in the /amadmin panel.
type helpSection int

const (
	helpHidden helpSection = iota
	helpSearch
	helpDownload
	helpTasks
	helpSettings
	helpOther
)

type helpSectionTitle struct {
	section helpSection
	zh, en  string
}

var helpSectionOrder = []helpSectionTitle{
	{helpSearch, "【搜索音乐】", "【Search】"},
	{helpDownload, "【下载与导出】", "【Download & Export】"},
	{helpTasks, "【我的任务】", "【My tasks】"},
	{helpSettings, "【设置】", "【Settings】"},
	{helpOther, "【其他】", "【Other】"},
}

// botCommand describes one Telegram command. The registry below is the single
// source for alias resolution, rate limiting, /help and the bot command menu;
// handleCommandWithContext dispatches on Name.
type botCommand struct {
	// Name is the canonical name handleCommandWithContext switches on.
	Name string
	// Aliases are extra spellings that resolve to Name.
	Aliases []string
	// Short is the spelling shown in /help and the command menu; it must be
	// Name or one of Aliases. Empty means Name.
	Short string

	Section        helpSection
	ArgsZh, ArgsEn string
	HelpZh, HelpEn string

	// RateLimited applies the per-user limiter. RateLimitedWithArgs applies it
	// only when arguments are given (e.g. /id alone just shows the chat id).
	RateLimited         bool
	RateLimitedWithArgs bool
}

var botCommands = []botCommand{
	// Search
	{Name: "search_song", Aliases: []string{"sg"}, Short: "sg", Section: helpSearch,
		ArgsZh: "<关键词>", ArgsEn: "<keywords>", HelpZh: "搜索歌曲", HelpEn: "Search songs"},
	{Name: "search_album", Aliases: []string{"sa"}, Short: "sa", Section: helpSearch,
		ArgsZh: "<关键词>", ArgsEn: "<keywords>", HelpZh: "搜索专辑", HelpEn: "Search albums"},
	{Name: "search_artist", Aliases: []string{"sr"}, Short: "sr", Section: helpSearch,
		ArgsZh: "<关键词>", ArgsEn: "<keywords>", HelpZh: "搜索艺人", HelpEn: "Search artists"},
	{Name: "search", Aliases: []string{"s"}, Short: "s", Section: helpSearch,
		ArgsZh: "<song|album|artist> <关键词>", ArgsEn: "<song|album|artist> <keywords>", HelpZh: "统一搜索", HelpEn: "Unified search"},

	// Download & export
	{Name: "url", Aliases: []string{"u"}, Short: "u", Section: helpDownload, RateLimited: true,
		ArgsZh: "<链接>", ArgsEn: "<link>",
		HelpZh: "下载（支持歌曲/专辑/歌单/艺人/MV）", HelpEn: "Download (song/album/playlist/artist/MV)"},
	{Name: "download_song", Aliases: []string{"dg"}, Short: "dg", Section: helpDownload, RateLimited: true,
		ArgsZh: "<歌名或 歌手 - 歌名>", ArgsEn: "<song or artist - song>",
		HelpZh: "点歌（直接下载第一个匹配歌曲，无需选择）", HelpEn: "Request a song (auto-download the top match, no selection)"},
	{Name: "refresh", Aliases: []string{"rf"}, Short: "rf", Section: helpDownload, RateLimited: true,
		ArgsZh: "<链接>", ArgsEn: "<link>",
		HelpZh: "强制重新下载（清缓存重传，文件损坏时用）", HelpEn: "Force re-download (clear cache; use when files are corrupted)"},
	{Name: "artistphoto", Aliases: []string{"ap"}, Short: "ap", Section: helpDownload, RateLimited: true,
		ArgsZh: "<艺人链接>", ArgsEn: "<artist link>",
		HelpZh: "导出艺人主页图 + 专辑封面合集", HelpEn: "Export artist image + album cover set"},
	{Name: "cover", Aliases: []string{"cv"}, Short: "cv", Section: helpDownload, RateLimited: true,
		ArgsZh: "<链接>", ArgsEn: "<link>", HelpZh: "只下载封面", HelpEn: "Download cover only"},
	{Name: "animatedcover", Aliases: []string{"ac", "motioncover"}, Short: "ac", Section: helpDownload, RateLimited: true,
		ArgsZh: "<链接>", ArgsEn: "<link>", HelpZh: "只下载动态封面", HelpEn: "Download animated cover only"},
	{Name: "lyrics", Aliases: []string{"ly", "lyric"}, Short: "ly", Section: helpDownload, RateLimited: true,
		ArgsZh: "<链接>", ArgsEn: "<link>", HelpZh: "导出歌词", HelpEn: "Export lyrics"},

	// My tasks
	{Name: "status", Aliases: []string{"stat"}, Section: helpTasks,
		HelpZh: "查看全局运行状态（队列/线程）", HelpEn: "View global status (queue/workers)"},
	{Name: "queue", Aliases: []string{"q"}, Section: helpTasks,
		HelpZh: "查看我的排队/运行任务", HelpEn: "View my queued/running tasks"},
	{Name: "history", Aliases: []string{"hist", "his"}, Section: helpTasks,
		ArgsZh: "[n]", ArgsEn: "[n]", HelpZh: "查看我最近的下载记录", HelpEn: "View my recent download history"},
	{Name: "quota", Section: helpTasks,
		HelpZh: "查看我的下载额度（剩余次数/恢复时间）", HelpEn: "View my download allowance (remaining / refill time)"},
	{Name: "cancel", Aliases: []string{"c"}, Section: helpTasks,
		ArgsZh: "<编号>", ArgsEn: "<id>",
		HelpZh: "取消我的任务（编号用 /queue 查看）", HelpEn: "Cancel one of my tasks (id from /queue)"},

	// Settings
	{Name: "settings", Aliases: []string{"st"}, Short: "st", Section: helpSettings,
		HelpZh: "查看或修改下载设置（音质/歌词/打包/线程等）", HelpEn: "View or change download settings (quality/lyrics/zip/workers)"},

	// Other
	{Name: "id", Aliases: []string{"i"}, Short: "i", Section: helpOther, RateLimitedWithArgs: true,
		HelpZh: "查看我的 chat_id", HelpEn: "Show my chat_id"},
	{Name: "amwhoami", Section: helpOther,
		HelpZh: "查看我的 user_id / chat_id", HelpEn: "Show my user_id / chat_id"},

	// Not listed in /help.
	{Name: "help", Aliases: []string{"h"}},
	{Name: "start"},
	{Name: "songid", RateLimited: true},
	{Name: "albumid", RateLimited: true},
	{Name: "playlistid", RateLimited: true},
	{Name: "stationid", RateLimited: true},
	{Name: "mvid", RateLimited: true},
	{Name: "artistid", RateLimited: true},
	{Name: "sub"},
	{Name: "subtemp"},
	{Name: "subrefresh"},
	{Name: "subrefreshall"},
	{Name: "amadmin"},
	{Name: "amcachepush"},
	{Name: "amwlon"},
	{Name: "amwloff"},
	{Name: "amwladd"},
	{Name: "amwldel"},
	{Name: "amban"},
	{Name: "amunban"},
}

var (
	botCommandByName  = map[string]*botCommand{}
	botCommandByAlias = map[string]*botCommand{}
)

func init() {
	for i := range botCommands {
		cmd := &botCommands[i]
		if _, dup := botCommandByName[cmd.Name]; dup {
			panic("duplicate bot command: " + cmd.Name)
		}
		botCommandByName[cmd.Name] = cmd
		for _, alias := range append([]string{cmd.Name}, cmd.Aliases...) {
			if other, dup := botCommandByAlias[alias]; dup && other != cmd {
				panic("bot command alias " + alias + " used by both " + other.Name + " and " + cmd.Name)
			}
			botCommandByAlias[alias] = cmd
		}
	}
}

func (c *botCommand) shortName() string {
	if c.Short != "" {
		return c.Short
	}
	return c.Name
}

func (c *botCommand) helpLine(lang string) string {
	args, help := c.ArgsZh, c.HelpZh
	if lang == telegramLanguageEn {
		args, help = c.ArgsEn, c.HelpEn
	}
	line := "/" + c.shortName()
	if args != "" {
		line += " " + args
	}
	return line + " " + help
}

// normalizeTelegramBotCommand resolves an alias to its canonical command
// name. Unknown commands are returned unchanged.
func normalizeTelegramBotCommand(cmd string) string {
	if c, ok := botCommandByAlias[strings.ToLower(strings.TrimSpace(cmd))]; ok {
		return c.Name
	}
	return cmd
}

// isRateLimitedCommand reports whether a command initiates expensive work
// (downloads / asset exports) and is therefore subject to the per-user limiter.
func isRateLimitedCommand(cmd string, args []string) bool {
	c, ok := botCommandByName[cmd]
	if !ok {
		return false
	}
	return c.RateLimited || (c.RateLimitedWithArgs && len(args) > 0)
}

// buildHelpCommandList renders the /help command sections for a language.
func buildHelpCommandList(lang string) string {
	var sections []string
	for _, sec := range helpSectionOrder {
		title := sec.zh
		if lang == telegramLanguageEn {
			title = sec.en
		}
		lines := []string{title}
		for i := range botCommands {
			if botCommands[i].Section == sec.section {
				lines = append(lines, botCommands[i].helpLine(lang))
			}
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

// telegramMenuCommand is one entry of the Bot API setMyCommands payload.
type telegramMenuCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// buildTelegramMenuCommands returns the commands shown in Telegram's command
// menu: everything listed in /help, in the same order.
func buildTelegramMenuCommands(lang string) []telegramMenuCommand {
	var out []telegramMenuCommand
	for _, sec := range helpSectionOrder {
		for i := range botCommands {
			c := &botCommands[i]
			if c.Section != sec.section {
				continue
			}
			desc := c.HelpZh
			if lang == telegramLanguageEn {
				desc = c.HelpEn
			}
			out = append(out, telegramMenuCommand{Command: c.shortName(), Description: desc})
		}
	}
	return out
}

// syncTelegramCommandMenu publishes the command menu (setMyCommands) for the
// default (Chinese) and English locales so Telegram's "/" menu always matches
// /help. It is opt-in because it replaces whatever was configured in
// BotFather.
func (b *TelegramBot) syncTelegramCommandMenu(ctx context.Context) {
	for _, locale := range []struct{ lang, code string }{
		{telegramLanguageZh, ""},
		{telegramLanguageEn, "en"},
	} {
		payload := map[string]any{"commands": buildTelegramMenuCommands(locale.lang)}
		if locale.code != "" {
			payload["language_code"] = locale.code
		}
		if err := b.postTelegramJSON(ctx, "setMyCommands", payload); err != nil {
			msg := fmt.Sprintf("telegram setMyCommands (%s) failed: %s", locale.lang, b.sanitizeTelegramErr(err))
			fmt.Println(msg)
			appendRuntimeErrorLog(msg)
			return
		}
	}
	fmt.Println("Telegram command menu synced from the command registry.")
}

func (b *TelegramBot) postTelegramJSON(ctx context.Context, method string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.apiURL(method), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	apiResp := apiResponse{}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if !apiResp.OK {
		return fmt.Errorf("%s: %s", method, apiResp.Description)
	}
	return nil
}
