package storage

import (
	"path/filepath"
	"testing"

	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func TestApplyTelegramStorageOverrides(t *testing.T) {
	cfg := &structs.ConfigSet{
		TelegramDownloadFolder: "/data/telegram",
		AlacSaveFolder:         "/data/alac",
		AtmosSaveFolder:        "/data/atmos",
		AacSaveFolder:          "/data/aac",
	}

	ApplyTelegramStorageOverrides(cfg)

	if cfg.AlacSaveFolder != "/data/telegram" || cfg.AtmosSaveFolder != "/data/telegram" || cfg.AacSaveFolder != "/data/telegram" {
		t.Fatalf("expected telegram download root override, got %#v", cfg)
	}
}

func TestTelegramCleanupRootsPreferTelegramOwnedRoot(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(tmpDir, "owned-tmp"))
	cfg := &structs.ConfigSet{
		TelegramDownloadFolder: filepath.Join(tmpDir, "telegram"),
		AlacSaveFolder:         filepath.Join(tmpDir, "cli-alac"),
		AtmosSaveFolder:        filepath.Join(tmpDir, "cli-atmos"),
		AacSaveFolder:          filepath.Join(tmpDir, "cli-aac"),
	}

	roots := TelegramCleanupRoots(cfg)

	if len(roots) != 2 {
		t.Fatalf("expected telegram download root plus temp root, got %#v", roots)
	}
	if roots[0].Owner != OwnerTelegram || roots[0].Mode != ModeDownload || roots[0].Path != filepath.Clean(cfg.TelegramDownloadFolder) {
		t.Fatalf("unexpected download root: %#v", roots[0])
	}
}

func TestTelegramCleanupRootsLegacyFallback(t *testing.T) {
	cfg := &structs.ConfigSet{
		AlacSaveFolder:  "/data/alac",
		AtmosSaveFolder: "/data/atmos",
		AacSaveFolder:   "/data/aac",
	}
	roots := TelegramCleanupRoots(cfg)
	downloadRoots := map[string]bool{}
	for _, root := range roots {
		if root.Mode == ModeDownload {
			downloadRoots[root.Path] = true
		}
	}
	for _, want := range []string{"/data/alac", "/data/atmos", "/data/aac"} {
		if !downloadRoots[want] {
			t.Fatalf("expected legacy download root %s, got %#v", want, roots)
		}
	}
}

func TestCleanupRootPathsSkipsEmptyRoots(t *testing.T) {
	paths := CleanupRootPaths([]CleanupRoot{
		{},
		{Owner: OwnerTelegram, Mode: ModeDownload, Path: "/data/telegram"},
	})
	if len(paths) != 1 || paths[0] != "/data/telegram" {
		t.Fatalf("unexpected cleanup root paths: %#v", paths)
	}
}

func TestCleanupRootSkipsSharedSystemTemp(t *testing.T) {
	if root := cleanupRoot(OwnerTelegram, ModeTemp, "/tmp"); root.Path != "" {
		t.Fatalf("expected /tmp to be skipped, got %#v", root)
	}
	if root := cleanupRoot(OwnerTelegram, ModeTemp, "/var/tmp"); root.Path != "" {
		t.Fatalf("expected /var/tmp to be skipped, got %#v", root)
	}
}

func TestCleanupRootSkipsBroadSystemDirectories(t *testing.T) {
	for _, path := range []string{"/Users", "/home", "/var", "/usr", "/etc", "/opt"} {
		if root := cleanupRoot(OwnerTelegram, ModeDownload, path); root.Path != "" {
			t.Fatalf("expected %s to be skipped, got %#v", path, root)
		}
	}
}

func TestCleanupRootSkipsUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if root := cleanupRoot(OwnerTelegram, ModeDownload, home); root.Path != "" {
		t.Fatalf("expected HOME directory to be skipped, got %#v", root)
	}
}
