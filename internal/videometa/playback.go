package videometa

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

func IsPlaybackRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

// ValidateURL checks the resource URL protocol, not membership in a route set.
func ValidateURL(u *url.URL, maxFileBytes int64) (Metadata, error) {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || !ValidHost(u.Host) {
		return Metadata{}, errors.New("unsupported video URL")
	}
	return Parse(u, maxFileBytes)
}

// PlaybackLocation parses one song API response. Response.Location resolves
// relative references against the actual request according to net/http. Callers
// own the request, body close, route selection, retries and observations.
func PlaybackLocation(resp *http.Response, maxFileBytes int64) (*url.URL, Metadata, error) {
	if !IsPlaybackRedirect(resp.StatusCode) {
		return nil, Metadata{}, fmt.Errorf("歌曲地址接口返回 %d", resp.StatusCode)
	}
	u, err := resp.Location()
	if err != nil {
		return nil, Metadata{}, err
	}
	meta, err := ValidateURL(u, maxFileBytes)
	return u, meta, err
}
