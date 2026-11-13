package cacheproxy

import (
	"context"
	"errors"
	"fmt"
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
	demand   func(context.Context, int64) func()
}

// Once all blocks are readable, withdrawing a source must not cancel publication.
func (s *spool) hasAllBytes(size int64) bool { return s.blocks.complete() }

// Only complete, validated blocks become visible to readers.
func (s *spool) WriteAt(p []byte, offset int64) (int, error) {
	if offset < 0 || offset%s.blocks.blockSize != 0 {
		return 0, errors.New("unaligned complete block write")
	}
	if err := s.blocks.store(offset/s.blocks.blockSize, p); err != nil {
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
	r := &spoolReader{spool: s, local: s.blocks.reader(), ctx: ctx, cancel: cancel, size: size,
		sources: readSources{size: size, blockSize: s.blocks.blockSize}}
	r.stopClose = context.AfterFunc(ctx, func() { r.local.Close() })
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
	sources       readSources
	spool         *spool
	local         *blockReader
	ctx           context.Context
	cancel        context.CancelFunc
	stopClose     func() bool
	closeOnce     sync.Once
	closeErr      error
	size, offset  int64
	err           error
	releaseDemand func()
	onWait        func()
}

func (r *spoolReader) Close() error {
	r.closeOnce.Do(func() {
		r.stopClose()
		r.cancel()
		if r.releaseDemand != nil {
			r.releaseDemand()
		}
		r.closeErr = r.local.Close()
		r.spool.release()
	})
	return r.closeErr
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
		next := min(int64(len(p)), r.size-r.offset)
		n, err := r.local.ReadAt(p[:next], r.offset)
		r.offset += int64(n)
		if !errors.Is(err, errBlockUnavailable) || n > 0 {
			if errors.Is(err, errBlockUnavailable) {
				err = nil // Deliver the prefix before waiting for the next block.
			}
			if err != nil {
				if cause := context.Cause(r.ctx); cause != nil {
					err = cause
				}
				r.err = err
			}
			return n, err
		}
		if failure != nil {
			if errors.Is(failure, ErrNotRetained) {
				failure = fmt.Errorf("requested block %d is unavailable after download completion: %w", r.offset/s.blocks.blockSize, failure)
			}
			r.err = failure
			return 0, failure
		}
		if complete {
			r.err = io.ErrUnexpectedEOF
			return 0, r.err
		}
		if demand != nil && release == nil {
			release = sync.OnceFunc(demand(r.ctx, r.offset))
			// Cancellation also cleans up if this Read is currently inside a
			// local file operation. Completion/error uses the same release.
			stopCancel = context.AfterFunc(r.ctx, release)
		}
		// Demand registration can itself make the block readable. Only count
		// a wait when the requested bytes are still absent at this boundary.
		select {
		case <-changed:
			continue
		default:
		}
		if s.blocks.available(r.offset) > r.offset {
			continue
		}
		r.sources.waitedBlock = r.offset/s.blocks.blockSize + 1
		if r.onWait != nil {
			r.onWait()
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
	if err := r.local.seek(offset); err != nil {
		return 0, err
	}
	r.offset = offset
	return offset, nil
}
