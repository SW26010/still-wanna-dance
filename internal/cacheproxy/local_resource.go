package cacheproxy

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
)

// localResource is a view of local complete blocks, not resumable execution.
// The engine owns the registry reference. Flights and old spools retain their
// own references, without retaining response cursors or half-block prefixes.
type localResource struct {
	mu         sync.Mutex
	storage    *blockFiles
	video      video
	etag       string
	sequential bool           // A 200 origin must reopen a single response after stopping.
	done       map[int64]bool // Availability only for the storage-free diagnostic loader.
}

func (d *localResource) has(block int64) bool {
	if d.storage != nil {
		return d.storage.has(block)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.done[block]
}
func (d *localResource) complete() bool {
	if d.storage != nil {
		return d.storage.complete()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return int64(len(d.done)) == blockCount(d.video.size)
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
		size += blockBytes(d.video.size, block)
	}
	return size
}
func (d *localResource) makeSpool(demand func(int64) func()) *spool {
	d.storage.retain()
	return &spool{blocks: d.storage, changed: make(chan struct{}), refs: 1, demand: demand}
}
func resourceVideo(v video) video {
	return video{key: v.key, size: v.size, host: v.host, path: v.path, query: v.query}
}

func (s *Server) newLocalResource(v video, first *http.Response, log *slog.Logger) (*localResource, error) {
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
		d = &localResource{storage: newBlockFiles(s.cfg.tempDir(), v.size, log), video: resourceVideo(v), etag: first.Header.Get("ETag"), sequential: first.StatusCode == http.StatusOK}
		s.localResources[v.key] = d
	}
	d.storage.retain() // Current execution; the initial reference belongs to the registry.
	return d, nil
}

func (s *Server) acquireLocalResource(v video) (*localResource, error) {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	d := s.localResources[v.key]
	if d != nil {
		if d.video.size != v.size {
			return nil, errors.New("local resource size mismatch")
		}
		d.video = resourceVideo(v) // A new execution uses the caller's current address.
		d.storage.retain()
	}
	return d, nil
}
func (s *Server) forgetLocalResource(key string, expected *localResource) {
	s.retentionMu.Lock()
	matched := s.localResources[key] == expected
	if matched {
		delete(s.localResources, key)
	}
	s.retentionMu.Unlock()
	if matched {
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
