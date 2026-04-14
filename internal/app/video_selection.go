package app

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
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

func listMusicVideoStreamOptions(masterURL string) ([]MusicVideoStreamOption, error) {
	mediaURL, err := url.Parse(masterURL)
	if err != nil {
		return nil, err
	}
	resp, err := networkHTTPClient.Get(masterURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(string(body)), true)
	if err != nil || listType != m3u8.MASTER {
		return nil, fmt.Errorf("m3u8 not of media type")
	}
	return buildMusicVideoStreamOptions(mediaURL, from.(*m3u8.MasterPlaylist).Variants), nil
}

func buildMusicVideoStreamOptions(baseURL *url.URL, variants []*m3u8.Variant) []MusicVideoStreamOption {
	candidates := buildVideoVariantCandidates(variants)
	type groupedCandidate struct {
		videoVariantCandidate
		AbsoluteURL string
	}
	grouped := make(map[string]groupedCandidate)
	for _, candidate := range candidates {
		if candidate.Variant == nil || strings.TrimSpace(candidate.Variant.URI) == "" || baseURL == nil {
			continue
		}
		absoluteURL, err := baseURL.Parse(candidate.Variant.URI)
		if err != nil {
			continue
		}
		rangeKey := normalizeVideoRange(candidate.Variant.VideoRange)
		groupKey := fmt.Sprintf("%dx%d|%s", candidate.Dimensions.Width, candidate.Dimensions.Height, rangeKey)
		current, exists := grouped[groupKey]
		replacement := groupedCandidate{
			videoVariantCandidate: candidate,
			AbsoluteURL:           absoluteURL.String(),
		}
		if !exists || candidate.Bandwidth > current.Bandwidth || (candidate.Bandwidth == current.Bandwidth && strings.Compare(replacement.AbsoluteURL, current.AbsoluteURL) < 0) {
			grouped[groupKey] = replacement
		}
	}
	ordered := make([]groupedCandidate, 0, len(grouped))
	for _, candidate := range grouped {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Quality != ordered[j].Quality {
			return ordered[i].Quality > ordered[j].Quality
		}
		if ordered[i].Area != ordered[j].Area {
			return ordered[i].Area > ordered[j].Area
		}
		if ordered[i].Bandwidth != ordered[j].Bandwidth {
			return ordered[i].Bandwidth > ordered[j].Bandwidth
		}
		return strings.Compare(ordered[i].AbsoluteURL, ordered[j].AbsoluteURL) < 0
	})
	options := make([]MusicVideoStreamOption, 0, len(ordered))
	for _, candidate := range ordered {
		rangeKey := normalizeVideoRange(candidate.Variant.VideoRange)
		options = append(options, MusicVideoStreamOption{
			Key:         buildMusicVideoStreamKey(candidate.AbsoluteURL, candidate.Dimensions, rangeKey),
			Label:       formatMusicVideoStreamLabel(candidate.Dimensions, candidate.Bandwidth, rangeKey),
			PlaylistURL: candidate.AbsoluteURL,
			Width:       candidate.Dimensions.Width,
			Height:      candidate.Dimensions.Height,
			VideoRange:  rangeKey,
			Bandwidth:   candidate.Bandwidth,
		})
	}
	return options
}

func normalizeVideoRange(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "SDR"
	}
	return value
}

func formatMusicVideoStreamLabel(dims videoVariantDimensions, bandwidth uint32, videoRange string) string {
	parts := []string{fmt.Sprintf("%dx%d", dims.Width, dims.Height)}
	if strings.TrimSpace(videoRange) != "" {
		parts = append(parts, strings.ToUpper(strings.TrimSpace(videoRange)))
	}
	if bandwidth > 0 {
		parts = append(parts, fmt.Sprintf("%dk", bandwidth/1000))
	}
	return strings.Join(parts, " · ")
}

func buildMusicVideoStreamKey(_ string, dims videoVariantDimensions, videoRange string) string {
	rangePart := strings.ToLower(strings.TrimSpace(videoRange))
	if rangePart == "" {
		rangePart = "sdr"
	}
	return fmt.Sprintf("%dx%d-%s", dims.Width, dims.Height, rangePart)
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
