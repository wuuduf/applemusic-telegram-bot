package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	internalcli "github.com/wuuduf/applemusic-telegram-bot/internal/cli"
	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/runv3"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
)

type albumDownloadContext struct {
	session         *DownloadSession
	cfg             *structs.ConfigSet
	album           *task.Album
	albumData       *ampapi.AlbumRespData
	token           string
	storefront      string
	mediaUserToken  string
	albumID         string
	urlArg          string
	releaseYear     string
	codec           string
	quality         string
	singerFolder    string
	albumFolder     string
	albumFolderPath string
	coverPath       string
}

type playlistDownloadContext struct {
	session        *DownloadSession
	cfg            *structs.ConfigSet
	playlist       *task.Playlist
	playlistData   *ampapi.PlaylistRespData
	token          string
	storefront     string
	mediaUserToken string
	playlistID     string
	codec          string
	quality        string
	singerFolder   string
	playlistFolder string
	playlistPath   string
	coverPath      string
}

type stationDownloadContext struct {
	session        *DownloadSession
	cfg            *structs.ConfigSet
	station        *task.Station
	stationData    *ampapi.StationRespData
	token          string
	storefront     string
	mediaUserToken string
	stationID      string
	codec          string
	singerFolder   string
	playlistFolder string
	playlistPath   string
	coverPath      string
}

type songDownloadContext struct {
	session        *DownloadSession
	cfg            *structs.ConfigSet
	token          string
	storefront     string
	mediaUserToken string
	songID         string
	track          task.Track
	codec          string
	coverPath      string
}

func resolveDownloadCodecStage(session *DownloadSession) string {
	if session.DlAtmos {
		return "ATMOS"
	}
	if session.DlAac {
		return "AAC"
	}
	return "ALAC"
}

func resolveDownloadRootStage(cfg *structs.ConfigSet, session *DownloadSession, artistFolderName string) string {
	root := cfg.AlacSaveFolder
	if session.DlAtmos {
		root = cfg.AtmosSaveFolder
	}
	if session.DlAac {
		root = cfg.AacSaveFolder
	}
	return filepath.Join(root, forbiddenNames.ReplaceAllString(strings.TrimSpace(artistFolderName), "_"))
}

// cleanFolderNameStage finishes a templated folder name: a name ending in a
// dot loses all its dots (such names are invalid on Windows), then
// surrounding whitespace is trimmed.
func cleanFolderNameStage(name string) string {
	if strings.HasSuffix(name, ".") {
		name = strings.ReplaceAll(name, ".", "")
	}
	return strings.TrimSpace(name)
}

// prepareArtistFolderStage renders the artist folder name from
// artist-folder-format, creates the folder under the codec's save root and
// returns its path. artistName is used as given (callers apply the length
// limit where it applies).
func prepareArtistFolderStage(cfg *structs.ConfigSet, session *DownloadSession, artistName string, artistID string) string {
	folderName := ""
	if cfg.ArtistFolderFormat != "" {
		folderName = cleanFolderNameStage(strings.NewReplacer(
			"{UrlArtistName}", artistName,
			"{ArtistName}", artistName,
			"{ArtistId}", artistID,
		).Replace(cfg.ArtistFolderFormat))
		fmt.Println(folderName)
	}
	folder := resolveDownloadRootStage(cfg, session, folderName)
	_ = os.MkdirAll(folder, 0o755)
	return folder
}

// buildFolderTagStage joins the configured {Tag} parts of a collection
// folder: the Apple Digital Master marker and the explicit/clean marker.
func buildFolderTagStage(cfg *structs.ConfigSet, appleMaster bool, contentRating string) string {
	tagParts := []string{}
	if appleMaster && cfg.AppleMasterChoice != "" {
		tagParts = append(tagParts, cfg.AppleMasterChoice)
	}
	if contentRating == "explicit" && cfg.ExplicitChoice != "" {
		tagParts = append(tagParts, cfg.ExplicitChoice)
	}
	if contentRating == "clean" && cfg.CleanChoice != "" {
		tagParts = append(tagParts, cfg.CleanChoice)
	}
	return strings.Join(tagParts, " ")
}

// prepareCollectionFolderStage renders a collection folder name from format
// with the given placeholder/value pairs, creates it under parent and
// returns the display name and the path.
func prepareCollectionFolderStage(parent string, format string, placeholders ...string) (name string, path string) {
	name = cleanFolderNameStage(strings.NewReplacer(placeholders...).Replace(format))
	path = filepath.Join(parent, forbiddenNames.ReplaceAllString(name, "_"))
	_ = os.MkdirAll(path, 0o755)
	fmt.Println(name)
	return name, path
}

// albumFolderFields are the placeholder values of album-folder-format.
type albumFolderFields struct {
	ReleaseDate, ReleaseYear string
	ArtistName, AlbumName    string
	UPC, RecordLabel         string
	Copyright, AlbumID       string
	Quality, Codec, Tag      string
}

func prepareAlbumFolderStage(cfg *structs.ConfigSet, parent string, f albumFolderFields) (name string, path string) {
	return prepareCollectionFolderStage(parent, cfg.AlbumFolderFormat,
		"{ReleaseDate}", f.ReleaseDate,
		"{ReleaseYear}", f.ReleaseYear,
		"{ArtistName}", f.ArtistName,
		"{AlbumName}", f.AlbumName,
		"{UPC}", f.UPC,
		"{RecordLabel}", f.RecordLabel,
		"{Copyright}", f.Copyright,
		"{AlbumId}", f.AlbumID,
		"{Quality}", f.Quality,
		"{Codec}", f.Codec,
		"{Tag}", f.Tag,
	)
}

// playlistFolderFields are the placeholder values of playlist-folder-format.
type playlistFolderFields struct {
	ArtistName, PlaylistName, PlaylistID string
	Quality, Codec, Tag                  string
}

func preparePlaylistFolderStage(cfg *structs.ConfigSet, parent string, f playlistFolderFields) (name string, path string) {
	return prepareCollectionFolderStage(parent, cfg.PlaylistFolderFormat,
		"{ArtistName}", f.ArtistName,
		"{PlaylistName}", f.PlaylistName,
		"{PlaylistId}", f.PlaylistID,
		"{Quality}", f.Quality,
		"{Codec}", f.Codec,
		"{Tag}", f.Tag,
	)
}

// downloadArtistCoverStage saves the artist image as "folder" in the artist
// folder when save-artist-cover is on and an artwork URL is known.
func downloadArtistCoverStage(cfg *structs.ConfigSet, artistFolder string, artworkURL string) {
	if !cfg.SaveArtistCover || artworkURL == "" {
		return
	}
	if _, err := writeCoverWithConfig(artistFolder, "folder", artworkURL, cfg); err != nil {
		fmt.Println("Failed to write artist cover.")
	}
}

// downloadStaticCoverStage saves the collection cover into folderPath and
// returns its path, or "" when static covers are disabled for the session.
func downloadStaticCoverStage(cfg *structs.ConfigSet, session *DownloadSession, folderPath string, artworkURL string) string {
	if !session.shouldDownloadStaticCover() {
		fmt.Println("Static cover download disabled by settings.")
		return ""
	}
	coverPath, err := writeCoverWithConfig(folderPath, "cover", artworkURL, cfg)
	if err != nil {
		fmt.Println("Failed to write cover.")
	}
	return coverPath
}

func assignTrackWorkspaceStage(tracks []task.Track, saveDir string, coverPath string, codec string) {
	for i := range tracks {
		tracks[i].CoverPath = coverPath
		tracks[i].SaveDir = saveDir
		tracks[i].Codec = codec
	}
}

func buildTrackSelectionStage(total int, dlSelect bool, chooser func() []int) []int {
	selected := make([]int, 0, total)
	for i := 1; i <= total; i++ {
		selected = append(selected, i)
	}
	if !dlSelect || chooser == nil {
		return selected
	}
	return chooser()
}

func resolveCollectionQualityStage(session *DownloadSession, storefront string, language string, token string, m3u8Scope string, firstTrackID string, audioTraits []string) (string, string) {
	cfg := &session.Config
	codec := resolveDownloadCodecStage(session)
	if session.DlAtmos {
		return fmt.Sprintf("%dKbps", cfg.AtmosMax-2000), codec
	}
	if session.DlAac && cfg.AacType == "aac-lc" {
		return "256Kbps", codec
	}
	manifest, err := ampapi.GetSongResp(storefront, firstTrackID, language, token)
	if err != nil {
		fmt.Println("Failed to get manifest.\n", err)
		return "", codec
	}
	songManifest, err := firstSongData("download.resolveCollectionQuality", manifest)
	if err != nil {
		fmt.Println("Failed to parse manifest.\n", err)
		return "", codec
	}
	if songManifest.Attributes.ExtendedAssetUrls.EnhancedHls == "" {
		return "256Kbps", "AAC"
	}
	needCheck := false
	if cfg.GetM3u8Mode == "all" {
		needCheck = true
	} else if cfg.GetM3u8Mode == "hires" && contains(audioTraits, "hi-res-lossless") {
		needCheck = true
	}
	if needCheck {
		enhancedHLS, _ := checkM3u8(session, firstTrackID, m3u8Scope)
		if strings.HasSuffix(enhancedHLS, ".m3u8") {
			songManifest.Attributes.ExtendedAssetUrls.EnhancedHls = enhancedHLS
		}
	}
	_, quality, err := extractMedia(session, songManifest.Attributes.ExtendedAssetUrls.EnhancedHls, true)
	if err != nil {
		fmt.Println("Failed to extract quality from manifest.\n", err)
	}
	return quality, codec
}

func prepareAnimatedArtworkStage(session *DownloadSession, folderPath string, squareSource string, tallSource string, unavailableMsg string) {
	cfg := &session.Config
	if !cfg.SaveAnimatedArtwork {
		fmt.Println("Static cover download disabled by settings.")
		return
	}
	if squareSource == "" && tallSource == "" {
		fmt.Println(unavailableMsg)
		return
	}
	fmt.Println("Found Animation Artwork.")

	squarePath := filepath.Join(folderPath, "square_animated_artwork.mp4")
	squareReady := false
	if squareSource != "" {
		motionvideoURLSquare, err := extractVideoWithPreferenceContext(session.downloadContext(), squareSource, *cfg, videoAspectSquare)
		if err != nil {
			fmt.Println("no motion video square.\n", err)
		} else {
			exists := false
			if session.shouldReuseExistingFiles() {
				exists, err = fileExistsNonEmpty(squarePath)
				if err != nil {
					fmt.Println("Failed to check if animated artwork square exists.")
				}
			}
			if exists {
				fmt.Println("Animated artwork square already exists locally.")
				squareReady = true
			} else {
				fmt.Println("Animation Artwork Square Downloading...")
				if _, err := runExternalCommand(session.downloadContext(), "ffmpeg", "-loglevel", "quiet", "-y", "-i", motionvideoURLSquare, "-c", "copy", squarePath); err != nil {
					fmt.Printf("animated artwork square dl err: %v\n", err)
				} else {
					fmt.Println("Animation Artwork Square Downloaded")
					squareReady = true
				}
			}
		}
	}

	if cfg.EmbyAnimatedArtwork && squareReady {
		if _, err := runExternalCommand(session.downloadContext(), "ffmpeg", "-i", squarePath, "-vf", "scale=440:-1", "-r", "24", "-f", "gif", filepath.Join(folderPath, "folder.jpg")); err != nil {
			fmt.Printf("animated artwork square to gif err: %v\n", err)
		}
	}

	if squareReady {
		return
	}

	if tallSource == "" {
		return
	}
	motionvideoURLTall, err := extractVideoWithPreferenceContext(session.downloadContext(), tallSource, *cfg, videoAspectPortrait)
	if err != nil {
		fmt.Println("no motion video tall.\n", err)
		return
	}
	exists := false
	if session.shouldReuseExistingFiles() {
		exists, err = fileExistsNonEmpty(filepath.Join(folderPath, "tall_animated_artwork.mp4"))
		if err != nil {
			fmt.Println("Failed to check if animated artwork tall exists.")
		}
	}
	if exists {
		fmt.Println("Animated artwork tall already exists locally.")
		return
	}
	fmt.Println("Animation Artwork Tall Downloading...")
	if _, err := runExternalCommand(session.downloadContext(), "ffmpeg", "-loglevel", "quiet", "-y", "-i", motionvideoURLTall, "-c", "copy", filepath.Join(folderPath, "tall_animated_artwork.mp4")); err != nil {
		fmt.Printf("animated artwork tall dl err: %v\n", err)
	} else {
		fmt.Println("Animation Artwork Tall Downloaded")
	}
}

func downloadSelectedTracksStage(session *DownloadSession, tracks []task.Track, preID string, selected []int, token string, mediaUserToken string) {
	for idx := range tracks {
		trackNum := idx + 1
		if isInArray(session.OkDict[preID], trackNum) {
			session.Counter.Total++
			session.Counter.Success++
			continue
		}
		if isInArray(selected, trackNum) {
			ripTrack(session, &tracks[idx], token, mediaUserToken)
		}
	}
}

func ripStation(session *DownloadSession, albumId string, token string, storefront string, mediaUserToken string) error {
	ctx, err := resolveStationMediaStage(session, albumId, token, storefront, mediaUserToken)
	if err != nil {
		return err
	}
	prepareStationWorkspaceStage(ctx)
	return downloadStationMediaStage(ctx)
}

func resolveStationMediaStage(session *DownloadSession, stationID string, token string, storefront string, mediaUserToken string) (*stationDownloadContext, error) {
	if session == nil {
		return nil, fmt.Errorf("download session is nil")
	}
	cfg := &session.Config
	station := task.NewStation(storefront, stationID)
	station.Context = session.downloadContext()
	if err := station.GetResp(mediaUserToken, token, cfg.Language); err != nil {
		return nil, err
	}
	fmt.Println(" -", station.Type)
	meta := station.Resp
	stationData, err := firstStationData("download.resolveStationMedia", &meta)
	if err != nil {
		return nil, err
	}
	return &stationDownloadContext{
		session:        session,
		cfg:            cfg,
		station:        station,
		stationData:    stationData,
		token:          token,
		storefront:     storefront,
		mediaUserToken: mediaUserToken,
		stationID:      stationID,
		codec:          resolveDownloadCodecStage(session),
	}, nil
}

func prepareStationWorkspaceStage(ctx *stationDownloadContext) {
	const artistName = "Apple Music Station"
	ctx.singerFolder = prepareArtistFolderStage(ctx.cfg, ctx.session, artistName, "")
	ctx.station.SaveDir = ctx.singerFolder

	ctx.playlistFolder, ctx.playlistPath = preparePlaylistFolderStage(ctx.cfg, ctx.singerFolder, playlistFolderFields{
		ArtistName:   artistName,
		PlaylistName: LimitStringWithConfig(ctx.cfg, ctx.station.Name),
		PlaylistID:   ctx.station.ID,
		Codec:        ctx.codec,
	})
	ctx.station.SaveName = ctx.playlistFolder

	ctx.coverPath = downloadStaticCoverStage(ctx.cfg, ctx.session, ctx.playlistPath, ctx.stationData.Attributes.Artwork.URL)
	ctx.station.CoverPath = ctx.coverPath

	if ctx.cfg.SaveAnimatedArtwork {
		prepareAnimatedArtworkStage(ctx.session, ctx.playlistPath, ctx.stationData.Attributes.EditorialVideo.MotionSquare.Video, "", "Animated artwork not available for this station.")
	}

	assignTrackWorkspaceStage(ctx.station.Tracks, ctx.playlistPath, ctx.coverPath, ctx.codec)
}

func downloadStationMediaStage(ctx *stationDownloadContext) error {
	if ctx.station.Type == "stream" {
		return downloadStationStreamStage(ctx)
	}
	selected := buildTrackSelectionStage(len(ctx.station.Tracks), false, nil)
	downloadSelectedTracksStage(ctx.session, ctx.station.Tracks, ctx.stationID, selected, ctx.token, ctx.mediaUserToken)
	return nil
}

func downloadStationStreamStage(ctx *stationDownloadContext) error {
	ctx.session.Counter.Total++
	songName := strings.NewReplacer(
		"{SongId}", ctx.station.ID,
		"{SongNumer}", "01",
		"{SongName}", LimitStringWithConfig(ctx.cfg, ctx.station.Name),
		"{ArtistName}", "Apple Music Station",
		"{DiscNumber}", "1",
		"{TrackNumber}", "1",
		"{Quality}", "256Kbps",
		"{Tag}", "",
		"{Codec}", "AAC",
	).Replace(ctx.cfg.SongFileFormat)
	fmt.Println(songName)
	trackPath := filepath.Join(ctx.playlistPath, fmt.Sprintf("%s.m4a", forbiddenNames.ReplaceAllString(songName, "_")))
	recordStationStream := func() {
		ctx.session.recordDownloadedFile(trackPath, AudioMeta{
			TrackID:   strings.TrimSpace(ctx.station.ID),
			Title:     strings.TrimSpace(ctx.station.Name),
			Performer: "Apple Music Station",
			Format:    telegramFormatAac,
		})
	}
	if isInArray(ctx.session.OkDict[ctx.station.ID], 1) {
		if exists, _ := fileExistsNonEmpty(trackPath); exists {
			recordStationStream()
		}
		ctx.session.Counter.Success++
		return nil
	}
	exists := false
	if ctx.session.shouldReuseExistingFiles() {
		exists, _ = fileExistsNonEmpty(trackPath)
	}
	if exists {
		recordStationStream()
		ctx.session.Counter.Success++
		ctx.session.OkDict[ctx.station.ID] = append(ctx.session.OkDict[ctx.station.ID], 1)
		fmt.Println("Radio already exists locally.")
		return nil
	}
	assetsURL, serverURL, err := ampapi.GetStationAssetsUrlAndServerUrlWithContext(ctx.session.downloadContext(), ctx.station.ID, ctx.mediaUserToken, ctx.token)
	if err != nil {
		fmt.Println("Failed to get station assets url.", err)
		ctx.session.Counter.Error++
		return err
	}
	trackM3U8 := strings.ReplaceAll(assetsURL, "index.m3u8", "256/prog_index.m3u8")
	keyAndURLs, err := runv3.RunWithContext(ctx.session.downloadContext(), ctx.station.ID, trackM3U8, ctx.token, ctx.mediaUserToken, true, serverURL, nil)
	if err != nil {
		fmt.Println("Failed to resolve station stream.", err)
		ctx.session.Counter.Error++
		return err
	}
	if err := runv3.ExtMvDataWithContext(ctx.session.downloadContext(), keyAndURLs, trackPath); err != nil {
		fmt.Println("Failed to download station stream.", err)
		ctx.session.Counter.Error++
		return err
	}
	tags := []string{
		"tool=",
		"disk=1/1",
		"track=1",
		"tracknum=1/1",
		fmt.Sprintf("artist=%s", "Apple Music Station"),
		fmt.Sprintf("performer=%s", "Apple Music Station"),
		fmt.Sprintf("album_artist=%s", "Apple Music Station"),
		fmt.Sprintf("album=%s", ctx.station.Name),
		fmt.Sprintf("title=%s", ctx.station.Name),
	}
	if ctx.cfg.EmbedCover && strings.TrimSpace(ctx.station.CoverPath) != "" {
		tags = append(tags, fmt.Sprintf("cover=%s", ctx.station.CoverPath))
	}
	if _, err := runMP4BoxWithTags(ctx.session.downloadContext(), tags, trackPath); err != nil {
		fmt.Printf("Embed failed: %v\n", err)
	}
	recordStationStream()
	ctx.session.Counter.Success++
	ctx.session.OkDict[ctx.station.ID] = append(ctx.session.OkDict[ctx.station.ID], 1)
	return nil
}

func ripAlbum(session *DownloadSession, albumId string, token string, storefront string, mediaUserToken string, urlArg_i string) error {
	ctx, err := resolveAlbumMediaStage(session, albumId, token, storefront, mediaUserToken, urlArg_i)
	if err != nil {
		fmt.Println("Failed to get album response.")
		return err
	}
	if debug_mode {
		return renderAlbumDebugStage(ctx)
	}
	prepareAlbumWorkspaceStage(ctx)
	return downloadAlbumMediaStage(ctx)
}

func resolveAlbumMediaStage(session *DownloadSession, albumID string, token string, storefront string, mediaUserToken string, urlArg string) (*albumDownloadContext, error) {
	if session == nil {
		return nil, fmt.Errorf("download session is nil")
	}
	cfg := &session.Config
	album := task.NewAlbum(storefront, albumID)
	album.Context = session.downloadContext()
	if err := album.GetResp(token, cfg.Language); err != nil {
		return nil, err
	}
	meta := album.Resp
	albumData, err := firstAlbumData("download.resolveAlbumMedia", &meta)
	if err != nil {
		return nil, err
	}
	releaseYear, err := safe.ReleaseYear("download.resolveAlbumMedia", "album.data[0].attributes.releaseDate", albumData.Attributes.ReleaseDate)
	if err != nil {
		return nil, err
	}
	codec := resolveDownloadCodecStage(session)
	quality := ""
	if strings.Contains(cfg.AlbumFolderFormat, "Quality") {
		firstTrack, trackErr := safe.FirstRef("download.resolveAlbumMedia", "album.data[0].relationships.tracks.data", albumData.Relationships.Tracks.Data)
		if trackErr != nil {
			return nil, trackErr
		}
		quality, codec = resolveCollectionQualityStage(session, storefront, album.Language, token, "album", firstTrack.ID, firstTrack.Attributes.AudioTraits)
	}
	return &albumDownloadContext{
		session:        session,
		cfg:            cfg,
		album:          album,
		albumData:      albumData,
		token:          token,
		storefront:     storefront,
		mediaUserToken: mediaUserToken,
		albumID:        albumID,
		urlArg:         urlArg,
		releaseYear:    releaseYear,
		codec:          codec,
		quality:        quality,
	}, nil
}

func renderAlbumDebugStage(ctx *albumDownloadContext) error {
	fmt.Println(ctx.albumData.Attributes.ArtistName)
	fmt.Println(ctx.albumData.Attributes.Name)
	for trackNum, track := range ctx.albumData.Relationships.Tracks.Data {
		trackNum++
		fmt.Printf("\nTrack %d of %d:\n", trackNum, len(ctx.albumData.Relationships.Tracks.Data))
		fmt.Printf("%02d. %s\n", trackNum, track.Attributes.Name)
		manifest, err := ampapi.GetSongRespWithContext(ctx.session.downloadContext(), ctx.storefront, track.ID, ctx.album.Language, ctx.token)
		if err != nil {
			fmt.Printf("Failed to get manifest for track %d: %v\n", trackNum, err)
			continue
		}
		songManifestData, err := firstSongData("download.renderAlbumDebug", manifest)
		if err != nil {
			fmt.Printf("Failed to parse manifest for track %d: %v\n", trackNum, err)
			continue
		}
		m3u8URL := songManifestData.Attributes.ExtendedAssetUrls.EnhancedHls
		needCheck := false
		if ctx.cfg.GetM3u8Mode == "all" {
			needCheck = true
		} else if ctx.cfg.GetM3u8Mode == "hires" && contains(track.Attributes.AudioTraits, "hi-res-lossless") {
			needCheck = true
		}
		if needCheck {
			fullM3u8URL, err := checkM3u8(ctx.session, track.ID, "song")
			if err == nil && strings.HasSuffix(fullM3u8URL, ".m3u8") {
				m3u8URL = fullM3u8URL
			} else {
				fmt.Println("Failed to get best quality m3u8 from device m3u8 port, will use m3u8 from Web API")
			}
		}
		if _, _, err := extractMedia(ctx.session, m3u8URL, true); err != nil {
			fmt.Printf("Failed to extract quality info for track %d: %v\n", trackNum, err)
		}
	}
	return nil
}

func prepareAlbumWorkspaceStage(ctx *albumDownloadContext) {
	attrs := &ctx.albumData.Attributes
	artistID, artistArtworkURL := "", ""
	if artistRef, artistErr := safe.FirstRef("download.prepareAlbumWorkspace", "album.data[0].relationships.artists.data", ctx.albumData.Relationships.Artists.Data); artistErr == nil {
		artistID = artistRef.ID
		artistArtworkURL = artistRef.Attributes.Artwork.Url
	}
	artistName := LimitStringWithConfig(ctx.cfg, attrs.ArtistName)
	ctx.singerFolder = prepareArtistFolderStage(ctx.cfg, ctx.session, artistName, artistID)
	ctx.album.SaveDir = ctx.singerFolder

	ctx.albumFolder, ctx.albumFolderPath = prepareAlbumFolderStage(ctx.cfg, ctx.singerFolder, albumFolderFields{
		ReleaseDate: attrs.ReleaseDate,
		ReleaseYear: ctx.releaseYear,
		ArtistName:  artistName,
		AlbumName:   LimitStringWithConfig(ctx.cfg, attrs.Name),
		UPC:         attrs.Upc,
		RecordLabel: attrs.RecordLabel,
		Copyright:   attrs.Copyright,
		AlbumID:     ctx.albumID,
		Quality:     ctx.quality,
		Codec:       ctx.codec,
		Tag:         buildFolderTagStage(ctx.cfg, attrs.IsAppleDigitalMaster || attrs.IsMasteredForItunes, attrs.ContentRating),
	})
	ctx.album.SaveName = ctx.albumFolder

	downloadArtistCoverStage(ctx.cfg, ctx.singerFolder, artistArtworkURL)
	ctx.coverPath = downloadStaticCoverStage(ctx.cfg, ctx.session, ctx.albumFolderPath, attrs.Artwork.URL)
	if ctx.cfg.SaveAnimatedArtwork {
		prepareAnimatedArtworkStage(ctx.session, ctx.albumFolderPath, attrs.EditorialVideo.MotionDetailSquare.Video, attrs.EditorialVideo.MotionDetailTall.Video, "Animated artwork not available for this album.")
	}

	assignTrackWorkspaceStage(ctx.album.Tracks, ctx.albumFolderPath, ctx.coverPath, ctx.codec)
}

func downloadAlbumMediaStage(ctx *albumDownloadContext) error {
	if ctx.session.DlSong {
		if strings.TrimSpace(ctx.urlArg) == "" {
			return nil
		}
		for i := range ctx.album.Tracks {
			if ctx.urlArg == ctx.album.Tracks[i].ID {
				ripTrack(ctx.session, &ctx.album.Tracks[i], ctx.token, ctx.mediaUserToken)
				return nil
			}
		}
		return nil
	}
	selected := buildTrackSelectionStage(len(ctx.album.Tracks), ctx.session.DlSelect, func() []int {
		return internalcli.SelectAlbumTracks(ctx.album)
	})
	downloadSelectedTracksStage(ctx.session, ctx.album.Tracks, ctx.albumID, selected, ctx.token, ctx.mediaUserToken)
	return nil
}

func ripPlaylist(session *DownloadSession, playlistId string, token string, storefront string, mediaUserToken string) error {
	ctx, err := resolvePlaylistMediaStage(session, playlistId, token, storefront, mediaUserToken)
	if err != nil {
		fmt.Println("Failed to get playlist response.")
		return err
	}
	if debug_mode {
		return renderPlaylistDebugStage(ctx)
	}
	preparePlaylistWorkspaceStage(ctx)
	return downloadPlaylistMediaStage(ctx)
}

func resolvePlaylistMediaStage(session *DownloadSession, playlistID string, token string, storefront string, mediaUserToken string) (*playlistDownloadContext, error) {
	if session == nil {
		return nil, fmt.Errorf("download session is nil")
	}
	cfg := &session.Config
	playlist := task.NewPlaylist(storefront, playlistID)
	playlist.Context = session.downloadContext()
	if err := playlist.GetResp(token, cfg.Language); err != nil {
		return nil, err
	}
	meta := playlist.Resp
	playlistData, err := firstPlaylistData("download.resolvePlaylistMedia", &meta)
	if err != nil {
		return nil, err
	}
	codec := resolveDownloadCodecStage(session)
	quality := ""
	if strings.Contains(cfg.AlbumFolderFormat, "Quality") {
		firstTrack, trackErr := safe.FirstRef("download.resolvePlaylistMedia", "playlist.data[0].relationships.tracks.data", playlistData.Relationships.Tracks.Data)
		if trackErr != nil {
			return nil, trackErr
		}
		quality, codec = resolveCollectionQualityStage(session, storefront, playlist.Language, token, "album", firstTrack.ID, firstTrack.Attributes.AudioTraits)
	}
	return &playlistDownloadContext{
		session:        session,
		cfg:            cfg,
		playlist:       playlist,
		playlistData:   playlistData,
		token:          token,
		storefront:     storefront,
		mediaUserToken: mediaUserToken,
		playlistID:     playlistID,
		codec:          codec,
		quality:        quality,
	}, nil
}

func renderPlaylistDebugStage(ctx *playlistDownloadContext) error {
	fmt.Println(ctx.playlistData.Attributes.ArtistName)
	fmt.Println(ctx.playlistData.Attributes.Name)
	for trackNum, track := range ctx.playlistData.Relationships.Tracks.Data {
		trackNum++
		fmt.Printf("\nTrack %d of %d:\n", trackNum, len(ctx.playlistData.Relationships.Tracks.Data))
		fmt.Printf("%02d. %s\n", trackNum, track.Attributes.Name)
		manifest, err := ampapi.GetSongRespWithContext(ctx.session.downloadContext(), ctx.storefront, track.ID, ctx.playlist.Language, ctx.token)
		if err != nil {
			fmt.Printf("Failed to get manifest for track %d: %v\n", trackNum, err)
			continue
		}
		songManifestData, err := firstSongData("download.renderPlaylistDebug", manifest)
		if err != nil {
			fmt.Printf("Failed to parse manifest for track %d: %v\n", trackNum, err)
			continue
		}
		m3u8URL := songManifestData.Attributes.ExtendedAssetUrls.EnhancedHls
		needCheck := false
		if ctx.cfg.GetM3u8Mode == "all" {
			needCheck = true
		} else if ctx.cfg.GetM3u8Mode == "hires" && contains(track.Attributes.AudioTraits, "hi-res-lossless") {
			needCheck = true
		}
		if needCheck {
			fullM3u8URL, err := checkM3u8(ctx.session, track.ID, "song")
			if err == nil && strings.HasSuffix(fullM3u8URL, ".m3u8") {
				m3u8URL = fullM3u8URL
			} else {
				fmt.Println("Failed to get best quality m3u8 from device m3u8 port, will use m3u8 from Web API")
			}
		}
		if _, _, err := extractMedia(ctx.session, m3u8URL, true); err != nil {
			fmt.Printf("Failed to extract quality info for track %d: %v\n", trackNum, err)
		}
	}
	return nil
}

func preparePlaylistWorkspaceStage(ctx *playlistDownloadContext) {
	const artistName = "Apple Music"
	attrs := &ctx.playlistData.Attributes
	ctx.singerFolder = prepareArtistFolderStage(ctx.cfg, ctx.session, artistName, "")
	ctx.playlist.SaveDir = ctx.singerFolder

	ctx.playlistFolder, ctx.playlistPath = preparePlaylistFolderStage(ctx.cfg, ctx.singerFolder, playlistFolderFields{
		ArtistName:   artistName,
		PlaylistName: LimitStringWithConfig(ctx.cfg, attrs.Name),
		PlaylistID:   ctx.playlistID,
		Quality:      ctx.quality,
		Codec:        ctx.codec,
		Tag:          buildFolderTagStage(ctx.cfg, attrs.IsAppleDigitalMaster || attrs.IsMasteredForItunes, attrs.ContentRating),
	})
	ctx.playlist.SaveName = ctx.playlistFolder

	ctx.coverPath = downloadStaticCoverStage(ctx.cfg, ctx.session, ctx.playlistPath, attrs.Artwork.URL)
	assignTrackWorkspaceStage(ctx.playlist.Tracks, ctx.playlistPath, ctx.coverPath, ctx.codec)

	if ctx.cfg.SaveAnimatedArtwork {
		prepareAnimatedArtworkStage(ctx.session, ctx.playlistPath, attrs.EditorialVideo.MotionDetailSquare.Video, attrs.EditorialVideo.MotionDetailTall.Video, "Animated artwork not available for this playlist.")
	}
}

func downloadPlaylistMediaStage(ctx *playlistDownloadContext) error {
	selected := buildTrackSelectionStage(len(ctx.playlist.Tracks), ctx.session.DlSelect, func() []int {
		return internalcli.SelectPlaylistTracks(ctx.playlist)
	})
	downloadSelectedTracksStage(ctx.session, ctx.playlist.Tracks, ctx.playlistID, selected, ctx.token, ctx.mediaUserToken)
	return nil
}

func ripSong(session *DownloadSession, songId string, token string, storefront string, mediaUserToken string) error {
	ctx, err := resolveSongMediaStage(session, songId, token, storefront, mediaUserToken)
	if err != nil {
		fmt.Println("Failed to get song response.")
		return err
	}
	prepareSongWorkspaceStage(ctx)
	ripTrack(ctx.session, &ctx.track, ctx.token, ctx.mediaUserToken)
	return nil
}

func resolveSongMediaStage(session *DownloadSession, songID string, token string, storefront string, mediaUserToken string) (*songDownloadContext, error) {
	if session == nil {
		return nil, fmt.Errorf("download session is nil")
	}
	manifest, err := ampapi.GetSongRespWithContext(session.downloadContext(), storefront, songID, session.Config.Language, token)
	if err != nil {
		return nil, err
	}
	songData, err := firstSongData("download.resolveSongMedia", manifest)
	if err != nil {
		return nil, err
	}
	albumID, err := firstSongAlbumID("download.resolveSongMedia", songData)
	if err != nil {
		return nil, err
	}
	track, err := buildDirectSongTrackStage(songData, storefront, session.Config.Language, albumID)
	if err != nil {
		return nil, err
	}
	track.Context = session.downloadContext()
	if err := track.GetAlbumData(token); err != nil {
		return nil, err
	}
	track.TaskTotal = track.AlbumData.TrackCount
	if track.TaskNum <= 0 {
		track.TaskNum = songData.Attributes.TrackNumber
	}
	return &songDownloadContext{
		session:        session,
		cfg:            &session.Config,
		token:          token,
		storefront:     storefront,
		mediaUserToken: mediaUserToken,
		songID:         songID,
		track:          track,
		codec:          resolveDownloadCodecStage(session),
	}, nil
}

func buildDirectSongTrackStage(songData *ampapi.SongRespData, storefront string, language string, albumID string) (task.Track, error) {
	if songData == nil {
		return task.Track{}, fmt.Errorf("song data is nil")
	}
	payload, err := json.Marshal(songData)
	if err != nil {
		return task.Track{}, err
	}
	resp := ampapi.TrackRespData{}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return task.Track{}, err
	}
	track := task.Track{
		Context:    context.Background(),
		ID:         songData.ID,
		Type:       songData.Type,
		Name:       songData.Attributes.Name,
		Language:   language,
		Storefront: storefront,
		TaskNum:    songData.Attributes.TrackNumber,
		TaskTotal:  1,
		M3u8:       songData.Attributes.ExtendedAssetUrls.EnhancedHls,
		WebM3u8:    songData.Attributes.ExtendedAssetUrls.EnhancedHls,
		Resp:       resp,
		PreType:    "albums",
		PreID:      albumID,
	}
	return track, nil
}

func prepareSongWorkspaceStage(ctx *songDownloadContext) {
	albumData := &ctx.track.AlbumData
	releaseYear, err := safe.ReleaseYear("download.prepareSongWorkspace", "album.data[0].attributes.releaseDate", albumData.ReleaseDate)
	if err != nil {
		releaseYear = ""
	}
	artistName := LimitStringWithConfig(ctx.cfg, albumData.ArtistName)
	singerFolder := prepareArtistFolderStage(ctx.cfg, ctx.session, artistName, albumData.ArtistID)

	quality := ""
	if strings.Contains(ctx.cfg.AlbumFolderFormat, "Quality") {
		quality, ctx.codec = resolveCollectionQualityStage(ctx.session, ctx.storefront, ctx.cfg.Language, ctx.token, "album", ctx.songID, ctx.track.Resp.Attributes.AudioTraits)
	}
	_, albumFolderPath := prepareAlbumFolderStage(ctx.cfg, singerFolder, albumFolderFields{
		ReleaseDate: albumData.ReleaseDate,
		ReleaseYear: releaseYear,
		ArtistName:  artistName,
		AlbumName:   LimitStringWithConfig(ctx.cfg, albumData.Name),
		UPC:         albumData.Upc,
		RecordLabel: albumData.RecordLabel,
		Copyright:   albumData.Copyright,
		AlbumID:     ctx.track.PreID,
		Quality:     quality,
		Codec:       ctx.codec,
		Tag:         buildFolderTagStage(ctx.cfg, albumData.IsAppleDigitalMaster || albumData.IsMasteredForItunes, albumData.ContentRating),
	})

	// A blank artist artwork URL means there is nothing to download.
	if strings.TrimSpace(albumData.ArtistArtworkURL) != "" {
		downloadArtistCoverStage(ctx.cfg, singerFolder, albumData.ArtistArtworkURL)
	}
	ctx.coverPath = downloadStaticCoverStage(ctx.cfg, ctx.session, albumFolderPath, albumData.ArtworkURL)
	if ctx.cfg.SaveAnimatedArtwork {
		prepareAnimatedArtworkStage(ctx.session, albumFolderPath, albumData.MotionDetailSquareVideo, albumData.MotionDetailTallVideo, "Animated artwork not available for this album.")
	}

	ctx.track.SaveDir = albumFolderPath
	ctx.track.CoverPath = ctx.coverPath
	ctx.track.Codec = ctx.codec
}
