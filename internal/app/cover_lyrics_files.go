package app

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func writeCover(sanAlbumFolder, name string, url string) (string, error) {
	return writeCoverWithConfig(sanAlbumFolder, name, url, &Config)
}

func writeCoverWithConfig(sanAlbumFolder, name string, url string, cfg *structs.ConfigSet) (string, error) {
	if cfg == nil {
		cfg = &Config
	}
	originalUrl := url
	var ext string
	var covPath string
	if cfg.CoverFormat == "original" {
		ext = strings.Split(url, "/")[len(strings.Split(url, "/"))-2]
		ext = ext[strings.LastIndex(ext, ".")+1:]
		covPath = filepath.Join(sanAlbumFolder, name+"."+ext)
	} else {
		covPath = filepath.Join(sanAlbumFolder, name+"."+cfg.CoverFormat)
	}
	if cfg.CoverFormat == "png" {
		re := regexp.MustCompile(`\{w\}x\{h\}`)
		parts := re.Split(url, 2)
		url = parts[0] + "{w}x{h}" + strings.Replace(parts[1], ".jpg", ".png", 1)
	}
	url = strings.Replace(url, "{w}x{h}", cfg.CoverSize, 1)
	if cfg.CoverFormat == "original" {
		url = strings.Replace(url, "is1-ssl.mzstatic.com/image/thumb", "a5.mzstatic.com/us/r1000/0", 1)
		url = url[:strings.LastIndex(url, "/")]
	}
	data, err := fetchCoverData(url)
	if err != nil {
		if cfg.CoverFormat == "original" {
			fmt.Println("Failed to get cover, falling back to " + ext + " url.")
			splitByDot := strings.Split(originalUrl, ".")
			last := splitByDot[len(splitByDot)-1]
			fallback := originalUrl[:len(originalUrl)-len(last)] + ext
			fallback = strings.Replace(fallback, "{w}x{h}", cfg.CoverSize, 1)
			fmt.Println("Fallback URL:", fallback)
			data, err = fetchCoverData(fallback)
			if err != nil {
				fmt.Println("Failed to get cover from fallback url.")
				return "", err
			}
		} else {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(covPath), 0755); err != nil {
		return "", err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(covPath), filepath.Base(covPath)+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := replaceFileAtomically(tmpPath, covPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return covPath, nil
}

func fetchCoverData(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	resp, err := networkHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func replaceFileAtomically(tmpPath string, targetPath string) error {
	exists, err := fileExists(targetPath)
	if err != nil {
		return err
	}
	backupPath := ""
	if exists {
		backupPath = fmt.Sprintf("%s.bak-%d", targetPath, time.Now().UnixNano())
		if err := os.Rename(targetPath, backupPath); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		if backupPath != "" {
			_ = os.Rename(backupPath, targetPath)
		}
		return err
	}
	if backupPath != "" {
		_ = os.Remove(backupPath)
	}
	return nil
}

func writeLyrics(sanAlbumFolder, filename string, lrc string) error {
	lyricspath := filepath.Join(sanAlbumFolder, filename)
	f, err := os.Create(lyricspath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(lrc)
	if err != nil {
		return err
	}
	return nil
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}
