package cacheproxy

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

const rangeBlockSize int64 = 1 << 20

var errRangeAbandoned = errors.New("range has no waiting readers")
var errRangeYielded = errors.New("range yielded to seek")

// readRangeBlock fills a worker-owned buffer, including one extra byte to detect
// oversized responses. Preserve non-EOF errors even when returned with data.
func readRangeBlock(r io.Reader, buf []byte) ([]byte, error) {
	n := 0
	for n < len(buf) {
		count, err := r.Read(buf[n:])
		n += count
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return buf[:n], err
		}
	}
	return buf[:n], nil
}

func validateRangeResponse(r *http.Response, start, end, size int64) error {
	if r.StatusCode != http.StatusPartialContent || r.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, end-1, size) {
		return errors.New("upstream returned an invalid or unsupported byte range")
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return errors.New("encoded byte range")
	}
	if r.ContentLength >= 0 && r.ContentLength != end-start {
		return errors.New("byte range length mismatch")
	}
	return nil
}
