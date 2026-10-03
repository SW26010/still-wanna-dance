// Package videometa validates the shared video path and query protocol.
// Host, scheme, redirect, and cache policies belong to callers.
package videometa

import (
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var videoPath = regexp.MustCompile(`^/files/[0-9]+/([1-9][0-9]*)-([a-zA-Z0-9]+)\.mp4$`)

type Metadata struct {
	// ResourceID identifies the URL resource; it need not equal the API song ID.
	ResourceID string
	Version    string
	Checksum   string
	Size       int64
}

// Parse does not modify u. maxFileBytes is the caller's inclusive size limit.
// Encoded paths are rejected to match the video request protocol unambiguously.
func Parse(u *url.URL, maxFileBytes int64) (Metadata, error) {
	if u == nil || u.RawPath != "" {
		return Metadata{}, errors.New("invalid video path")
	}
	m := videoPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Metadata{}, errors.New("invalid video path")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Metadata{}, errors.New("invalid query")
	}
	if len(q["e"]) != 1 || len(q["s"]) != 1 {
		return Metadata{}, errors.New("exactly one e and s required")
	}
	checksum := strings.ToLower(q.Get("e"))
	digest, err := hex.DecodeString(checksum)
	if err != nil || len(digest) != 16 {
		return Metadata{}, errors.New("e must be a 32-character MD5")
	}
	size, err := strconv.ParseInt(q.Get("s"), 10, 64)
	if err != nil || size <= 0 || size > maxFileBytes {
		return Metadata{}, errors.New("s exceeds allowed size or is invalid")
	}
	return Metadata{ResourceID: m[1], Version: m[2], Checksum: checksum, Size: size}, nil
}
