package utils

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"

	"github.com/AlecAivazis/survey/v2"
)

// SearchResultItem is a unified struct to hold search results for display.
type SearchResultItem struct {
	Type          string
	Name          string
	Detail        string
	URL           string
	ID            string
	ContentRating string
	Artist        string
	Album         string
}

// SearchSelection represents the chosen search result and user preferences.
type SearchSelection struct {
	URL     string
	IsSong  bool
	Quality string
}

// QualityOption holds information about a downloadable quality.
type QualityOption struct {
	ID          string
	Description string
}

func contentRatingBadge(rating string) string {
	switch strings.ToLower(rating) {
	case "explicit":
		return "E"
	case "clean":
		return "C"
	default:
		return ""
	}
}

func formatSearchItem(item SearchResultItem) string {
	label := item.Name
	if item.Detail != "" {
		label = fmt.Sprintf("%s - %s", item.Name, item.Detail)
	}
	badge := contentRatingBadge(item.ContentRating)
	if badge == "" {
		return label
	}
	return fmt.Sprintf("[%s] %s", badge, label)
}

func formatSearchItemWithParenDetail(item SearchResultItem) string {
	label := item.Name
	if item.Detail != "" {
		label = fmt.Sprintf("%s (%s)", item.Name, item.Detail)
	}
	badge := contentRatingBadge(item.ContentRating)
	if badge == "" {
		return label
	}
	return fmt.Sprintf("[%s] %s", badge, label)
}

func songGroupKey(item SearchResultItem) string {
	name := strings.TrimSpace(item.Name)
	artist := strings.TrimSpace(item.Artist)
	album := strings.TrimSpace(item.Album)
	if name == "" && artist == "" && album == "" {
		if item.ID != "" {
			return "id:" + item.ID
		}
		if item.URL != "" {
			return "url:" + item.URL
		}
		return ""
	}
	return strings.ToLower(name + "|" + artist + "|" + album)
}

func ratingRank(rating string) int {
	switch strings.ToLower(rating) {
	case "explicit":
		return 0
	case "clean":
		return 1
	default:
		return 2
	}
}

func sortSongVariants(items []SearchResultItem) {
	if len(items) < 2 {
		return
	}
	groups := make([][]SearchResultItem, 0, len(items))
	index := make(map[string]int)
	for _, item := range items {
		key := songGroupKey(item)
		if key == "" {
			groups = append(groups, []SearchResultItem{item})
			continue
		}
		if idx, ok := index[key]; ok {
			groups[idx] = append(groups[idx], item)
		} else {
			index[key] = len(groups)
			groups = append(groups, []SearchResultItem{item})
		}
	}

	pos := 0
	for _, group := range groups {
		if len(group) > 1 {
			sort.SliceStable(group, func(i, j int) bool {
				return ratingRank(group[i].ContentRating) < ratingRank(group[j].ContentRating)
			})
		}
		for _, item := range group {
			items[pos] = item
			pos++
		}
	}
}

// promptForQuality asks the user to select a download quality for the chosen media.
func promptForQuality(item SearchResultItem) (string, error) {
	if item.Type == "Artist" {
		fmt.Println("Artist selected. Proceeding to list all albums/videos.")
		return "default", nil
	}

	fmt.Printf("\nFetching available qualities for: %s\n", item.Name)

	qualities := []QualityOption{
		{ID: "alac", Description: "Lossless (ALAC)"},
		{ID: "aac", Description: "High-Quality (AAC)"},
		{ID: "atmos", Description: "Dolby Atmos"},
	}
	qualityOptions := []string{}
	for _, q := range qualities {
		qualityOptions = append(qualityOptions, q.Description)
	}

	prompt := &survey.Select{
		Message:  "Select a quality to download:",
		Options:  qualityOptions,
		PageSize: 5,
	}

	selectedIndex := 0
	err := survey.AskOne(prompt, &selectedIndex)
	if err != nil {
		// This can happen if the user presses Ctrl+C
		return "", nil
	}

	return qualities[selectedIndex].ID, nil
}

// HandleSearch manages the entire interactive search process.
func HandleSearch(searchType string, queryParts []string, token, storefront, language string) (*SearchSelection, error) {
	query := strings.Join(queryParts, " ")
	searchType = strings.ToLower(strings.TrimSpace(searchType))
	validTypes := map[string]bool{"album": true, "song": true, "artist": true}
	if !validTypes[searchType] {
		return nil, fmt.Errorf("invalid search type: %s. Use 'album', 'song', or 'artist'", searchType)
	}

	fmt.Printf("Searching for %ss: \"%s\" in storefront \"%s\"\n", searchType, query, storefront)

	offset := 0
	limit := 15 // Increased limit for better navigation

	apiSearchType := searchType + "s"

	for {
		searchResp, err := ampapi.Search(storefront, query, apiSearchType, language, token, limit, offset)
		if err != nil {
			return nil, fmt.Errorf("error fetching search results: %w", err)
		}

		items := []SearchResultItem{}
		hasNext := false

		// Special options for navigation
		const prevPageOpt = "⬅️  Previous Page"
		const nextPageOpt = "➡️  Next Page"

		switch searchType {
		case "album":
			if searchResp.Results.Albums != nil {
				for _, item := range searchResp.Results.Albums.Data {
					year := ""
					if len(item.Attributes.ReleaseDate) >= 4 {
						year = item.Attributes.ReleaseDate[:4]
					}
					trackInfo := fmt.Sprintf("%d tracks", item.Attributes.TrackCount)
					detail := fmt.Sprintf("%s (%s, %s)", item.Attributes.ArtistName, year, trackInfo)
					items = append(items, SearchResultItem{
						Type:          "Album",
						Name:          item.Attributes.Name,
						Detail:        detail,
						URL:           item.Attributes.URL,
						ID:            item.ID,
						ContentRating: item.Attributes.ContentRating,
						Artist:        item.Attributes.ArtistName,
					})
				}
				hasNext = searchResp.Results.Albums.Next != ""
			}
		case "song":
			if searchResp.Results.Songs != nil {
				for _, item := range searchResp.Results.Songs.Data {
					detail := fmt.Sprintf("%s (%s)", item.Attributes.ArtistName, item.Attributes.AlbumName)
					items = append(items, SearchResultItem{
						Type:          "Song",
						Name:          item.Attributes.Name,
						Detail:        detail,
						URL:           item.Attributes.URL,
						ID:            item.ID,
						ContentRating: item.Attributes.ContentRating,
						Artist:        item.Attributes.ArtistName,
						Album:         item.Attributes.AlbumName,
					})
				}
				sortSongVariants(items)
				hasNext = searchResp.Results.Songs.Next != ""
			}
		case "artist":
			if searchResp.Results.Artists != nil {
				for _, item := range searchResp.Results.Artists.Data {
					detail := ""
					if len(item.Attributes.GenreNames) > 0 {
						detail = strings.Join(item.Attributes.GenreNames, ", ")
					}
					items = append(items, SearchResultItem{
						Type:   "Artist",
						Name:   item.Attributes.Name,
						Detail: detail,
						URL:    item.Attributes.URL,
						ID:     item.ID,
					})
				}
				hasNext = searchResp.Results.Artists.Next != ""
			}
		}

		if len(items) == 0 && offset == 0 {
			fmt.Println("No results found.")
			return nil, nil
		}

		displayOptions := []string{}
		if offset > 0 {
			displayOptions = append(displayOptions, prevPageOpt)
		}
		for _, item := range items {
			displayOptions = append(displayOptions, formatSearchItem(item))
		}
		if hasNext {
			displayOptions = append(displayOptions, nextPageOpt)
		}

		prompt := &survey.Select{
			Message:  "Use arrow keys to navigate, Enter to select:",
			Options:  displayOptions,
			PageSize: limit, // Show a full page of results
		}

		selectedIndex := 0
		err = survey.AskOne(prompt, &selectedIndex)
		if err != nil {
			// User pressed Ctrl+C
			return nil, nil
		}

		selectedOption := displayOptions[selectedIndex]

		// Handle pagination
		if selectedOption == nextPageOpt {
			offset += limit
			continue
		}
		if selectedOption == prevPageOpt {
			offset -= limit
			continue
		}

		// Adjust index to match the `items` slice if "Previous Page" was an option
		itemIndex := selectedIndex
		if offset > 0 {
			itemIndex--
		}

		selectedItem := items[itemIndex]

		quality, err := promptForQuality(selectedItem)
		if err != nil {
			return nil, fmt.Errorf("could not process quality selection: %w", err)
		}
		if quality == "" { // User cancelled quality selection
			fmt.Println("Selection cancelled.")
			return nil, nil
		}

		return &SearchSelection{
			URL:     selectedItem.URL,
			IsSong:  selectedItem.Type == "Song",
			Quality: quality,
		}, nil
	}
}

// BuildSearchItems formats a search response into display-ready items.
func BuildSearchItems(kind string, resp *ampapi.SearchResp) ([]SearchResultItem, bool) {
	items := []SearchResultItem{}
	hasNext := false
	switch kind {
	case "song":
		if resp.Results.Songs == nil {
			return items, false
		}
		for _, item := range resp.Results.Songs.Data {
			detail := fmt.Sprintf("%s / %s", item.Attributes.ArtistName, item.Attributes.AlbumName)
			items = append(items, SearchResultItem{
				Type:          "Song",
				Name:          item.Attributes.Name,
				Detail:        detail,
				URL:           item.Attributes.URL,
				ID:            item.ID,
				ContentRating: item.Attributes.ContentRating,
				Artist:        item.Attributes.ArtistName,
				Album:         item.Attributes.AlbumName,
			})
		}
		sortSongVariants(items)
		hasNext = resp.Results.Songs.Next != ""
	case "album":
		if resp.Results.Albums == nil {
			return items, false
		}
		for _, item := range resp.Results.Albums.Data {
			year := ""
			if len(item.Attributes.ReleaseDate) >= 4 {
				year = item.Attributes.ReleaseDate[:4]
			}
			detail := fmt.Sprintf("%s (%s, %d tracks)", item.Attributes.ArtistName, year, item.Attributes.TrackCount)
			items = append(items, SearchResultItem{
				Type:          "Album",
				Name:          item.Attributes.Name,
				Detail:        detail,
				URL:           item.Attributes.URL,
				ID:            item.ID,
				ContentRating: item.Attributes.ContentRating,
				Artist:        item.Attributes.ArtistName,
			})
		}
		hasNext = resp.Results.Albums.Next != ""
	case "artist":
		if resp.Results.Artists == nil {
			return items, false
		}
		for _, item := range resp.Results.Artists.Data {
			detail := strings.Join(item.Attributes.GenreNames, ", ")
			items = append(items, SearchResultItem{
				Type:   "Artist",
				Name:   item.Attributes.Name,
				Detail: detail,
				URL:    item.Attributes.URL,
				ID:     item.ID,
			})
		}
		hasNext = resp.Results.Artists.Next != ""
	}
	return items, hasNext
}

// FormatSearchResults creates a Telegram-friendly list of results.
func FormatSearchResults(kind, query string, items []SearchResultItem) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Search %s: %s\n", kind, query))
	for i, item := range items {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, formatSearchItem(item)))
	}
	return strings.TrimSpace(b.String())
}

// FormatArtistAlbums formats artist album search results.
func FormatArtistAlbums(artistName string, items []SearchResultItem) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Albums by %s:\n", artistName))
	for i, item := range items {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, formatSearchItemWithParenDetail(item)))
	}
	return strings.TrimSpace(b.String())
}

// FormatArtistMusicVideos formats artist music video search results.
func FormatArtistMusicVideos(artistName string, items []SearchResultItem) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Music Videos by %s:\n", artistName))
	for i, item := range items {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, formatSearchItemWithParenDetail(item)))
	}
	return strings.TrimSpace(b.String())
}
