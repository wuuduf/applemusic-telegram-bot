package ampapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
)

const (
	defaultAppleUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"
	maxAPIErrorBodyBytes  = 4096
	tokenCacheTTL         = 6 * time.Hour
	ampAPIMaxAttempts     = 4
	ampAPIRetryBaseDelay  = 250 * time.Millisecond
)

var (
	ampAPIBaseURL        = "https://amp-api.music.apple.com"
	appleMusicWebBaseURL = "https://music.apple.com"
	retrySleep           = sleepWithContext
	nowFunc              = time.Now

	appleMusicTokenCache struct {
		mu        sync.Mutex
		token     string
		fetchedAt time.Time
	}
)

type APIError struct {
	StatusCode int
	Status     string
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("apple music api error: status=%s retry_after=%s body=%q", e.Status, e.RetryAfter, e.Body)
	}
	return fmt.Sprintf("apple music api error: status=%s body=%q", e.Status, e.Body)
}

func doJSONWithRetry(ctx context.Context, providedToken string, out any, reqFactory func(token string) (*http.Request, error)) error {
	if ctx == nil {
		ctx = context.Background()
	}

	token := providedToken
	if token == "" {
		freshToken, err := getTokenWithContext(ctx, false)
		if err != nil {
			return err
		}
		token = freshToken
	}

	refreshedToken := false
	for attempt := 1; attempt <= ampAPIMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		req, err := reqFactory(token)
		if err != nil {
			return err
		}
		resp, err := nethttp.Do(req)
		if err != nil {
			if !shouldRetryError(ctx, err) || attempt == ampAPIMaxAttempts {
				return err
			}
			if err := retrySleep(ctx, retryDelay(attempt, 0)); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return err
			}
			return nil
		}

		apiErr, err := newAPIError(resp)
		if err != nil {
			return err
		}

		if apiErr.StatusCode == http.StatusUnauthorized && !refreshedToken {
			freshToken, refreshErr := getTokenWithContext(ctx, true)
			if refreshErr != nil {
				return refreshErr
			}
			refreshedToken = true
			token = freshToken
			continue
		}

		if shouldRetryStatus(apiErr.StatusCode) && attempt < ampAPIMaxAttempts {
			if err := retrySleep(ctx, retryDelay(attempt, apiErr.RetryAfter)); err != nil {
				return err
			}
			continue
		}

		return apiErr
	}

	return fmt.Errorf("apple music api request exhausted retries")
}

func newAPIError(resp *http.Response) (*APIError, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIErrorBodyBytes))
	if err != nil {
		return nil, err
	}
	return &APIError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(body)),
		RetryAfter: parseRetryAfterHeader(resp.Header.Get("Retry-After")),
	}, nil
}

func setAppleMusicHeaders(req *http.Request, token, mediaUserToken string) {
	if token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}
	req.Header.Set("User-Agent", defaultAppleUserAgent)
	req.Header.Set("Origin", appleMusicWebBaseURL)
	if mediaUserToken != "" {
		req.Header.Set("Media-User-Token", mediaUserToken)
	}
}

func apiURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return strings.TrimRight(ampAPIBaseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func webURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if path == "" {
		return appleMusicWebBaseURL
	}
	return strings.TrimRight(appleMusicWebBaseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(1<<(attempt-1)) * ampAPIRetryBaseDelay
}

func parseRetryAfterHeader(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if ts, err := http.ParseTime(raw); err == nil {
		d := time.Until(ts)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

func shouldRetryStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code <= 599)
}

func shouldRetryError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if ne, ok := err.(net.Error); ok {
		return ne.Timeout() || ne.Temporary()
	}
	return true
}

func appendPagedTrackResults(ctx context.Context, providedToken, next string, dst *[]TrackRespData) error {
	for next != "" {
		page := new(TrackResp)
		err := doJSONWithRetry(ctx, providedToken, page, func(token string) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL(next), nil)
			if err != nil {
				return nil, err
			}
			setAppleMusicHeaders(req, token, "")
			query := req.URL.Query()
			query.Set("omit[resource]", "autos")
			query.Set("include", "artists")
			query.Set("extend", "editorialVideo,extendedAssetUrls")
			req.URL.RawQuery = query.Encode()
			return req, nil
		})
		if err != nil {
			return err
		}
		*dst = append(*dst, page.Data...)
		next = page.Next
	}
	return nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if ctx == nil {
		time.Sleep(d)
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
