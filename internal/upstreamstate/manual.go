package upstreamstate

import (
	"context"
	"errors"
	"time"
)

type CheckKind string

const (
	CheckCatalog    CheckKind = "catalog"
	CheckPlayback   CheckKind = "playback"
	CheckLatency    CheckKind = "latency"
	CheckThroughput CheckKind = "throughput"
)

func ValidCheckKind(kind CheckKind) bool {
	return kind == CheckCatalog || kind == CheckPlayback || kind == CheckLatency || kind == CheckThroughput
}

// CheckSelected preempts automatic work and reserves the next batch for the
// requested operation. Prerequisite lookups do not replace unrelated results.
func (m *Monitor) CheckSelected(ctx context.Context, kind CheckKind) error {
	if !ValidCheckKind(kind) {
		return errors.New("unknown monitor check kind")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("monitor closed")
	}
	if m.manualDone != nil || (m.active != nil && m.batchManual) {
		m.mu.Unlock()
		return errors.New("manual check already running")
	}
	m.manualDone = make(chan struct{})
	if m.active != nil {
		m.batchCancel()
	}
	m.mu.Unlock()
	defer func() { m.mu.Lock(); close(m.manualDone); m.manualDone = nil; m.mu.Unlock() }()
	return m.checkRequest(ctx, true, kind)
}

func (m *Monitor) cachedSong(p Policy) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Now().Before(m.songAt.Add(p.SampleLifetime)) {
		return m.songID
	}
	return 0
}

func (k CheckKind) includes(op Operation) bool {
	return k == "" || (k == CheckCatalog && op == Catalog) || (k == CheckPlayback && op == PlaybackURL) || ((k == CheckLatency || k == CheckThroughput) && op == Resource)
}
