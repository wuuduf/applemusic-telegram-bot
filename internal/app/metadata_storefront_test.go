package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func metadataTestSong(id, isrc, title, artist, album string) *ampapi.SongRespData {
	song := &ampapi.SongRespData{ID: id}
	song.Attributes.Isrc = isrc
	song.Attributes.Name = title
	song.Attributes.ArtistName = artist
	song.Attributes.AlbumName = album
	song.Attributes.DurationInMillis = 180_000
	return song
}

func TestSameSongRecordingRejectsCrossStorefrontDurationMismatch(t *testing.T) {
	source := metadataTestSong("tr-track", "ISRC-123", "Title", "Artist", "Album")
	candidate := metadataTestSong("cn-track", "ISRC-123", "Title", "Artist", "Album")
	candidate.Attributes.DurationInMillis += 5_000
	if sameSongRecording(source, candidate) {
		t.Fatal("sameSongRecording() accepted a different-duration cross-storefront result")
	}
}

func TestUniqueISRCMatchRejectsAmbiguousCandidates(t *testing.T) {
	source := metadataTestSong("tr-track", "ISRC-123", "Title", "Artist", "Album")
	first := *metadataTestSong("cn-track-one", "ISRC-123", "Title", "Artist", "Album")
	second := *metadataTestSong("cn-track-two", "ISRC-123", "Title", "Artist", "Album")
	second.Attributes.DurationInMillis += 500

	got, ok := uniqueISRCMatch(source, []ampapi.SongRespData{first, second})
	if ok || got != nil {
		t.Fatalf("uniqueISRCMatch() = (%v, %v), want ambiguous candidates rejected", got, ok)
	}
}

func TestUniqueISRCMatchSelectsOnlyDurationCompatibleCandidate(t *testing.T) {
	source := metadataTestSong("tr-track", "ISRC-123", "Title", "Artist", "Album")
	wrongDuration := *metadataTestSong("cn-wrong", "ISRC-123", "Title", "Artist", "Album")
	wrongDuration.Attributes.DurationInMillis += 10_000
	matching := *metadataTestSong("cn-match", "ISRC-123", "Title", "Artist", "Album")
	matching.Attributes.DurationInMillis += 500

	got, ok := uniqueISRCMatch(source, []ampapi.SongRespData{wrongDuration, matching})
	if !ok || got == nil || got.ID != "cn-match" {
		t.Fatalf("uniqueISRCMatch() = (%v, %v), want cn-match", got, ok)
	}
}

func TestLocalizedMetadataSourceCopiesTrackIdentityAndTagFields(t *testing.T) {
	track := &ampapi.TrackRespData{ID: "tr-track"}
	track.Attributes.Isrc = "ISRC-123"
	track.Attributes.Name = "Turkish title"
	track.Attributes.ArtistName = "Artist"
	track.Attributes.AlbumName = "Album"
	track.Attributes.ComposerName = "Composer"
	track.Attributes.DurationInMillis = 180_000

	got := localizedMetadataSourceFromTrack(track)
	if got == nil {
		t.Fatal("localizedMetadataSourceFromTrack() returned nil")
	}
	if got.ID != "tr-track" || got.Attributes.Isrc != "ISRC-123" || got.Attributes.Name != "Turkish title" || got.Attributes.ArtistName != "Artist" || got.Attributes.AlbumName != "Album" || got.Attributes.ComposerName != "Composer" || got.Attributes.DurationInMillis != 180_000 {
		t.Fatalf("localized metadata source = %+v, want copied identity and tag fields", got)
	}
}

func TestMP4TagTextFieldsUseLocalizedMetadataWhenAvailable(t *testing.T) {
	got := mp4TagTextFieldsFor("Türkçe", "Artist", "Album", "Composer", localizedTrackMetadata{
		Title:    "中文歌名",
		Artist:   "中文歌手",
		Album:    "中文专辑",
		Composer: "中文作曲",
	})
	want := mp4TagTextFields{Title: "中文歌名", Artist: "中文歌手", Album: "中文专辑", Composer: "中文作曲"}
	if got != want {
		t.Fatalf("mp4TagTextFieldsFor() = %+v, want %+v", got, want)
	}
}

func TestMetadataStorefrontCandidatesPreferConfiguredChineseMarketsAndDeduplicate(t *testing.T) {
	got := metadataStorefrontCandidates("CN", "tr")
	want := []string{"cn", "tw", "hk", "us", "tr"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadataStorefrontCandidates() = %v, want %v", got, want)
	}
	defaultMarkets := metadataStorefrontCandidates("", "TR")
	wantDefaultMarkets := []string{"cn", "tw", "hk", "us", "tr"}
	if !reflect.DeepEqual(defaultMarkets, wantDefaultMarkets) {
		t.Fatalf("metadataStorefrontCandidates() default = %v, want %v", defaultMarkets, wantDefaultMarkets)
	}
}

func TestResolveLocalizedSongMetadataFallsBackByISRCAndRejectsWrongRecording(t *testing.T) {
	source := metadataTestSong("tr-track", "ISRC-123", "Turkish title", "Artist", "Album")
	cfg := &structs.ConfigSet{MetadataStorefront: "cn", Language: "zh-Hans-CN"}
	var calls []string
	lookup := func(_ context.Context, storefront string, _ *ampapi.SongRespData, _, _ string) (*ampapi.SongRespData, error) {
		calls = append(calls, storefront)
		switch storefront {
		case "cn":
			return metadataTestSong("wrong-track", "OTHER-ISRC", "错误歌曲", "错误歌手", "错误专辑"), nil
		case "tw":
			return metadataTestSong("tw-track", "ISRC-123", "繁體歌名", "歌手", "專輯"), nil
		default:
			return nil, errors.New("not found")
		}
	}

	got, storefront := resolveLocalizedSongMetadata(context.Background(), source, "tr", cfg, "test-token", lookup)
	if storefront != "tw" {
		t.Fatalf("resolved storefront = %q, want tw", storefront)
	}
	if got.Title != "繁體歌名" || got.Artist != "歌手" || got.Album != "專輯" {
		t.Fatalf("localized metadata = %+v, want Chinese metadata from tw", got)
	}
	if got.SongIDs["tw"] != "tw-track" {
		t.Fatalf("localized song IDs = %v, want tw-track for tw storefront", got.SongIDs)
	}
	if !reflect.DeepEqual(calls, []string{"cn", "tw"}) {
		t.Fatalf("lookup calls = %v, want [cn tw]", calls)
	}
}

func TestResolveLocalizedSongMetadataStopsWhenLookupContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := &structs.ConfigSet{MetadataStorefront: "cn", Language: "zh-Hans-CN"}
	calls := 0
	lookup := func(_ context.Context, _ string, _ *ampapi.SongRespData, _, _ string) (*ampapi.SongRespData, error) {
		calls++
		cancel()
		return nil, context.Canceled
	}

	resolveLocalizedSongMetadata(ctx, metadataTestSong("tr-track", "ISRC-123", "Title", "Artist", "Album"), "tr", cfg, "test-token", lookup)
	if calls != 1 {
		t.Fatalf("lookup calls after cancellation = %d, want 1", calls)
	}
}

func TestResolveLocalizedLyricsContinuesUntilChineseLyricsAreFound(t *testing.T) {
	cfg := &structs.ConfigSet{
		MetadataStorefront: "cn",
		Language:           "zh-Hans-CN",
		LrcType:            "lyrics",
		LrcFormat:          "lrc",
	}
	var calls []string
	lookup := func(_ context.Context, storefront, _, _, _, _, _, _ string) (string, error) {
		calls = append(calls, storefront)
		if storefront == "cn" {
			return "[00:01.00]English lyrics", nil
		}
		if storefront == "tw" {
			return "[00:01.00]中文歌詞", nil
		}
		return "", errors.New("lyrics unavailable")
	}

	got, err := resolveLocalizedLyrics(context.Background(), "tr-track", "tr", cfg, "test-token", "test-media-token", lookup)
	if err != nil {
		t.Fatalf("resolveLocalizedLyrics() error = %v", err)
	}
	if got != "[00:01.00]中文歌詞" {
		t.Fatalf("resolved lyrics = %q, want Chinese lyrics", got)
	}
	if !reflect.DeepEqual(calls, []string{"cn", "tw"}) {
		t.Fatalf("lyrics lookup calls = %v, want [cn tw]", calls)
	}
}

func TestResolveLocalizedLyricsFallsBackToDownloadStorefrontLyricsIfNoChineseFound(t *testing.T) {
	cfg := &structs.ConfigSet{MetadataStorefront: "cn", Language: "zh-Hans-CN", LrcType: "lyrics", LrcFormat: "lrc"}
	lookup := func(_ context.Context, storefront, _, _, _, _, _, _ string) (string, error) {
		switch storefront {
		case "cn":
			return "[00:01.00]English lyrics", nil
		case "tr":
			return "[00:01.00]Original Turkish lyrics", nil
		default:
			return "", errors.New("lyrics unavailable")
		}
	}

	got, err := resolveLocalizedLyrics(context.Background(), "tr-track", "tr", cfg, "test-token", "test-media-token", lookup)
	if err != nil {
		t.Fatalf("resolveLocalizedLyrics() error = %v", err)
	}
	if got != "[00:01.00]Original Turkish lyrics" {
		t.Fatalf("resolved lyrics = %q, want download-storefront lyrics when no Chinese translation exists", got)
	}
}

func TestMetadataStorefrontLookupContextHasBoundedDeadline(t *testing.T) {
	ctx, cancel := newMetadataStorefrontLookupContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("localized storefront lookup context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > metadataStorefrontLookupTimeout {
		t.Fatalf("localized storefront lookup deadline has %s remaining, want within %s", remaining, metadataStorefrontLookupTimeout)
	}
}

func TestResolveLocalizedLyricsReservesTimeForDownloadStorefrontFallback(t *testing.T) {
	cfg := &structs.ConfigSet{MetadataStorefront: "cn", Language: "zh-Hans-CN", LrcType: "lyrics", LrcFormat: "lrc"}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
	defer cancel()
	var calls []string
	lookup := func(_ context.Context, storefront, _, _, _, _, _, _ string) (string, error) {
		calls = append(calls, storefront)
		if storefront == "tr" {
			return "[00:01.00]Original Turkish lyrics", nil
		}
		return "", errors.New("lyrics unavailable")
	}

	got, err := resolveLocalizedLyrics(ctx, "tr-track", "tr", cfg, "test-token", "test-media-token", lookup)
	if err != nil {
		t.Fatalf("resolveLocalizedLyrics() error = %v", err)
	}
	if got != "[00:01.00]Original Turkish lyrics" {
		t.Fatalf("resolved lyrics = %q, want download-storefront fallback", got)
	}
	if !reflect.DeepEqual(calls, []string{"tr"}) {
		t.Fatalf("lyrics lookup order = %v, want only original storefront while the deadline reserve is active", calls)
	}
}
