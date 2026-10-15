package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type playbackClock struct {
	at     time.Time
	offset int64
	media  mediaTiming
}

type playbackClockKey struct{}

func (p *playbackClock) ready(offset int64) time.Time {
	return p.at.Add(max(0, p.media.at(offset)-p.media.at(p.offset)))
}

func (p *playbackClock) end() time.Time { return p.ready(p.media.size) }

func clockFromContext(ctx context.Context) *playbackClock {
	p, _ := ctx.Value(playbackClockKey{}).(*playbackClock)
	return p
}

// Only estimates a playback position. HTTP response validation and Range
// serving still belong to net/http. Invalid/multipart ranges are not clocks.
func playbackRangeStart(r *http.Request, v video) (int64, bool) {
	if r.Method != http.MethodGet {
		return 0, false
	}
	header := r.Header.Get("Range")
	if header == "" {
		return 0, true
	}
	// An unmatched validator causes ServeContent to send the full entity.
	// playbackContext resolves dates against the verified file before this call.
	if validator := r.Header.Get("If-Range"); validator != "" && validator != `"`+v.key+`"` {
		return 0, true
	}
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, false
	}
	left, right, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(header, "bytes=")), "-")
	if !ok {
		return 0, false
	}
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" {
		length, err := strconv.ParseInt(right, 10, 64)
		if err != nil || length <= 0 {
			return 0, false
		}
		return max(0, v.size-length), true
	}
	start, err := strconv.ParseInt(left, 10, 64)
	if err != nil || start < 0 || start >= v.size {
		return 0, false
	}
	if right != "" {
		end, err := strconv.ParseInt(right, 10, 64)
		if err != nil || end < start {
			return 0, false
		}
	}
	return start, true
}

func (s *Server) playbackContext(r *http.Request, v video, at time.Time) (context.Context, error) {
	if r.Method == http.MethodGet && r.Header.Get("Range") != "" {
		if date, err := http.ParseTime(r.Header.Get("If-Range")); err == nil {
			// ServeContent applies a date range only to a matching local file;
			// cold spools have no modification time. Use the same verified handle
			// and quota as serving, with a pin for its complete lifetime.
			s.pinVideo(v)
			file, verifyErr := s.verifiedFile(r.Context(), v)
			matches := verifyErr == nil && file.info.ModTime().Unix() == date.Unix()
			s.releaseVideo(v)
			if verifyErr != nil && !os.IsNotExist(verifyErr) && !errors.Is(verifyErr, errInvalidCache) {
				return nil, s.storageError(verifyErr)
			}
			if matches {
				r = r.Clone(r.Context())
				r.Header.Del("If-Range")
			}
		}
	}
	offset, ok := playbackRangeStart(r, v)
	if !ok {
		return context.WithValue(r.Context(), playbackClockKey{}, (*playbackClock)(nil)), nil
	}
	m, err := s.mediaTiming(r.Context(), v)
	if err != nil {
		return nil, err
	}
	p := &playbackClock{at: at, offset: offset, media: m}
	if s.cfg.Logger != nil {
		s.cfg.Logger.Debug("playback_timing", "resource_key", v.key, "arrived_at", at, "range_start", offset, "media_bytes", m.size, "duration_seconds", m.duration.Seconds(), "estimated_end", p.end())
	}
	return context.WithValue(r.Context(), playbackClockKey{}, p), nil
}
