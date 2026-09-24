package cacheproxy

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

// spool is a single download shared by independently seeking HTTP clients.
// ReadAt does not disturb the download writer's offset. The last byte is held
// until verification/publication, so an invalid full file never completes cleanly.
type spool struct {
	mu        sync.Mutex
	file      *os.File
	changed   chan struct{}
	available int64
	complete  bool
	err       error
	refs      int
}

func (s *spool) Write(p []byte) (int, error) {
	n, err := s.file.Write(p)
	s.mu.Lock()
	s.available += int64(n)
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	return n, err
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
	return &spoolReader{spool: s, ctx: ctx, size: size}
}

func (s *spool) release() {
	s.mu.Lock()
	s.refs--
	if s.refs == 0 {
		s.file.Close()
		os.Remove(s.file.Name())
	}
	s.mu.Unlock()
}

type spoolReader struct {
	spool        *spool
	ctx          context.Context
	size, offset int64
	err          error
}

func (r *spoolReader) Close() error { r.spool.release(); return nil }

func (r *spoolReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if err := r.ctx.Err(); err != nil {
			r.err = err
			return 0, err
		}
		if r.offset >= r.size {
			return 0, io.EOF
		}
		s := r.spool
		s.mu.Lock()
		available, complete, failure, changed := s.available, s.complete, s.err, s.changed
		s.mu.Unlock()
		if failure != nil {
			r.err = failure
			return 0, failure
		}
		if !complete && available >= r.size {
			available = r.size - 1
		}
		if available > r.size {
			available = r.size
		}
		if available > r.offset {
			next := int64(len(p))
			if next > available-r.offset {
				next = available - r.offset
			}
			n, err := s.file.ReadAt(p[:next], r.offset)
			r.offset += int64(n)
			if err != nil {
				r.err = err
			}
			return n, err
		}
		if complete {
			r.err = io.ErrUnexpectedEOF
			return 0, r.err
		}
		select {
		case <-r.ctx.Done():
			r.err = r.ctx.Err()
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
