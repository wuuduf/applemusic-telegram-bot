package nethttp

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	defaultTimeoutSec = 45
	minTimeoutSec     = 5
)

var (
	once   sync.Once
	client *http.Client
)

func timeoutFromEnv() time.Duration {
	raw := os.Getenv("AMDL_HTTP_TIMEOUT_SEC")
	if raw == "" {
		return defaultTimeoutSec * time.Second
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < minTimeoutSec {
		return defaultTimeoutSec * time.Second
	}
	return time.Duration(v) * time.Second
}

func Client() *http.Client {
	once.Do(func() {
		client = &http.Client{
			Timeout: timeoutFromEnv(),
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   20,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}
	})
	return client
}

func Do(req *http.Request) (*http.Response, error) {
	return Client().Do(req)
}

func Get(rawurl string) (*http.Response, error) {
	return Client().Get(rawurl)
}

func GetWithContext(ctx context.Context, rawurl string) (*http.Response, error) {
	if ctx == nil {
		return Get(rawurl)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, err
	}
	return Do(req)
}
