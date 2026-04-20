package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type CuratorAlbumItem struct {
	ID            string
	Name          string
	URL           string
	ReleaseDate   string
	ContentRating string
	ArtistName    string
}

type curatorCollectionPage struct {
	Next string                  `json:"next"`
	Data []curatorCollectionItem `json:"data"`
}

type curatorCollectionItem struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Href       string `json:"href"`
	Attributes struct {
		Name                 string   `json:"name"`
		ShortName            string   `json:"shortName"`
		Title                string   `json:"title"`
		URL                  string   `json:"url"`
		CollectionID         string   `json:"collectionId"`
		ResourceTypes        []string `json:"resourceTypes"`
		ArtistName           string   `json:"artistName"`
		ReleaseDate          string   `json:"releaseDate"`
		ContentRating        string   `json:"contentRating"`
		EditorialElementKind string   `json:"editorialElementKind"`
	} `json:"attributes"`
	Relationships struct {
		Grouping struct {
			Href string                  `json:"href"`
			Data []curatorCollectionItem `json:"data"`
		} `json:"grouping"`
		Tabs struct {
			Href string                  `json:"href"`
			Data []curatorCollectionItem `json:"data"`
		} `json:"tabs"`
		Children struct {
			Href string                  `json:"href"`
			Data []curatorCollectionItem `json:"data"`
		} `json:"children"`
		Contents struct {
			Href string                  `json:"href"`
			Data []curatorCollectionItem `json:"data"`
		} `json:"contents"`
		Room struct {
			Href string                  `json:"href"`
			Data []curatorCollectionItem `json:"data"`
		} `json:"room"`
	} `json:"relationships"`
}

func curatorCatalogURL(storefront string) string {
	return fmt.Sprintf("https://amp-api.music.apple.com/v1/catalog/%s", storefront)
}

func curatorRoomContentsURL(storefront string, roomID string) string {
	return fmt.Sprintf("https://amp-api.music.apple.com/v1/editorial/%s/rooms/%s/contents", storefront, roomID)
}

func curatorAlbumItemFromResource(item curatorCollectionItem) CuratorAlbumItem {
	return CuratorAlbumItem{
		ID:            strings.TrimSpace(item.ID),
		Name:          strings.TrimSpace(item.Attributes.Name),
		URL:           strings.TrimSpace(item.Attributes.URL),
		ReleaseDate:   strings.TrimSpace(item.Attributes.ReleaseDate),
		ContentRating: strings.TrimSpace(item.Attributes.ContentRating),
		ArtistName:    strings.TrimSpace(item.Attributes.ArtistName),
	}
}

func curatorItemName(item curatorCollectionItem) string {
	for _, value := range []string{item.Attributes.Name, item.Attributes.ShortName, item.Attributes.Title} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func curatorItemHasAlbumContents(item curatorCollectionItem) bool {
	for _, content := range item.Relationships.Contents.Data {
		if strings.EqualFold(strings.TrimSpace(content.Type), "albums") {
			return true
		}
	}
	return false
}

func curatorItemHasAlbumResourceType(item curatorCollectionItem) bool {
	for _, value := range item.Attributes.ResourceTypes {
		if strings.EqualFold(strings.TrimSpace(value), "albums") {
			return true
		}
	}
	return false
}

func collectCuratorInlineAlbumsAndRooms(items []curatorCollectionItem, seenAlbums map[string]struct{}, albums *[]CuratorAlbumItem, seenRooms map[string]struct{}, roomIDs *[]string) {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Type), "albums") {
			album := curatorAlbumItemFromResource(item)
			if album.ID != "" {
				if _, ok := seenAlbums[album.ID]; !ok {
					seenAlbums[album.ID] = struct{}{}
					*albums = append(*albums, album)
				}
			}
		}

		if len(item.Relationships.Room.Data) > 0 && (curatorItemHasAlbumResourceType(item) || curatorItemHasAlbumContents(item)) {
			for _, room := range item.Relationships.Room.Data {
				if !strings.EqualFold(strings.TrimSpace(room.Type), "rooms") {
					continue
				}
				roomID := strings.TrimSpace(room.ID)
				if roomID == "" {
					continue
				}
				if _, ok := seenRooms[roomID]; ok {
					continue
				}
				seenRooms[roomID] = struct{}{}
				*roomIDs = append(*roomIDs, roomID)
			}
		}

		collectCuratorInlineAlbumsAndRooms(item.Relationships.Grouping.Data, seenAlbums, albums, seenRooms, roomIDs)
		collectCuratorInlineAlbumsAndRooms(item.Relationships.Tabs.Data, seenAlbums, albums, seenRooms, roomIDs)
		collectCuratorInlineAlbumsAndRooms(item.Relationships.Children.Data, seenAlbums, albums, seenRooms, roomIDs)
		collectCuratorInlineAlbumsAndRooms(item.Relationships.Contents.Data, seenAlbums, albums, seenRooms, roomIDs)
	}
}

func (s *Service) fetchCuratorPage(storefront string, curatorID string) (*curatorCollectionPage, error) {
	storefront = strings.TrimSpace(storefront)
	curatorID = strings.TrimSpace(curatorID)
	if storefront == "" || curatorID == "" {
		return nil, fmt.Errorf("invalid curator query")
	}

	pageURL := curatorCatalogURL(storefront)
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.AppleToken))
	req.Header.Set("User-Agent", s.userAgent())
	req.Header.Set("Origin", "https://music.apple.com")

	query := req.URL.Query()
	query.Set("ids[curators]", curatorID)
	query.Set("ids[apple-curators]", curatorID)
	query.Set("art[url]", "f")
	query.Set("include", "grouping,playlists")
	query.Set("extend[apple-curators]", "playlistCount")
	query.Set("extend[curators]", "playlistCount")
	query.Set("fields[albums]", "artistName,artistUrl,artwork,contentRating,editorialArtwork,name,playParams,releaseDate,url")
	if strings.TrimSpace(s.Language) != "" {
		query.Set("l", s.Language)
	}
	req.URL.RawQuery = query.Encode()

	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("curator request failed: %s", resp.Status)
	}

	page := new(curatorCollectionPage)
	if err := json.NewDecoder(resp.Body).Decode(page); err != nil {
		return nil, err
	}
	return page, nil
}

func (s *Service) FetchCuratorName(storefront string, curatorID string) (string, error) {
	page, err := s.fetchCuratorPage(storefront, curatorID)
	if err != nil {
		return "", err
	}
	for _, item := range page.Data {
		if strings.TrimSpace(item.ID) == strings.TrimSpace(curatorID) {
			if name := curatorItemName(item); name != "" {
				return name, nil
			}
		}
	}
	for _, item := range page.Data {
		if name := curatorItemName(item); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("curator not found")
}

func (s *Service) fetchCuratorRoomAlbumsAll(storefront string, roomID string) ([]CuratorAlbumItem, error) {
	storefront = strings.TrimSpace(storefront)
	roomID = strings.TrimSpace(roomID)
	if storefront == "" || roomID == "" {
		return nil, fmt.Errorf("invalid curator room query")
	}

	pageURL := curatorRoomContentsURL(storefront, roomID)
	pageLimit := 100
	items := make([]CuratorAlbumItem, 0)
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
		query.Set("fields[albums]", "artistName,artistUrl,artwork,contentRating,editorialArtwork,name,playParams,releaseDate,url")
		query.Set("art[url]", "f")
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
			return nil, fmt.Errorf("curator room request failed: %s", resp.Status)
		}

		page := new(curatorCollectionPage)
		if err := json.NewDecoder(resp.Body).Decode(page); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, item := range page.Data {
			if !strings.EqualFold(strings.TrimSpace(item.Type), "albums") {
				continue
			}
			album := curatorAlbumItemFromResource(item)
			if album.ID == "" {
				continue
			}
			items = append(items, album)
		}

		if strings.TrimSpace(page.Next) == "" {
			break
		}
		pageURL = artistCollectionNextURL(pageURL, page.Next)
	}

	return items, nil
}

func (s *Service) FetchCuratorAlbums(storefront string, curatorID string) ([]CuratorAlbumItem, error) {
	page, err := s.fetchCuratorPage(storefront, curatorID)
	if err != nil {
		return nil, err
	}

	seenAlbums := make(map[string]struct{})
	seenRooms := make(map[string]struct{})
	albums := make([]CuratorAlbumItem, 0)
	roomIDs := make([]string, 0)
	collectCuratorInlineAlbumsAndRooms(page.Data, seenAlbums, &albums, seenRooms, &roomIDs)

	for _, roomID := range roomIDs {
		roomAlbums, err := s.fetchCuratorRoomAlbumsAll(storefront, roomID)
		if err != nil {
			return nil, err
		}
		for _, album := range roomAlbums {
			if album.ID == "" {
				continue
			}
			if _, ok := seenAlbums[album.ID]; ok {
				continue
			}
			seenAlbums[album.ID] = struct{}{}
			albums = append(albums, album)
		}
	}

	return albums, nil
}
