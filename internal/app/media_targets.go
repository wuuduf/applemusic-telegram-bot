package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

func normalizeCommandMediaType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case mediaTypeSong, "songs":
		return mediaTypeSong
	case mediaTypeAlbum, "albums":
		return mediaTypeAlbum
	case mediaTypePlaylist, "playlists":
		return mediaTypePlaylist
	case mediaTypeStation, "stations":
		return mediaTypeStation
	case "mv", "mvs", "video", "videos", mediaTypeMusicVideo, "musicvideo", "musicvideos":
		return mediaTypeMusicVideo
	case mediaTypeArtist, "artists":
		return mediaTypeArtist
	case mediaTypeCurator, "curators":
		return mediaTypeCurator
	case mediaTypeCuratorLPs:
		return mediaTypeCuratorLPs
	default:
		return ""
	}
}

func splitBatchArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		for _, part := range strings.FieldsFunc(arg, func(r rune) bool {
			switch r {
			case ',', '，', ';', '；', '\n', '\r', '\t':
				return true
			default:
				return false
			}
		}) {
			token := strings.TrimSpace(part)
			if token == "" {
				continue
			}
			out = append(out, token)
		}
	}
	return out
}

func normalizeMediaIdentifier(mediaType, rawID string) (string, error) {
	normalizedType := normalizeCommandMediaType(mediaType)
	if normalizedType == "" {
		normalizedType = strings.TrimSpace(mediaType)
	}
	id := strings.TrimSpace(rawID)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	if len(id) > maxMediaIDLength {
		return "", fmt.Errorf("invalid id")
	}
	if strings.ContainsAny(id, "/\\\r\n\t\x00") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid id")
	}
	switch normalizedType {
	case mediaTypeSong, mediaTypeAlbum, mediaTypeArtist, mediaTypeMusicVideo:
		if !mediaIDNumericRe.MatchString(id) {
			return "", fmt.Errorf("invalid id")
		}
	case mediaTypePlaylist:
		if !mediaIDPlaylistRe.MatchString(id) {
			return "", fmt.Errorf("invalid id")
		}
	case mediaTypeStation:
		if !mediaIDStationRe.MatchString(id) {
			return "", fmt.Errorf("invalid id")
		}
	default:
		if !mediaIDGenericRe.MatchString(id) {
			return "", fmt.Errorf("invalid id")
		}
	}
	return id, nil
}

func normalizeAppleURLTarget(target *AppleURLTarget) (*AppleURLTarget, error) {
	if target == nil {
		return nil, fmt.Errorf("invalid target")
	}
	mediaType := normalizeCommandMediaType(target.MediaType)
	if mediaType == "" {
		return nil, fmt.Errorf("invalid media_type")
	}
	mediaID, err := normalizeMediaIdentifier(mediaType, target.ID)
	if err != nil {
		return nil, err
	}
	return &AppleURLTarget{
		MediaType:  mediaType,
		Storefront: resolveStorefront(target),
		ID:         mediaID,
		RawURL:     strings.TrimSpace(target.RawURL),
	}, nil
}

func joinFileWithinRoot(root string, fileName string) (string, error) {
	cleanRoot := filepath.Clean(strings.TrimSpace(root))
	if cleanRoot == "" {
		return "", fmt.Errorf("invalid root")
	}
	candidate := strings.TrimSpace(fileName)
	if candidate == "" {
		return "", fmt.Errorf("invalid filename")
	}
	if filepath.IsAbs(candidate) {
		return "", fmt.Errorf("invalid filename")
	}
	cleanName := filepath.Clean(candidate)
	if cleanName == "." || cleanName == ".." {
		return "", fmt.Errorf("invalid filename")
	}
	if strings.Contains(cleanName, "/") || strings.Contains(cleanName, `\`) {
		return "", fmt.Errorf("invalid filename")
	}
	target := filepath.Join(cleanRoot, cleanName)
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return target, nil
}

func resolveCommandTarget(args []string, defaultType string) (*AppleURLTarget, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("empty args")
	}
	joined := strings.TrimSpace(strings.Join(args, " "))
	if urlText := extractFirstAppleMusicURL(joined); urlText != "" {
		target, err := parseAppleMusicURL(urlText)
		if err != nil {
			return nil, err
		}
		return normalizeAppleURLTarget(target)
	}
	first := strings.TrimSpace(args[0])
	lowerFirst := strings.ToLower(first)
	if strings.HasPrefix(lowerFirst, "http://") || strings.HasPrefix(lowerFirst, "https://") {
		target, err := parseAppleMusicURL(first)
		if err != nil {
			return nil, err
		}
		return normalizeAppleURLTarget(target)
	}
	if len(args) >= 2 {
		mediaType := normalizeCommandMediaType(args[0])
		mediaID := strings.TrimSpace(args[1])
		if mediaType != "" && mediaID != "" {
			normalizedID, err := normalizeMediaIdentifier(mediaType, mediaID)
			if err != nil {
				return nil, err
			}
			return &AppleURLTarget{
				MediaType:  mediaType,
				Storefront: Config.Storefront,
				ID:         normalizedID,
			}, nil
		}
	}
	defaultType = normalizeCommandMediaType(defaultType)
	if defaultType != "" {
		mediaID := strings.TrimSpace(args[0])
		if mediaID == "" {
			return nil, fmt.Errorf("empty media id")
		}
		normalizedID, err := normalizeMediaIdentifier(defaultType, mediaID)
		if err != nil {
			return nil, err
		}
		return &AppleURLTarget{
			MediaType:  defaultType,
			Storefront: Config.Storefront,
			ID:         normalizedID,
		}, nil
	}
	return nil, fmt.Errorf("unable to resolve target")
}

func resolveRefreshURLTarget(args []string) (*AppleURLTarget, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("empty args")
	}
	trimmedArgs := args
	if len(trimmedArgs) > 0 {
		switch strings.ToLower(strings.TrimSpace(trimmedArgs[0])) {
		case "url", "ulr":
			trimmedArgs = trimmedArgs[1:]
		}
	}
	if len(trimmedArgs) == 0 {
		return nil, fmt.Errorf("missing url")
	}
	joined := strings.TrimSpace(strings.Join(trimmedArgs, " "))
	raw := extractFirstAppleMusicURL(joined)
	if raw == "" {
		raw = strings.TrimSpace(trimmedArgs[0])
	}
	return parseAppleMusicURL(raw)
}

func normalizeLyricsOutputFormat(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "lrc":
		return "lrc"
	case "ttml":
		return "ttml"
	default:
		return ""
	}
}

func sanitizeFileBaseName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "apple-music"
	}
	name = forbiddenNames.ReplaceAllString(name, "_")
	name = strings.ReplaceAll(name, "\n", " ")
	name = strings.Join(strings.Fields(name), " ")
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "apple-music"
	}
	return name
}

func resolveStorefront(target *AppleURLTarget) string {
	storefront := strings.TrimSpace(Config.Storefront)
	if storefront != "" {
		return storefront
	}
	if target != nil && strings.TrimSpace(target.Storefront) != "" {
		return strings.TrimSpace(target.Storefront)
	}
	return "us"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func composeArtistTitle(artistName, title string) string {
	artistName = strings.TrimSpace(artistName)
	title = strings.TrimSpace(title)
	if artistName != "" && title != "" {
		return artistName + " - " + title
	}
	if title != "" {
		return title
	}
	return artistName
}

func firstSongData(op string, resp *ampapi.SongResp) (*ampapi.SongRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "song.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "song.data", resp.Data)
}

func firstAlbumData(op string, resp *ampapi.AlbumResp) (*ampapi.AlbumRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "album.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "album.data", resp.Data)
}

func firstPlaylistData(op string, resp *ampapi.PlaylistResp) (*ampapi.PlaylistRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "playlist.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "playlist.data", resp.Data)
}

func firstStationData(op string, resp *ampapi.StationResp) (*ampapi.StationRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "station.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "station.data", resp.Data)
}

func firstMusicVideoData(op string, resp *ampapi.MusicVideoResp) (*ampapi.MusicVideoRespData, error) {
	if resp == nil {
		return nil, &safe.AccessError{Op: op, Path: "music_video.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "music_video.data", resp.Data)
}

func firstSongAlbumID(op string, item *ampapi.SongRespData) (string, error) {
	if item == nil {
		return "", &safe.AccessError{Op: op, Path: "song.data[0]", Reason: "nil track item"}
	}
	albumRef, err := safe.FirstRef(op, "song.data[0].relationships.albums.data", item.Relationships.Albums.Data)
	if err != nil {
		return "", err
	}
	albumID := strings.TrimSpace(albumRef.ID)
	if albumID == "" {
		return "", &safe.AccessError{Op: op, Path: "song.data[0].relationships.albums.data[0].id", Reason: "empty album id"}
	}
	return albumID, nil
}

func firstGenreName(op string, genres []string) (string, error) {
	return safe.FirstString(op, "attributes.genreNames", genres)
}
