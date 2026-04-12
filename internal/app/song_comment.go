package app

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
)

const (
	songCommentFetchTimeout         = 4 * time.Second
	songCommentAppleMetaTimeout     = 5 * time.Second
	songCommentCacheTTL             = 24 * time.Hour
	lastFMTrackInfoAPI              = "https://ws.audioscrobbler.com/2.0/"
	maxSongCommentSummaryBytes      = 260
	defaultSongCommentSummaryZH     = "从公开标签看，这首歌更强调律动和氛围，结构稳定，重在情绪传达。"
	defaultSongCommentSummaryEN     = "Based on public tags, this track emphasizes groove and atmosphere with a stable song form."
	defaultSongCommentConclusionZH  = "整体来看，这首作品更偏向“质感与情绪优先”，而不是结构上的实验性变化。"
	defaultSongCommentConclusionEN  = "Overall, this work prioritizes texture and emotion over structural experimentation."
	defaultSongCommentTitleZH       = "未知歌曲"
	defaultSongCommentArtistZH      = "未知艺人"
	defaultSongCommentTitleEN       = "Unknown Track"
	defaultSongCommentArtistEN      = "Unknown Artist"
	defaultSongCommentStorefront    = "us"
	defaultSongCommentAppleLangZH   = "zh-Hans-CN"
	defaultSongCommentAppleLangEN   = "en-US"
	defaultSongCommentMaxStyleCount = 4
)

var (
	lastFMHTMLTagRe = regexp.MustCompile(`(?s)<[^>]*>`)
	lastFMNoiseKeys = []string{
		"Read more on Last.fm",
		"User-contributed text is available under the Creative Commons By-SA License",
	}
)

type lastFMTag struct {
	Name string `json:"name"`
}

type lastFMTrackInfoResponse struct {
	Error   int    `json:"error"`
	Message string `json:"message"`
	Track   struct {
		Name      string `json:"name"`
		Artist    any    `json:"artist"`
		Listeners string `json:"listeners"`
		Playcount string `json:"playcount"`
		Wiki      struct {
			Summary string `json:"summary"`
			Content string `json:"content"`
		} `json:"wiki"`
		TopTags struct {
			Tag []lastFMTag `json:"tag"`
		} `json:"toptags"`
	} `json:"track"`
}

type lastFMTopTagsResponse struct {
	Error   int    `json:"error"`
	Message string `json:"message"`
	TopTags struct {
		Tag []lastFMTag `json:"tag"`
	} `json:"toptags"`
}

type songCommentProfile struct {
	Title     string
	Performer string
	Summary   string
	TrackTags []string
	Listeners int64
	Playcount int64
}

func resolveLastFMAPIKey() string {
	if raw := strings.TrimSpace(Config.LastFMAPIKey); raw != "" {
		return raw
	}
	return strings.TrimSpace(os.Getenv("LASTFM_API_KEY"))
}

func (b *TelegramBot) maybeSendSongCommentAfterAudio(chatID int64, replyToID int, meta AudioMeta) {
	if b == nil || replyToID <= 0 {
		return
	}
	settings := normalizeChatSettings(b.getChatSettings(chatID))
	if !settings.SongComment {
		return
	}
	if strings.TrimSpace(meta.Title) == "" && strings.TrimSpace(meta.Performer) == "" && strings.TrimSpace(meta.TrackID) == "" {
		return
	}
	go func() {
		runWithRecovery("telegram song comment", nil, func() {
			comment, err := b.buildSongCommentForTrack(chatID, meta)
			if err != nil || strings.TrimSpace(comment) == "" {
				return
			}
			if err := b.waitTelegramSend(b.operationContext(), chatID); err != nil {
				return
			}
			_ = b.sendMessageWithReply(chatID, comment, nil, replyToID)
		})
	}()
}

func (b *TelegramBot) buildSongCommentForTrack(chatID int64, meta AudioMeta) (string, error) {
	if strings.TrimSpace(resolveLastFMAPIKey()) == "" {
		return "", fmt.Errorf("lastfm api key is empty")
	}
	lang := normalizeTelegramLanguage(b.getChatLanguage(chatID))
	if lang == "" {
		lang = telegramLanguageZh
	}
	meta = normalizeSongCommentMeta(meta)
	meta = b.enrichSongMetaFromApple(chatID, meta)

	cacheKey := buildSongCommentCacheKey(meta.TrackID, meta.Title, meta.Performer, lang)
	if cached, ok := b.getSongCommentFromCache(cacheKey); ok {
		return cached, nil
	}

	profile, profileErr := b.fetchLastFMTrackProfile(lang, meta.Title, meta.Performer)
	if profile.Title != "" && meta.Title == "" {
		meta.Title = profile.Title
	}
	if profile.Performer != "" && meta.Performer == "" {
		meta.Performer = profile.Performer
	}

	trackTags := append([]string(nil), profile.TrackTags...)
	if len(trackTags) == 0 {
		if fallbackTrackTags, err := b.fetchLastFMTrackTopTags(meta.Title, meta.Performer); err == nil {
			trackTags = fallbackTrackTags
		}
	}
	artistTags := []string{}
	if len(trackTags) < 2 {
		if fallbackArtistTags, err := b.fetchLastFMArtistTopTags(meta.Performer); err == nil {
			artistTags = fallbackArtistTags
		}
	}

	styleTags := buildMergedStyleTags(lang, meta.GenreNames, trackTags, artistTags)
	comment := renderSongCommentTemplate(lang, meta, profile.Summary, styleTags, profile.Listeners, profile.Playcount)
	if strings.TrimSpace(comment) == "" {
		if profileErr != nil {
			return "", profileErr
		}
		return "", fmt.Errorf("song comment is empty")
	}
	b.storeSongCommentCache(cacheKey, comment)
	return comment, nil
}

func normalizeSongCommentMeta(meta AudioMeta) AudioMeta {
	meta.TrackID = strings.TrimSpace(meta.TrackID)
	meta.Title = strings.TrimSpace(meta.Title)
	meta.Performer = strings.TrimSpace(meta.Performer)
	meta.AlbumName = strings.TrimSpace(meta.AlbumName)
	meta.ComposerName = strings.TrimSpace(meta.ComposerName)
	meta.ReleaseDate = strings.TrimSpace(meta.ReleaseDate)
	meta.Storefront = strings.TrimSpace(strings.ToLower(meta.Storefront))
	meta.ContentRating = strings.TrimSpace(strings.ToLower(meta.ContentRating))
	meta.GenreNames = sanitizeTagList(meta.GenreNames)
	meta.AudioTraits = sanitizeTagList(meta.AudioTraits)
	return meta
}

func (b *TelegramBot) enrichSongMetaFromApple(chatID int64, meta AudioMeta) AudioMeta {
	if b == nil || strings.TrimSpace(meta.TrackID) == "" {
		return meta
	}
	needsMeta := meta.DurationMillis <= 0 ||
		len(meta.GenreNames) == 0 ||
		strings.TrimSpace(meta.AlbumName) == "" ||
		strings.TrimSpace(meta.ComposerName) == "" ||
		strings.TrimSpace(meta.ReleaseDate) == ""
	if !needsMeta {
		return meta
	}

	storefront := normalizeSongCommentStorefront(meta.Storefront)
	lang := defaultSongCommentAppleLangZH
	if normalizeTelegramLanguage(b.getChatLanguage(chatID)) == telegramLanguageEn {
		lang = defaultSongCommentAppleLangEN
	}
	if configured := strings.TrimSpace(Config.Language); configured != "" {
		lang = configured
	}
	token := strings.TrimSpace(b.appleToken)
	ctx := b.operationContext()
	reqCtx, cancel := context.WithTimeout(ctx, songCommentAppleMetaTimeout)
	defer cancel()

	resp, err := ampapi.GetSongRespWithContext(reqCtx, storefront, meta.TrackID, lang, token)
	if err != nil || resp == nil || len(resp.Data) == 0 {
		return meta
	}
	attributes := resp.Data[0].Attributes
	if meta.Title == "" {
		meta.Title = strings.TrimSpace(attributes.Name)
	}
	if meta.Performer == "" {
		meta.Performer = strings.TrimSpace(attributes.ArtistName)
	}
	if meta.DurationMillis <= 0 {
		meta.DurationMillis = int64(attributes.DurationInMillis)
	}
	if meta.AlbumName == "" {
		meta.AlbumName = strings.TrimSpace(attributes.AlbumName)
	}
	if meta.ComposerName == "" {
		meta.ComposerName = strings.TrimSpace(attributes.ComposerName)
	}
	if meta.ReleaseDate == "" {
		meta.ReleaseDate = strings.TrimSpace(attributes.ReleaseDate)
	}
	if len(meta.GenreNames) == 0 {
		meta.GenreNames = sanitizeTagList(attributes.GenreNames)
	}
	if len(meta.AudioTraits) == 0 {
		meta.AudioTraits = sanitizeTagList(attributes.AudioTraits)
	}
	if meta.ContentRating == "" {
		meta.ContentRating = strings.TrimSpace(strings.ToLower(attributes.ContentRating))
	}
	meta.HasLyrics = meta.HasLyrics || attributes.HasLyrics
	if meta.Storefront == "" {
		meta.Storefront = storefront
	}
	return normalizeSongCommentMeta(meta)
}

func normalizeSongCommentStorefront(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if len(raw) == 2 {
		return raw
	}
	cfg := strings.TrimSpace(strings.ToLower(Config.Storefront))
	if len(cfg) == 2 {
		return cfg
	}
	return defaultSongCommentStorefront
}

func (b *TelegramBot) fetchLastFMTrackProfile(lang string, title string, performer string) (songCommentProfile, error) {
	values := url.Values{}
	values.Set("method", "track.getInfo")
	values.Set("autocorrect", "1")
	if lang == telegramLanguageEn {
		values.Set("lang", "en")
	} else {
		values.Set("lang", "zh")
	}
	if strings.TrimSpace(title) != "" {
		values.Set("track", strings.TrimSpace(title))
	}
	if strings.TrimSpace(performer) != "" {
		values.Set("artist", strings.TrimSpace(performer))
	}
	var resp lastFMTrackInfoResponse
	if err := b.doLastFMRequest(values, &resp); err != nil {
		return songCommentProfile{}, err
	}
	if resp.Error != 0 {
		return songCommentProfile{}, fmt.Errorf("lastfm track.getInfo error %d: %s", resp.Error, strings.TrimSpace(resp.Message))
	}
	summary := normalizeLastFMSummary(resp.Track.Wiki.Summary)
	if summary == "" {
		summary = normalizeLastFMSummary(resp.Track.Wiki.Content)
	}
	summary = truncateUTF8ByBytes(summary, maxSongCommentSummaryBytes)
	return songCommentProfile{
		Title:     strings.TrimSpace(resp.Track.Name),
		Performer: extractLastFMArtist(resp.Track.Artist),
		Summary:   summary,
		TrackTags: collectLastFMTags(resp.Track.TopTags.Tag),
		Listeners: parseInt64Loose(resp.Track.Listeners),
		Playcount: parseInt64Loose(resp.Track.Playcount),
	}, nil
}

func (b *TelegramBot) fetchLastFMTrackTopTags(title string, performer string) ([]string, error) {
	values := url.Values{}
	values.Set("method", "track.getTopTags")
	values.Set("autocorrect", "1")
	if strings.TrimSpace(title) != "" {
		values.Set("track", strings.TrimSpace(title))
	}
	if strings.TrimSpace(performer) != "" {
		values.Set("artist", strings.TrimSpace(performer))
	}
	var resp lastFMTopTagsResponse
	if err := b.doLastFMRequest(values, &resp); err != nil {
		return nil, err
	}
	if resp.Error != 0 {
		return nil, fmt.Errorf("lastfm track.getTopTags error %d: %s", resp.Error, strings.TrimSpace(resp.Message))
	}
	return collectLastFMTags(resp.TopTags.Tag), nil
}

func (b *TelegramBot) fetchLastFMArtistTopTags(performer string) ([]string, error) {
	performer = strings.TrimSpace(performer)
	if performer == "" {
		return nil, fmt.Errorf("artist is empty")
	}
	values := url.Values{}
	values.Set("method", "artist.getTopTags")
	values.Set("autocorrect", "1")
	values.Set("artist", performer)
	var resp lastFMTopTagsResponse
	if err := b.doLastFMRequest(values, &resp); err != nil {
		return nil, err
	}
	if resp.Error != 0 {
		return nil, fmt.Errorf("lastfm artist.getTopTags error %d: %s", resp.Error, strings.TrimSpace(resp.Message))
	}
	return collectLastFMTags(resp.TopTags.Tag), nil
}

func (b *TelegramBot) doLastFMRequest(values url.Values, out any) error {
	if b == nil {
		return fmt.Errorf("bot is nil")
	}
	if out == nil {
		return fmt.Errorf("lastfm decode target is nil")
	}
	apiKey := strings.TrimSpace(resolveLastFMAPIKey())
	if apiKey == "" {
		return fmt.Errorf("lastfm api key is empty")
	}
	query := url.Values{}
	for key, vals := range values {
		if len(vals) == 0 {
			continue
		}
		for _, v := range vals {
			query.Add(key, v)
		}
	}
	query.Set("api_key", apiKey)
	query.Set("format", "json")
	endpoint := lastFMTrackInfoAPI + "?" + query.Encode()

	ctx := b.operationContext()
	reqCtx, cancel := context.WithTimeout(ctx, songCommentFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	client := networkHTTPClient
	if b.client != nil {
		client = b.client
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("lastfm request failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(out)
}

func renderSongCommentTemplate(lang string, meta AudioMeta, summary string, styleTags []string, listeners int64, playcount int64) string {
	if lang == telegramLanguageEn {
		return renderSongCommentTemplateEN(meta, summary, styleTags, listeners, playcount)
	}
	return renderSongCommentTemplateZH(meta, summary, styleTags, listeners, playcount)
}

func renderSongCommentTemplateZH(meta AudioMeta, summary string, styleTags []string, listeners int64, playcount int64) string {
	title := meta.Title
	if title == "" {
		title = defaultSongCommentTitleZH
	}
	performer := meta.Performer
	if performer == "" {
		performer = defaultSongCommentArtistZH
	}
	if strings.TrimSpace(summary) == "" {
		summary = defaultSongCommentSummaryZH
	}
	stylesText := "流行"
	if len(styleTags) > 0 {
		stylesText = strings.Join(styleTags, " / ")
	}

	lines := []string{
		"🎧 歌曲赏析",
		fmt.Sprintf("《%s》- %s", title, performer),
		fmt.Sprintf("风格：整体偏向 %s，重心在律动与情绪氛围的持续推进。", stylesText),
		fmt.Sprintf("结构：%s", buildStructureSentenceZH(meta.DurationMillis)),
		fmt.Sprintf("编曲：%s", buildArrangementSentenceZH(styleTags, meta.AudioTraits)),
		fmt.Sprintf("人声：%s", buildVocalSentenceZH(styleTags)),
		fmt.Sprintf("主题：%s", buildThemeSentenceZH(meta.HasLyrics, meta.ContentRating)),
	}
	if credit := buildCreditSentenceZH(meta); credit != "" {
		lines = append(lines, "创作信息："+credit)
	}
	lines = append(lines, "资料补充："+summary)
	if heat := buildHeatSentenceZH(listeners, playcount); heat != "" {
		lines = append(lines, "社区热度："+heat)
	}
	lines = append(lines, "总结："+buildConclusionSentenceZH(styleTags))
	return strings.Join(lines, "\n")
}

func renderSongCommentTemplateEN(meta AudioMeta, summary string, styleTags []string, listeners int64, playcount int64) string {
	title := meta.Title
	if title == "" {
		title = defaultSongCommentTitleEN
	}
	performer := meta.Performer
	if performer == "" {
		performer = defaultSongCommentArtistEN
	}
	if strings.TrimSpace(summary) == "" {
		summary = defaultSongCommentSummaryEN
	}
	stylesText := "Pop"
	if len(styleTags) > 0 {
		stylesText = strings.Join(styleTags, " / ")
	}
	lines := []string{
		"🎧 Song comment",
		fmt.Sprintf("%s - %s", title, performer),
		fmt.Sprintf("Style: The track leans toward %s with emphasis on groove and emotional atmosphere.", stylesText),
		fmt.Sprintf("Structure: %s", buildStructureSentenceEN(meta.DurationMillis)),
		fmt.Sprintf("Arrangement: %s", buildArrangementSentenceEN(styleTags, meta.AudioTraits)),
		fmt.Sprintf("Vocal: %s", buildVocalSentenceEN(styleTags)),
		fmt.Sprintf("Theme: %s", buildThemeSentenceEN(meta.HasLyrics, meta.ContentRating)),
	}
	if credit := buildCreditSentenceEN(meta); credit != "" {
		lines = append(lines, "Credits: "+credit)
	}
	lines = append(lines, "Notes: "+summary)
	if heat := buildHeatSentenceEN(listeners, playcount); heat != "" {
		lines = append(lines, "Community: "+heat)
	}
	lines = append(lines, "Summary: "+buildConclusionSentenceEN(styleTags))
	return strings.Join(lines, "\n")
}

func buildStructureSentenceZH(durationMillis int64) string {
	seconds := durationMillis / 1000
	if seconds <= 0 {
		return "整体采用常见主歌—副歌推进，段落衔接平稳，情绪由弱到强逐步展开。"
	}
	if seconds < 210 {
		return fmt.Sprintf("时长约 %s，段落较紧凑，通常以主歌—副歌循环为主，过门简洁。", formatDurationMMSS(seconds))
	}
	if seconds < 300 {
		return fmt.Sprintf("时长约 %s，主歌与副歌构成主体，中段通过桥段或器乐过门完成抬升。", formatDurationMMSS(seconds))
	}
	return fmt.Sprintf("时长约 %s，在主副歌框架外留有更完整的桥段与尾声，层次展开更充分。", formatDurationMMSS(seconds))
}

func buildStructureSentenceEN(durationMillis int64) string {
	seconds := durationMillis / 1000
	if seconds <= 0 {
		return "It follows a familiar verse-chorus flow with smooth transitions and gradual emotional lift."
	}
	if seconds < 210 {
		return fmt.Sprintf("At around %s, the form is compact, mostly driven by verse-chorus repetition with brief transitions.", formatDurationMMSS(seconds))
	}
	if seconds < 300 {
		return fmt.Sprintf("At around %s, verse and chorus remain central while a bridge/interlude provides the key lift.", formatDurationMMSS(seconds))
	}
	return fmt.Sprintf("At around %s, the song leaves room beyond standard verse-chorus for fuller bridge and outro development.", formatDurationMMSS(seconds))
}

func buildArrangementSentenceZH(styleTags []string, audioTraits []string) string {
	if hasAnyStyle(styleTags, "r&b", "rnb", "soul") {
		base := "编曲更偏向和弦铺底与轻量鼓组驱动，低频控制相对克制，整体听感偏温暖。"
		if hasAnyTrait(audioTraits, "dolby-atmos", "spatial") {
			return base + "在空间音频下，层次定位与背景声部会更容易分辨。"
		}
		if hasAnyTrait(audioTraits, "hi-res-lossless", "lossless") {
			return base + "在无损规格下，细节和动态边缘更清晰。"
		}
		return base
	}
	if hasAnyStyle(styleTags, "hip-hop", "rap", "trap") {
		return "编曲以节奏骨架和低频律动为核心，段落变化靠鼓点密度与音色切换推动。"
	}
	base := "编曲以旋律与和声层叠推进，鼓组和低频承担稳态支撑，重在氛围连续性。"
	if hasAnyTrait(audioTraits, "hi-res-lossless", "lossless", "dolby-atmos", "spatial") {
		return base + "若使用高规格播放，空间层次与细节会更明显。"
	}
	return base
}

func buildArrangementSentenceEN(styleTags []string, audioTraits []string) string {
	if hasAnyStyle(styleTags, "r&b", "rnb", "soul") {
		base := "The arrangement leans on harmonic beds and lightweight drums, with restrained low-end and a warm overall tone."
		if hasAnyTrait(audioTraits, "dolby-atmos", "spatial") {
			return base + " In spatial playback, layer positioning and backing details become easier to hear."
		}
		if hasAnyTrait(audioTraits, "hi-res-lossless", "lossless") {
			return base + " In lossless playback, micro-details and transients are clearer."
		}
		return base
	}
	if hasAnyStyle(styleTags, "hip-hop", "rap", "trap") {
		return "The arrangement is rhythm-forward, where section contrast is shaped by drum density and timbral switches."
	}
	base := "The arrangement advances through melody and harmony layering, while drums and low-end keep a steady backbone."
	if hasAnyTrait(audioTraits, "hi-res-lossless", "lossless", "dolby-atmos", "spatial") {
		return base + " Higher-quality playback reveals more separation and detail."
	}
	return base
}

func buildVocalSentenceZH(styleTags []string) string {
	if hasAnyStyle(styleTags, "r&b", "rnb", "soul") {
		return "人声倾向连音与气声处理，咬字不过度用力，更多通过细微强弱变化传递情绪。"
	}
	return "人声表达整体偏克制，强调语气与旋律贴合度，以连续情绪而非高强度爆发为主。"
}

func buildVocalSentenceEN(styleTags []string) string {
	if hasAnyStyle(styleTags, "r&b", "rnb", "soul") {
		return "The vocal delivery favors legato and airy phrasing, relying on subtle dynamics rather than forceful projection."
	}
	return "The vocal approach stays relatively restrained, prioritizing phrase shaping and melodic continuity."
}

func buildThemeSentenceZH(hasLyrics bool, contentRating string) string {
	if !hasLyrics {
		return "文本信息相对有限，情绪主要通过旋律线与编曲纹理完成传达。"
	}
	if contentRating == "explicit" {
		return "歌词围绕个人关系与情绪波动展开，表达更直接，部分措辞强度更高。"
	}
	return "歌词主要围绕个人情感关系展开，语言偏直白，核心记忆点通常落在副歌重复句式。"
}

func buildThemeSentenceEN(hasLyrics bool, contentRating string) string {
	if !hasLyrics {
		return "Textual information is limited; emotion is carried mainly by melody and arrangement texture."
	}
	if contentRating == "explicit" {
		return "The lyric narrative focuses on personal relationships and emotional shifts with more direct wording."
	}
	return "The lyric narrative centers on personal emotions and relationships, often anchored by repeated chorus hooks."
}

func buildCreditSentenceZH(meta AudioMeta) string {
	parts := make([]string, 0, 3)
	if composer := strings.TrimSpace(meta.ComposerName); composer != "" {
		parts = append(parts, "作曲/署名："+composer)
	}
	if album := strings.TrimSpace(meta.AlbumName); album != "" {
		parts = append(parts, "所属专辑《"+album+"》")
	}
	if year := extractReleaseYear(meta.ReleaseDate); year != "" {
		parts = append(parts, "发行年份 "+year)
	}
	return strings.Join(parts, "；")
}

func buildCreditSentenceEN(meta AudioMeta) string {
	parts := make([]string, 0, 3)
	if composer := strings.TrimSpace(meta.ComposerName); composer != "" {
		parts = append(parts, "composer credit: "+composer)
	}
	if album := strings.TrimSpace(meta.AlbumName); album != "" {
		parts = append(parts, "album: "+album)
	}
	if year := extractReleaseYear(meta.ReleaseDate); year != "" {
		parts = append(parts, "released in "+year)
	}
	return strings.Join(parts, "; ")
}

func buildHeatSentenceZH(listeners int64, playcount int64) string {
	if listeners <= 0 && playcount <= 0 {
		return ""
	}
	if listeners > 0 && playcount > 0 {
		return fmt.Sprintf("Last.fm 记录播放约 %s 次、听众约 %s 人。", formatIntWithComma(playcount), formatIntWithComma(listeners))
	}
	if playcount > 0 {
		return fmt.Sprintf("Last.fm 记录播放约 %s 次。", formatIntWithComma(playcount))
	}
	return fmt.Sprintf("Last.fm 记录听众约 %s 人。", formatIntWithComma(listeners))
}

func buildHeatSentenceEN(listeners int64, playcount int64) string {
	if listeners <= 0 && playcount <= 0 {
		return ""
	}
	if listeners > 0 && playcount > 0 {
		return fmt.Sprintf("Last.fm reports about %s plays and %s listeners.", formatIntWithComma(playcount), formatIntWithComma(listeners))
	}
	if playcount > 0 {
		return fmt.Sprintf("Last.fm reports about %s plays.", formatIntWithComma(playcount))
	}
	return fmt.Sprintf("Last.fm reports about %s listeners.", formatIntWithComma(listeners))
}

func buildConclusionSentenceZH(styleTags []string) string {
	if hasAnyStyle(styleTags, "r&b", "rnb", "soul") {
		return "作品重点在律动细节与情绪延展，整体更注重听感质地而非大幅结构反转。"
	}
	return defaultSongCommentConclusionZH
}

func buildConclusionSentenceEN(styleTags []string) string {
	if hasAnyStyle(styleTags, "r&b", "rnb", "soul") {
		return "The core value lies in groove detail and emotional extension, with texture prioritized over dramatic structural turns."
	}
	return defaultSongCommentConclusionEN
}

func buildMergedStyleTags(lang string, genreNames []string, trackTags []string, artistTags []string) []string {
	merged := make([]string, 0, defaultSongCommentMaxStyleCount)
	seen := make(map[string]struct{}, defaultSongCommentMaxStyleCount*2)
	appendTag := func(raw string) {
		display, key := normalizeStyleTag(raw, lang)
		if display == "" || key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		merged = append(merged, display)
	}
	for _, genre := range genreNames {
		appendTag(genre)
		if len(merged) >= defaultSongCommentMaxStyleCount {
			return merged
		}
	}
	for _, tag := range trackTags {
		appendTag(tag)
		if len(merged) >= defaultSongCommentMaxStyleCount {
			return merged
		}
	}
	for _, tag := range artistTags {
		appendTag(tag)
		if len(merged) >= defaultSongCommentMaxStyleCount {
			return merged
		}
	}
	return merged
}

func normalizeStyleTag(raw string, lang string) (display string, key string) {
	tag := strings.TrimSpace(raw)
	if tag == "" {
		return "", ""
	}
	normalized := strings.ToLower(strings.TrimSpace(tag))
	normalized = strings.ReplaceAll(normalized, "_", " ")
	normalized = strings.ReplaceAll(normalized, "-", " ")
	normalized = strings.Join(strings.Fields(normalized), " ")
	if normalized == "" {
		return "", ""
	}
	switch normalized {
	case "rnb", "r&b", "rhythm and blues", "rhythm & blues":
		return "R&B", "r&b"
	case "soul":
		return "Soul", "soul"
	case "hip hop", "hiphop", "rap", "trap":
		return "Hip-Hop", "hip-hop"
	case "pop":
		return "Pop", "pop"
	case "jazz":
		return "Jazz", "jazz"
	case "neo soul", "neosoul":
		return "Neo Soul", "neo-soul"
	case "funk":
		return "Funk", "funk"
	case "electronic", "electronica":
		return "Electronic", "electronic"
	case "c pop", "cpop", "mandopop", "chinese pop", "chinese":
		if lang == telegramLanguageEn {
			return "C-Pop", "c-pop"
		}
		return "华语流行", "c-pop"
	}

	if isWeakStyleTag(normalized) {
		return "", ""
	}
	if len(tag) > 24 {
		return "", ""
	}
	key = normalized
	return strings.TrimSpace(tag), key
}

func isWeakStyleTag(normalized string) bool {
	switch normalized {
	case "hong kong", "male vocalists", "female vocalists", "seen live", "favorites", "favourite", "favorite":
		return true
	default:
		return false
	}
}

func hasAnyStyle(styleTags []string, candidates ...string) bool {
	if len(styleTags) == 0 || len(candidates) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(styleTags))
	for _, tag := range styleTags {
		_, key := normalizeStyleTag(tag, telegramLanguageEn)
		if key == "" {
			continue
		}
		set[key] = struct{}{}
	}
	for _, raw := range candidates {
		_, key := normalizeStyleTag(raw, telegramLanguageEn)
		if key == "" {
			continue
		}
		if _, ok := set[key]; ok {
			return true
		}
	}
	return false
}

func hasAnyTrait(traits []string, candidates ...string) bool {
	if len(traits) == 0 || len(candidates) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(traits))
	for _, trait := range traits {
		key := strings.ToLower(strings.TrimSpace(trait))
		if key == "" {
			continue
		}
		set[key] = struct{}{}
	}
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" {
			continue
		}
		if _, ok := set[key]; ok {
			return true
		}
	}
	return false
}

func buildSongCommentCacheKey(trackID string, title string, performer string, lang string) string {
	trackID = strings.TrimSpace(trackID)
	if trackID != "" {
		return "track|" + strings.ToLower(trackID) + "|" + strings.ToLower(strings.TrimSpace(lang))
	}
	return "meta|" +
		strings.ToLower(strings.TrimSpace(title)) + "|" +
		strings.ToLower(strings.TrimSpace(performer)) + "|" +
		strings.ToLower(strings.TrimSpace(lang))
}

func (b *TelegramBot) getSongCommentFromCache(key string) (string, bool) {
	if b == nil || strings.TrimSpace(key) == "" {
		return "", false
	}
	now := time.Now()
	b.songCommentMu.Lock()
	defer b.songCommentMu.Unlock()
	entry, ok := b.songCommentCache[key]
	if !ok {
		return "", false
	}
	if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
		delete(b.songCommentCache, key)
		return "", false
	}
	return entry.Text, strings.TrimSpace(entry.Text) != ""
}

func (b *TelegramBot) storeSongCommentCache(key string, text string) {
	if b == nil || strings.TrimSpace(key) == "" || strings.TrimSpace(text) == "" {
		return
	}
	b.songCommentMu.Lock()
	if b.songCommentCache == nil {
		b.songCommentCache = make(map[string]songCommentCacheEntry)
	}
	b.songCommentCache[key] = songCommentCacheEntry{
		Text:      text,
		ExpiresAt: time.Now().Add(songCommentCacheTTL),
	}
	b.songCommentMu.Unlock()
}

func extractLastFMArtist(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if name, ok := v["name"].(string); ok {
			return strings.TrimSpace(name)
		}
		if text, ok := v["#text"].(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func collectLastFMTags(tags []lastFMTag) []string {
	if len(tags) == 0 {
		return nil
	}
	result := make([]string, 0, defaultSongCommentMaxStyleCount)
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		name := strings.TrimSpace(tag.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
		if len(result) >= defaultSongCommentMaxStyleCount {
			break
		}
	}
	return result
}

func sanitizeTagList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item)
		if text == "" {
			continue
		}
		key := strings.ToLower(text)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, text)
	}
	return out
}

func normalizeLastFMSummary(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	text = lastFMHTMLTagRe.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	for _, marker := range lastFMNoiseKeys {
		if idx := strings.Index(text, marker); idx >= 0 {
			text = text[:idx]
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

func parseInt64Loose(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	raw = strings.ReplaceAll(raw, ",", "")
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func extractReleaseYear(releaseDate string) string {
	releaseDate = strings.TrimSpace(releaseDate)
	if len(releaseDate) < 4 {
		return ""
	}
	year := releaseDate[:4]
	if _, err := strconv.Atoi(year); err != nil {
		return ""
	}
	return year
}

func formatDurationMMSS(seconds int64) string {
	if seconds <= 0 {
		return "0:00"
	}
	min := seconds / 60
	sec := seconds % 60
	return fmt.Sprintf("%d:%02d", min, sec)
}

func formatIntWithComma(value int64) string {
	if value <= 0 {
		return "0"
	}
	raw := strconv.FormatInt(value, 10)
	if len(raw) <= 3 {
		return raw
	}
	neg := false
	if strings.HasPrefix(raw, "-") {
		neg = true
		raw = strings.TrimPrefix(raw, "-")
	}
	mod := len(raw) % 3
	if mod == 0 {
		mod = 3
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(raw[:mod])
	for i := mod; i < len(raw); i += 3 {
		b.WriteByte(',')
		b.WriteString(raw[i : i+3])
	}
	return b.String()
}
