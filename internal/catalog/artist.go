package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type ArtistRelationshipItem struct {
	ID            string
	Name          string
	URL           string
	ReleaseDate   string
	ContentRating string
	ArtistName    string
	AlbumName     string
}

type artistCollectionPage struct {
	Next string `json:"next"`
	Data []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Href       string `json:"href"`
		Attributes struct {
			ArtistName           string   `json:"artistName"`
			URL                  string   `json:"url"`
			ContentRating        string   `json:"contentRating"`
			ReleaseDate          string   `json:"releaseDate"`
			Name                 string   `json:"name"`
			AlbumName            string   `json:"albumName"`
			GenreNames           []string `json:"genreNames"`
			HasTimeSyncedLyrics  bool     `json:"hasTimeSyncedLyrics"`
			IsAppleDigitalMaster bool     `json:"isAppleDigitalMaster"`
			DurationInMillis     int      `json:"durationInMillis"`
			Isrc                 string   `json:"isrc"`
			AudioTraits          []string `json:"audioTraits"`
			HasLyrics            bool     `json:"hasLyrics"`
			TrackNumber          int      `json:"trackNumber"`
			AudioLocale          string   `json:"audioLocale"`
			ComposerName         string   `json:"composerName"`
		} `json:"attributes"`
	} `json:"data"`
}

func artistCollectionPageLimit(endpoint string) int {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(endpoint), "/")) {
	case "songs":
		return 20
	default:
		return 100
	}
}

func artistCollectionEndpointURL(storefront string, artistID string, endpoint string) string {
	return fmt.Sprintf("https://amp-api.music.apple.com/v1/catalog/%s/artists/%s/%s", storefront, artistID, strings.Trim(strings.TrimSpace(endpoint), "/"))
}

func artistCollectionNextURL(pageURL string, nextPath string) string {
	nextPath = strings.TrimSpace(nextPath)
	if nextPath == "" {
		return ""
	}
	if strings.HasPrefix(nextPath, "http://") || strings.HasPrefix(nextPath, "https://") {
		return nextPath
	}
	baseURL, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil {
		if strings.HasPrefix(nextPath, "/") {
			return "https://amp-api.music.apple.com" + nextPath
		}
		return strings.TrimSpace(pageURL)
	}
	nextURL, err := url.Parse(nextPath)
	if err != nil {
		if strings.HasPrefix(nextPath, "/") {
			return "https://amp-api.music.apple.com" + nextPath
		}
		return strings.TrimSpace(pageURL)
	}
	return baseURL.ResolveReference(nextURL).String()
}

func (s *Service) fetchArtistCollectionAll(storefront string, artistID string, endpoint string) ([]ArtistRelationshipItem, error) {
	storefront = strings.TrimSpace(storefront)
	artistID = strings.TrimSpace(artistID)
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	if storefront == "" || artistID == "" || endpoint == "" {
		return nil, fmt.Errorf("invalid artist collection query")
	}

	pageURL := artistCollectionEndpointURL(storefront, artistID, endpoint)
	pageLimit := artistCollectionPageLimit(endpoint)
	apiOffset := 0
	items := make([]ArtistRelationshipItem, 0)
	for {
		req, err := http.NewRequest("GET", pageURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.AppleToken))
		req.Header.Set("User-Agent", s.userAgent())
		req.Header.Set("Origin", "https://music.apple.com")
		query := req.URL.Query()
		if query.Get("limit") == "" {
			query.Set("limit", strconv.Itoa(pageLimit))
		}
		if query.Get("offset") == "" {
			query.Set("offset", strconv.Itoa(apiOffset))
		}
		if strings.TrimSpace(s.Language) != "" && query.Get("l") == "" {
			query.Set("l", s.Language)
		}
		req.URL.RawQuery = query.Encode()

		resp, err := s.client().Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("artist %s request failed: %s", endpoint, resp.Status)
		}

		page := new(artistCollectionPage)
		if err := json.NewDecoder(resp.Body).Decode(page); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, item := range page.Data {
			items = append(items, ArtistRelationshipItem{
				ID:            strings.TrimSpace(item.ID),
				Name:          strings.TrimSpace(item.Attributes.Name),
				URL:           strings.TrimSpace(item.Attributes.URL),
				ReleaseDate:   strings.TrimSpace(item.Attributes.ReleaseDate),
				ContentRating: strings.TrimSpace(item.Attributes.ContentRating),
				ArtistName:    strings.TrimSpace(item.Attributes.ArtistName),
				AlbumName:     strings.TrimSpace(item.Attributes.AlbumName),
			})
		}

		if page.Next == "" {
			break
		}
		pageURL = artistCollectionNextURL(pageURL, page.Next)
		apiOffset += pageLimit
	}
	return items, nil
}

func (s *Service) FetchArtistRelationshipAll(storefront string, artistID string, relationship string) ([]ArtistRelationshipItem, error) {
	relationship = strings.TrimSpace(relationship)
	if relationship == "" {
		return nil, fmt.Errorf("invalid artist relationship query")
	}
	return s.fetchArtistCollectionAll(storefront, artistID, relationship)
}

func (s *Service) FetchArtistViewAll(storefront string, artistID string, view string) ([]ArtistRelationshipItem, error) {
	view = strings.Trim(strings.TrimSpace(view), "/")
	if view == "" {
		return nil, fmt.Errorf("invalid artist view query")
	}
	return s.fetchArtistCollectionAll(storefront, artistID, "view/"+view)
}
