package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"
)

const typicalDuration = 210 * time.Second
const typicalSize int64 = 45 << 20

// TypicalBitrate is the estimated total media bitrate, in bits/s, including
// audio and container overhead. It is not measured metadata for a video.
const TypicalBitrate = 2_000_000

// An estimate only: never persisted as media metadata. Bitrate is derived from
// the pair when needed, so byte positions and the end time use the same scale.
type mediaTiming struct {
	size     int64
	duration time.Duration
}

func estimateMediaTiming(size int64, seconds float64) mediaTiming {
	hasSize := size > 0
	hasDuration := seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && seconds < float64(math.MaxInt64)/float64(time.Second)
	switch {
	case !hasSize && !hasDuration:
		return mediaTiming{typicalSize, typicalDuration}
	case !hasDuration:
		seconds = float64(size) * 8 / TypicalBitrate
	case !hasSize:
		bytes := math.Ceil(seconds * TypicalBitrate / 8)
		if bytes >= float64(math.MaxInt64) {
			return mediaTiming{typicalSize, typicalDuration}
		}
		size = max(1, int64(bytes))
	}
	nanos := seconds * float64(time.Second)
	if nanos >= float64(math.MaxInt64) {
		return mediaTiming{typicalSize, typicalDuration}
	}
	return mediaTiming{size, max(time.Nanosecond, time.Duration(nanos))}
}

func (m mediaTiming) at(offset int64) time.Duration {
	if m.size <= 0 || m.duration <= 0 {
		m = estimateMediaTiming(0, 0)
	}
	if offset >= m.size {
		return m.duration
	}
	return time.Duration(float64(max(0, offset)) / float64(m.size) * float64(m.duration))
}

// A song's end is usable only for the same resource version. Direct resource
// requests have no song ID; the earliest valid end among matching songs is the
// conservative estimate when multiple catalog entries reference the same MD5.
func (s *Server) mediaTiming(ctx context.Context, v video) (mediaTiming, error) {
	var size sql.NullInt64
	var seconds sql.NullFloat64
	if s.usage != nil {
		err := s.usage.db.QueryRowContext(ctx, `SELECT byte_size,
 (SELECT MIN(s.end) FROM songs s JOIN song_media sm ON sm.song_id=s.song_id
  WHERE sm.md5=media.md5 AND (?='' OR s.song_id=?) AND s.end>0)
 FROM media WHERE md5=?`, v.songID, v.songID, v.key).Scan(&size, &seconds)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return mediaTiming{}, s.storageError(err)
		}
	}
	if !size.Valid || size.Int64 <= 0 {
		// A newly received, validated URL may precede the media row. Its s is
		// the same authoritative byte count, not a guessed file size.
		size.Int64 = v.size
	}
	return estimateMediaTiming(size.Int64, seconds.Float64), nil
}
