package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// SearchResp represents the top-level response from the search API.
type SearchResp struct {
	Results SearchResults `json:"results"`
}

// SearchResults contains the different types of search results.
type SearchResults struct {
	Songs   *SongResults   `json:"songs,omitempty"`
	Albums  *AlbumResults  `json:"albums,omitempty"`
	Artists *ArtistResults `json:"artists,omitempty"`
}

// SongResults contains a list of song search results.
type SongResults struct {
	Href string         `json:"href"`
	Next string         `json:"next"`
	Data []SongRespData `json:"data"`
}

// AlbumResults contains a list of album search results.
type AlbumResults struct {
	Href string          `json:"href"`
	Next string          `json:"next"`
	Data []AlbumRespData `json:"data"`
}

// ArtistResults contains a list of artist search results.
type ArtistResults struct {
	Href string `json:"href"`
	Next string `json:"next"`
	Data []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Href       string `json:"href"`
		Attributes struct {
			Name       string   `json:"name"`
			GenreNames []string `json:"genreNames"`
			URL        string   `json:"url"`
		} `json:"attributes"`
	} `json:"data"`
}

// Search performs a search query against the Apple Music API.
func Search(storefront, term, types, language, token string, limit, offset int) (*SearchResp, error) {
	return SearchWithContext(context.Background(), storefront, term, types, language, token, limit, offset)
}

func SearchWithContext(ctx context.Context, storefront, term, types, language, token string, limit, offset int) (*SearchResp, error) {
	obj := new(SearchResp)
	err := doJSONWithRetry(ctx, token, obj, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL(fmt.Sprintf("/v1/catalog/%s/search", storefront)), nil)
		if err != nil {
			return nil, err
		}
		setAppleMusicHeaders(req, token, "")
		query := url.Values{}
		query.Set("term", term)
		query.Set("types", types)
		query.Set("limit", fmt.Sprintf("%d", limit))
		query.Set("offset", fmt.Sprintf("%d", offset))
		query.Set("l", language)
		req.URL.RawQuery = query.Encode()
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	return obj, nil
}
