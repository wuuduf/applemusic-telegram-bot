package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/lyrics"
	"github.com/wuuduf/applemusic-telegram-bot/utils/runv2"
	"github.com/wuuduf/applemusic-telegram-bot/utils/runv3"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
)

type trackDownloadContext struct {
	session        *DownloadSession
	cfg            *structs.ConfigSet
	track          *task.Track
	token          string
	mediaUserToken string

	needDlAacLc       bool
	actualFormat      string
	quality           string
	tagString         string
	lrc               string
	trackPath         string
	convertedPath     string
	conversionEnabled bool
	considerConverted bool
}

func (ctx *trackDownloadContext) sourceFormat() string {
	switch {
	case ctx == nil:
		return defaultTelegramFormat
	case ctx.session != nil && ctx.session.DlAtmos:
		return telegramFormatAtmos
	case ctx != nil && (ctx.session != nil && ctx.session.DlAac || ctx.needDlAacLc):
		return telegramFormatAac
	default:
		return telegramFormatAlac
	}
}

func (ctx *trackDownloadContext) recordedFormat() string {
	if ctx == nil || ctx.track == nil {
		return defaultTelegramFormat
	}
	path := strings.TrimSpace(ctx.track.SavePath)
	if path == "" {
		path = strings.TrimSpace(ctx.trackPath)
	}
	return inferTelegramAudioFormatFromPath(path, ctx.sourceFormat())
}

func ripTrack(session *DownloadSession, track *task.Track, token string, mediaUserToken string) {
	if session == nil || track == nil {
		return
	}
	cfg := &session.Config
	session.Counter.Total++
	fmt.Printf("Track %d of %d: %s\n", track.TaskNum, track.TaskTotal, track.Type)
	if track.PreType == "playlists" && cfg.UseSongInfoForPlaylist {
		track.GetAlbumData(token)
	}
	if track.Type == "music-videos" {
		handleTrackMusicVideoStage(session, track, token, mediaUserToken)
		return
	}

	ctx, ok := resolveTrackDownloadContextStage(session, track, token, mediaUserToken)
	if !ok {
		return
	}
	if handleTrackReuseStage(ctx) {
		return
	}
	if !downloadTrackMediaStage(ctx) {
		return
	}
	if !postProcessTrackStage(ctx) {
		return
	}
	finishTrackDownloadStage(ctx)
}

func handleTrackMusicVideoStage(session *DownloadSession, track *task.Track, token string, mediaUserToken string) {
	if len(mediaUserToken) <= 50 {
		fmt.Println("Invalid media-user-token")
		session.Counter.Error++
		return
	}
	if _, err := exec.LookPath("mp4decrypt"); err != nil {
		fmt.Println("mp4decrypt is not found.")
		session.Counter.Error++
		return
	}
	if _, err := os.Stat(track.SaveDir); os.IsNotExist(err) {
		if err := os.MkdirAll(track.SaveDir, os.ModePerm); err != nil {
			fmt.Println("Failed to prepare MV save directory.")
			session.Counter.Error++
			return
		}
	}
	if _, err := os.Stat(track.SaveDir); err != nil {
		fmt.Println("Failed to prepare MV save directory.")
		session.Counter.Error++
		return
	}
	if err := mvDownloader(session, track.ID, track.SaveDir, token, track.Storefront, mediaUserToken, track); err != nil {
		fmt.Println("⚠ Failed to dl MV:", err)
		session.Counter.Error++
		return
	}
	session.Counter.Success++
}

func resolveTrackDownloadContextStage(session *DownloadSession, track *task.Track, token string, mediaUserToken string) (*trackDownloadContext, bool) {
	cfg := &session.Config
	ctx := &trackDownloadContext{
		session:        session,
		cfg:            cfg,
		track:          track,
		token:          token,
		mediaUserToken: mediaUserToken,
	}

	needDlAacLc := session.DlAac && cfg.AacType == "aac-lc"
	if track.WebM3u8 == "" && !needDlAacLc {
		if session.DlAtmos {
			fmt.Println("Unavailable")
			session.Counter.Unavailable++
			return nil, false
		}
		fmt.Println("Unavailable, trying to dl aac-lc")
		needDlAacLc = true
	}
	ctx.needDlAacLc = needDlAacLc

	needCheck := false
	if cfg.GetM3u8Mode == "all" {
		needCheck = true
	} else if cfg.GetM3u8Mode == "hires" && contains(track.Resp.Attributes.AudioTraits, "hi-res-lossless") {
		needCheck = true
	}
	if needCheck && !ctx.needDlAacLc {
		enhancedHLS, _ := checkM3u8(session, track.ID, "song")
		if strings.HasSuffix(enhancedHLS, ".m3u8") {
			track.DeviceM3u8 = enhancedHLS
			track.M3u8 = enhancedHLS
		}
	}

	if strings.Contains(cfg.SongFileFormat, "Quality") {
		if session.DlAtmos {
			ctx.quality = fmt.Sprintf("%dKbps", cfg.AtmosMax-2000)
		} else if ctx.needDlAacLc {
			ctx.quality = "256Kbps"
		} else {
			var err error
			_, ctx.quality, err = extractMedia(session, track.M3u8, true)
			if err != nil {
				fmt.Println("Failed to extract quality from manifest.\n", err)
				session.Counter.Error++
				return nil, false
			}
		}
	}
	track.Quality = ctx.quality

	tagParts := []string{}
	if track.Resp.Attributes.IsAppleDigitalMaster && cfg.AppleMasterChoice != "" {
		tagParts = append(tagParts, cfg.AppleMasterChoice)
	}
	if track.Resp.Attributes.ContentRating == "explicit" && cfg.ExplicitChoice != "" {
		tagParts = append(tagParts, cfg.ExplicitChoice)
	}
	if track.Resp.Attributes.ContentRating == "clean" && cfg.CleanChoice != "" {
		tagParts = append(tagParts, cfg.CleanChoice)
	}
	ctx.tagString = strings.Join(tagParts, " ")

	songName := strings.NewReplacer(
		"{SongId}", track.ID,
		"{SongNumer}", fmt.Sprintf("%02d", track.TaskNum),
		"{SongName}", LimitStringWithConfig(cfg, track.Resp.Attributes.Name),
		"{ArtistName}", LimitStringWithConfig(cfg, track.Resp.Attributes.ArtistName),
		"{DiscNumber}", fmt.Sprintf("%0d", track.Resp.Attributes.DiscNumber),
		"{TrackNumber}", fmt.Sprintf("%0d", track.Resp.Attributes.TrackNumber),
		"{Quality}", ctx.quality,
		"{Tag}", ctx.tagString,
		"{Codec}", track.Codec,
	).Replace(cfg.SongFileFormat)
	fmt.Println(songName)
	filename := fmt.Sprintf("%s.m4a", forbiddenNames.ReplaceAllString(songName, "_"))
	track.SaveName = filename
	ctx.trackPath = filepath.Join(track.SaveDir, track.SaveName)
	lrcFilename := fmt.Sprintf("%s.%s", forbiddenNames.ReplaceAllString(songName, "_"), cfg.LrcFormat)

	ctx.conversionEnabled = cfg.ConvertAfterDownload &&
		cfg.ConvertFormat != "" &&
		strings.ToLower(cfg.ConvertFormat) != "copy"
	if ctx.conversionEnabled {
		ctx.convertedPath = strings.TrimSuffix(ctx.trackPath, filepath.Ext(ctx.trackPath)) + "." + strings.ToLower(cfg.ConvertFormat)
		if !cfg.ConvertKeepOriginal {
			ctx.considerConverted = true
		}
	}
	switch {
	case ctx.conversionEnabled && strings.EqualFold(cfg.ConvertFormat, telegramFormatFlac):
		ctx.actualFormat = telegramFormatFlac
	case session.DlAtmos:
		ctx.actualFormat = telegramFormatAtmos
	case session.DlAac || ctx.needDlAacLc:
		ctx.actualFormat = telegramFormatAac
	default:
		ctx.actualFormat = telegramFormatAlac
	}

	if cfg.EmbedLrc || cfg.SaveLrcFile {
		lrcStr, err := lyrics.GetWithContext(session.downloadContext(), track.Storefront, track.ID, cfg.LrcType, cfg.Language, cfg.LrcFormat, token, mediaUserToken)
		if err != nil {
			fmt.Println(err)
		} else {
			if cfg.SaveLrcFile {
				if err := writeLyrics(track.SaveDir, lrcFilename, lrcStr); err != nil {
					fmt.Printf("Failed to write lyrics")
				}
			}
			if cfg.EmbedLrc {
				ctx.lrc = lrcStr
			}
		}
	}

	return ctx, true
}

func handleTrackReuseStage(ctx *trackDownloadContext) bool {
	if !ctx.session.shouldReuseExistingFiles() {
		return false
	}

	existsOriginal, err := fileExists(ctx.trackPath)
	if err != nil {
		fmt.Println("Failed to check if track exists.")
	}
	if existsOriginal {
		fmt.Println("Track already exists locally.")
		ctx.track.SavePath = ctx.trackPath
		ctx.track.SaveName = filepath.Base(ctx.trackPath)
		if ctx.conversionEnabled {
			if ctx.considerConverted {
				existsConverted, err2 := fileExists(ctx.convertedPath)
				if err2 == nil && existsConverted {
					ctx.track.SavePath = ctx.convertedPath
					ctx.track.SaveName = filepath.Base(ctx.convertedPath)
				} else {
					convertIfNeeded(ctx.session, ctx.track, ctx.lrc)
				}
			} else {
				convertIfNeeded(ctx.session, ctx.track, ctx.lrc)
			}
		}
		ctx.session.recordDownloadedTrack(ctx.track, ctx.recordedFormat())
		ctx.session.Counter.Success++
		ctx.session.OkDict[ctx.track.PreID] = append(ctx.session.OkDict[ctx.track.PreID], ctx.track.TaskNum)
		return true
	}

	if ctx.considerConverted {
		existsConverted, err2 := fileExists(ctx.convertedPath)
		if err2 == nil && existsConverted {
			fmt.Println("Converted track already exists locally.")
			ctx.track.SavePath = ctx.convertedPath
			ctx.track.SaveName = filepath.Base(ctx.convertedPath)
			ctx.session.recordDownloadedTrack(ctx.track, ctx.recordedFormat())
			ctx.session.Counter.Success++
			ctx.session.OkDict[ctx.track.PreID] = append(ctx.session.OkDict[ctx.track.PreID], ctx.track.TaskNum)
			return true
		}
	}

	return false
}

func downloadTrackMediaStage(ctx *trackDownloadContext) bool {
	if ctx.needDlAacLc {
		if len(ctx.mediaUserToken) <= 50 {
			fmt.Println("Invalid media-user-token")
			ctx.session.Counter.Error++
			return false
		}
		if _, err := runv3.RunWithContext(ctx.session.downloadContext(), ctx.track.ID, ctx.trackPath, ctx.token, ctx.mediaUserToken, false, "", ctx.session.ActiveProgress); err != nil {
			fmt.Println("Failed to dl aac-lc:", err)
			if err.Error() == "Unavailable" {
				ctx.session.Counter.Unavailable++
				return false
			}
			ctx.session.Counter.Error++
			return false
		}
		return true
	}

	trackM3U8URL, _, err := extractMedia(ctx.session, ctx.track.M3u8, false)
	if err != nil {
		fmt.Println("⚠ Failed to extract info from manifest:", err)
		ctx.session.Counter.Unavailable++
		return false
	}
	if err := runv2.RunWithContext(ctx.session.downloadContext(), ctx.track.ID, trackM3U8URL, ctx.trackPath, ctx.session.Config, ctx.session.ActiveProgress); err != nil {
		fmt.Println("Failed to run v2:", err)
		ctx.session.Counter.Error++
		return false
	}
	return true
}

func postProcessTrackStage(ctx *trackDownloadContext) bool {
	tags := []string{
		"tool=",
		"artist=AppleMusic",
	}
	if ctx.cfg.EmbedCover {
		if ctx.session.shouldDownloadStaticCover() && (strings.Contains(ctx.track.PreID, "pl.") || strings.Contains(ctx.track.PreID, "ra.")) && ctx.cfg.DlAlbumcoverForPlaylist {
			var err error
			ctx.track.CoverPath, err = writeCoverWithConfig(ctx.track.SaveDir, ctx.track.ID, ctx.track.Resp.Attributes.Artwork.URL, ctx.cfg)
			if err != nil {
				fmt.Println("Failed to write cover.")
			}
		}
		if strings.TrimSpace(ctx.track.CoverPath) != "" {
			tags = append(tags, fmt.Sprintf("cover=%s", ctx.track.CoverPath))
		}
	}
	if _, err := runMP4BoxWithTags(ctx.session.downloadContext(), tags, ctx.trackPath); err != nil {
		fmt.Printf("Embed failed: %v\n", err)
		ctx.session.Counter.Error++
		return false
	}
	if strings.TrimSpace(ctx.track.CoverPath) != "" && (strings.Contains(ctx.track.PreID, "pl.") || strings.Contains(ctx.track.PreID, "ra.")) && ctx.cfg.DlAlbumcoverForPlaylist {
		if err := os.Remove(ctx.track.CoverPath); err != nil {
			fmt.Printf("Error deleting file: %s\n", ctx.track.CoverPath)
			ctx.session.Counter.Error++
			return false
		}
	}
	ctx.track.SavePath = ctx.trackPath
	if err := writeMP4Tags(ctx.track, ctx.lrc, ctx.cfg); err != nil {
		fmt.Println("⚠ Failed to write tags in media:", err)
		ctx.session.Counter.Unavailable++
		return false
	}
	convertIfNeeded(ctx.session, ctx.track, ctx.lrc)
	return true
}

func finishTrackDownloadStage(ctx *trackDownloadContext) {
	ctx.session.recordDownloadedTrack(ctx.track, ctx.recordedFormat())
	ctx.session.Counter.Success++
	ctx.session.OkDict[ctx.track.PreID] = append(ctx.session.OkDict[ctx.track.PreID], ctx.track.TaskNum)
}
