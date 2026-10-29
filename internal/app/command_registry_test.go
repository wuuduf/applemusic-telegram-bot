package app

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// dispatchCaseLabels returns the string case labels of the command switch in
// handleCommandWithContext.
func dispatchCaseLabels(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "telegram_commands.go", nil, 0)
	if err != nil {
		t.Fatalf("parse telegram_commands.go: %v", err)
	}
	labels := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handleCommandWithContext" {
			continue
		}
		for _, stmt := range fn.Body.List {
			sw, ok := stmt.(*ast.SwitchStmt)
			if !ok {
				continue
			}
			if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "cmd" {
				continue
			}
			for _, clause := range sw.Body.List {
				for _, expr := range clause.(*ast.CaseClause).List {
					if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						value, _ := strconv.Unquote(lit.Value)
						labels[value] = true
					}
				}
			}
		}
	}
	if len(labels) == 0 {
		t.Fatalf("could not find the command switch in handleCommandWithContext")
	}
	return labels
}

func TestCommandRegistryMatchesDispatch(t *testing.T) {
	labels := dispatchCaseLabels(t)
	for i := range botCommands {
		if name := botCommands[i].Name; !labels[name] {
			t.Errorf("registered command %q has no case in handleCommandWithContext", name)
		}
	}
	for label := range labels {
		if _, ok := botCommandByAlias[label]; !ok {
			t.Errorf("dispatched command %q is missing from the command registry", label)
		}
	}
}

func TestNormalizeTelegramBotCommandResolvesAliases(t *testing.T) {
	cases := map[string]string{
		"h": "help", "HELP": "help", "stat": "status", "q": "queue", "hist": "history", "his": "history",
		"c": "cancel", "i": "id", "sg": "search_song", "SG": "search_song", "dg": "download_song",
		"sa": "search_album", "sr": "search_artist", "s": "search", "u": "url", "ap": "artistphoto",
		"cv": "cover", "ac": "animatedcover", "motioncover": "animatedcover", "ly": "lyrics",
		"lyric": "lyrics", "st": "settings", "rf": "refresh", "Unknown": "Unknown",
	}
	for in, want := range cases {
		if got := normalizeTelegramBotCommand(in); got != want {
			t.Errorf("normalizeTelegramBotCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsRateLimitedCommandCoversDownloads(t *testing.T) {
	for _, alias := range []string{"u", "dg", "rf", "ap", "cv", "ac", "motioncover", "ly", "lyric", "songid", "albumid", "artistid"} {
		if !isRateLimitedCommand(normalizeTelegramBotCommand(alias), nil) {
			t.Errorf("/%s starts a download and must be rate limited", alias)
		}
	}
	for _, alias := range []string{"h", "sg", "status", "q", "st", "amadmin"} {
		if isRateLimitedCommand(normalizeTelegramBotCommand(alias), []string{"x"}) {
			t.Errorf("/%s must not be rate limited", alias)
		}
	}
	if isRateLimitedCommand("id", nil) || !isRateLimitedCommand("id", []string{"song", "1"}) {
		t.Errorf("/id is rate limited only when it downloads (has arguments)")
	}
}

func TestHelpListsVisibleCommandsOnly(t *testing.T) {
	for _, lang := range []string{telegramLanguageZh, telegramLanguageEn} {
		text := botHelpTextForLanguage(lang)
		for i := range botCommands {
			c := &botCommands[i]
			line := c.helpLine(lang)
			if c.Section == helpHidden {
				if strings.Contains(text, "\n/"+c.shortName()+" ") {
					t.Errorf("[%s] hidden command /%s should not be in /help", lang, c.shortName())
				}
				continue
			}
			if c.HelpZh == "" || c.HelpEn == "" {
				t.Errorf("visible command %s needs zh and en help", c.Name)
			}
			if !strings.Contains(text, line) {
				t.Errorf("[%s] /help is missing %q", lang, line)
			}
		}
	}
}

func TestTelegramMenuCommandsAreValid(t *testing.T) {
	nameRe := regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	for _, lang := range []string{telegramLanguageZh, telegramLanguageEn} {
		menu := buildTelegramMenuCommands(lang)
		if len(menu) == 0 || len(menu) > 100 {
			t.Fatalf("[%s] menu must have 1..100 commands, got %d", lang, len(menu))
		}
		for _, item := range menu {
			if !nameRe.MatchString(item.Command) {
				t.Errorf("[%s] invalid menu command %q", lang, item.Command)
			}
			if n := len([]rune(item.Description)); n < 1 || n > 256 {
				t.Errorf("[%s] /%s description length %d out of range", lang, item.Command, n)
			}
		}
	}
}

func TestReadmesDocumentVisibleCommands(t *testing.T) {
	for _, path := range []string{"../../README.md", "../../README-CN.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i := range botCommands {
			c := &botCommands[i]
			if c.Section == helpHidden {
				continue
			}
			if !strings.Contains(string(data), "`/"+c.shortName()) {
				t.Errorf("%s does not document /%s", path, c.shortName())
			}
		}
	}
}

func TestSyncTelegramCommandMenuPostsBothLocales(t *testing.T) {
	var mu sync.Mutex
	var payloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			t.Errorf("unexpected API call %s", r.URL.Path)
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		payloads = append(payloads, payload)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	b := &TelegramBot{token: "test-token", apiBase: server.URL, client: server.Client()}
	b.syncTelegramCommandMenu(context.Background())

	if len(payloads) != 2 {
		t.Fatalf("expected 2 setMyCommands calls, got %d", len(payloads))
	}
	if _, ok := payloads[0]["language_code"]; ok {
		t.Fatalf("first call should set the default (zh) menu without language_code")
	}
	if payloads[1]["language_code"] != "en" {
		t.Fatalf("second call should target language_code en, got %v", payloads[1]["language_code"])
	}
	first := payloads[0]["commands"].([]any)[0].(map[string]any)
	if first["command"] != "sg" || first["description"] != "搜索歌曲" {
		t.Fatalf("unexpected first menu entry: %v", first)
	}
}

func TestSyncTelegramCommandMenuStopsOnAPIError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: invalid command"}`))
	}))
	defer server.Close()
	b := &TelegramBot{token: "test-token", apiBase: server.URL, client: server.Client()}
	b.syncTelegramCommandMenu(context.Background())
	if calls != 1 {
		t.Fatalf("expected sync to stop after the first failure, got %d calls", calls)
	}
}
