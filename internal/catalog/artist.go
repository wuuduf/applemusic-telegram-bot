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

type artistRelationshipPage struct {
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

func (s *Service) FetchArtistRelationshipAll(storefront string, artistID string, relationship string) ([]ArtistRelationshipItem, error) {
	storefront = strings.TrimSpace(storefront)
	artistID = strings.TrimSpace(artistID)
	relationship = strings.TrimSpace(relationship)
	if storefront == "" || artistID == "" || relationship == "" {
		return nil, fmt.Errorf("invalid artist relationship query")
	}

	apiOffset := 0
	items := make([]ArtistRelationshipItem, 0)
	for {
		req, err := http.NewRequest("GET", fmt.Sprintf("https://amp-api.music.apple.com/v1/catalog/%s/artists/%s/%s", storefront, artistID, relationship), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.AppleToken))
		req.Header.Set("User-Agent", s.userAgent())
		req.Header.Set("Origin", "https://music.apple.com")
		query := url.Values{}
		query.Set("limit", "100")
		query.Set("offset", strconv.Itoa(apiOffset))
		if strings.TrimSpace(s.Language) != "" {
			query.Set("l", s.Language)
		}
		req.URL.RawQuery = query.Encode()

		resp, err := s.client().Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("artist %s request failed: %s", relationship, resp.Status)
		}

		page := new(artistRelationshipPage)
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
		apiOffset += 100
	}
	return items, nil
}
