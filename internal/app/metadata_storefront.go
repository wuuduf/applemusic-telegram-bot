package app

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/lyrics"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

const defaultMetadataStorefront = "cn"
const maxCrossStorefrontDurationDeltaMillis = 2_000
const metadataStorefrontLookupTimeout = 8 * time.Second
const metadataStorefrontLyricsFallbackReserve = 2 * time.Second

var metadataStorefrontFallbacks = []string{"cn", "tw", "hk", "us"}

type localizedTrackMetadata struct {
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	Composer    string
	GenreNames  []string
	SongIDs     map[string]string
}

type localizedSongMetadataLookup func(context.Context, string, *ampapi.SongRespData, string, string) (*ampapi.SongRespData, error)
type localizedLyricsLookup func(context.Context, string, string, string, string, string, string, string) (string, error)

func newMetadataStorefrontLookupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, metadataStorefrontLookupTimeout)
}

func newLocalizedLyricsSearchContext(parent context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := parent.Deadline()
	if !ok {
		return parent, func() {}
	}
	return context.WithDeadline(parent, deadline.Add(-metadataStorefrontLyricsFallbackReserve))
}

func normalizeMetadataStorefront(storefront string) string {
	storefront = strings.ToLower(strings.TrimSpace(storefront))
	if len(storefront) != 2 {
		return ""
	}
	for _, r := range storefront {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return storefront
}

func metadataStorefrontCandidates(preferred, download string) []string {
	candidates := make([]string, 0, len(metadataStorefrontFallbacks)+2)
	seen := make(map[string]struct{}, cap(candidates))
	add := func(storefront string) {
		storefront = normalizeMetadataStorefront(storefront)
		if storefront == "" {
			return
		}
		if _, ok := seen[storefront]; ok {
			return
		}
		seen[storefront] = struct{}{}
		candidates = append(candidates, storefront)
	}

	preferred = normalizeMetadataStorefront(preferred)
	if preferred == "" {
		preferred = defaultMetadataStorefront
	}
	add(preferred)
	for _, storefront := range metadataStorefrontFallbacks {
		add(storefront)
	}
	add(download)
	return candidates
}

func sameSongRecording(source, candidate *ampapi.SongRespData) bool {
	if source == nil || candidate == nil {
		return false
	}
	sourceISRC := strings.ToUpper(strings.TrimSpace(source.Attributes.Isrc))
	candidateISRC := strings.ToUpper(strings.TrimSpace(candidate.Attributes.Isrc))
	sourceID := strings.TrimSpace(source.ID)
	candidateID := strings.TrimSpace(candidate.ID)
	if sourceID != "" && sourceID == candidateID {
		return sourceISRC == "" || candidateISRC == "" || sourceISRC == candidateISRC
	}
	if sourceISRC == "" || candidateISRC == "" || sourceISRC != candidateISRC {
		return false
	}
	if source.Attributes.DurationInMillis <= 0 || candidate.Attributes.DurationInMillis <= 0 {
		return false
	}
	durationDelta := source.Attributes.DurationInMillis - candidate.Attributes.DurationInMillis
	if durationDelta < 0 {
		durationDelta = -durationDelta
	}
	return durationDelta <= maxCrossStorefrontDurationDeltaMillis
}

func uniqueISRCMatch(source *ampapi.SongRespData, candidates []ampapi.SongRespData) (*ampapi.SongRespData, bool) {
	var match *ampapi.SongRespData
	for i := range candidates {
		candidate := &candidates[i]
		if !sameSongRecording(source, candidate) {
			continue
		}
		if match == nil {
			match = candidate
			continue
		}
		if strings.TrimSpace(match.ID) == strings.TrimSpace(candidate.ID) {
			continue
		}
		return nil, false
	}
	return match, match != nil
}

func localizedMetadataSourceFromTrack(track *ampapi.TrackRespData) *ampapi.SongRespData {
	if track == nil {
		return nil
	}
	source := &ampapi.SongRespData{ID: track.ID}
	source.Attributes.Name = track.Attributes.Name
	source.Attributes.Isrc = track.Attributes.Isrc
	source.Attributes.ArtistName = track.Attributes.ArtistName
	source.Attributes.AlbumName = track.Attributes.AlbumName
	source.Attributes.ComposerName = track.Attributes.ComposerName
	source.Attributes.DurationInMillis = track.Attributes.DurationInMillis
	source.Attributes.GenreNames = append([]string(nil), track.Attributes.GenreNames...)
	return source
}

func containsHanCharacters(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func localizedTrackMetadataFromSong(source, candidate *ampapi.SongRespData) localizedTrackMetadata {
	if !sameSongRecording(source, candidate) {
		return localizedTrackMetadata{}
	}
	var localized localizedTrackMetadata
	setLocalized := func(candidateValue, sourceValue string) string {
		candidateValue = strings.TrimSpace(candidateValue)
		if candidateValue != "" && containsHanCharacters(candidateValue) && !containsHanCharacters(sourceValue) {
			return candidateValue
		}
		return ""
	}

	localized.Title = setLocalized(candidate.Attributes.Name, source.Attributes.Name)
	localized.Artist = setLocalized(candidate.Attributes.ArtistName, source.Attributes.ArtistName)
	localized.Album = setLocalized(candidate.Attributes.AlbumName, source.Attributes.AlbumName)
	localized.Composer = setLocalized(candidate.Attributes.ComposerName, source.Attributes.ComposerName)
	if len(candidate.Relationships.Albums.Data) > 0 {
		if localized.Album == "" {
			localized.Album = setLocalized(candidate.Relationships.Albums.Data[0].Attributes.Name, source.Attributes.AlbumName)
		}
		// The actual source album artist is available only when assembling tags.
		localized.AlbumArtist = setLocalized(candidate.Relationships.Albums.Data[0].Attributes.ArtistName, "")
	}
	if len(source.Attributes.GenreNames) == 0 || !containsHanCharacters(source.Attributes.GenreNames[0]) {
		for _, genre := range candidate.Attributes.GenreNames {
			genre = strings.TrimSpace(genre)
			if genre != "" && containsHanCharacters(genre) {
				localized.GenreNames = append(localized.GenreNames, genre)
			}
		}
	}
	return localized
}

func mergeLocalizedTrackMetadata(dst *localizedTrackMetadata, src localizedTrackMetadata) {
	if dst == nil {
		return
	}
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.Artist == "" {
		dst.Artist = src.Artist
	}
	if dst.Album == "" {
		dst.Album = src.Album
	}
	if dst.AlbumArtist == "" {
		dst.AlbumArtist = src.AlbumArtist
	}
	if dst.Composer == "" {
		dst.Composer = src.Composer
	}
	if len(dst.GenreNames) == 0 && len(src.GenreNames) > 0 {
		dst.GenreNames = append([]string(nil), src.GenreNames...)
	}
	if len(src.SongIDs) > 0 {
		if dst.SongIDs == nil {
			dst.SongIDs = make(map[string]string, len(src.SongIDs))
		}
		for storefront, songID := range src.SongIDs {
			if dst.SongIDs[storefront] == "" {
				dst.SongIDs[storefront] = songID
			}
		}
	}
}

func (metadata localizedTrackMetadata) empty() bool {
	return metadata.Title == "" && metadata.Artist == "" && metadata.Album == "" && metadata.AlbumArtist == "" && metadata.Composer == "" && len(metadata.GenreNames) == 0
}

func localizedMetadataValue(localized, fallback string) string {
	if containsHanCharacters(fallback) {
		return fallback
	}
	if value := strings.TrimSpace(localized); value != "" {
		return value
	}
	return fallback
}

func resolveLocalizedSongMetadata(ctx context.Context, source *ampapi.SongRespData, downloadStorefront string, cfg *structs.ConfigSet, token string, lookup localizedSongMetadataLookup) (localizedTrackMetadata, string) {
	if source == nil || cfg == nil || lookup == nil {
		return localizedTrackMetadata{}, ""
	}
	var localized localizedTrackMetadata
	selectedStorefront := ""
	for _, storefront := range metadataStorefrontCandidates(cfg.MetadataStorefront, downloadStorefront) {
		if ctx.Err() != nil {
			break
		}
		if storefront == normalizeMetadataStorefront(downloadStorefront) {
			continue
		}
		candidate, err := lookup(ctx, storefront, source, cfg.Language, token)
		if err != nil || !sameSongRecording(source, candidate) {
			continue
		}
		if localized.SongIDs == nil {
			localized.SongIDs = make(map[string]string)
		}
		songID := strings.TrimSpace(candidate.ID)
		if songID == "" {
			songID = strings.TrimSpace(source.ID)
		}
		localized.SongIDs[storefront] = songID

		candidateMetadata := localizedTrackMetadataFromSong(source, candidate)
		if candidateMetadata.empty() {
			continue
		}
		if selectedStorefront == "" {
			selectedStorefront = storefront
		}
		mergeLocalizedTrackMetadata(&localized, candidateMetadata)
		if localized.Title != "" {
			return localized, storefront
		}
	}
	return localized, selectedStorefront
}

func fetchLocalizedSongMetadata(ctx context.Context, storefront string, source *ampapi.SongRespData, language, token string) (*ampapi.SongRespData, error) {
	if source == nil {
		return nil, fmt.Errorf("source song metadata is missing")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	storefront = normalizeMetadataStorefront(storefront)
	if storefront == "" {
		return nil, fmt.Errorf("invalid metadata storefront")
	}

	if strings.TrimSpace(source.ID) != "" {
		resp, err := ampapi.GetSongRespWithContext(ctx, storefront, source.ID, language, token)
		if err == nil && resp != nil {
			for i := range resp.Data {
				if sameSongRecording(source, &resp.Data[i]) {
					return &resp.Data[i], nil
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	isrc := strings.TrimSpace(source.Attributes.Isrc)
	if isrc == "" {
		return nil, fmt.Errorf("no matching song in storefront %s", storefront)
	}
	resp, err := ampapi.SearchWithContext(ctx, storefront, isrc, "songs", language, token, 25, 0)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Results.Songs == nil {
		return nil, fmt.Errorf("no song search results in storefront %s", storefront)
	}
	if strings.TrimSpace(resp.Results.Songs.Next) != "" {
		return nil, fmt.Errorf("incomplete song search results in storefront %s", storefront)
	}
	match, ok := uniqueISRCMatch(source, resp.Results.Songs.Data)
	if !ok {
		return nil, fmt.Errorf("no unique ISRC and duration match in storefront %s", storefront)
	}
	return match, nil
}

func resolveLocalizedLyrics(ctx context.Context, songID, downloadStorefront string, cfg *structs.ConfigSet, token, mediaUserToken string, lookup localizedLyricsLookup) (string, error) {
	if cfg == nil || lookup == nil {
		return "", fmt.Errorf("lyrics lookup is not configured")
	}
	localizedCtx, cancelLocalized := newLocalizedLyricsSearchContext(ctx)
	defer cancelLocalized()
	var firstAvailable string
	var downloadStorefrontLyrics string
	var firstErr error
	originalStorefront := normalizeMetadataStorefront(downloadStorefront)
	for _, storefront := range metadataStorefrontCandidates(cfg.MetadataStorefront, downloadStorefront) {
		if storefront == originalStorefront {
			continue
		}
		if localizedCtx.Err() != nil {
			break
		}
		localizedSongID := strings.TrimSpace(songID)
		if localizedSongID == "" {
			continue
		}
		content, err := lookup(localizedCtx, storefront, localizedSongID, cfg.LrcType, cfg.Language, cfg.LrcFormat, token, mediaUserToken)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if localizedCtx.Err() != nil {
				break
			}
			continue
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		if containsHanCharacters(content) {
			return content, nil
		}
		if firstAvailable == "" {
			firstAvailable = content
		}
	}
	if originalStorefront != "" && ctx.Err() == nil {
		localizedSongID := strings.TrimSpace(songID)
		if localizedSongID != "" {
			content, err := lookup(ctx, originalStorefront, localizedSongID, cfg.LrcType, cfg.Language, cfg.LrcFormat, token, mediaUserToken)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
			} else if strings.TrimSpace(content) != "" {
				if containsHanCharacters(content) {
					return content, nil
				}
				downloadStorefrontLyrics = content
			}
		}
	}
	if downloadStorefrontLyrics != "" {
		return downloadStorefrontLyrics, nil
	}
	if firstAvailable != "" {
		return firstAvailable, nil
	}
	if firstErr != nil {
		return "", firstErr
	}
	return "", fmt.Errorf("no lyrics available in metadata storefronts")
}

func fetchLyricsForMetadataStorefront(ctx context.Context, storefront string, source *ampapi.SongRespData, sourceStorefront string, cfg *structs.ConfigSet, token, mediaUserToken string, songIDs map[string]string) (string, error) {
	if source == nil || cfg == nil {
		return "", fmt.Errorf("source song or config is missing")
	}
	storefront = normalizeMetadataStorefront(storefront)
	if storefront == "" {
		return "", fmt.Errorf("invalid metadata storefront")
	}
	songID := strings.TrimSpace(source.ID)
	if mappedID := strings.TrimSpace(songIDs[storefront]); mappedID != "" {
		songID = mappedID
	} else if storefront != normalizeMetadataStorefront(sourceStorefront) {
		localizedSong, err := fetchLocalizedSongMetadata(ctx, storefront, source, cfg.Language, token)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(localizedSong.ID) != "" {
			songID = strings.TrimSpace(localizedSong.ID)
		}
		if songIDs != nil {
			songIDs[storefront] = songID
		}
	}
	if songID == "" {
		return "", fmt.Errorf("song ID is missing for storefront %s", storefront)
	}
	return lyrics.GetWithContext(ctx, storefront, songID, cfg.LrcType, cfg.Language, cfg.LrcFormat, token, mediaUserToken)
}
