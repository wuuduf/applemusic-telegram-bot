package ampapi

import (
	"errors"
	"io"
	"net/http"
	"regexp"

	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
)

func GetToken() (string, error) {
	req, err := http.NewRequest("GET", "https://music.apple.com", nil)
	if err != nil {
		return "", err
	}

	resp, err := nethttp.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	regex := regexp.MustCompile(`/assets/index~[^/]+\.js`)
	indexJsUri := regex.FindString(string(body))
	if indexJsUri == "" {
		return "", errors.New("failed to locate apple music index js")
	}

	req, err = http.NewRequest("GET", "https://music.apple.com"+indexJsUri, nil)
	if err != nil {
		return "", err
	}

	resp, err = nethttp.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	regex = regexp.MustCompile(`eyJh([^"]*)`)
	token := regex.FindString(string(body))
	if token == "" {
		return "", errors.New("failed to extract bearer token from apple music page")
	}

	return token, nil
}
