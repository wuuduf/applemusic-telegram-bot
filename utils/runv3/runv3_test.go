package runv3

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExtractKidBase64RejectsMalformedKeyURI(t *testing.T) {
	t.Parallel()
	playlist := "#EXTM3U\n" +
		"#EXT-X-VERSION:7\n" +
		"#EXT-X-TARGETDURATION:4\n" +
		"#EXT-X-MAP:URI=\"init.mp4\"\n" +
		"#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://itunes.apple.com/P000000000/s1/e1\"\n" +
		"#EXTINF:4,\n" +
		"seg1.m4s\n" +
		"#EXT-X-ENDLIST\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(playlist))
	}))
	defer srv.Close()

	_, _, _, err := extractKidBase64(srv.URL+"/index.m3u8", false)
	if err == nil {
		t.Fatalf("expected malformed key uri to return error")
	}
}

type failingWriter struct{}

func (f *failingWriter) Write(p []byte) (int, error) {
	return 0, errors.New("forced write failure")
}

func TestFileWriterReturnsWriteError(t *testing.T) {
	t.Parallel()
	segmentsChan := make(chan Segment, 1)
	segmentsChan <- Segment{Index: 0, Data: []byte("segment")}
	close(segmentsChan)

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	fileWriter(&wg, segmentsChan, &failingWriter{}, 1, errCh)
	wg.Wait()

	err := <-errCh
	if err == nil {
		t.Fatalf("expected fileWriter to return write error")
	}
	got := err.Error()
	if got == "" {
		t.Fatalf("expected non-empty error message")
	}
	if want := "写入分段"; got != "" && !containsText(got, want) {
		t.Fatalf("expected error to contain %q, got %q", want, got)
	}
}

func containsText(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func TestDownloadSegmentStopsRetryingWhenCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	limiter := make(chan struct{}, 1)
	limiter <- struct{}{}
	stats := newSegmentStats(1)
	started := time.Now()
	downloadSegment(ctx, server.URL, 0, &wg, make(chan Segment, 1), server.Client(), limiter, stats)
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("canceled segment kept retrying for %s", elapsed)
	}
	if !stats.hasFailure() {
		t.Fatalf("canceled segment must be recorded as failed")
	}
}

func TestFileWriterReportsInOrderProgress(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	ch := make(chan Segment, 3)
	ch <- Segment{Index: 1, Data: []byte("b")}
	ch <- Segment{Index: 0, Data: []byte("a")}
	ch <- Segment{Index: 2, Data: []byte("c")}
	close(ch)
	var out bytes.Buffer
	errCh := make(chan error, 1)
	var progress []int
	fileWriterWithProgress(&wg, ch, &out, 3, errCh, func(next int) { progress = append(progress, next) })
	if err := <-errCh; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.String() != "abc" {
		t.Fatalf("segments written out of order: %q", out.String())
	}
	if len(progress) != 3 || progress[2] != 3 {
		t.Fatalf("unexpected progress reports: %v", progress)
	}
}
