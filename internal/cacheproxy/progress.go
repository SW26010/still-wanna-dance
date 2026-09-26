package cacheproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"stepstash/internal/applog"
)

// One low-frequency observer per active flight also reports blocked reads.
// Stop joins it so no progress record can follow the terminal event.
type downloadProgress struct {
	mu                       sync.Mutex
	log                      *slog.Logger
	start, changed, lastByte time.Time
	stage                    string
	host                     string
	samples                  [5]progressSample
	bytes, size              int64
	stop, done               chan struct{}
}

type progressSample struct{ second, bytes int64 }

// Five bounded buckets cover the current second and the preceding four seconds.
// Snapshot reads never reset counters, so multiple observers see the same rate.
func (p *downloadProgress) rate(now time.Time) float64 {
	if p.stage != "download_and_hash" {
		return 0
	}
	var bytes int64
	for _, sample := range p.samples {
		if sample.second > now.Unix()-5 && sample.second <= now.Unix() {
			bytes += sample.bytes
		}
	}
	seconds := min(5.0, now.Sub(p.changed).Seconds())
	if seconds <= 0 {
		return 0
	}
	return float64(bytes) / seconds
}

func (p *downloadProgress) setHost(host string) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}

func startProgress(log *slog.Logger, size int64, interval time.Duration) *downloadProgress {
	now := time.Now()
	p := &downloadProgress{log: log, start: now, changed: now, lastByte: now,
		stage: "cache_check", size: size, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		last, previous := now, int64(0)
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.mu.Lock()
				stage, bytes, changed, lastByte := p.stage, p.bytes, p.changed, p.lastByte
				p.mu.Unlock()
				now := time.Now()
				p.log.Info("download_progress", "stage", stage, "bytes", bytes, "expected_bytes", p.size,
					"bytes_per_second", float64(bytes-previous)/now.Sub(last).Seconds(),
					"idle_ms", now.Sub(lastByte).Milliseconds(), "stage_elapsed_ms", now.Sub(changed).Milliseconds(), "elapsed_ms", now.Sub(p.start).Milliseconds())
				last, previous = now, bytes
			}
		}
	}()
	return p
}

func (p *downloadProgress) setStage(stage string) {
	p.mu.Lock()
	previous, elapsed := p.stage, time.Since(p.changed).Milliseconds()
	p.stage, p.changed = stage, time.Now()
	p.mu.Unlock()
	p.log.Info("download_stage", "stage", stage, "previous_stage", previous, "previous_elapsed_ms", elapsed)
}

func (p *downloadProgress) finish(err error) {
	close(p.stop)
	<-p.done
	outcome := "completed"
	if err != nil {
		outcome = "failed"
	}
	if errors.Is(err, context.Canceled) {
		outcome = "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		outcome = "timeout"
	}
	p.log.Info("cache_task_finished", "outcome", outcome, "stage", p.stage, "bytes", p.bytes,
		"stage_elapsed_ms", time.Since(p.changed).Milliseconds(), "elapsed_ms", time.Since(p.start).Milliseconds(), "error", applog.SafeError(err))
}

type progressReader struct {
	io.Reader
	progress *downloadProgress
}

func (r progressReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if n > 0 {
		p := r.progress
		p.mu.Lock()
		first := p.bytes == 0
		p.bytes += int64(n)
		p.lastByte = time.Now()
		second := p.lastByte.Unix()
		sample := &p.samples[second%int64(len(p.samples))]
		if sample.second != second {
			*sample = progressSample{second: second}
		}
		sample.bytes += int64(n)
		p.mu.Unlock()
		if first {
			p.log.Info("download_first_byte", "elapsed_ms", time.Since(p.start).Milliseconds())
		}
	}
	return n, err
}
