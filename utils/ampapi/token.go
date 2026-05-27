package ampapi

import (
	"context"
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
	appleMusicTokenRegex   = regexp.MustCompile(`eyJh([^"']*)`)
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

	if !force && appleMusicTokenCache.token != "" && nowFunc().Sub(appleMusicTokenCache.fetchedAt) < tokenCacheTTL {
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

	token := strings.TrimSpace(appleMusicTokenRegex.FindString(string(body)))
	if token == "" {
		return "", errors.New("failed to extract bearer token from apple music page")
	}

	return token, nil
}
