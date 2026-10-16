package cacheproxy

import "still-wanna-dance/internal/videometa"

// IsSongRedirect reports whether a song API response supplies a video redirect.
func IsSongRedirect(status int) bool {
	return videometa.IsPlaybackRedirect(status)
}
