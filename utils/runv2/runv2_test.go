package runv2

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/itouakirai/mp4ff/mp4"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func TestTransformInitInvalidInputReturnsError(t *testing.T) {
	t.Parallel()
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("TransformInit panicked on invalid input: %v", rec)
		}
	}()

	_, err := TransformInit(mp4.NewMP4Init())
	if err == nil {
		t.Fatalf("expected error for invalid init segment")
	}
}

func TestSanitizeInitMissingTrackReturnsError(t *testing.T) {
	t.Parallel()
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("sanitizeInit panicked on missing track: %v", rec)
		}
	}()

	err := sanitizeInit(&mp4.InitSegment{})
	if err == nil {
		t.Fatalf("expected error when init segment has no track")
	}
}

func TestDownloadAndDecryptFileRemovesIncompleteOutputOnError(t *testing.T) {
	t.Parallel()
	outfile := filepath.Join(t.TempDir(), "track.m4a")

	err := downloadAndDecryptFile(
		bytes.NewBuffer(nil),
		bytes.NewReader(nil),
		outfile,
		"123",
		nil,
		0,
		structs.ConfigSet{},
		nil,
		"Downloading",
	)
	if err == nil {
		t.Fatalf("expected downloadAndDecryptFile to fail for empty input")
	}
	if _, statErr := os.Stat(outfile); !os.IsNotExist(statErr) {
		t.Fatalf("expected incomplete output file removed, statErr=%v", statErr)
	}
}

func TestCloseAndCleanupOutputFileKeepsSuccessfulOutput(t *testing.T) {
	t.Parallel()
	outfile := filepath.Join(t.TempDir(), "ok.m4a")
	file, err := os.Create(outfile)
	if err != nil {
		t.Fatalf("create output failed: %v", err)
	}
	if _, err := file.Write([]byte("ok")); err != nil {
		t.Fatalf("write output failed: %v", err)
	}
	closeAndCleanupOutputFile(file, outfile, true)
	if _, err := os.Stat(outfile); err != nil {
		t.Fatalf("expected output kept on success: %v", err)
	}
}
