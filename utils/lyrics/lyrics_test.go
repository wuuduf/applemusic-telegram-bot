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
