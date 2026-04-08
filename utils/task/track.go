package task

import (
	"context"
	"github.com/wuuduf/applemusic-telegram-bot/utils/ampapi"
	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

type Track struct {
	Context    context.Context
	ID         string
	Type       string
	Name       string
	Storefront string
	Language   string

	SaveDir    string
	SaveName   string
	SavePath   string
	Codec      string
	TaskNum    int
	TaskTotal  int
	M3u8       string
	WebM3u8    string
	DeviceM3u8 string
	Quality    string
	CoverPath  string

	Resp         ampapi.TrackRespData
	PreType      string // 上级类型 专辑或者歌单
	PreID        string // 上级ID
	DiscTotal    int
	AlbumData    TrackAlbumData
	PlaylistData TrackPlaylistData
}

func (t *Track) GetAlbumData(token string) error {
	var err error
	ctx := t.Context
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := ampapi.GetAlbumRespByHrefWithContext(ctx, t.Resp.Href, t.Language, token)
	if err != nil {
		return err
	}
	albumData, err := safe.FirstRef("task.Track.GetAlbumData", "album.data", resp.Data)
	if err != nil {
		return err
	}
	t.AlbumData = buildTrackAlbumData(albumData)
	//尝试获取该track所在album的disk总数
	len := len(albumData.Relationships.Tracks.Data)
	if len > 0 {
		t.DiscTotal = albumData.Relationships.Tracks.Data[len-1].Attributes.DiscNumber
	}

	return nil
}
