package app

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/grafov/m3u8"
)

var videoVariantDimensionRe = regexp.MustCompile(`(?:^|[^0-9])(\d{2,5})x(\d{2,5})(?:[^0-9]|$)`)

type videoAspectPreference int

const (
	videoAspectAny videoAspectPreference = iota
	videoAspectLandscape
	videoAspectSquare
	videoAspectPortrait
)

type videoVariantDimensions struct {
	Width  int
	Height int
}

type videoVariantCandidate struct {
	Variant    *m3u8.Variant
	Dimensions videoVariantDimensions
	Quality    int
	Area       int
	Bandwidth  uint32
}

func parseVideoVariantDimensions(variant *m3u8.Variant) (videoVariantDimensions, bool) {
	if variant == nil {
		return videoVariantDimensions{}, false
	}
	if dims, ok := parseVideoResolution(strings.TrimSpace(variant.Resolution)); ok {
		return dims, true
	}
	matches := videoVariantDimensionRe.FindStringSubmatch(strings.TrimSpace(variant.URI))
	if len(matches) != 3 {
		return videoVariantDimensions{}, false
	}
	width, err := strconv.Atoi(matches[1])
	if err != nil || width <= 0 {
		return videoVariantDimensions{}, false
	}
	height, err := strconv.Atoi(matches[2])
	if err != nil || height <= 0 {
		return videoVariantDimensions{}, false
	}
	return videoVariantDimensions{Width: width, Height: height}, true
}

func parseVideoResolution(value string) (videoVariantDimensions, bool) {
	parts := strings.SplitN(strings.TrimSpace(value), "x", 2)
	if len(parts) != 2 {
		return videoVariantDimensions{}, false
	}
	width, err := strconv.Atoi(parts[0])
	if err != nil || width <= 0 {
		return videoVariantDimensions{}, false
	}
	height, err := strconv.Atoi(parts[1])
	if err != nil || height <= 0 {
		return videoVariantDimensions{}, false
	}
	return videoVariantDimensions{Width: width, Height: height}, true
}

func buildVideoVariantCandidates(variants []*m3u8.Variant) []videoVariantCandidate {
	candidates := make([]videoVariantCandidate, 0, len(variants))
	for _, variant := range variants {
		dims, ok := parseVideoVariantDimensions(variant)
		if !ok {
			continue
		}
		bandwidth := variant.AverageBandwidth
		if bandwidth == 0 {
			bandwidth = variant.Bandwidth
		}
		candidates = append(candidates, videoVariantCandidate{
			Variant:    variant,
			Dimensions: dims,
			Quality:    minInt(dims.Width, dims.Height),
			Area:       dims.Width * dims.Height,
			Bandwidth:  bandwidth,
		})
	}
	return candidates
}

func matchesVideoAspect(dims videoVariantDimensions, preference videoAspectPreference) bool {
	switch preference {
	case videoAspectLandscape:
		return dims.Width > dims.Height
	case videoAspectPortrait:
		return dims.Height > dims.Width
	case videoAspectSquare:
		if dims.Width == dims.Height {
			return true
		}
		tolerance := maxInt(1, minInt(dims.Width, dims.Height)/20)
		diff := dims.Width - dims.Height
		if diff < 0 {
			diff = -diff
		}
		return diff <= tolerance
	default:
		return true
	}
}

func filterVideoCandidates(candidates []videoVariantCandidate, maxQuality int, preference videoAspectPreference) []videoVariantCandidate {
	filtered := make([]videoVariantCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if maxQuality > 0 && candidate.Quality > maxQuality {
			continue
		}
		if !matchesVideoAspect(candidate.Dimensions, preference) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func rankVideoCandidates(candidates []videoVariantCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Quality != candidates[j].Quality {
			return candidates[i].Quality > candidates[j].Quality
		}
		if candidates[i].Area != candidates[j].Area {
			return candidates[i].Area > candidates[j].Area
		}
		if candidates[i].Bandwidth != candidates[j].Bandwidth {
			return candidates[i].Bandwidth > candidates[j].Bandwidth
		}
		return strings.Compare(candidates[i].Variant.URI, candidates[j].Variant.URI) < 0
	})
}

func selectVideoVariant(variants []*m3u8.Variant, maxQuality int, preference videoAspectPreference) (*m3u8.Variant, videoVariantDimensions, error) {
	candidates := buildVideoVariantCandidates(variants)
	if len(candidates) == 0 {
		return nil, videoVariantDimensions{}, fmt.Errorf("no video variants with resolution metadata found")
	}

	searchOrder := []struct {
		maxQuality int
		preference videoAspectPreference
	}{
		{maxQuality: maxQuality, preference: preference},
		{maxQuality: maxQuality, preference: videoAspectAny},
		{maxQuality: 0, preference: preference},
		{maxQuality: 0, preference: videoAspectAny},
	}

	for _, step := range searchOrder {
		filtered := filterVideoCandidates(candidates, step.maxQuality, step.preference)
		if len(filtered) == 0 {
			continue
		}
		rankVideoCandidates(filtered)
		return filtered[0].Variant, filtered[0].Dimensions, nil
	}

	return nil, videoVariantDimensions{}, fmt.Errorf("no suitable video stream found")
}

func preferredAnimatedArtworkPath(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	square := filepath.Join(dir, "square_animated_artwork.mp4")
	if fileExistsRegular(square) {
		return square
	}
	tall := filepath.Join(dir, "tall_animated_artwork.mp4")
	if fileExistsRegular(tall) {
		return tall
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
