package task

import (
	"context"
	"errors"

	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

type Album struct {
	Context    context.Context
	Storefront string
	ID         string

	SaveDir   string
	SaveName  string
	Codec     string
	CoverPath string

	Language string
	Resp     ampapi.AlbumResp
	Name     string
	Tracks   []Track
}

func NewAlbum(st string, id string) *Album {
	a := new(Album)
	a.Storefront = st
	a.ID = id

	//fmt.Println("Album created")
	return a

}

func (a *Album) GetResp(token, l string) error {
	var err error
	ctx := a.Context
	if ctx == nil {
		ctx = context.Background()
	}
	a.Language = l
	resp, err := ampapi.GetAlbumRespWithContext(ctx, a.Storefront, a.ID, a.Language, token)
	if err != nil {
		return errors.New("error getting album response")
	}
	a.Resp = *resp
	a.Tracks = nil
	albumData, err := safe.FirstRef("task.Album.GetResp", "album.data", a.Resp.Data)
	if err != nil {
		return err
	}
	//简化高频调用名称
	a.Name = albumData.Attributes.Name
	//fmt.Println("Getting album response")
	//从resp中的Tracks数据中提取trackData信息到新的Track结构体中
	tracks := albumData.Relationships.Tracks.Data
	discTotal := 0
	if len(tracks) > 0 {
		discTotal = tracks[len(tracks)-1].Attributes.DiscNumber
	}
	for i, trackData := range tracks {
		len := len(tracks)
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

			Resp:      trackData,
			PreType:   "albums",
			DiscTotal: discTotal,
			PreID:     a.ID,
			AlbumData: buildTrackAlbumData(albumData),
		})
	}
	return nil
}

func (a *Album) GetArtwork() string {
	if len(a.Resp.Data) == 0 {
		return ""
	}
	return a.Resp.Data[0].Attributes.Artwork.URL
}
