package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

func GetAlbumResp(storefront string, id string, language string, token string) (*AlbumResp, error) {
	return GetAlbumRespWithContext(context.Background(), storefront, id, language, token)
}

func GetAlbumRespWithContext(ctx context.Context, storefront string, id string, language string, token string) (*AlbumResp, error) {
	obj := new(AlbumResp)
	err := doJSONWithRetry(ctx, token, obj, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL(fmt.Sprintf("/v1/catalog/%s/albums/%s", storefront, id)), nil)
		if err != nil {
			return nil, err
		}
		setAppleMusicHeaders(req, token, "")
		query := url.Values{}
		query.Set("omit[resource]", "autos")
		query.Set("include", "tracks,artists,record-labels")
		query.Set("include[songs]", "artists")
		query.Set("extend", "editorialVideo,extendedAssetUrls")
		query.Set("l", language)
		req.URL.RawQuery = query.Encode()
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	albumData, err := validateAlbumResponse("ampapi.GetAlbumResp", obj)
	if err != nil {
		return nil, err
	}
	if err := appendPagedTrackResults(ctx, token, albumData.Relationships.Tracks.Next, &albumData.Relationships.Tracks.Data); err != nil {
		return nil, err
	}
	return obj, nil
}

func GetAlbumRespByHref(href string, language string, token string) (*AlbumResp, error) {
	return GetAlbumRespByHrefWithContext(context.Background(), href, language, token)
}

func GetAlbumRespByHrefWithContext(ctx context.Context, href string, language string, token string) (*AlbumResp, error) {
	href = strings.Split(href, "?")[0]
	obj := new(AlbumResp)
	err := doJSONWithRetry(ctx, token, obj, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL(fmt.Sprintf("%s/albums", href)), nil)
		if err != nil {
			return nil, err
		}
		setAppleMusicHeaders(req, token, "")
		query := url.Values{}
		query.Set("omit[resource]", "autos")
		query.Set("include", "tracks,artists,record-labels")
		query.Set("include[songs]", "artists")
		query.Set("extend", "editorialVideo,extendedAssetUrls")
		query.Set("l", language)
		req.URL.RawQuery = query.Encode()
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	albumData, err := validateAlbumResponse("ampapi.GetAlbumRespByHref", obj)
	if err != nil {
		return nil, err
	}
	if err := appendPagedTrackResults(ctx, token, albumData.Relationships.Tracks.Next, &albumData.Relationships.Tracks.Data); err != nil {
		return nil, err
	}
	return obj, nil
}

func validateAlbumResponse(op string, obj *AlbumResp) (*AlbumRespData, error) {
	if obj == nil {
		return nil, &safe.AccessError{Op: op, Path: "album.response", Reason: "nil response"}
	}
	return safe.FirstRef(op, "album.data", obj.Data)
}

type AlbumResp struct {
	Href string          `json:"href"`
	Next string          `json:"next"`
	Data []AlbumRespData `json:"data"`
}

type AlbumRespData struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Href       string `json:"href"`
	Attributes struct {
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
		IsSingle             bool     `json:"isSingle"`
		URL                  string   `json:"url"`
		IsComplete           bool     `json:"isComplete"`
		GenreNames           []string `json:"genreNames"`
		TrackCount           int      `json:"trackCount"`
		IsMasteredForItunes  bool     `json:"isMasteredForItunes"`
		IsAppleDigitalMaster bool     `json:"isAppleDigitalMaster"`
		ContentRating        string   `json:"contentRating"`
		ReleaseDate          string   `json:"releaseDate"`
		Name                 string   `json:"name"`
		RecordLabel          string   `json:"recordLabel"`
		Upc                  string   `json:"upc"`
		AudioTraits          []string `json:"audioTraits"`
		Copyright            string   `json:"copyright"`
		PlayParams           struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"playParams"`
		IsCompilation  bool `json:"isCompilation"`
		EditorialVideo struct {
			MotionTall struct {
				Video string `json:"video"`
			} `json:"motionTallVideo3x4"`
			MotionSquare struct {
				Video string `json:"video"`
			} `json:"motionSquareVideo1x1"`
			MotionDetailTall struct {
				Video string `json:"video"`
			} `json:"motionDetailTall"`
			MotionDetailSquare struct {
				Video string `json:"video"`
			} `json:"motionDetailSquare"`
		} `json:"editorialVideo"`
	} `json:"attributes"`
	Relationships struct {
		RecordLabels struct {
			Href string        `json:"href"`
			Data []interface{} `json:"data"`
		} `json:"record-labels"`
		Artists struct {
			Href string `json:"href"`
			Data []struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Href       string `json:"href"`
				Attributes struct {
					Name    string `json:"name"`
					Artwork struct {
						Url string `json:"url"`
					} `json:"artwork"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"artists"`
		Tracks TrackResp `json:"tracks"`
	} `json:"relationships"`
}
