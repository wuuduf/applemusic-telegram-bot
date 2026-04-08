package task

import (
	"context"
	"errors"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

type Playlist struct {
	Context    context.Context
	Storefront string
	ID         string

	SaveDir   string
	SaveName  string
	Codec     string
	CoverPath string

	Language string
	Resp     ampapi.PlaylistResp
	Name     string
	Tracks   []Track
}

func NewPlaylist(st string, id string) *Playlist {
	a := new(Playlist)
	a.Storefront = st
	a.ID = id

	//fmt.Println("Album created")
	return a

}

func (a *Playlist) GetResp(token, l string) error {
	var err error
	ctx := a.Context
	if ctx == nil {
		ctx = context.Background()
	}
	a.Language = l
	resp, err := ampapi.GetPlaylistRespWithContext(ctx, a.Storefront, a.ID, a.Language, token)
	if err != nil {
		return errors.New("error getting album response")
	}
	a.Resp = *resp
	a.Tracks = nil
	playlistData, err := safe.FirstRef("task.Playlist.GetResp", "playlist.data", a.Resp.Data)
	if err != nil {
		return err
	}

	playlistData.Attributes.ArtistName = "Apple Music"
	//简化高频调用名称
	a.Name = playlistData.Attributes.Name
	//fmt.Println("Getting album response")
	//从resp中的Tracks数据中提取trackData信息到新的Track结构体中
	for i, trackData := range playlistData.Relationships.Tracks.Data {
		len := len(playlistData.Relationships.Tracks.Data)
		a.Tracks = append(a.Tracks, Track{
			Context:    ctx,
			ID:         trackData.ID,
			Type:       trackData.Type,
			Name:       trackData.Attributes.Name,
			Language:   a.Language,
			Storefront: a.Storefront,

			//SaveDir:   filepath.Join(a.SaveDir, a.SaveName),
			//Codec:     a.Codec,
			TaskNum:   i + 1,
			TaskTotal: len,
			M3u8:      trackData.Attributes.ExtendedAssetUrls.EnhancedHls,
			WebM3u8:   trackData.Attributes.ExtendedAssetUrls.EnhancedHls,
			//CoverPath: a.CoverPath,

			Resp:    trackData,
			PreType: "playlists",
			//DiscTotal: a.Resp.Data[0].Relationships.Tracks.Data[len-1].Attributes.DiscNumber, 在它处获取
			PreID:        a.ID,
			PlaylistData: buildTrackPlaylistData(playlistData),
		})
	}
	return nil
}

func (a *Playlist) GetArtwork() string {
	if len(a.Resp.Data) == 0 {
		return ""
	}
	return a.Resp.Data[0].Attributes.Artwork.URL
}
