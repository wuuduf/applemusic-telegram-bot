package storage

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

type Owner string
type Mode string

const (
	OwnerTelegram Owner = "telegram"
	OwnerCLI      Owner = "cli"
)

const (
	ModeDownload Mode = "download"
	ModeTemp     Mode = "temp"
	ModeCache    Mode = "cache"
)

type CleanupRoot struct {
	Owner Owner
	Mode  Mode
	Path  string
}

func ApplyTelegramStorageOverrides(cfg *structs.ConfigSet) {
	if cfg == nil {
		return
	}
	root := strings.TrimSpace(cfg.TelegramDownloadFolder)
	if root == "" {
		return
	}
	cfg.AlacSaveFolder = root
	cfg.AtmosSaveFolder = root
	cfg.AacSaveFolder = root
}

func TelegramCleanupRoots(cfg *structs.ConfigSet) []CleanupRoot {
	if cfg == nil {
		return nil
	}
	candidates := []CleanupRoot{
		cleanupRoot(OwnerTelegram, ModeDownload, cfg.TelegramDownloadFolder),
		cleanupRoot(OwnerTelegram, ModeTemp, os.Getenv("AMDL_TMPDIR")),
		cleanupRoot(OwnerTelegram, ModeTemp, os.Getenv("TMPDIR")),
	}
	if strings.TrimSpace(cfg.TelegramDownloadFolder) == "" {
		candidates = append(candidates,
			cleanupRoot(OwnerTelegram, ModeDownload, cfg.AlacSaveFolder),
			cleanupRoot(OwnerTelegram, ModeDownload, cfg.AtmosSaveFolder),
			cleanupRoot(OwnerTelegram, ModeDownload, cfg.AacSaveFolder),
		)
	}
	return dedupeRoots(candidates)
}

func CleanupRootPaths(roots []CleanupRoot) []string {
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root.Path) == "" {
			continue
		}
		paths = append(paths, root.Path)
	}
	return paths
}

func cleanupRoot(owner Owner, mode Mode, raw string) CleanupRoot {
	clean := filepath.Clean(strings.TrimSpace(raw))
	if isBroadCleanupRoot(clean) {
		return CleanupRoot{}
	}
	return CleanupRoot{
		Owner: owner,
		Mode:  mode,
		Path:  clean,
	}
}

func isBroadCleanupRoot(clean string) bool {
	if clean == "." || clean == "" || clean == string(filepath.Separator) {
		return true
	}
	switch clean {
	case "/tmp", "/var/tmp", "/var", "/usr", "/etc", "/bin", "/sbin", "/opt", "/home", "/Users":
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		home = filepath.Clean(strings.TrimSpace(home))
		if home != "" && clean == home {
			return true
		}
	}
	if filepath.IsAbs(clean) && cleanupPathDepth(clean) < 2 {
		return true
	}
	return false
}

func cleanupPathDepth(path string) int {
	trimmed := strings.Trim(path, string(filepath.Separator))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, string(filepath.Separator)))
}

func dedupeRoots(roots []CleanupRoot) []CleanupRoot {
	result := make([]CleanupRoot, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root.Path) == "" {
			continue
		}
		key := string(root.Owner) + "|" + string(root.Mode) + "|" + root.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, root)
	}
	return result
}
