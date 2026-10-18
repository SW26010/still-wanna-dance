package cacheproxy

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

const rangeBlockSize int64 = 1 << 20

// Shared geometry for source ranges, download admission and local storage.
// Ranges use a nonnegative start and an exclusive end, matching HTTP adapters.
func blocksForRange(start, end int64) (first, last int64) {
	first = start / rangeBlockSize
	if end <= start {
		return first, first
	}
	return first, blockCount(end)
}

func blockCount(size int64) int64 {
	if size <= 0 {
		return 0
	}
	return 1 + (size-1)/rangeBlockSize
}

func blockBytes(size, block int64) int64 {
	if block < 0 || block >= blockCount(size) {
		return 0
	}
	return min(rangeBlockSize, size-block*rangeBlockSize)
}

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
