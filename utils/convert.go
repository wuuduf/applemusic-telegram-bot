package utils

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/cmdrunner"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
)

// ProgressFunc reports conversion progress.
type ProgressFunc func(phase string, done, total int64)

// CONVERSION FEATURE: Determine if source codec is lossy (rough heuristic by extension/codec name).
func isLossySource(ext string, codec string) bool {
	ext = strings.ToLower(ext)
	if ext == ".m4a" && (codec == "AAC" || strings.Contains(codec, "AAC") || strings.Contains(codec, "ATMOS")) {
		return true
	}
	if ext == ".mp3" || ext == ".opus" || ext == ".ogg" {
		return true
	}
	return false
}

func normalizeLyrics(lrc string) string {
	lrc = strings.ReplaceAll(lrc, "\r\n", "\n")
	lrc = strings.ReplaceAll(lrc, "\r", "\n")
	return lrc
}

// CONVERSION FEATURE: Build ffmpeg arguments for desired target.
func buildFFmpegArgs(ffmpegPath, inPath, outPath, targetFmt, extraArgs string, coverPath string, lyrics string) ([]string, error) {
	args := []string{"-y", "-i", inPath}
	if coverPath != "" {
		args = append(args, "-i", coverPath)
	}
	switch targetFmt {
	case "flac":
		if coverPath != "" {
			args = append(args,
				"-map", "0:a",
				"-map", "1:v",
				"-c:a", "flac",
				"-c:v", "mjpeg",
				"-disposition:v", "attached_pic",
			)
		} else {
			args = append(args, "-map", "0:a", "-c:a", "flac")
		}
	case "mp3":
		args = append(args, "-vn")
		// VBR quality 2 ~ high quality
		args = append(args, "-c:a", "libmp3lame", "-qscale:a", "2")
	case "opus":
		args = append(args, "-vn")
		// Medium/high quality
		args = append(args, "-c:a", "libopus", "-b:a", "192k", "-vbr", "on")
	case "wav":
		args = append(args, "-vn")
		args = append(args, "-c:a", "pcm_s16le")
	case "copy":
		args = append(args, "-vn")
		// Just container copy (probably pointless for same container)
		args = append(args, "-c", "copy")
	default:
		return nil, fmt.Errorf("unsupported convert-format: %s", targetFmt)
	}
	if targetFmt == "flac" && lyrics != "" {
		args = append(args, "-metadata", fmt.Sprintf("LYRICS=%s", normalizeLyrics(lyrics)))
	}
	if extraArgs != "" {
		// naive split; for complex quoting you could enhance
		args = append(args, strings.Fields(extraArgs)...)
	}
	args = append(args, outPath)
	return args, nil
}

// ConvertIfNeeded performs post-download conversion when enabled.
func ConvertIfNeeded(track *task.Track, lrc string, cfg *structs.ConfigSet, coverPath string, progress ProgressFunc) {
	ConvertIfNeededWithContext(context.Background(), track, lrc, cfg, coverPath, progress)
}

func ConvertIfNeededWithContext(ctx context.Context, track *task.Track, lrc string, cfg *structs.ConfigSet, coverPath string, progress ProgressFunc) {
	if cfg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !cfg.ConvertAfterDownload {
		return
	}
	if cfg.ConvertFormat == "" {
		return
	}
	srcPath := track.SavePath
	if srcPath == "" {
		return
	}
	ext := strings.ToLower(filepath.Ext(srcPath))
	targetFmt := strings.ToLower(cfg.ConvertFormat)

	// Map extension for output
	if targetFmt == "copy" {
		fmt.Println("Convert (copy) requested; skipping because it produces no new format.")
		return
	}

	if cfg.ConvertSkipIfSourceMatch {
		if ext == "."+targetFmt {
			fmt.Printf("Conversion skipped (already %s)\n", targetFmt)
			return
		}
	}

	outBase := strings.TrimSuffix(srcPath, ext)
	outPath := outBase + "." + targetFmt

	// Handle lossy -> lossless cases: optionally skip or warn
	if (targetFmt == "flac" || targetFmt == "wav") && isLossySource(ext, track.Codec) {
		if cfg.ConvertSkipLossyToLossless {
			fmt.Println("Skipping conversion: source appears lossy and target is lossless; configured to skip.")
			return
		}
		if cfg.ConvertWarnLossyToLossless {
			fmt.Println("Warning: Converting lossy source to lossless container will not improve quality.")
		}
	}

	if _, err := exec.LookPath(cfg.FFmpegPath); err != nil {
		fmt.Printf("ffmpeg not found at '%s'; skipping conversion.\n", cfg.FFmpegPath)
		return
	}

	if progress != nil {
		progress("Converting", 0, 0)
	}
	args, err := buildFFmpegArgs(cfg.FFmpegPath, srcPath, outPath, targetFmt, cfg.ConvertExtraArgs, coverPath, lrc)
	if err != nil {
		fmt.Println("Conversion config error:", err)
		return
	}

	fmt.Printf("Converting -> %s ...\n", targetFmt)
	result, err := cmdrunner.RunWithOptions(ctx, cfg.FFmpegPath, args, cmdrunner.RunOptions{})
	if err != nil {
		fmt.Println("Conversion failed:", err)
		// leave original
		return
	}
	fmt.Printf("Conversion completed in %s: %s\n", result.Duration.Truncate(time.Millisecond), filepath.Base(outPath))

	if !cfg.ConvertKeepOriginal {
		if err := os.Remove(srcPath); err != nil {
			fmt.Println("Failed to remove original after conversion:", err)
		} else {
			track.SavePath = outPath
			track.SaveName = filepath.Base(outPath)
			fmt.Println("Original removed.")
		}
	} else {
		// Keep both but point track to new file (optional decision)
		track.SavePath = outPath
		track.SaveName = filepath.Base(outPath)
	}
}
