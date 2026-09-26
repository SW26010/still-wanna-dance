package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

const verifiedRecordLimit = 64
const verifiedRecordTTL = 30 * time.Second

// Idle records retain metadata only, never handles or retention pins. The TTL
// starts at the full hash and is not extended by hits. Metadata cannot detect
// writes that preserve identity, size and timestamps; expiration bounds reuse.
type verificationRecord struct {
	info      os.FileInfo
	checkedAt time.Time
	usedAt    time.Time
}

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
		record := s.verificationRecords[v.key]
		delete(s.verificationRecords, v.key)
		s.verified[v.key] = entry
		s.verifyMu.Unlock()

		select {
		case s.localChecks <- struct{}{}:
			entry.file, entry.err = os.Open(s.cfg.videoFile(v.key))
			if entry.err == nil {
				entry.err = entry.validate(ctx, v, record)
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

func (f *verifiedFile) validate(ctx context.Context, v video, record verificationRecord) error {
	before, err := f.file.Stat()
	if err != nil {
		return err
	}
	if time.Since(record.checkedAt) < verifiedRecordTTL && sameVerifiedFile(record.info, before) {
		f.info, f.checkedAt = before, record.checkedAt
		return ctx.Err()
	}
	if err := checkOpenFile(ctx, f.file, v); err != nil {
		return err
	}
	after, err := f.file.Stat()
	if err != nil {
		return err
	}
	if !sameVerifiedFile(before, after) {
		return fmt.Errorf("%w: cached file changed during validation", errInvalidCache)
	}
	f.info, f.checkedAt = after, time.Now()
	return nil
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
		s.rememberVerification(key, entry, time.Now())
		return entry.file
	}
	return nil
}

// Called with verifyMu held. Remove expired records lazily and evict the least
// recently released record when full; active handles are never evicted here.
func (s *Server) rememberVerification(key string, entry *verifiedFile, now time.Time) {
	for k, record := range s.verificationRecords {
		if now.Sub(record.checkedAt) >= verifiedRecordTTL {
			delete(s.verificationRecords, k)
		}
	}
	if entry.err != nil || entry.info == nil || now.Sub(entry.checkedAt) >= verifiedRecordTTL {
		return
	}
	if s.verificationRecords == nil {
		s.verificationRecords = make(map[string]verificationRecord)
	}
	if len(s.verificationRecords) >= verifiedRecordLimit {
		var oldest string
		for k, record := range s.verificationRecords {
			if oldest == "" || record.usedAt.Before(s.verificationRecords[oldest].usedAt) {
				oldest = k
			}
		}
		delete(s.verificationRecords, oldest)
	}
	s.verificationRecords[key] = verificationRecord{info: entry.info, checkedAt: entry.checkedAt, usedAt: now}
}
