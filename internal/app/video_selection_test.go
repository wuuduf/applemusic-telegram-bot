package app

import (
	"net/url"
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

func TestBuildMusicVideoStreamOptionsDeduplicatesResolutionAndRange(t *testing.T) {
	baseURL, err := url.Parse("https://example.com/master.m3u8")
	if err != nil {
		t.Fatalf("parse base url: %v", err)
	}
	options := buildMusicVideoStreamOptions(baseURL, []*m3u8.Variant{
		{
			URI: "a/1920x1080_sdr.m3u8",
			VariantParams: m3u8.VariantParams{
				Resolution:       "1920x1080",
				AverageBandwidth: 5000000,
				VideoRange:       "SDR",
			},
		},
		{
			URI: "b/1920x1080_sdr_better.m3u8",
			VariantParams: m3u8.VariantParams{
				Resolution:       "1920x1080",
				AverageBandwidth: 6000000,
				VideoRange:       "SDR",
			},
		},
		{
			URI: "c/1920x1080_hdr.m3u8",
			VariantParams: m3u8.VariantParams{
				Resolution:       "1920x1080",
				AverageBandwidth: 7000000,
				VideoRange:       "HDR",
			},
		},
	})
	if len(options) != 2 {
		t.Fatalf("expected 2 deduplicated options, got %d", len(options))
	}
	if options[0].VideoRange != "HDR" {
		t.Fatalf("expected HDR option first, got %#v", options[0])
	}
	if options[1].PlaylistURL != "https://example.com/b/1920x1080_sdr_better.m3u8" {
		t.Fatalf("expected highest bandwidth SDR url, got %q", options[1].PlaylistURL)
	}
}
