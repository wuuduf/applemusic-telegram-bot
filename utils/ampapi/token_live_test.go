package ampapi

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in only: no credentials/configuration needed, no Telegram side effects.
func TestLiveAutomaticTokenRefresh(t *testing.T) {
	if os.Getenv("AMDL_LIVE_TOKEN_TEST") != "1" {
		t.Skip("set AMDL_LIVE_TOKEN_TEST=1 to contact Apple")
	}
	resetAMPAPITestState(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	album, err := GetAlbumRespWithContext(ctx, "tr", "1725071467", "en-US", "intentionally-invalid-token")
	if err != nil {
		t.Fatalf("automatic refresh and catalog request failed: %v", err)
	}
	if len(album.Data) == 0 || album.Data[0].ID != "1725071467" {
		t.Fatal("missing expected album")
	}
	if !appleMusicTokenIsUnexpired(appleMusicTokenCache.token) {
		t.Fatal("refresh did not cache a valid JWT")
	}
	t.Logf("401 -> official homepage/JS auto-refresh -> catalog success; album=%s tracks=%d", album.Data[0].ID, len(album.Data[0].Relationships.Tracks.Data))
}
