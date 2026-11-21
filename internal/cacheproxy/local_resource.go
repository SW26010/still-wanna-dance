package cacheproxy

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
)

// localResource is a view of local complete blocks, not resumable execution.
// The engine owns the registry reference. Flights and old spools retain their
// own references, without retaining response cursors or half-block prefixes.
type localResource struct {
	blockSize  int64 // Immutable for both stored blocks and storage-free measurements.
	mu         sync.Mutex
	storage    *blockFiles
	video      video             // Identity is immutable; routing is protected by Server.retentionMu.
	validators map[string]string // Strong ETags by validated endpoint.
	entitySet  bool
	done       map[int64]bool // Availability only for the storage-free diagnostic loader.
}

// HTTP validators are scoped to the validated final URL.
func (d *localResource) etagFor(v video) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.validators[v.requestURL()]
}

func (d *localResource) boundToOtherAddress(v video) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	current := v.requestURL()
	for endpoint := range d.validators {
		if endpoint != current {
			return true
		}
	}
	return false
}

func (d *localResource) acceptEntity(v video, etag string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := v.requestURL()
	if old := d.validators[key]; old != "" && old != etag {
		return errors.New("upstream entity changed between ranges")
	}
	if etag != "" && !strings.HasPrefix(etag, "W/") {
		if d.validators == nil {
			d.validators = make(map[string]string)
		}
		d.validators[key] = etag
	}
	if !d.entitySet {
		d.video.host, d.video.path, d.video.query = v.host, v.path, v.query
		d.entitySet = true
	}
	return nil
}

func (d *localResource) has(block int64) bool {
	if d.storage != nil {
		return d.storage.has(block)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.done[block]
}

func (d *localResource) firstMissing(span useSpan) (int64, bool) {
	for b := span.first; b < span.last; b++ {
		if d == nil || !d.has(b) {
			return b, true
		}
	}
	return 0, false
}

func (s *Server) firstMissingLocal(span useSpan) (int64, bool) {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	if _, retained := s.retained[span.key]; retained {
		return 0, false
	}
	return s.localResources[span.key].firstMissing(span)
}

func (d *localResource) complete() bool {
	if d.storage != nil {
		return d.storage.complete()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return int64(len(d.done)) == blockCount(d.video.size, d.blockSize)
}
func (d *localResource) finish(block int64) {
	if d.storage != nil {
		return
	}
	d.mu.Lock()
	d.done[block] = true
	d.mu.Unlock()
}
func (d *localResource) bytes() int64 {
	if d.storage != nil {
		return d.storage.bytes()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var size int64
	for block := range d.done {
		size += blockBytes(d.video.size, block, d.blockSize)
	}
	return size
}
func (d *localResource) makeSpool(demand func(context.Context, int64) func()) *spool {
	d.storage.retain()
	return &spool{blocks: d.storage, changed: make(chan struct{}), refs: 1, demand: demand}
}
func resourceVideo(v video) video {
	return video{key: v.key, size: v.size, host: v.host, path: v.path, query: v.query}
}

func (s *Server) newLocalResource(v video, log *slog.Logger) (*localResource, error) {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	if s.localResources == nil {
		s.localResources = make(map[string]*localResource)
	}
	d := s.localResources[v.key]
	if d != nil && d.video.size != v.size {
		return nil, errors.New("local resource size mismatch")
	}
	if d == nil {
		d = &localResource{blockSize: s.cfg.BlockSize, storage: newBlockFiles(s.cfg.tempDir(), v.size, s.cfg.BlockSize, log), video: resourceVideo(v)}
		d.storage.onDiskChange = s.observeBlockCacheBytes
		s.localResources[v.key] = d
	}
	d.storage.retain() // Current execution; the initial reference belongs to the registry.
	return d, nil
}

// acquireLocalResource retains a caller reference independently of the registry.
func (s *Server) acquireLocalResource(v video) (*localResource, error) {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	d := s.localResources[v.key]
	if d != nil {
		if d.video.size != v.size {
			return nil, errors.New("local resource size mismatch")
		}
		d.storage.retain()
	}
	return d, nil
}

// reuseLocalResource refreshes routing before any executor starts. A strong
// validator belongs to its old address: drop both registry and execution
// references when it changes, while old readers keep their own references.
func (s *Server) reuseLocalResource(v video, d *localResource) *localResource {
	if d == nil || v.host == "" {
		return d
	}
	s.retentionMu.Lock()
	changed := d.boundToOtherAddress(v)
	if !changed {
		d.video.host, d.video.path, d.video.query = v.host, v.path, v.query
	}
	s.retentionMu.Unlock()
	if changed {
		s.forgetLocalResource(v.key, d)
		d.storage.release()
		return nil
	}
	return d
}

// finishLocalResource releases the execution reference. Keep reusable blocks
// registered after cancellation or declined publication; success, invalid data
// and empty storage no longer need the registry reference. Spools are independent.
func (s *Server) finishLocalResource(d *localResource, result error) {
	if d == nil {
		return
	}
	if result == nil || errors.Is(result, errInvalidCache) || d.bytes() == 0 {
		s.forgetLocalResource(d.video.key, d)
	}
	d.storage.release()
}

func (s *Server) forgetLocalResource(key string, expected *localResource) {
	s.retentionMu.Lock()
	matched := s.localResources[key] == expected
	if matched {
		delete(s.localResources, key)
	}
	s.retentionMu.Unlock()
	if matched {
		s.notifyBlockObservation()
		expected.storage.release()
	}
}
func (s *Server) closeLocalResources() {
	s.retentionMu.Lock()
	resources := s.localResources
	s.localResources = nil
	s.retentionMu.Unlock()
	for _, d := range resources {
		d.storage.release()
	}
}

// A flight can finish before its caller takes the streaming branch. Temporary
// bytes remain independently readable even though publication was declined.
func (s *Server) localBlockReader(ctx context.Context, v video) (*spoolReader, error) {
	local, err := s.acquireLocalResource(v)
	if err != nil {
		return nil, err
	}
	if local == nil {
		return nil, ErrNotRetained
	}
	defer local.storage.release()
	sp := local.makeSpool(nil)
	sp.finish(ErrNotRetained)
	reader := sp.reader(ctx, v.size)
	sp.release()
	return reader, nil
}
