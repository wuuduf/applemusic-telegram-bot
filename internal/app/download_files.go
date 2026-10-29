package app

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	sharedstorage "github.com/wuuduf/applemusic-telegram-bot/internal/storage"
)

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

// telegramPersistentFiles lists the bot's own persistence files (cache, state,
// history, SQLite database and its sidecars, plus migrated JSON backups), which
// the download cleanup must never delete even when they live under a
// download root.
func telegramPersistentFiles(cacheFile string) map[string]bool {
	protected := map[string]bool{}
	cacheFile = strings.TrimSpace(cacheFile)
	if cacheFile == "" {
		return protected
	}
	stateFile := resolveTelegramStateFile(cacheFile)
	paths := []string{cacheFile, stateFile, resolveTelegramHistoryFile(cacheFile, stateFile)}
	for _, p := range append([]string(nil), paths...) {
		paths = append(paths, p+legacyMigratedSuffix)
	}
	paths = append(paths, sqliteSidecarPaths(resolveTelegramDBFile(cacheFile))...)
	for _, p := range paths {
		protected[filepath.Clean(p)] = true
	}
	return protected
}

func scanDownloadFolder(root string, cacheFile string) (int64, []downloadFileEntry, error) {
	var totalSize int64
	entries := []downloadFileEntry{}
	protected := telegramPersistentFiles(cacheFile)
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
		if protected[filepath.Clean(path)] {
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
		header.Method = zipMethodForPath(path)
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

// zipPrecompressedExts lists media formats that are already compressed;
// deflating them burns CPU for little to no size reduction.
var zipPrecompressedExts = map[string]bool{
	".m4a": true, ".mp4": true, ".m4v": true, ".mov": true, ".flac": true,
	".mp3": true, ".aac": true, ".ec3": true, ".ac3": true, ".opus": true,
	".ogg": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
	".gif": true, ".zip": true,
}

func zipMethodForPath(path string) uint16 {
	if zipPrecompressedExts[strings.ToLower(filepath.Ext(path))] {
		return zip.Store
	}
	return zip.Deflate
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
