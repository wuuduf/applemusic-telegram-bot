package ampapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetSongRespWithContextRetriesOnServerError(t *testing.T) {
	resetAMPAPITestState(t)

	var mu sync.Mutex
	attempts := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer apiServer.Close()

	ampAPIBaseURL = apiServer.URL
	retrySleep = func(ctx context.Context, d time.Duration) error { return nil }

	resp, err := GetSongRespWithContext(context.Background(), "us", "123", "en-US", "test-token")
	if err != nil {
		t.Fatalf("GetSongRespWithContext returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestGetSongRespWithContextRefreshesTokenOnUnauthorized(t *testing.T) {
	resetAMPAPITestState(t)

	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<html><script src="/assets/index~abc.js"></script></html>`))
		case "/assets/index~abc.js":
			_, _ = w.Write([]byte(`window.__TOKEN__="eyJh.fresh-token";`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer webServer.Close()

	var mu sync.Mutex
	var authHeaders []string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer eyJh.fresh-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"stale token"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer apiServer.Close()

	appleMusicWebBaseURL = webServer.URL
	ampAPIBaseURL = apiServer.URL
	retrySleep = func(ctx context.Context, d time.Duration) error { return nil }

	resp, err := GetSongRespWithContext(context.Background(), "us", "123", "en-US", "stale-token")
	if err != nil {
		t.Fatalf("GetSongRespWithContext returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	if len(authHeaders) != 2 {
		t.Fatalf("expected 2 api attempts, got %d", len(authHeaders))
	}
	if authHeaders[0] != "Bearer stale-token" {
		t.Fatalf("unexpected first authorization header: %q", authHeaders[0])
	}
	if authHeaders[1] != "Bearer eyJh.fresh-token" {
		t.Fatalf("unexpected refreshed authorization header: %q", authHeaders[1])
	}
}

func TestGetSongRespWithContextHonorsRetryAfter(t *testing.T) {
	resetAMPAPITestState(t)

	attempts := 0
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer apiServer.Close()

	ampAPIBaseURL = apiServer.URL
	var gotDelay time.Duration
	retrySleep = func(ctx context.Context, d time.Duration) error {
		gotDelay = d
		return nil
	}

	resp, err := GetSongRespWithContext(context.Background(), "us", "123", "en-US", "test-token")
	if err != nil {
		t.Fatalf("GetSongRespWithContext returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if gotDelay != time.Second {
		t.Fatalf("expected retry-after delay 1s, got %s", gotDelay)
	}
}

func resetAMPAPITestState(t *testing.T) {
	t.Helper()
	ampAPIBaseURL = "https://amp-api.music.apple.com"
	appleMusicWebBaseURL = "https://music.apple.com"
	retrySleep = sleepWithContext
	nowFunc = time.Now
	appleMusicTokenCache.mu.Lock()
	appleMusicTokenCache.token = ""
	appleMusicTokenCache.fetchedAt = time.Time{}
	appleMusicTokenCache.inflight = nil
	appleMusicTokenCache.mu.Unlock()
	if t != nil {
		t.Cleanup(func() {
			ampAPIBaseURL = "https://amp-api.music.apple.com"
			appleMusicWebBaseURL = "https://music.apple.com"
			retrySleep = sleepWithContext
			nowFunc = time.Now
			appleMusicTokenCache.mu.Lock()
			appleMusicTokenCache.token = ""
			appleMusicTokenCache.fetchedAt = time.Time{}
			appleMusicTokenCache.inflight = nil
			appleMusicTokenCache.mu.Unlock()
		})
	}
}

func TestAPIErrorIncludesRetryAfter(t *testing.T) {
	err := (&APIError{Status: "429 Too Many Requests", RetryAfter: 2 * time.Second, Body: "busy"}).Error()
	want := `apple music api error: status=429 Too Many Requests retry_after=2s body="busy"`
	if err != want {
		t.Fatalf("unexpected APIError string: got %q want %q", err, want)
	}
}

func TestParseRetryAfterHeaderHTTPDate(t *testing.T) {
	future := time.Now().Add(1500 * time.Millisecond).UTC().Format(http.TimeFormat)
	if d := parseRetryAfterHeader(future); d <= 0 {
		t.Fatalf("expected positive duration from retry-after date, got %s", d)
	}
}

func ExampleAPIError() {
	fmt.Println((&APIError{Status: "500 Internal Server Error", Body: "oops"}).Error())
	// Output: apple music api error: status=500 Internal Server Error body="oops"
}

func TestGetTokenWithContextSharesInflightFetch(t *testing.T) {
	resetAMPAPITestState(t)

	var fetchCount int32
	fetchSignal := make(chan struct{})
	fetchHold := make(chan struct{})

	webServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			// Only the leader's first homepage hit should arrive here while the
			// hold channel is open. Followers must join the inflight fetch
			// instead of issuing their own HTTP request.
			if atomic.AddInt32(&fetchCount, 1) == 1 {
				close(fetchSignal)
				<-fetchHold
			}
			_, _ = w.Write([]byte(`<html><script src="/assets/index~abc.js"></script></html>`))
		case "/assets/index~abc.js":
			_, _ = w.Write([]byte(`window.__TOKEN__="eyJh.singleflight-token";`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer webServer.Close()

	appleMusicWebBaseURL = webServer.URL

	const callers = 8
	var (
		wg      sync.WaitGroup
		results = make([]string, callers)
		errs    = make([]error, callers)
	)
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(idx int) {
			defer wg.Done()
			tok, err := getTokenWithContext(context.Background(), false)
			results[idx] = tok
			errs[idx] = err
		}(i)
	}

	// Wait until the leader's request lands at the server, then give the
	// follower goroutines a moment to enter the inflight-join branch.
	select {
	case <-fetchSignal:
	case <-time.After(2 * time.Second):
		t.Fatalf("first token fetch did not arrive at server")
	}
	time.Sleep(100 * time.Millisecond)
	close(fetchHold)

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d returned error: %v", i, err)
		}
		if results[i] != "eyJh.singleflight-token" {
			t.Fatalf("caller %d got token %q, want shared singleflight token", i, results[i])
		}
	}
	if got := atomic.LoadInt32(&fetchCount); got != 1 {
		t.Fatalf("expected exactly 1 homepage fetch under inflight join, got %d", got)
	}
}
