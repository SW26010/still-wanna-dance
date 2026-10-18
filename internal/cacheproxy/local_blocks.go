package cacheproxy

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"still-wanna-dance/internal/applog"
)

// blockFiles owns immutable, complete blocks for one content identity. The
// engine, current execution and old readers hold separate resource references;
// none of those references alone prevents reclaiming an unprotected block.
// A pinned block/snapshot is a necessary I/O reference and prevents deletion.
type blockFiles struct {
	mu     sync.Mutex
	dir    string
	size   int64
	log    *slog.Logger
	files  map[int64]*localBlockFile
	refs   int
	closed bool
}

type localBlockFile struct {
	path    string
	size    int64
	pins    int
	retired bool
}

func newBlockFiles(dir string, size int64, log *slog.Logger) *blockFiles {
	return &blockFiles{dir: dir, size: size, log: log, files: make(map[int64]*localBlockFile), refs: 1}
}

func blockCount(size int64) int64 { return (size + rangeBlockSize - 1) / rangeBlockSize }
func blockBytes(size, block int64) int64 {
	if block < 0 || block >= blockCount(size) {
		return 0
	}
	return min(rangeBlockSize, size-block*rangeBlockSize)
}

func (f *blockFiles) retain() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		panic("retaining closed block resource")
	}
	f.refs++
}

func (f *blockFiles) release() {
	f.mu.Lock()
	f.refs--
	var remove []string
	if f.refs == 0 {
		f.closed = true
		remove = f.retireLocked(func(int64) bool { return true })
	}
	f.mu.Unlock()
	f.removeFiles(remove)
}

// store never exposes a short write or partially received block. Ordinary
// cached file I/O deliberately leaves cache residency/writeback to the OS.
func (f *blockFiles) store(block int64, data []byte) error {
	if size := blockBytes(f.size, block); size == 0 || int64(len(data)) != size {
		return fmt.Errorf("invalid complete block %d length %d", block, len(data))
	}
	f.mu.Lock()
	closed, existing := f.closed, f.files[block] != nil
	f.mu.Unlock()
	if closed {
		return os.ErrClosed
	}
	if existing {
		return nil
	}
	file, err := os.CreateTemp(f.dir, "download-block-*.part")
	if err != nil {
		return err
	}
	path := file.Name()
	kept := false
	defer func() {
		if !kept {
			os.Remove(path)
		}
	}()
	n, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	if closeErr != nil {
		return closeErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return os.ErrClosed
	}
	if f.files[block] != nil {
		return nil
	}
	f.files[block] = &localBlockFile{path: path, size: int64(n)}
	kept = true
	return nil
}

func (f *blockFiles) has(block int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[block] != nil
}

func (f *blockFiles) complete() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.files)) == blockCount(f.size)
}

func (f *blockFiles) bytes() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var size int64
	for _, b := range f.files {
		size += b.size
	}
	return size
}

func (f *blockFiles) available(offset int64) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if offset < 0 || offset >= f.size {
		return offset
	}
	block := offset / rangeBlockSize
	end := offset
	for f.files[block] != nil {
		end = block*rangeBlockSize + f.files[block].size
		block++
	}
	return end
}

func (f *blockFiles) pin(block int64) (*localBlockFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, os.ErrClosed
	}
	b := f.files[block]
	if b == nil {
		return nil, os.ErrNotExist
	}
	b.pins++
	return b, nil
}

func (f *blockFiles) unpin(b *localBlockFile) {
	f.mu.Lock()
	b.pins--
	remove := b.retired && b.pins == 0
	f.mu.Unlock()
	if remove {
		f.removeFiles([]string{b.path})
	}
}

func readBlockFile(b *localBlockFile, p []byte, offset int64) (int, error) {
	file, err := os.Open(b.path)
	if err != nil {
		return 0, err
	}
	n, err := file.ReadAt(p, offset)
	return n, errors.Join(err, file.Close())
}

func (f *blockFiles) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative block read offset")
	}
	n := 0
	for len(p) > 0 && offset < f.size {
		block := offset / rangeBlockSize
		b, err := f.pin(block)
		if err != nil {
			return n, err
		}
		within := offset % rangeBlockSize
		length := min(int64(len(p)), b.size-within)
		read, err := readBlockFile(b, p[:length], within)
		f.unpin(b)
		n += read
		offset += int64(read)
		p = p[read:]
		if err != nil {
			return n, err
		}
	}
	if len(p) > 0 {
		return n, io.EOF
	}
	return n, nil
}

// Reclamation detaches an identity atomically with its protection decision.
// Disk removal happens after the caller releases the protection lock. A later
// store has its own unique path, so stale cleanup cannot delete replacement data.
func (f *blockFiles) retire(eligible func(int64) bool) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.retireLocked(eligible)
}

func (f *blockFiles) retireLocked(eligible func(int64) bool) []string {
	var remove []string
	for block, b := range f.files {
		if !eligible(block) {
			continue
		}
		delete(f.files, block)
		b.retired = true
		if b.pins == 0 {
			remove = append(remove, b.path)
		}
	}
	return remove
}

func (f *blockFiles) removeFiles(paths []string) {
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) && f.log != nil {
			f.log.Warn("temporary_cleanup_failed", "error", applog.SafeError(err))
		}
	}
}

// A merge needs an atomic complete snapshot. Subsequent reclamation can remove
// availability for new reads, but cannot remove these exact files until Close.
type blockSnapshot struct {
	owner  *blockFiles
	blocks []*localBlockFile
	once   sync.Once
}

func (f *blockFiles) snapshot() (*blockSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, os.ErrClosed
	}
	if int64(len(f.files)) != blockCount(f.size) {
		return nil, os.ErrNotExist
	}
	s := &blockSnapshot{owner: f, blocks: make([]*localBlockFile, blockCount(f.size))}
	for block := range s.blocks {
		b := f.files[int64(block)]
		b.pins++
		s.blocks[block] = b
	}
	return s, nil
}

func (s *blockSnapshot) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative snapshot read offset")
	}
	n := 0
	for len(p) > 0 && offset < s.owner.size {
		b := s.blocks[offset/rangeBlockSize]
		within := offset % rangeBlockSize
		length := min(int64(len(p)), b.size-within)
		read, err := readBlockFile(b, p[:length], within)
		n += read
		offset += int64(read)
		p = p[read:]
		if err != nil {
			return n, err
		}
	}
	if len(p) > 0 {
		return n, io.EOF
	}
	return n, nil
}

func (s *blockSnapshot) Close() error {
	s.once.Do(func() {
		for _, b := range s.blocks {
			s.owner.unpin(b)
		}
	})
	return nil
}
