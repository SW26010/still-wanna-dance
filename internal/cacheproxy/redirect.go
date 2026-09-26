package cacheproxy

import "net/http"

// IsSongRedirect reports whether a song API response supplies a video redirect.
func IsSongRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}
