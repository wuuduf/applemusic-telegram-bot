package utils

import (
	"sort"
	"strings"
	"time"

	sharedcatalog "github.com/wuuduf/applemusic-telegram-bot/internal/catalog"
	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
)

func newArtistCatalogService(token string, language string) *sharedcatalog.Service {
	return &sharedcatalog.Service{
		AppleToken: token,
		Language:   language,
		HTTPClient: nethttp.Client(),
		UserAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
		OpPrefix:   "utils.artist",
	}
}

func buildArtistSearchItems(related []sharedcatalog.ArtistRelationshipItem, itemType string) []SearchResultItem {
	items := make([]SearchResultItem, 0, len(related))
	for _, item := range related {
		items = append(items, SearchResultItem{
			Type:          itemType,
			Name:          item.Name,
			Detail:        item.ReleaseDate,
			URL:           item.URL,
			ID:            item.ID,
			ContentRating: item.ContentRating,
			Artist:        item.ArtistName,
			Album:         item.AlbumName,
		})
	}
	return items
}

func dedupeSearchItemsByID(items []SearchResultItem) []SearchResultItem {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	filtered := make([]SearchResultItem, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, item)
	}
	return filtered
}

func sortSearchItemsByReleaseDateDesc(items []SearchResultItem) {
	if len(items) == 0 {
		return
	}
	sort.Slice(items, func(i, j int) bool {
		di, err1 := time.Parse("2006-01-02", items[i].Detail)
		dj, err2 := time.Parse("2006-01-02", items[j].Detail)
		if err1 != nil || err2 != nil {
			return items[i].Name < items[j].Name
		}
		return di.After(dj)
	})
}

func paginateSearchItems(items []SearchResultItem, limit int, pageOffset int) ([]SearchResultItem, bool) {
	if pageOffset < 0 {
		pageOffset = 0
	}
	if limit <= 0 {
		return items, false
	}
	if pageOffset >= len(items) {
		return []SearchResultItem{}, false
	}
	end := pageOffset + limit
	if end > len(items) {
		end = len(items)
	}
	hasNext := end < len(items)
	return items[pageOffset:end], hasNext
}

func fetchArtistRelationship(storefront, artistID, token, relationship, itemType string, limit int, pageOffset int, language string) ([]SearchResultItem, bool, error) {
	service := newArtistCatalogService(token, language)
	related, err := service.FetchArtistRelationshipAll(storefront, artistID, relationship)
	if err != nil {
		return nil, false, err
	}
	items := buildArtistSearchItems(related, itemType)
	if relationship == "songs" {
		items = dedupeSearchItemsByID(items)
	}
	sortSearchItemsByReleaseDateDesc(items)
	paged, hasNext := paginateSearchItems(items, limit, pageOffset)
	return paged, hasNext, nil
}

func FetchArtistAlbums(storefront, artistID, token string, limit int, pageOffset int, language string) ([]SearchResultItem, bool, error) {
	return fetchArtistRelationship(storefront, artistID, token, "albums", "Album", limit, pageOffset, language)
}

func FetchArtistMusicVideos(storefront, artistID, token string, limit int, pageOffset int, language string) ([]SearchResultItem, bool, error) {
	return fetchArtistRelationship(storefront, artistID, token, "music-videos", "Music Video", limit, pageOffset, language)
}

func FetchArtistSongs(storefront, artistID, token string, limit int, pageOffset int, language string) ([]SearchResultItem, bool, error) {
	return fetchArtistRelationship(storefront, artistID, token, "songs", "Song", limit, pageOffset, language)
}

func FetchArtistLPAlbums(storefront, artistID, token string, limit int, pageOffset int, language string) ([]SearchResultItem, bool, error) {
	service := newArtistCatalogService(token, language)
	related, err := service.FetchArtistViewAll(storefront, artistID, "full-albums")
	if err != nil {
		return nil, false, err
	}
	items := dedupeSearchItemsByID(buildArtistSearchItems(related, "Album"))
	paged, hasNext := paginateSearchItems(items, limit, pageOffset)
	return paged, hasNext, nil
}
