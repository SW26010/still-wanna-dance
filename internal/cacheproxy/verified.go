package cacheproxy

import (
	"context"
	"io"
	"os"
	"time"
)

// A verified handle lives until the version's last worker/request pin is released.
// Keeping the handle open prevents pathname replacement from changing the bytes
// served. Each response uses a SectionReader so concurrent seeks are independent.
type verifiedFile struct {
	done      chan struct{}
	file      *os.File
	info      os.FileInfo
	err       error
	checkedAt time.Time
}

// The caller must hold a version pin for the entire lifetime of the returned file.
func (s *Server) verifiedFile(ctx context.Context, v video) (*verifiedFile, error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.verifyMu.Lock()
		if s.verified == nil {
			s.verified = make(map[string]*verifiedFile)
		}
		if existing := s.verified[v.key]; existing != nil {
			s.verifyMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-existing.done:
				// A canceled validator must not cancel other callers.
				if existing.err != nil {
					continue
				}
				return existing, nil
			}
		}
		entry := &verifiedFile{done: make(chan struct{})}
		s.verified[v.key] = entry
		s.verifyMu.Unlock()

		select {
		case s.localChecks <- struct{}{}:
			entry.file, entry.err = os.Open(s.cfg.videoFile(v.key))
			if entry.err == nil {
				_, entry.err = validateLocalFile(ctx, s.cfg.StorageDir, entry, v, false)
			}
			<-s.localChecks
		case <-ctx.Done():
			entry.err = ctx.Err()
		}
		s.verifyMu.Lock()
		if entry.err != nil {
			if entry.file != nil {
				entry.file.Close()
			}
			delete(s.verified, v.key)
		}
		close(entry.done)
		s.verifyMu.Unlock()
		return entry, entry.err
	}
}

func sameVerifiedFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) &&
		a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (f *verifiedFile) reader() *io.SectionReader {
	return io.NewSectionReader(f.file, 0, f.info.Size())
}

// Detach under retentionMu after the last pin; close the handle outside it.
func (s *Server) detachVerified(key string) *os.File {
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	if entry := s.verified[key]; entry != nil {
		delete(s.verified, key)
		return entry.file
	}
	return nil
}
