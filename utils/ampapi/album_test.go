package ampapi

import (
	"errors"
	"testing"

	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

func TestValidateAlbumResponseEmptyData(t *testing.T) {
	_, err := validateAlbumResponse("ampapi.test", &AlbumResp{})
	if err == nil {
		t.Fatalf("expected error for empty album data")
	}
	var accessErr *safe.AccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected safe.AccessError, got %T", err)
	}
}
