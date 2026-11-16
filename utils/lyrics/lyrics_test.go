package lyrics

import (
	"errors"
	"testing"

	"github.com/wuuduf/applemusic-telegram-bot/utils/safe"
)

func TestExtractLyricsPayloadEmptyData(t *testing.T) {
	_, err := extractLyricsPayload(&SongLyrics{})
	if err == nil {
		t.Fatalf("expected error for empty lyrics data")
	}
	var accessErr *safe.AccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected safe.AccessError, got %T", err)
	}
}

func TestTtmlToLrcMissingBodyReturnsAccessError(t *testing.T) {
	_, err := TtmlToLrc(`<tt xmlns:itunes="http://itunes.apple.com/ns/1.0"></tt>`)
	if err == nil {
		t.Fatalf("expected error for missing body")
	}
	var accessErr *safe.AccessError
	if !errors.As(err, &accessErr) {
		t.Fatalf("expected safe.AccessError, got %T", err)
	}
}

func TestTtmlToLrcMissingLineKeyDoesNotPanic(t *testing.T) {
	ttml := `
<tt xmlns:itunes="http://itunes.apple.com/ns/1.0">
  <head>
    <metadata>
      <iTunesMetadata>
        <translations>
          <translation>
            <text for="line-1" text="Hello"></text>
          </translation>
        </translations>
        <transliterations>
          <transliteration>
            <text for="line-1" text="Ni Hao"></text>
          </transliteration>
        </transliterations>
      </iTunesMetadata>
    </metadata>
  </head>
  <body>
    <div>
      <p begin="00:01.00">你好</p>
    </div>
  </body>
</tt>`
	lrc, err := TtmlToLrc(ttml)
	if err != nil {
		t.Fatalf("expected valid fallback LRC, got %v", err)
	}
	if lrc != "[00:01.00]你好" {
		t.Fatalf("unexpected LRC output: %q", lrc)
	}
}

func TestTtmlToLrcWordTimingMissingLineKeyDoesNotPanic(t *testing.T) {
	ttml := `
<tt xmlns:itunes="http://itunes.apple.com/ns/1.0" itunes:timing="Word">
  <head>
    <metadata>
      <iTunesMetadata>
        <translations>
          <translation>
            <text for="line-1" text="Hello"></text>
          </translation>
        </translations>
      </iTunesMetadata>
    </metadata>
  </head>
  <body>
    <div>
      <p>
        <span begin="00:01.00" end="00:01.50" text="你"></span>
        <span begin="00:01.50" end="00:02.00" text="好"></span>
      </p>
    </div>
  </body>
</tt>`
	lrc, err := TtmlToLrc(ttml)
	if err != nil {
		t.Fatalf("expected valid syllable LRC, got %v", err)
	}
	if lrc == "" {
		t.Fatalf("expected non-empty syllable LRC")
	}
}

func TestParseTTMLClockHandlesAnyFractionLength(t *testing.T) {
	cases := []struct {
		in      string
		m, s, c int
	}{
		{"1:02.5", 1, 2, 50},
		{"9.73", 0, 9, 73},
		{"12.345", 0, 12, 34},
		{"12", 0, 12, 0},
		{"3:04", 3, 4, 0},
		{"1:02:03.007", 62, 3, 0},
		{"00:01.50", 0, 1, 50},
	}
	for _, tc := range cases {
		m, s, c, err := parseTTMLClock(tc.in)
		if err != nil || m != tc.m || s != tc.s || c != tc.c {
			t.Errorf("parseTTMLClock(%q) = %d:%d.%d err=%v, want %d:%d.%d", tc.in, m, s, c, err, tc.m, tc.s, tc.c)
		}
	}
	for _, bad := range []string{"", "a:b", "1:2:3:4", "1.x"} {
		if _, _, _, err := parseTTMLClock(bad); err == nil {
			t.Errorf("parseTTMLClock(%q) should fail", bad)
		}
	}
}

func TestTtmlToLrcKeepsShortFractions(t *testing.T) {
	ttml := `<tt xmlns="http://www.w3.org/ns/ttml" xmlns:itunes="http://music.apple.com/lyric-ttml-internal" itunes:timing="Line"><body><div><p begin="1:02.5" end="1:04">a</p><p begin="9.73" end="10">b</p></div></body></tt>`
	got, err := TtmlToLrc(ttml)
	if err != nil {
		t.Fatalf("TtmlToLrc failed: %v", err)
	}
	if want := "[01:02.50]a\n[00:09.73]b"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
