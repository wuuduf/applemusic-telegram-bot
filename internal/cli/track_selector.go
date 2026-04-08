package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/olekukonko/tablewriter"
	"github.com/wuuduf/applemusic-telegram-bot/utils/task"
)

type trackSelectionRow struct {
	number string
	name   string
	rating string
	kind   string
}

func SelectAlbumTracks(album *task.Album) []int {
	if album == nil || len(album.Resp.Data) == 0 {
		return nil
	}
	rows := make([]trackSelectionRow, 0, len(album.Resp.Data[0].Relationships.Tracks.Data))
	for idx, track := range album.Resp.Data[0].Relationships.Tracks.Data {
		rows = append(rows, trackSelectionRow{
			number: fmt.Sprint(idx + 1),
			name:   fmt.Sprintf("%02d. %s", track.Attributes.TrackNumber, track.Attributes.Name),
			rating: track.Attributes.ContentRating,
			kind:   track.Type,
		})
	}
	caption := fmt.Sprintf("Storefront: %s, %d tracks missing", strings.ToUpper(album.Storefront), album.Resp.Data[0].Attributes.TrackCount-len(album.Resp.Data[0].Relationships.Tracks.Data))
	return selectTrackNumbers("Please select from the track options above (multiple options separated by commas, ranges supported, or type 'all' to select all)", caption, rows)
}

func SelectPlaylistTracks(playlist *task.Playlist) []int {
	if playlist == nil || len(playlist.Resp.Data) == 0 {
		return nil
	}
	rows := make([]trackSelectionRow, 0, len(playlist.Resp.Data[0].Relationships.Tracks.Data))
	for idx, track := range playlist.Resp.Data[0].Relationships.Tracks.Data {
		rows = append(rows, trackSelectionRow{
			number: fmt.Sprint(idx + 1),
			name:   fmt.Sprintf("%s - %s", track.Attributes.Name, track.Attributes.ArtistName),
			rating: track.Attributes.ContentRating,
			kind:   track.Type,
		})
	}
	caption := fmt.Sprintf("Playlists: %d tracks", len(playlist.Resp.Data[0].Relationships.Tracks.Data))
	return selectTrackNumbers("Please select from the track options above (multiple options separated by commas, ranges supported, or type 'all' to select all)", caption, rows)
}

func selectTrackNumbers(message string, caption string, rows []trackSelectionRow) []int {
	if len(rows) == 0 {
		return nil
	}
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"", "Track Name", "Rating", "Type"})
	table.SetRowLine(false)
	table.SetCaption(true, caption)
	table.SetHeaderColor(tablewriter.Colors{},
		tablewriter.Colors{tablewriter.FgRedColor, tablewriter.Bold},
		tablewriter.Colors{tablewriter.FgBlackColor, tablewriter.Bold},
		tablewriter.Colors{tablewriter.FgBlackColor, tablewriter.Bold})
	table.SetColumnColor(tablewriter.Colors{tablewriter.FgCyanColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgRedColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgBlackColor})
	for _, row := range rows {
		table.Append([]string{
			row.number,
			row.name,
			normalizeSelectionRating(row.rating),
			normalizeSelectionKind(row.kind),
		})
	}
	table.Render()
	fmt.Println(message)
	color.New(color.FgCyan).Print("select: ")
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println(err)
		return nil
	}
	return parseSelectionInput(strings.TrimSpace(input), len(rows))
}

func parseSelectionInput(input string, total int) []int {
	all := make([]int, 0, total)
	for i := 1; i <= total; i++ {
		all = append(all, i)
	}
	if input == "all" {
		fmt.Println("You have selected all options:")
		return all
	}
	selected := []int{}
	selectedOptions := [][]string{}
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			selectedOptions = append(selectedOptions, strings.Split(part, "-"))
		} else {
			selectedOptions = append(selectedOptions, []string{part})
		}
	}
	for _, opt := range selectedOptions {
		if len(opt) == 1 {
			num, err := strconv.Atoi(strings.TrimSpace(opt[0]))
			if err != nil || num < 1 || num > total {
				continue
			}
			selected = append(selected, num)
			continue
		}
		if len(opt) != 2 {
			continue
		}
		start, err1 := strconv.Atoi(strings.TrimSpace(opt[0]))
		end, err2 := strconv.Atoi(strings.TrimSpace(opt[1]))
		if err1 != nil || err2 != nil || start < 1 || end > total || start > end {
			continue
		}
		for i := start; i <= end; i++ {
			selected = append(selected, i)
		}
	}
	return selected
}

func normalizeSelectionRating(rating string) string {
	switch rating {
	case "explicit":
		return "E"
	case "clean":
		return "C"
	default:
		return "None"
	}
}

func normalizeSelectionKind(kind string) string {
	switch kind {
	case "music-videos":
		return "MV"
	case "songs":
		return "SONG"
	default:
		return kind
	}
}
