package ampapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Synthetic JWT-shaped fixture; never a real credential.
func tokenFixture(header string, expiry time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(header)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, expiry.Unix()))) + "." + enc([]byte("test-signature"))
}

func serveTokenFixture(t *testing.T, script string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<script src="/assets/index~fixture.js"></script>`)
		case "/assets/index~fixture.js":
			fmt.Fprint(w, script)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	appleMusicWebBaseURL = server.URL
}

func TestFetchTokenAcceptsModernJWTHeader(t *testing.T) {
	resetAMPAPITestState(t)
	want := tokenFixture(`{"typ":"JWT","alg":"ES256"}`, time.Now().Add(time.Hour))
	serveTokenFixture(t, fmt.Sprintf(`const token=%q;`, want))
	got, err := fetchTokenWithContext(context.Background())
	if err != nil {
		t.Fatalf("modern JWT rejected: %v", err)
	}
	if got != want {
		t.Fatal("did not extract the complete modern JWT")
	}
}

func TestGetTokenRefreshesExpiredCacheWithinTTL(t *testing.T) {
	resetAMPAPITestState(t)
	now := time.Now()
	nowFunc = func() time.Time { return now }
	fresh := tokenFixture(`{"typ":"JWT","alg":"ES256"}`, now.Add(time.Hour))
	serveTokenFixture(t, fmt.Sprintf(`const token=%q;`, fresh))
	appleMusicTokenCache.token = tokenFixture(`{"alg":"ES256"}`, now.Add(-time.Minute))
	appleMusicTokenCache.fetchedAt = now.Add(-time.Minute)
	got, err := getTokenWithContext(context.Background(), false)
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if got != fresh {
		t.Fatal("reused expired JWT because cache TTL had not elapsed")
	}
}

func TestFetchTokenSkipsExpiredAndMalformedCandidates(t *testing.T) {
	cases := []struct{ name, prefix string }{
		{"expired", tokenFixture(`{"alg":"ES256","typ":"JWT"}`, time.Now().Add(-time.Hour))},
		{"malformed_payload", "eyJhbGciOiJFUzI1NiJ9.bm90LWpzb24.c2ln"},
		{"missing_expiry", "eyJhbGciOiJFUzI1NiJ9.e30.c2ln"},
		{"redacted", "eyJ0eX...redacted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAMPAPITestState(t)
			want := tokenFixture(`{"typ":"JWT","alg":"ES256"}`, time.Now().Add(time.Hour))
			serveTokenFixture(t, fmt.Sprintf(`const old=%q; const fresh=%q;`, tc.prefix, want))
			got, err := fetchTokenWithContext(context.Background())
			if err != nil {
				t.Fatalf("valid fallback rejected: %v", err)
			}
			if got != want {
				t.Fatal("selected an expired or malformed candidate instead of the valid JWT")
			}
		})
	}
}
