package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

func GetSongResp(storefront string, id string, language string, token string) (*SongResp, error) {
	return GetSongRespWithContext(context.Background(), storefront, id, language, token)
}

func GetSongRespWithContext(ctx context.Context, storefront string, id string, language string, token string) (*SongResp, error) {
	obj := new(SongResp)
	err := doJSONWithRetry(ctx, token, obj, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL(fmt.Sprintf("/v1/catalog/%s/songs/%s", storefront, id)), nil)
		if err != nil {
			return nil, err
		}
		setAppleMusicHeaders(req, token, "")
		query := url.Values{}
		query.Set("include", "albums,artists")
		query.Set("extend", "extendedAssetUrls")
		query.Set("l", language)
		req.URL.RawQuery = query.Encode()
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

type SongResp struct {
	Href string         `json:"href"`
	Next string         `json:"next"`
	Data []SongRespData `json:"data"`
}

type SongRespData struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Href       string `json:"href"`
	Attributes struct {
		Previews []struct {
			URL string `json:"url"`
		} `json:"previews"`
		Artwork struct {
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			URL        string `json:"url"`
			BgColor    string `json:"bgColor"`
			TextColor1 string `json:"textColor1"`
			TextColor2 string `json:"textColor2"`
			TextColor3 string `json:"textColor3"`
			TextColor4 string `json:"textColor4"`
		} `json:"artwork"`
		ArtistName           string   `json:"artistName"`
		URL                  string   `json:"url"`
		DiscNumber           int      `json:"discNumber"`
		GenreNames           []string `json:"genreNames"`
		HasTimeSyncedLyrics  bool     `json:"hasTimeSyncedLyrics"`
		IsMasteredForItunes  bool     `json:"isMasteredForItunes"`
		IsAppleDigitalMaster bool     `json:"isAppleDigitalMaster"`
		ContentRating        string   `json:"contentRating"`
		DurationInMillis     int      `json:"durationInMillis"`
		ReleaseDate          string   `json:"releaseDate"`
		Name                 string   `json:"name"`
		ExtendedAssetUrls    struct {
			EnhancedHls string `json:"enhancedHls"`
		} `json:"extendedAssetUrls"`
		Isrc        string   `json:"isrc"`
		AudioTraits []string `json:"audioTraits"`
		HasLyrics   bool     `json:"hasLyrics"`
		AlbumName   string   `json:"albumName"`
		PlayParams  struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"playParams"`
		TrackNumber  int    `json:"trackNumber"`
		AudioLocale  string `json:"audioLocale"`
		ComposerName string `json:"composerName"`
	} `json:"attributes"`
	Relationships struct {
		Artists struct {
			Href string `json:"href"`
			Data []struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Href       string `json:"href"`
				Attributes struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"artists"`
		Albums struct {
			Href string `json:"href"`
			Data []struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Href       string `json:"href"`
				Attributes struct {
					ArtistName string `json:"artistName"`
					Artwork    struct {
						Width      int    `json:"width"`
						Height     int    `json:"height"`
						URL        string `json:"url"`
						BgColor    string `json:"bgColor"`
						TextColor1 string `json:"textColor1"`
						TextColor2 string `json:"textColor2"`
						TextColor3 string `json:"textColor3"`
						TextColor4 string `json:"textColor4"`
					} `json:"artwork"`
					GenreNames          []string `json:"genreNames"`
					IsCompilation       bool     `json:"isCompilation"`
					IsComplete          bool     `json:"isComplete"`
					IsMasteredForItunes bool     `json:"isMasteredForItunes"`
					IsPrerelease        bool     `json:"isPrerelease"`
					IsSingle            bool     `json:"isSingle"`
					Name                string   `json:"name"`
					PlayParams          struct {
						ID   string `json:"id"`
						Kind string `json:"kind"`
					} `json:"playParams"`
					ReleaseDate string `json:"releaseDate"`
					TrackCount  int    `json:"trackCount"`
					Upc         string `json:"upc"`
					URL         string `json:"url"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"albums"`
	} `json:"relationships"`
}
