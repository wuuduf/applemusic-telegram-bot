package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

func GetStationResp(storefront string, id string, language string, token string) (*StationResp, error) {
	return GetStationRespWithContext(context.Background(), storefront, id, language, token)
}

func GetStationRespWithContext(ctx context.Context, storefront string, id string, language string, token string) (*StationResp, error) {
	obj := new(StationResp)
	err := doJSONWithRetry(ctx, token, obj, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL(fmt.Sprintf("/v1/catalog/%s/stations/%s", storefront, id)), nil)
		if err != nil {
			return nil, err
		}
		setAppleMusicHeaders(req, token, "")
		query := url.Values{}
		query.Set("omit[resource]", "autos")
		query.Set("extend", "editorialVideo")
		query.Set("l", language)
		req.URL.RawQuery = query.Encode()
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func GetStationAssetsUrlAndServerUrl(id string, mutoken string, token string) (string, string, error) {
	return GetStationAssetsUrlAndServerUrlWithContext(context.Background(), id, mutoken, token)
}

func GetStationAssetsUrlAndServerUrlWithContext(ctx context.Context, id string, mutoken string, token string) (string, string, error) {
	obj := new(StationAssets)
	err := doJSONWithRetry(ctx, token, obj, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL("/v1/play/assets"), nil)
		if err != nil {
			return nil, err
		}
		setAppleMusicHeaders(req, token, mutoken)
		query := url.Values{}
		query.Set("id", id)
		query.Set("kind", "radioStation")
		query.Set("keyFormat", "web")
		req.URL.RawQuery = query.Encode()
		return req, nil
	})
	if err != nil {
		return "", "", err
	}
	firstAsset, err := safe.FirstRef("ampapi.GetStationAssetsUrlAndServerUrl", "station.results.assets", obj.Results.Assets)
	if err != nil {
		return "", "", err
	}
	return firstAsset.Url, firstAsset.KeyServerUrl, nil
}

func GetStationNextTracks(id, mutoken, language, token string) (*TrackResp, error) {
	return GetStationNextTracksWithContext(context.Background(), id, mutoken, language, token)
}

func GetStationNextTracksWithContext(ctx context.Context, id, mutoken, language, token string) (*TrackResp, error) {
	obj := new(TrackResp)
	err := doJSONWithRetry(ctx, token, obj, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(fmt.Sprintf("/v1/me/stations/next-tracks/%s", id)), nil)
		if err != nil {
			return nil, err
		}
		setAppleMusicHeaders(req, token, mutoken)
		query := url.Values{}
		query.Set("omit[resource]", "autos")
		query.Set("include[songs]", "artists,albums")
		query.Set("limit", "10")
		query.Set("extend", "editorialVideo,extendedAssetUrls")
		query.Set("l", language)
		req.URL.RawQuery = query.Encode()
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

type StationResp struct {
	Href string            `json:"href"`
	Next string            `json:"next"`
	Data []StationRespData `json:"data"`
}

type StationAssets struct {
	Results struct {
		Assets []struct {
			KeyServerUrl              string `json:"keyServerUrl"`
			Url                       string `json:"url"`
			WidevineKeyCertificateUrl string `json:"widevineKeyCertificateUrl"`
			FairPlayKeyCertificateUrl string `json:"fairPlayKeyCertificateUrl"`
		} `json:"assets"`
	} `json:"results"`
}

type StationRespData struct {
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
		IsLive         bool   `json:"isLive"`
		URL            string `json:"url"`
		Name           string `json:"name"`
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
		PlayParams struct {
			ID          string `json:"id"`
			Kind        string `json:"kind"`
			Format      string `json:"format"`
			StationHash string `json:"stationHash"`
		} `json:"playParams"`
	} `json:"attributes"`
}
