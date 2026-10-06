package cacheproxy

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

// spool is a single download shared by independently seeking HTTP clients.
// ReadAt does not disturb the writer's offset. Sequential and full HTTP reads
// hold the last byte until verification; range readers can access the tail early.
type spool struct {
	mu        sync.Mutex
	file      *os.File
	changed   chan struct{}
	available int64
	complete  bool
	err       error
	refs      int
	intervals []byteInterval
	demand    func(int64) func()
}

type byteInterval struct{ start, end int64 }

// WriteAt publishes only bytes whose write has completed, never sparse length.
func (s *spool) WriteAt(p []byte, offset int64) (int, error) {
	n, err := s.file.WriteAt(p, offset)
	if n == 0 {
		return n, err
	}
	s.mu.Lock()
	merged := byteInterval{offset, offset + int64(n)}
	var out []byteInterval
	inserted := false
	for _, interval := range s.intervals {
		if interval.end < merged.start {
			out = append(out, interval)
		} else if merged.end < interval.start {
			if !inserted {
				out = append(out, merged)
				inserted = true
			}
			out = append(out, interval)
		} else {
			merged.start = min(merged.start, interval.start)
			merged.end = max(merged.end, interval.end)
		}
	}
	if !inserted {
		out = append(out, merged)
	}
	s.intervals = out
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	return n, err
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
	ctx, cancel := context.WithCancel(ctx)
	return &spoolReader{spool: s, ctx: ctx, cancel: cancel, size: size}
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
	cancel       context.CancelFunc
	closeOnce    sync.Once
	size, offset int64
	err          error
	verifyFull   bool
}

func (r *spoolReader) Close() error {
	r.closeOnce.Do(func() { r.cancel(); r.spool.release() })
	return nil
}

func (r *spoolReader) Read(p []byte) (int, error) {
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
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
		demand := s.demand
		if demand != nil {
			available = 0
			for _, interval := range s.intervals {
				if interval.start <= r.offset && r.offset < interval.end {
					available = interval.end
					break
				}
			}
		}
		s.mu.Unlock()
		if failure != nil {
			r.err = failure
			return 0, failure
		}
		if (demand == nil || r.verifyFull) && !complete && available >= r.size {
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
		if demand != nil && release == nil {
			release = demand(r.offset)
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
