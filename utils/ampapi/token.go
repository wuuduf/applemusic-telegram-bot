package ampapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
)

var (
	appleMusicIndexJSRegex = regexp.MustCompile(`/assets/index~[^/]+\.js`)
	appleMusicTokenRegex   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
)

func GetToken() (string, error) {
	return getTokenWithContext(context.Background(), false)
}

func getTokenWithContext(ctx context.Context, force bool) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	appleMusicTokenCache.mu.Lock()

	// If a fetch is already in progress, join it. This is the freshest
	// available signal regardless of cache state, so even forced refreshes
	// piggy-back instead of stampeding Apple.
	if inflight := appleMusicTokenCache.inflight; inflight != nil {
		appleMusicTokenCache.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-inflight.done:
			return inflight.token, inflight.err
		}
	}

	if !force && appleMusicTokenIsUnexpired(appleMusicTokenCache.token) && nowFunc().Sub(appleMusicTokenCache.fetchedAt) < tokenCacheTTL {
		token := appleMusicTokenCache.token
		appleMusicTokenCache.mu.Unlock()
		return token, nil
	}

	// We are the leader. Publish an inflight handle so concurrent callers
	// join us instead of issuing parallel fetches, then drop the mutex
	// before doing any network I/O.
	fetch := &appleMusicTokenFetch{done: make(chan struct{})}
	appleMusicTokenCache.inflight = fetch
	appleMusicTokenCache.mu.Unlock()

	token, err := fetchTokenWithContext(ctx)

	appleMusicTokenCache.mu.Lock()
	if err == nil {
		appleMusicTokenCache.token = token
		appleMusicTokenCache.fetchedAt = nowFunc()
	}
	appleMusicTokenCache.inflight = nil
	fetch.token = token
	fetch.err = err
	appleMusicTokenCache.mu.Unlock()
	close(fetch.done)

	return token, err
}

func fetchTokenWithContext(ctx context.Context) (string, error) {
	resp, err := nethttp.GetWithContext(ctx, webURL(""))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("apple music homepage request failed: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	indexJSURI := appleMusicIndexJSRegex.FindString(string(body))
	if indexJSURI == "" {
		return "", errors.New("failed to locate apple music index js")
	}

	resp, err = nethttp.GetWithContext(ctx, webURL(indexJSURI))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("apple music index js request failed: %s", resp.Status)
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// JWT headers may start with alg or typ. Validate the complete payload
	// rather than relying on one encoded header prefix or accepting redactions.
	for _, token := range appleMusicTokenRegex.FindAllString(string(body), -1) {
		if appleMusicTokenIsUnexpired(token) {
			return token, nil
		}
	}
	return "", errors.New("failed to extract an unexpired bearer token from apple music page")
}

// This is a freshness check, not signature verification: tokens are obtained
// from Apple's HTTPS origin and Apple validates the signature on API requests.
func appleMusicTokenIsUnexpired(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	return claims.ExpiresAt > nowFunc().Unix()
}
