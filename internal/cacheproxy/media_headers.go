package cacheproxy

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func setVideoHeaders(w http.ResponseWriter, key string) {
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("ETag", `"`+key+`"`)
	w.Header().Set("Accept-Ranges", "bytes")
}

// serveVideoHEAD uses only resolved metadata and published-file attributes.
// It must stay outside download admission, even when no local file exists.
func (s *Server) serveVideoHEAD(w http.ResponseWriter, r *http.Request, v video) error {
	s.pinVideo(v)
	defer s.releaseVideo(v)
	size, modified, source := v.size, time.Time{}, "MISS"
	file, err := s.verifiedFile(r.Context(), v)
	if err == nil {
		size, modified, source = file.info.Size(), file.info.ModTime(), "HIT"
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errInvalidCache) {
		return err
	}
	serveVideoMetadata(w, r, v, size, modified, source)
	return nil
}

func serveVideoMetadata(w http.ResponseWriter, r *http.Request, v video, size int64, modified time.Time, source string) {
	setVideoHeaders(w, v.key)
	w.Header().Set("X-SWD-Cache", source)
	r = r.Clone(r.Context())
	r.Method = http.MethodHead
	r.Header.Del("Range")
	r.Header.Del("If-Range")
	// ServeContent handles validators and size through Seek without reading a
	// HEAD body when Content-Type is known. No video bytes need allocating.
	content := io.NewSectionReader(strings.NewReader(""), 0, size)
	http.ServeContent(w, r, v.path, modified, content)
}
