package app

// End-to-end tests: a real bot runtime (polling, update dispatcher, command
// handling, rate limiting, queue and workers, status messages, uploads,
// file_id cache, SQLite persistence, shutdown and recovery) runs against a
// fake Telegram Bot API and a fake Apple Music API. Only the download itself
// is replaced (botRipSong / botRipAlbum): it writes a small file the way a
// real download would, instead of talking to Apple and the decrypt wrapper.

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
)

const (
	e2eChatID      int64 = 42
	e2eUserID      int64 = 7
	e2eAdminID     int64 = 99
	e2eBotToken          = "123456:e2e-test-token"
	e2eWaitTimeout       = 15 * time.Second
)

func e2eSongURL(songID string) string {
	return "https://music.apple.com/us/song/test-song/" + songID
}

// ---------------------------------------------------------------------------
// Fake Telegram Bot API
// ---------------------------------------------------------------------------

// fakeTelegramCall is one Bot API request the bot made.
type fakeTelegramCall struct {
	Method string
	// Fields holds the request parameters as strings; nested values such as
	// reply_markup are kept as their JSON text.
	Fields map[string]string
	// File is the uploaded file of a multipart request (nil otherwise).
	File      []byte
	FileName  string
	MessageID int // message_id returned to the bot
}

type fakeTelegram struct {
	server *httptest.Server

	mu            sync.Mutex
	updates       []Update
	confirmed     int // updates below this id were acknowledged or dropped
	nextUpdateID  int
	nextMessageID int
	calls         []fakeTelegramCall
	longPolls     int
	changed       chan struct{}
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{nextUpdateID: 1, nextMessageID: 1000, changed: make(chan struct{})}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

// notifyLocked wakes everything waiting for a change. Caller holds f.mu.
func (f *fakeTelegram) notifyLocked() {
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *fakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if method == "getUpdates" {
		f.serveGetUpdates(w, r)
		return
	}
	call := fakeTelegramCall{Method: method, Fields: map[string]string{}}
	if err := parseFakeTelegramRequest(r, &call); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	if method == "deleteWebhook" && call.Fields["drop_pending_updates"] == "true" {
		f.confirmed = f.nextUpdateID
	}
	f.nextMessageID++
	call.MessageID = f.nextMessageID
	f.calls = append(f.calls, call)
	f.notifyLocked()
	f.mu.Unlock()

	var result any
	switch method {
	case "getMe":
		result = map[string]any{"id": 1, "is_bot": true, "username": "test_bot"}
	default:
		chatID, _ := strconv.ParseInt(call.Fields["chat_id"], 10, 64)
		media := map[string]any{
			"file_id":        fmt.Sprintf("file-%d", call.MessageID),
			"file_unique_id": fmt.Sprintf("unique-%d", call.MessageID),
			"file_size":      len(call.File),
		}
		result = map[string]any{
			"message_id": call.MessageID,
			"chat":       map[string]any{"id": chatID, "type": "private"},
			"audio":      media,
			"document":   media,
			"video":      media,
		}
	}
	writeFakeTelegramResult(w, result)
}

func (f *fakeTelegram) serveGetUpdates(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	timeoutSec, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
	deadline := time.Now().Add(150 * time.Millisecond)

	f.mu.Lock()
	if offset > f.confirmed {
		f.confirmed = offset
	}
	if timeoutSec > 0 {
		f.longPolls++
		f.notifyLocked()
	}
	for {
		pending := f.pendingLocked()
		if len(pending) > 0 || timeoutSec == 0 || time.Now().After(deadline) {
			f.mu.Unlock()
			writeFakeTelegramResult(w, pending)
			return
		}
		changed := f.changed
		f.mu.Unlock()
		select {
		case <-changed:
		case <-time.After(time.Until(deadline)):
		case <-r.Context().Done():
			return
		}
		f.mu.Lock()
	}
}

func (f *fakeTelegram) pendingLocked() []Update {
	pending := []Update{}
	for _, upd := range f.updates {
		if upd.UpdateID >= f.confirmed {
			pending = append(pending, upd)
		}
	}
	return pending
}

func writeFakeTelegramResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func parseFakeTelegramRequest(r *http.Request, call *fakeTelegramCall) error {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch mediaType {
	case "application/json":
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return err
		}
		for key, raw := range payload {
			var text string
			if json.Unmarshal(raw, &text) == nil {
				call.Fields[key] = text
			} else {
				call.Fields[key] = string(raw)
			}
		}
	case "multipart/form-data":
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			return err
		}
		for key, values := range r.MultipartForm.Value {
			if len(values) > 0 {
				call.Fields[key] = values[0]
			}
		}
		for field, headers := range r.MultipartForm.File {
			if len(headers) == 0 || field == "thumbnail" {
				continue
			}
			file, err := headers[0].Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				return err
			}
			call.File = data
			call.FileName = headers[0].Filename
		}
	default:
		if err := r.ParseForm(); err != nil {
			return err
		}
		for key, values := range r.Form {
			if len(values) > 0 {
				call.Fields[key] = values[0]
			}
		}
	}
	return nil
}

func (f *fakeTelegram) push(upd Update) {
	f.mu.Lock()
	defer f.mu.Unlock()
	upd.UpdateID = f.nextUpdateID
	f.nextUpdateID++
	f.updates = append(f.updates, upd)
	f.notifyLocked()
}

// sendText delivers a user message to the bot and returns its message_id.
func (f *fakeTelegram) sendText(userID int64, text string) int {
	f.mu.Lock()
	f.nextMessageID++
	messageID := f.nextMessageID
	f.mu.Unlock()
	f.push(Update{Message: &Message{
		MessageID: messageID,
		From:      &User{ID: userID},
		Chat:      Chat{ID: e2eChatID, Type: "private"},
		Text:      text,
	}})
	return messageID
}

// tapButton presses an inline button on a message the bot sent.
func (f *fakeTelegram) tapButton(userID int64, messageID int, data string) {
	f.mu.Lock()
	f.nextMessageID++
	callbackID := strconv.Itoa(f.nextMessageID)
	f.mu.Unlock()
	f.push(Update{CallbackQuery: &CallbackQuery{
		ID:      callbackID,
		From:    &User{ID: userID},
		Message: &Message{MessageID: messageID, Chat: Chat{ID: e2eChatID, Type: "private"}},
		Data:    data,
	}})
}

func (f *fakeTelegram) snapshot() ([]fakeTelegramCall, int, chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeTelegramCall(nil), f.calls...), f.longPolls, f.changed
}

// waitFor blocks until match finds what it is looking for in the calls made
// so far and returns it.
func waitFor[T any](t *testing.T, f *fakeTelegram, what string, match func(calls []fakeTelegramCall) (T, bool)) T {
	t.Helper()
	deadline := time.After(e2eWaitTimeout)
	for {
		calls, _, changed := f.snapshot()
		if value, ok := match(calls); ok {
			return value
		}
		select {
		case <-changed:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s; Telegram calls so far:\n%s", what, describeCalls(calls))
		}
	}
}

// waitForMessage returns the first sendMessage containing text, sent after
// the first `after` calls.
func (f *fakeTelegram) waitForMessage(t *testing.T, after int, text string) fakeTelegramCall {
	t.Helper()
	return waitFor(t, f, fmt.Sprintf("a message containing %q", text), func(calls []fakeTelegramCall) (fakeTelegramCall, bool) {
		for _, call := range calls[min(after, len(calls)):] {
			if call.Method == "sendMessage" && strings.Contains(call.Fields["text"], text) {
				return call, true
			}
		}
		return fakeTelegramCall{}, false
	})
}

func (f *fakeTelegram) callCount() int {
	calls, _, _ := f.snapshot()
	return len(calls)
}

func uploadedAudio(calls []fakeTelegramCall) []fakeTelegramCall {
	var out []fakeTelegramCall
	for _, call := range calls {
		if call.Method == "sendAudio" && call.File != nil {
			out = append(out, call)
		}
	}
	return out
}

func describeCalls(calls []fakeTelegramCall) string {
	var b strings.Builder
	for _, call := range calls {
		text := call.Fields["text"]
		if text == "" {
			text = call.Fields["caption"]
		}
		fmt.Fprintf(&b, "  %s msg=%d file=%dB %q\n", call.Method, call.MessageID, len(call.File), text)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Fake Apple Music API and downloads
// ---------------------------------------------------------------------------

type fakeApple struct {
	mu       sync.Mutex
	searches int
	other    []string
}

func (a *fakeApple) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.HasSuffix(r.URL.Path, "/search") {
		a.searches++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":{"songs":{"data":[
			{"id":"1440000001","type":"songs","attributes":{"name":"First Song","artistName":"Test Artist","albumName":"Test Album","url":"`+e2eSongURL("1440000001")+`"}},
			{"id":"1440000002","type":"songs","attributes":{"name":"Second Song","artistName":"Test Artist","albumName":"Test Album","url":"`+e2eSongURL("1440000002")+`"}}
		]}}}`)
		return
	}
	// Anything else means the bot needed Apple data the fake download
	// already provided; record it so the test can fail on it.
	a.other = append(a.other, r.URL.String())
	http.NotFound(w, r)
}

func (a *fakeApple) counts() (searches int, other []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.searches, append([]string(nil), a.other...)
}

// hostRewriteTransport sends every request to one test server.
type hostRewriteTransport struct{ target string }

func (t hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(t.target, "http://")
	clone.Host = clone.URL.Host
	return http.DefaultTransport.RoundTrip(clone)
}

type fakeDownloads struct {
	mu      sync.Mutex
	started []string
	results map[string][]error
	gates   map[string]chan struct{}
	changed chan struct{}
}

func newFakeDownloads() *fakeDownloads {
	return &fakeDownloads{results: map[string][]error{}, gates: map[string]chan struct{}{}, changed: make(chan struct{})}
}

// hold makes downloads of songID wait until release (or cancellation).
func (d *fakeDownloads) hold(songID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gates[songID] = make(chan struct{})
}

func (d *fakeDownloads) releaseAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, gate := range d.gates {
		close(gate)
		delete(d.gates, id)
	}
}

func (d *fakeDownloads) note(update func()) {
	d.mu.Lock()
	update()
	close(d.changed)
	d.changed = make(chan struct{})
	d.mu.Unlock()
}

func (d *fakeDownloads) ripSong(session *DownloadSession, songID string, _ string, storefront string, _ string) (err error) {
	d.note(func() { d.started = append(d.started, songID) })
	defer func() { d.note(func() { d.results[songID] = append(d.results[songID], err) }) }()

	d.mu.Lock()
	gate := d.gates[songID]
	d.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-session.downloadContext().Done():
			return session.downloadContext().Err()
		}
	}

	dir := filepath.Join(session.Config.AlacSaveFolder, "Test Artist", "Test Album")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, songID+".m4a")
	if err := os.WriteFile(path, e2eAudioBytes(songID), 0o644); err != nil {
		return err
	}
	session.recordDownloadedFile(path, AudioMeta{
		TrackID:        songID,
		Title:          "Song " + songID,
		Performer:      "Test Artist",
		DurationMillis: 180_000,
		Format:         telegramFormatAlac,
		AlbumName:      "Test Album",
		GenreNames:     []string{"Pop"},
		Storefront:     storefront,
	})
	return nil
}

func e2eAudioBytes(songID string) []byte {
	return []byte(strings.Repeat("audio:"+songID+";", 512))
}

func (d *fakeDownloads) snapshot() ([]string, map[string][]error, chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	results := make(map[string][]error, len(d.results))
	for id, errs := range d.results {
		results[id] = append([]error(nil), errs...)
	}
	return append([]string(nil), d.started...), results, d.changed
}

func (d *fakeDownloads) waitFor(t *testing.T, what string, match func(started []string, results map[string][]error) bool) {
	t.Helper()
	deadline := time.After(e2eWaitTimeout)
	for {
		started, results, changed := d.snapshot()
		if match(started, results) {
			return
		}
		select {
		case <-changed:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s; downloads started=%v results=%v", what, started, results)
		}
	}
}

func countOf(items []string, want string) int {
	n := 0
	for _, item := range items {
		if item == want {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Environment
// ---------------------------------------------------------------------------

type e2eEnv struct {
	t         *testing.T
	dir       string
	telegram  *fakeTelegram
	apple     *fakeApple
	downloads *fakeDownloads
}

// newE2EEnv configures the bot (temporary storage and download folders,
// fake APIs, fast send limiter) and installs the fakes. configure may adjust
// Config further.
func newE2EEnv(t *testing.T, configure func()) *e2eEnv {
	t.Helper()
	env := &e2eEnv{
		t:         t,
		dir:       t.TempDir(),
		telegram:  newFakeTelegram(t),
		apple:     &fakeApple{},
		downloads: newFakeDownloads(),
	}
	t.Setenv("AMDL_TELEGRAM_SEND_GLOBAL_INTERVAL_MS", "1")
	t.Setenv("AMDL_TELEGRAM_SEND_CHAT_INTERVAL_MS", "1")

	withConfigForTest(t, func() {
		rateLimitOff := false
		Config.Storefront = "us"
		Config.TelegramAPIURL = env.telegram.server.URL
		Config.TelegramNoProxy = true
		Config.TelegramCacheFile = filepath.Join(env.dir, "runtime", "telegram-cache.json")
		Config.TelegramStateFile = ""
		Config.TelegramDBFile = ""
		Config.TelegramStorageBackend = ""
		Config.TelegramHistoryFile = ""
		Config.AlacSaveFolder = filepath.Join(env.dir, "downloads", "ALAC")
		Config.AtmosSaveFolder = filepath.Join(env.dir, "downloads", "Atmos")
		Config.AacSaveFolder = filepath.Join(env.dir, "downloads", "AAC")
		Config.TelegramDownloadFolder = ""
		Config.TelegramAdminUserIDs = []int64{e2eAdminID}
		Config.TelegramAllowedChatIDs = nil
		Config.TelegramForwardChatID = 0
		Config.TelegramMinFreeDiskMB = 1
		Config.TelegramMinFreeTmpMB = 1
		Config.TelegramMaxMemoryMB = 0
		Config.MaxMemoryLimit = 0
		Config.TelegramRateLimitEnabled = &rateLimitOff
		Config.TelegramMetricsListenAddr = ""
		Config.TelegramSyncCommandMenu = false
		Config.TelegramCaptionSignature = ""
		if configure != nil {
			configure()
		}
	})

	appleServer := httptest.NewServer(http.HandlerFunc(env.apple.serve))
	t.Cleanup(appleServer.Close)
	appleClient := nethttp.Client()
	previousTransport := appleClient.Transport
	appleClient.Transport = hostRewriteTransport{target: appleServer.URL}
	t.Cleanup(func() { appleClient.Transport = previousTransport })

	previousRipSong := botRipSong
	botRipSong = env.downloads.ripSong
	t.Cleanup(func() { botRipSong = previousRipSong })
	return env
}

type e2eRuntime struct {
	bot      *TelegramBot
	loopDone <-chan struct{}
	stopOnce sync.Once
}

// start brings up a bot runtime and waits until it is polling for updates,
// i.e. past its startup handling of pending updates.
func (env *e2eEnv) start() *e2eRuntime {
	env.t.Helper()
	_, pollsBefore, _ := env.telegram.snapshot()
	bot := newTelegramBot(e2eBotToken, "apple-test-token", false)
	bot.setChatLanguage(e2eChatID, telegramLanguageEn)
	rt := &e2eRuntime{bot: bot, loopDone: bot.startRuntimeLoop()}
	env.t.Cleanup(rt.stop)
	waitFor(env.t, env.telegram, "the bot to start long polling", func([]fakeTelegramCall) (bool, bool) {
		_, polls, _ := env.telegram.snapshot()
		return true, polls > pollsBefore
	})
	return rt
}

// waitIdle waits until the bot has no queued or running request left. A
// worker finishes a request (and frees its duplicate-request key) only
// after the upload call returns, so tests wait for this before sending the
// same link again or inspecting /queue.
func (rt *e2eRuntime) waitIdle(t *testing.T, telegram *fakeTelegram) {
	t.Helper()
	waitFor(t, telegram, "the bot to finish its requests", func([]fakeTelegramCall) (bool, bool) {
		return true, rt.bot.trackedRequestCount() == 0 && rt.bot.inflightCount() == 0
	})
}

// stop shuts the runtime down the way a signal or daily restart does.
func (rt *e2eRuntime) stop() {
	rt.stopOnce.Do(func() { rt.bot.shutdownRuntime(rt.loopDone) })
}

func (env *e2eEnv) assertNoUnexpectedAppleRequests() {
	env.t.Helper()
	if _, other := env.apple.counts(); len(other) > 0 {
		env.t.Fatalf("bot made Apple requests the fake download should have made unnecessary: %v", other)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestE2ESongLinkIsDownloadedUploadedAndThenServedFromCache(t *testing.T) {
	env := newE2EEnv(t, nil)
	rt := env.start()
	const songID = "1440000001"

	linkMessageID := env.telegram.sendText(e2eUserID, "listen to this "+e2eSongURL(songID))
	upload := waitFor(t, env.telegram, "the audio upload", func(calls []fakeTelegramCall) (fakeTelegramCall, bool) {
		uploads := uploadedAudio(calls)
		if len(uploads) == 0 {
			return fakeTelegramCall{}, false
		}
		return uploads[0], true
	})
	if string(upload.File) != string(e2eAudioBytes(songID)) {
		t.Fatalf("uploaded bytes differ from the downloaded file (%d bytes)", len(upload.File))
	}
	if upload.FileName != songID+".m4a" {
		t.Fatalf("unexpected upload file name %q", upload.FileName)
	}
	if got := upload.Fields["chat_id"]; got != strconv.FormatInt(e2eChatID, 10) {
		t.Fatalf("uploaded to chat %s", got)
	}
	if got := upload.Fields["reply_to_message_id"]; got != strconv.Itoa(linkMessageID) {
		t.Fatalf("upload replies to %s, want the link message %d", got, linkMessageID)
	}
	if upload.Fields["title"] != "Song "+songID || upload.Fields["performer"] != "Test Artist" {
		t.Fatalf("unexpected title/performer %q / %q", upload.Fields["title"], upload.Fields["performer"])
	}
	caption := upload.Fields["caption"]
	for _, want := range []string{"#AppleMusic #alac", "Test Album", "Pop", "via @test_bot"} {
		if !strings.Contains(caption, want) {
			t.Fatalf("caption %q is missing %q", caption, want)
		}
	}

	// The same link again is answered from the file_id cache.
	rt.waitIdle(t, env.telegram)
	before := env.telegram.callCount()
	env.telegram.sendText(e2eUserID, e2eSongURL(songID))
	resent := waitFor(t, env.telegram, "the cached audio", func(calls []fakeTelegramCall) (fakeTelegramCall, bool) {
		for _, call := range calls[before:] {
			if call.Method == "sendAudio" {
				return call, true
			}
		}
		return fakeTelegramCall{}, false
	})
	if resent.File != nil {
		t.Fatalf("second request uploaded the file again instead of reusing the cache")
	}
	if want := fmt.Sprintf("file-%d", upload.MessageID); resent.Fields["audio"] != want {
		t.Fatalf("cached send used file_id %q, want %q", resent.Fields["audio"], want)
	}

	// The finished download shows up in /history.
	rt.waitIdle(t, env.telegram)
	before = env.telegram.callCount()
	env.telegram.sendText(e2eUserID, "/history")
	history := env.telegram.waitForMessage(t, before, "song:"+songID)
	if !strings.Contains(history.Fields["text"], "success") {
		t.Fatalf("history does not report success: %q", history.Fields["text"])
	}

	started, _, _ := env.downloads.snapshot()
	if countOf(started, songID) != 1 {
		t.Fatalf("song downloaded %d times, want 1", countOf(started, songID))
	}
	env.assertNoUnexpectedAppleRequests()
}

func TestE2ESearchSelectionIsChargedAgainstTheRateLimit(t *testing.T) {
	env := newE2EEnv(t, func() {
		rateLimitOn := true
		Config.TelegramRateLimitEnabled = &rateLimitOn
		Config.TelegramRateLimitPerUserBurst = 1
		Config.TelegramRateLimitPerUserIntervalSec = 3600
	})
	env.start()

	// Searching is free; picking a result starts a download and is charged.
	before := env.telegram.callCount()
	env.telegram.sendText(e2eUserID, "/sg test")
	panel := env.telegram.waitForMessage(t, before, "First Song")
	if !strings.Contains(panel.Fields["reply_markup"], `"sel:1"`) {
		t.Fatalf("search results have no selection buttons: %s", panel.Fields["reply_markup"])
	}
	env.telegram.tapButton(e2eUserID, panel.MessageID, "sel:1")
	waitFor(t, env.telegram, "the selected song upload", func(calls []fakeTelegramCall) (bool, bool) {
		return true, len(uploadedAudio(calls)) == 1
	})

	// The second pick within the interval is refused.
	before = env.telegram.callCount()
	env.telegram.sendText(e2eUserID, "/sg test")
	panel = env.telegram.waitForMessage(t, before, "Second Song")
	env.telegram.tapButton(e2eUserID, panel.MessageID, "sel:2")
	env.telegram.waitForMessage(t, before, "Rate limit exceeded")

	started, _, _ := env.downloads.snapshot()
	if countOf(started, "1440000002") != 0 {
		t.Fatalf("a rate-limited selection still started a download: %v", started)
	}
	// Both searches went to Apple through the general HTTP client (the fake
	// Apple server), not through the Telegram client.
	if searches, _ := env.apple.counts(); searches != 2 {
		t.Fatalf("expected 2 Apple searches, got %d", searches)
	}
	env.assertNoUnexpectedAppleRequests()
}

func TestE2EOnlyAdminsChangeTheWorkerLimit(t *testing.T) {
	env := newE2EEnv(t, nil)
	rt := env.start()

	before := env.telegram.callCount()
	env.telegram.sendText(e2eUserID, "/settings")
	panel := env.telegram.waitForMessage(t, before, "")
	env.telegram.tapButton(e2eUserID, panel.MessageID, "setting_worker:4")
	env.telegram.waitForMessage(t, before, "只有管理员可以修改任务线程数")
	if got := rt.bot.getTaskWorkerLimit(); got != 1 {
		t.Fatalf("a regular user changed the worker limit to %d", got)
	}

	env.telegram.sendText(e2eAdminID, "/settings worker3")
	waitFor(t, env.telegram, "the admin's change", func([]fakeTelegramCall) (bool, bool) {
		return true, rt.bot.getTaskWorkerLimit() == 3
	})
}

var e2eRequestIDRe = regexp.MustCompile(`id=(tg-[0-9-]+)`)

func TestE2ECancelStopsARunningDownload(t *testing.T) {
	env := newE2EEnv(t, nil)
	rt := env.start()
	const songID = "1440000001"
	env.downloads.hold(songID)
	t.Cleanup(env.downloads.releaseAll)

	env.telegram.sendText(e2eUserID, e2eSongURL(songID))
	env.downloads.waitFor(t, "the download to start", func(started []string, _ map[string][]error) bool {
		return countOf(started, songID) == 1
	})

	before := env.telegram.callCount()
	env.telegram.sendText(e2eUserID, "/queue")
	queue := env.telegram.waitForMessage(t, before, "running")
	match := e2eRequestIDRe.FindStringSubmatch(queue.Fields["text"])
	if match == nil {
		t.Fatalf("no request id in /queue reply: %q", queue.Fields["text"])
	}

	env.telegram.sendText(e2eUserID, "/cancel "+match[1])
	env.telegram.waitForMessage(t, before, "Cancellation requested")
	env.downloads.waitFor(t, "the download to observe the cancellation", func(_ []string, results map[string][]error) bool {
		errs := results[songID]
		return len(errs) == 1 && isContextCancellationError(errs[0])
	})

	rt.waitIdle(t, env.telegram)
	before = env.telegram.callCount()
	env.telegram.sendText(e2eUserID, "/queue")
	env.telegram.waitForMessage(t, before, "No queued or running tasks")
	calls, _, _ := env.telegram.snapshot()
	if uploads := uploadedAudio(calls); len(uploads) != 0 {
		t.Fatalf("a canceled download was uploaded")
	}
}

func TestE2ERestartResumesInterruptedAndQueuedDownloads(t *testing.T) {
	env := newE2EEnv(t, nil)
	first := env.start()
	const runningSong, queuedSong = "1440000001", "1440000002"
	env.downloads.hold(runningSong)
	t.Cleanup(env.downloads.releaseAll)

	env.telegram.sendText(e2eUserID, e2eSongURL(runningSong))
	env.downloads.waitFor(t, "the first download to start", func(started []string, _ map[string][]error) bool {
		return countOf(started, runningSong) == 1
	})
	before := env.telegram.callCount()
	env.telegram.sendText(e2eUserID, e2eSongURL(queuedSong))
	env.telegram.waitForMessage(t, before, "Queued.")

	// Graceful stop while one download runs and one waits.
	first.stop()
	_, results, _ := env.downloads.snapshot()
	if errs := results[runningSong]; len(errs) != 1 || !isContextCancellationError(errs[0]) {
		t.Fatalf("running download was not interrupted by the shutdown: %v", errs)
	}
	started, _, _ := env.downloads.snapshot()
	if countOf(started, queuedSong) != 0 {
		t.Fatalf("queued download started during shutdown")
	}

	// The next runtime picks both up from the saved state.
	env.downloads.releaseAll()
	second := env.start()
	uploads := waitFor(t, env.telegram, "both songs to be uploaded after the restart", func(calls []fakeTelegramCall) ([]fakeTelegramCall, bool) {
		uploads := uploadedAudio(calls)
		return uploads, len(uploads) == 2
	})
	names := []string{uploads[0].FileName, uploads[1].FileName}
	for _, want := range []string{runningSong + ".m4a", queuedSong + ".m4a"} {
		if countOf(names, want) != 1 {
			t.Fatalf("after restart uploaded %v, want each song once", names)
		}
	}

	second.waitIdle(t, env.telegram)
	before = env.telegram.callCount()
	env.telegram.sendText(e2eUserID, "/queue")
	env.telegram.waitForMessage(t, before, "No queued or running tasks")
	started, _, _ = env.downloads.snapshot()
	if countOf(started, runningSong) != 2 || countOf(started, queuedSong) != 1 {
		t.Fatalf("unexpected download attempts after restart: %v", started)
	}
	env.assertNoUnexpectedAppleRequests()
}
