package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/grafov/m3u8"
)

func TestSelectVideoVariantPrefersLandscapeForMusicVideo(t *testing.T) {
	variants := []*m3u8.Variant{
		{
			URI: "portrait_1080x1920.m3u8",
			VariantParams: m3u8.VariantParams{
				Resolution:       "1080x1920",
				AverageBandwidth: 9000000,
			},
		},
		{
			URI: "landscape_1920x1080.m3u8",
			VariantParams: m3u8.VariantParams{
				Resolution:       "1920x1080",
				AverageBandwidth: 6000000,
			},
		},
	}

	selected, dims, err := selectVideoVariant(variants, 2160, videoAspectLandscape)
	if err != nil {
		t.Fatalf("selectVideoVariant failed: %v", err)
	}
	if selected.URI != "landscape_1920x1080.m3u8" {
		t.Fatalf("expected landscape variant, got %q", selected.URI)
	}
	if dims.Width != 1920 || dims.Height != 1080 {
		t.Fatalf("unexpected selected dimensions: %#v", dims)
	}
}

func TestPreferredAnimatedArtworkPathPrefersSquare(t *testing.T) {
	dir := t.TempDir()
	square := filepath.Join(dir, "square_animated_artwork.mp4")
	tall := filepath.Join(dir, "tall_animated_artwork.mp4")
	if err := os.WriteFile(square, []byte("square"), 0o644); err != nil {
		t.Fatalf("write square: %v", err)
	}
	if err := os.WriteFile(tall, []byte("tall"), 0o644); err != nil {
		t.Fatalf("write tall: %v", err)
	}

	if got := preferredAnimatedArtworkPath(dir); got != square {
		t.Fatalf("expected square artwork path, got %q", got)
	}
}
