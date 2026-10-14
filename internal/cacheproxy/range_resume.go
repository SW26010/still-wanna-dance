package cacheproxy

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"still-wanna-dance/internal/applog"
)

// A completed flight and its readers may outlive the next flight. Share file
// ownership, not the old spool's terminal error or reader-demand callbacks.
type spoolFile struct {
	mu   sync.Mutex
	file *os.File
	refs int
	log  *slog.Logger
}

func (f *spoolFile) retain() {
	f.mu.Lock()
	f.refs++
	f.mu.Unlock()
}

func (f *spoolFile) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs--
	if f.refs != 0 {
		return
	}
	if err := f.file.Close(); err != nil {
		f.log.Warn("temporary_close_failed", "error", applog.SafeError(err))
	}
	if err := os.Remove(f.file.Name()); err != nil && !os.IsNotExist(err) {
		f.log.Warn("temporary_cleanup_failed", "error", applog.SafeError(err))
	}
}

// Only this engine owns these checkpoints. Bytes live in the managed .part
// file; no buffers, readers, running workers or HTTP bodies survive a pause.
// Server.mu transfers ownership between a flight and Server.paused.
type rangeDownload struct {
	mu          sync.Mutex
	storage     *spoolFile
	video       video
	etag        string
	done        map[int64]bool
	partial     map[int64]*rangeResumeBlock
	prefix      int64
	pausedOrder uint64
	pausedSize  int64
}

type rangeResumeBlock struct {
	video    video
	received int64
	failures int
	last     error
	attempts *resourceAttempts
}

func (d *rangeDownload) take(block int64) *rangeResumeBlock {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.partial[block]
	delete(d.partial, block)
	if state == nil {
		state = &rangeResumeBlock{video: d.video, attempts: new(resourceAttempts)}
	}
	return state
}

func (d *rangeDownload) finish(block int64) {
	d.mu.Lock()
	d.done[block] = true
	for d.done[d.prefix] {
		d.prefix++
	}
	d.mu.Unlock()
}

func (d *rangeDownload) save(block int64, state *rangeResumeBlock, buffer []byte) error {
	if state.received > 0 {
		if _, err := d.storage.file.WriteAt(buffer[:state.received], block*rangeBlockSize); err != nil {
			return err
		}
	}
	if r := state.attempts.request; r != nil {
		// Do not retain the canceled flight context and its reader callbacks.
		state.attempts.request = r.Clone(context.Background())
	}
	d.mu.Lock()
	d.partial[block] = state
	d.mu.Unlock()
	return nil
}

// Called only while no range worker is running.
func (d *rangeDownload) bytes() int64 {
	var n int64
	for block := range d.done {
		n += min(rangeBlockSize, d.video.size-block*rangeBlockSize)
	}
	for _, state := range d.partial {
		n += state.received
	}
	return n
}

func (d *rangeDownload) makeSpool(demand func(int64) func(), track func(context.Context) (func(int64), func())) *spool {
	d.storage.retain()
	sp := &spool{file: d.storage.file, storage: d.storage, changed: make(chan struct{}), refs: 1, demand: demand, track: track, sparse: true}
	for block := int64(0); block < (d.video.size+rangeBlockSize-1)/rangeBlockSize; block++ {
		if !d.done[block] {
			continue
		}
		start, end := block*rangeBlockSize, min((block+1)*rangeBlockSize, d.video.size)
		if n := len(sp.intervals); n > 0 && sp.intervals[n-1].end == start {
			sp.intervals[n-1].end = end
		} else {
			sp.intervals = append(sp.intervals, byteInterval{start, end})
		}
	}
	return sp
}

func rangeVideo(v video) video {
	return video{key: v.key, size: v.size, host: v.host, path: v.path, query: v.query}
}

func (s *Server) newRangeDownload(v video, first *http.Response, log *slog.Logger) (*rangeDownload, error) {
	f, err := os.CreateTemp(s.cfg.tempDir(), "download-*.part")
	if err != nil {
		return nil, err
	}
	return &rangeDownload{storage: &spoolFile{file: f, refs: 1, log: log}, video: rangeVideo(v),
		etag: first.Header.Get("ETag"), done: make(map[int64]bool), partial: make(map[int64]*rangeResumeBlock)}, nil
}

// Bound retained checkpoints independently of published-cache retention. Count
// file length (including gaps), not just validated bytes. Readers own separate
// references, so eviction cannot close an older response's open file.
const maxPausedFiles = 32
const maxPausedBytes int64 = 512 << 20

// Caller holds Server.mu and releases returned references outside the lock.
func (s *Server) retainPausedLocked(key string, d *rangeDownload) []*rangeDownload {
	info, err := d.storage.file.Stat()
	if err != nil {
		s.cfg.Logger.Warn("paused_stat_failed", "error", applog.SafeError(err))
		return []*rangeDownload{d}
	}
	if s.paused == nil {
		s.paused = make(map[string]*rangeDownload)
	}
	d.pausedOrder, d.pausedSize = s.sequence.Add(1), info.Size()
	s.paused[key] = d
	return s.trimPausedLocked(maxPausedFiles, maxPausedBytes)
}

func (s *Server) trimPausedLocked(files int, limit int64) []*rangeDownload {
	var drop []*rangeDownload
	for {
		var bytes int64
		var oldest string
		for key, checkpoint := range s.paused {
			bytes += checkpoint.pausedSize
			if oldest == "" || checkpoint.pausedOrder < s.paused[oldest].pausedOrder {
				oldest = key
			}
		}
		if len(s.paused) <= files && bytes <= limit {
			break
		}
		victim := s.paused[oldest]
		delete(s.paused, oldest)
		drop = append(drop, victim)
		s.cfg.Logger.Debug("paused_evicted", "resource_key", oldest, "file_bytes", victim.pausedSize, "reason", "checkpoint_limit")
	}
	return drop
}
