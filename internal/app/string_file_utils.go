package app

import (
	"os"
	"strings"
	"unicode/utf8"

	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func LimitString(s string) string {
	return LimitStringWithConfig(&Config, s)
}

func LimitStringWithConfig(cfg *structs.ConfigSet, s string) string {
	if cfg == nil {
		return s
	}
	if cfg.LimitMax > 0 && len([]rune(s)) > cfg.LimitMax {
		s = string([]rune(s)[:cfg.LimitMax])
	}
	return truncateUTF8ByBytes(s, maxTemplateValueBytes)
}

func truncateUTF8ByBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	var b strings.Builder
	b.Grow(maxBytes)
	used := 0
	for _, r := range s {
		size := utf8.RuneLen(r)
		if size <= 0 {
			size = 1
		}
		if used+size > maxBytes {
			break
		}
		b.WriteRune(r)
		used += size
	}
	return b.String()
}

func isInArray(arr []int, target int) bool {
	for _, num := range arr {
		if num == target {
			return true
		}
	}
	return false
}

func fileExists(path string) (bool, error) {
	f, err := os.Stat(path)
	if err == nil {
		return !f.IsDir(), nil
	} else if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func fileExistsNonEmpty(path string) (bool, error) {
	f, err := os.Stat(path)
	if err == nil {
		return !f.IsDir() && f.Size() > 0, nil
	} else if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
