package cacheproxy

import (
	"fmt"
	"strconv"
	"strings"
)

// Download task identity includes size, unlike the resource/block identity of
// usage and observation views. Keep its encoding here; blocksched treats
// task keys as opaque strings.
func downloadTaskKey(resource string, size, block int64) string {
	return fmt.Sprintf("%s/%d/%d", resource, size, block)
}

func parseDownloadTaskKey(key string) (resource string, size, block int64, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 {
		return "", 0, 0, false
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, 0, false
	}
	block, err = strconv.ParseInt(parts[2], 10, 64)
	return parts[0], size, block, err == nil
}
