package cacheproxy

import (
	"context"
	"errors"
	"io"
	"sync"
)

// spool is a single download shared by independently seeking HTTP clients.
// ReadAt does not disturb the writer's offset. HTTP byte delivery is independent
// of whole-video verification and publication.
type spool struct {
	mu       sync.Mutex
	blocks   *blockFiles
	changed  chan struct{}
	complete bool
	err      error
	refs     int
	demand   func(int64) func()
}

// Once all blocks are readable, withdrawing a source must not cancel publication.
func (s *spool) hasAllBytes(size int64) bool { return s.blocks.complete() }

// Only complete, validated blocks become visible to readers.
func (s *spool) WriteAt(p []byte, offset int64) (int, error) {
	if offset < 0 || offset%rangeBlockSize != 0 {
		return 0, errors.New("unaligned complete block write")
	}
	if err := s.blocks.store(offset/rangeBlockSize, p); err != nil {
		return 0, err
	}
	s.mu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	return len(p), nil
}

func (s *spool) finish(err error) {
	s.mu.Lock()
	s.complete, s.err = true, err
	close(s.changed)
	s.mu.Unlock()
}

func (s *spool) reader(ctx context.Context, size int64) *spoolReader {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refs == 0 {
		return nil
	}
	s.refs++
	ctx, cancel := context.WithCancel(ctx)
	r := &spoolReader{spool: s, ctx: ctx, cancel: cancel, size: size}
	return r
}

func (s *spool) release() {
	s.mu.Lock()
	s.refs--
	if s.refs == 0 {
		s.blocks.release()
	}
	s.mu.Unlock()
}

type spoolReader struct {
	spool         *spool
	ctx           context.Context
	cancel        context.CancelFunc
	closeOnce     sync.Once
	size, offset  int64
	err           error
	releaseDemand func()
}

func (r *spoolReader) Close() error {
	r.closeOnce.Do(func() {
		r.cancel()
		if r.releaseDemand != nil {
			r.releaseDemand()
		}
		r.spool.release()
	})
	return nil
}

func (r *spoolReader) Read(p []byte) (int, error) {
	var release func()
	var stopCancel func() bool
	defer func() {
		if release != nil {
			stopCancel()
			release()
		}
	}()
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if err := context.Cause(r.ctx); err != nil {
			r.err = err
			return 0, err
		}
		if r.offset >= r.size {
			return 0, io.EOF
		}
		s := r.spool
		s.mu.Lock()
		complete, failure, changed := s.complete, s.err, s.changed
		demand := s.demand
		s.mu.Unlock()
		available := s.blocks.available(r.offset)
		if available > r.size {
			available = r.size
		}
		if available > r.offset {
			next := int64(len(p))
			if next > available-r.offset {
				next = available - r.offset
			}
			n, err := s.blocks.ReadAt(p[:next], r.offset)
			r.offset += int64(n)
			if errors.Is(err, errBlockUnavailable) {
				if n > 0 {
					return n, nil
				}
				continue // Re-check availability and register a genuine missing read.
			}
			if err != nil {
				r.err = err
			}
			return n, err
		}
		if failure != nil {
			r.err = failure
			return 0, failure
		}
		if complete {
			r.err = io.ErrUnexpectedEOF
			return 0, r.err
		}
		if demand != nil && release == nil {
			release = sync.OnceFunc(demand(r.offset))
			// Cancellation also cleans up if this Read is currently inside a
			// local file operation. Completion/error uses the same release.
			stopCancel = context.AfterFunc(r.ctx, release)
		}
		select {
		case <-r.ctx.Done():
			r.err = context.Cause(r.ctx)
			return 0, r.err
		case <-changed:
		}
	}
}

func (r *spoolReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.offset
	case io.SeekEnd:
		offset += r.size
	default:
		return 0, errors.New("invalid seek origin")
	}
	if offset < 0 {
		return 0, errors.New("negative seek")
	}
	r.offset = offset
	return offset, nil
}
