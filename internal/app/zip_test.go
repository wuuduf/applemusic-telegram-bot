package app

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateZipFromPathsBasic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	aPath := writeSizedFileForTest(t, root, "a.txt", 10)
	subDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	bPath := writeSizedFileForTest(t, subDir, "b.txt", 12)

	zipPath, displayName, err := createZipFromPaths([]string{aPath, bPath})
	if err != nil {
		t.Fatalf("create zip failed: %v", err)
	}
	defer os.Remove(zipPath)
	if displayName == "" {
		t.Fatalf("expected display name")
	}

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip failed: %v", err)
	}
	defer reader.Close()
	if len(reader.File) != 2 {
		t.Fatalf("expected 2 files in zip, got %d", len(reader.File))
	}
	names := map[string]bool{}
	for _, f := range reader.File {
		names[f.Name] = true
	}
	if !names["a.txt"] {
		t.Fatalf("expected a.txt in zip")
	}
	if !names["sub/b.txt"] {
		t.Fatalf("expected sub/b.txt in zip")
	}
}

func TestCreateZipFromPathsStoresPrecompressedMedia(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	audioPath := writeSizedFileForTest(t, root, "01 Track.M4A", 64)
	textPath := writeSizedFileForTest(t, root, "lyrics.lrc", 64)

	zipPath, _, err := createZipFromPaths([]string{audioPath, textPath})
	if err != nil {
		t.Fatalf("create zip failed: %v", err)
	}
	defer os.Remove(zipPath)

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip failed: %v", err)
	}
	defer reader.Close()
	methods := map[string]uint16{}
	for _, f := range reader.File {
		methods[f.Name] = f.Method
	}
	if methods["01 Track.M4A"] != zip.Store {
		t.Fatalf("expected audio to be stored, got method %d", methods["01 Track.M4A"])
	}
	if methods["lyrics.lrc"] != zip.Deflate {
		t.Fatalf("expected text to be deflated, got method %d", methods["lyrics.lrc"])
	}
}
