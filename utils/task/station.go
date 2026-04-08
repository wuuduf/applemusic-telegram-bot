package task

import (
	"context"
	//"bufio"
	"errors"
	"fmt"

	//"os"
	//"strconv"
	//"strings"

	//"github.com/fatih/color"
	//"github.com/olekukonko/tablewriter"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

type Station struct {
	Context    context.Context
	Storefront string
	ID         string

	SaveDir   string
	SaveName  string
	Codec     string
	CoverPath string

	Language string
	Resp     ampapi.StationResp
	Type     string
	Name     string
	Tracks   []Track
}

func (a *Station) appendTrack(ctx context.Context, trackData ampapi.TrackRespData, discTotal int, albumData TrackAlbumData, taskNum int, taskTotal int) {
	a.Tracks = append(a.Tracks, Track{
		Context:    ctx,
		ID:         trackData.ID,
		Type:       trackData.Type,
		Name:       trackData.Attributes.Name,
		Language:   a.Language,
		Storefront: a.Storefront,
		TaskNum:    taskNum,
		TaskTotal:  taskTotal,
		M3u8:       trackData.Attributes.ExtendedAssetUrls.EnhancedHls,
		WebM3u8:    trackData.Attributes.ExtendedAssetUrls.EnhancedHls,
		Resp:       trackData,
		PreType:    "stations",
		DiscTotal:  discTotal,
		PreID:      a.ID,
		AlbumData:  albumData,
	})
	a.Tracks[len(a.Tracks)-1].PlaylistData = TrackPlaylistData{
		Name:       a.Name,
		ArtistName: "Apple Music Station",
	}
}

func NewStation(st string, id string) *Station {
	a := new(Station)
	a.Storefront = st
	a.ID = id
	//fmt.Println("Album created")
	return a

}

func (a *Station) GetResp(mutoken, token, l string) error {
	var err error
	ctx := a.Context
	if ctx == nil {
		ctx = context.Background()
	}
	a.Language = l
	resp, err := ampapi.GetStationRespWithContext(ctx, a.Storefront, a.ID, a.Language, token)
	if err != nil {
		return errors.New("error getting station response")
	}
	a.Resp = *resp
	a.Tracks = nil
	stationData, err := safe.FirstRef("task.Station.GetResp", "station.data", a.Resp.Data)
	if err != nil {
		return err
	}
	//简化高频调用名称
	a.Type = stationData.Attributes.PlayParams.Format
	a.Name = stationData.Attributes.Name
	if a.Type != "tracks" {
		return nil
	}
	tracksResp, err := ampapi.GetStationNextTracksWithContext(ctx, a.ID, mutoken, a.Language, token)
	if err != nil {
		return errors.New("error getting station tracks response")
	}
	//fmt.Println("Getting album response")
	//从resp中的Tracks数据中提取trackData信息到新的Track结构体中
	for i, trackData := range tracksResp.Data {
		albumResp, err := ampapi.GetAlbumRespByHrefWithContext(ctx, trackData.Href, a.Language, token)
		if err != nil {
			fmt.Println("Error getting album response:", err)
			continue
		}
		albumData, dataErr := safe.FirstRef("task.Station.GetResp", "station.track.album.data", albumResp.Data)
		if dataErr != nil {
			fmt.Println("Error parsing album response:", dataErr)
			continue
		}
		albumLen := len(albumData.Relationships.Tracks.Data)
		discTotal := 0
		if albumLen > 0 {
			discTotal = albumData.Relationships.Tracks.Data[albumLen-1].Attributes.DiscNumber
		}
		a.appendTrack(ctx, trackData, discTotal, buildTrackAlbumData(albumData), i+1, len(tracksResp.Data))
	}
	return nil
}

func (a *Station) GetArtwork() string {
	if len(a.Resp.Data) == 0 {
		return ""
	}
	return a.Resp.Data[0].Attributes.Artwork.URL
}
