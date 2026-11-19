package cacheproxy

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"still-wanna-dance/internal/vrclog"
)

// The room owns this clock. GETs only consult it at admission, including
// after the source has released every block in its remaining use span.
type formalPlayback struct {
	started   time.Time
	song, key string
	clock     playbackClock // at is the wall time corresponding to media position zero
	use       useSpan
}

func (p *formalPlayback) remaining(now time.Time, size int64) useSpan {
	n := blockCount(p.clock.media.size, size)
	first := sort.Search(int(n), func(i int) bool { return !now.After(p.clock.ready(int64(i) * size)) })
	return useSpan{p.key, int64(first), n}
}

// ClearPlaybackLog withdraws the gateway's room state when its consumer stops.
func (s *Server) ClearPlaybackLog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playerRole = vrclog.UnknownRole
	s.clearFormalLocked(time.Now())
}

// ObservePlaybackLog applies the supplied events in file order. The caller owns
// the timeline and serializes delivery and shutdown for this gateway run.
func (s *Server) ObservePlaybackLog(t *vrclog.PlayerTimeline, events []vrclog.Event, now time.Time) {
	for _, e := range events {
		role, anchor := t.Role, t.Anchor
		t.Observe(e, now)
		anchorChanged := anchor != t.Anchor
		if !anchorChanged && role == t.Role {
			continue
		}
		s.cfg.Logger.Debug("正式播放日志更新", "event_name", "playback_timeline", "role", t.Role.String(), "song_id", t.Anchor.SongID, "anchor", t.Anchor.At, "target_seconds", t.Anchor.Position, "catalog_start", t.Anchor.CatalogStart)
		s.applyPlaybackTimeline(*t, anchorChanged)
	}
}

func (p *formalPlayback) setPosition(at time.Time, position float64) {
	if position < 0 || position >= p.clock.media.duration.Seconds() {
		p.clock.at = time.Time{}
		return
	}
	p.clock.at = at.Add(-time.Duration(position * float64(time.Second)))
}

func (s *Server) applyPlaybackTimeline(t vrclog.PlayerTimeline, anchorChanged bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.playerRole = t.Role
	if !anchorChanged {
		return
	}
	now := time.Now()
	s.refreshUseSourcesLocked(now)
	a := t.Anchor
	if a.SongID == 0 {
		s.clearFormalLocked(now)
		return
	}
	song := strconv.FormatInt(a.SongID, 10)
	p := s.formal
	if p == nil || p.song != song || p.started != a.Started {
		// Retire old playback for GET admission while the queue keeps its
		// last published base until the new clock has been resolved.
		if p != nil {
			s.dropFormalLocked(now)
			s.notifyUseChangedLocked(p.key)
		}
		s.mu.Unlock()
		meta, err := s.Store.formalMedia(s.ctx, a.SongID)
		if err != nil && s.ctx.Err() == nil {
			s.cfg.Logger.Warn("正式播放元数据不可用", "song_id", a.SongID, "error", err)
		}
		s.mu.Lock()
		if s.closed {
			return
		}
		if err != nil || meta.key == "" || meta.size <= 0 || meta.size > s.cfg.MaxFileBytes || !validMediaDuration(meta.end) {
			s.clearFormalLocked(time.Now())
			return
		}
		p = &formalPlayback{song: song, key: meta.key, started: a.Started, clock: playbackClock{media: estimateMediaTiming(meta.size, meta.end)}}
		if a.CatalogStart {
			p.setPosition(a.At, meta.start)
		}
	}
	next := *p
	if !a.CatalogStart {
		next.setPosition(a.At, a.Position)
	}
	span := next.remaining(time.Now(), s.cfg.BlockSize)
	allowed := true
	if s.formal != p || (span.first < span.last && !p.use.contains(p.key, span.first)) {
		s.mu.Unlock()
		err := s.Store.CheckSongAllowed(s.ctx, a.SongID)
		if err != nil && !errors.Is(err, ErrSongBlocked) && s.ctx.Err() == nil {
			s.cfg.Logger.Warn("正式播放准入检查失败", "song_id", a.SongID, "error", err)
		}
		s.mu.Lock()
		if s.closed {
			return
		}
		allowed = err == nil
		if !allowed && s.formal != p {
			s.clearFormalLocked(time.Now())
			return
		}
	}
	p.clock = next.clock
	s.updateFormalLocked(p, time.Now(), allowed)
}

func (s *Server) updateFormalLocked(p *formalPlayback, now time.Time, allowExpansion bool) {
	end := p.clock.end()
	if !now.Before(end) {
		s.clearFormalLocked(now)
		return
	}
	span := p.remaining(now, s.cfg.BlockSize)
	// Refused expansion keeps the current suffix on the corrected clock.
	if allowExpansion && span.last-span.first <= maxUseBlocks && s.swapUseLocked(p.use, span, false) == nil {
		p.use = span
	}
	s.formal = p
	keys := []string{p.key}
	if !end.Equal(s.queueStart) {
		s.queueStart = end
		keys = append(keys, s.retimeQueueLocked(now)...)
	}
	s.notifyUseChangedLocked(keys...)
}

func (s *Server) clearFormalLocked(now time.Time) {
	p := s.formal
	if p == nil && !s.queueStart.After(now) {
		return
	}
	s.dropFormalLocked(now)
	var keys []string
	if p != nil {
		keys = append(keys, p.key)
	}
	if s.queueStart.After(now) {
		s.queueStart = now
		keys = append(keys, s.retimeQueueLocked(now)...)
	}
	s.notifyUseChangedLocked(keys...)
}

func (s *Server) dropFormalLocked(at time.Time) {
	if p := s.formal; p != nil {
		s.releaseUseLocked(p.use, at)
		p.use = useSpan{}
		s.formal = nil
	}
}

func (s *Server) nextFormalExpiryLocked() time.Time {
	p := s.formal
	if p == nil {
		return time.Time{}
	}
	if p.use.first < p.use.last {
		return p.clock.ready(p.use.first * s.cfg.BlockSize).Add(time.Nanosecond)
	}
	return p.clock.end()
}

func (s *Server) expireFormalLocked(now time.Time) {
	p := s.formal
	if p == nil {
		return
	}
	for p.use.first < p.use.last {
		b := p.use.first
		n := p.clock.ready(b * s.cfg.BlockSize)
		if !now.After(n) {
			break
		}
		s.releaseUseLocked(useSpan{p.key, b, b + 1}, n.Add(time.Nanosecond))
		p.use.first++
	}
	if !now.Before(p.clock.end()) {
		s.dropFormalLocked(p.clock.end())
	}
}

// Only look at the Range head, or its immediate neighbor for the player's
// small rewind. Absence of a local block registry is unknown availability.
func (s *Server) latePlayerBlockLocked(v video, span byteInterval, now time.Time) (int64, time.Time, bool) {
	p := s.formal
	if p == nil || v.localOnly || v.songID == "" || p.song != v.songID || p.key != v.key || p.clock.media.size != v.size || !now.Before(p.clock.end()) {
		return 0, time.Time{}, false
	}
	size := s.cfg.BlockSize
	b := span.start / size
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	if _, ready := s.retained[v.key]; ready {
		return 0, time.Time{}, false
	}
	local := s.localResources[v.key]
	if local == nil || local.video.size != v.size {
		return 0, time.Time{}, false
	}
	if local.has(b) {
		distance := size - span.start%size
		if distance > 8192 || distance >= span.end-span.start || local.has(b+1) {
			return 0, time.Time{}, false
		}
		b++
	}
	n := p.clock.ready(b * size)
	return b, n, now.After(n)
}

type playerAdmission struct {
	role   vrclog.PlayerRole
	reason string
	reject bool
	block  int64
	needBy time.Time
}

func (s *Server) playerAdmission(r *http.Request, v video) playerAdmission {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := playerAdmission{role: s.playerRole}
	if d.role != vrclog.Follower {
		return d
	}
	clock, _ := r.Context().Value(playbackClockKey{}).(*playbackClock)
	if clock == nil || r.Header.Get("Range") == "" {
		d.reason = "no-single-range"
		return d
	}
	if value := r.Header.Get("If-Range"); value != "" && value != strconv.Quote(v.key) {
		d.reason = "if-range"
		return d
	}
	d.block, d.needBy, d.reject = s.latePlayerBlockLocked(v, byteInterval{clock.offset, clock.limit}, time.Now())
	if d.reject {
		d.reason = "late-missing-block"
	}
	return d
}

func (s *Server) admitPlayerGET(w *responseWriter, r *http.Request, v video, preview *previewRequest, log *slog.Logger) bool {
	if preview != nil || r.Method != http.MethodGet || !strings.HasPrefix(r.UserAgent(), "NSPlayer/") {
		return false
	}
	d := s.playerAdmission(r, v)
	handled := d.reject
	if d.reject && (r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != "") {
		// A missing block is served from a spool with an ETag and no modtime.
		// Let ServeContent apply GET/HEAD validator precedence before the 504.
		pending := &streamResponseWriter{ResponseWriter: w, header: w.Header().Clone()}
		serveVideoMetadata(pending, r, v, v.size, time.Time{}, "MISS")
		if pending.status == http.StatusNotModified || pending.status == http.StatusPreconditionFailed {
			pending.commit()
			d.reject, d.reason = false, "http-precondition"
		}
	}
	w.earlyVideoHeaders = d.role == vrclog.Owner
	log.Debug("正式播放器请求判定", "event_name", "playback_admission", "role", d.role.String(), "reason", d.reason, "block", d.block, "need_by", d.needBy, "reject", d.reject, "early_headers", w.earlyVideoHeaders)
	if d.reject {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, http.StatusText(http.StatusGatewayTimeout), http.StatusGatewayTimeout)
	}
	return handled
}
