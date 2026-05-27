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
	defer appleMusicTokenCache.mu.Unlock()

	if !force && appleMusicTokenCache.token != "" && nowFunc().Sub(appleMusicTokenCache.fetchedAt) < tokenCacheTTL {
		return appleMusicTokenCache.token, nil
	}

	token, err := fetchTokenWithContext(ctx)
	if err != nil {
		return "", err
	}
	appleMusicTokenCache.token = token
	appleMusicTokenCache.fetchedAt = nowFunc()
	return token, nil
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
