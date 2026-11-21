package cacheproxy

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"still-wanna-dance/internal/applog"
)

var errBlockUnavailable = fmt.Errorf("block is no longer available: %w", os.ErrNotExist)

// blockFiles owns immutable, complete blocks for one content identity. The
// engine, current execution and old readers hold separate resource references;
// none of those references alone prevents reclaiming an unprotected block.
// A pinned block/snapshot is a necessary I/O reference and prevents deletion.
type blockFiles struct {
	blockSize    int64 // Retained with the files so readers/snapshots share their geometry.
	mu           sync.Mutex
	dir          string
	size         int64
	log          *slog.Logger
	files        map[int64]*localBlockFile
	refs         int
	closed       bool
	capture      bool         // One shared flight can capture the complete set.
	publication  *blockReader // Owned by that flight until taken for publication.
	onDiskChange func(int64)  // File lengths, including retired files until deletion.
}

type localBlockFile struct {
	path    string
	size    int64
	pins    int
	retired bool
}

func newBlockFiles(dir string, size, blockSize int64, log *slog.Logger) *blockFiles {
	return &blockFiles{blockSize: blockSize, dir: dir, size: size, log: log, files: make(map[int64]*localBlockFile), refs: 1}
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
	var remove []*localBlockFile
	if f.refs == 0 {
		f.closed = true
		remove = f.retireLocked(func(int64) bool { return true })
	}
	f.mu.Unlock()
	f.removeFiles(remove)
}

// emptyAndUnshared allows the registry to drop its last resource reference.
// The caller must prevent new registry acquisitions until it detaches the entry.
// Retired files may still have I/O pins; those independently delay deletion.
func (f *blockFiles) emptyAndUnshared() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.files) == 0 && f.refs == 1
}

// store never exposes a short write or partially received block. Ordinary
// cached file I/O deliberately leaves cache residency/writeback to the OS.
func (f *blockFiles) store(block int64, data []byte) error {
	if size := blockBytes(f.size, block, f.blockSize); size == 0 || int64(len(data)) != size {
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
	b := &localBlockFile{path: file.Name()}
	kept := false
	defer func() {
		if !kept {
			f.removeFiles([]*localBlockFile{b})
		}
	}()
	n, writeErr := file.Write(data)
	b.size = int64(n)
	if f.onDiskChange != nil {
		f.onDiskChange(b.size)
	}
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
	f.files[block] = b
	kept = true
	f.captureCompleteLocked()
	return nil
}

func (f *blockFiles) has(block int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[block] != nil
}

func (f *blockFiles) availableBlocks() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	blocks := make([]int64, 0, len(f.files))
	for block := range f.files {
		blocks = append(blocks, block)
	}
	return blocks
}

func (f *blockFiles) complete() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completeLocked()
}

func (f *blockFiles) completeLocked() bool {
	return int64(len(f.files)) == blockCount(f.size, f.blockSize)
}

func (f *blockFiles) bytes() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := int64(len(f.files))
	// Every committed block has a full block length except a possible short tail.
	if tail := f.files[blockCount(f.size, f.blockSize)-1]; tail != nil {
		return (count-1)*f.blockSize + tail.size
	}
	return count * f.blockSize
}

func (f *blockFiles) available(offset int64) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if offset < 0 || offset >= f.size {
		return offset
	}
	block := offset / f.blockSize
	end := offset
	for f.files[block] != nil {
		end = block*f.blockSize + f.files[block].size
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
		return nil, errBlockUnavailable
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
		f.removeFiles([]*localBlockFile{b})
	}
}

// Reclamation detaches an identity atomically with its protection decision.
// Disk removal happens after the caller releases the protection lock. A later
// store has its own unique path, so stale cleanup cannot delete replacement data.
func (f *blockFiles) retire(eligible func(int64) bool) []*localBlockFile {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Readers joining a publishing flight still use its spool. Keep the
	// captured set available until publication hands them a terminal result.
	if f.publication != nil {
		return nil
	}
	return f.retireLocked(eligible)
}

func (f *blockFiles) retireLocked(eligible func(int64) bool) []*localBlockFile {
	var remove []*localBlockFile
	for block, b := range f.files {
		if !eligible(block) {
			continue
		}
		delete(f.files, block)
		b.retired = true
		if b.pins == 0 {
			remove = append(remove, b)
		}
	}
	return remove
}

func (f *blockFiles) removeFiles(files []*localBlockFile) {
	for _, b := range files {
		if err := os.Remove(b.path); err != nil && !os.IsNotExist(err) {
			if f.log != nil {
				f.log.Warn("临时视频块清理失败", "event_name", "temporary_cleanup_failed", "error", applog.SafeError(err))
			}
			continue
		}
		if f.onDiskChange != nil {
			f.onDiskChange(-b.size)
		}
	}
}

// Capture already-complete reused data or pin the complete set in the same
// critical section as its last write, before reclamation can detach a block.
func (f *blockFiles) beginPublicationCapture() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capture = true
	f.captureCompleteLocked()
}

func (f *blockFiles) captureCompleteLocked() {
	if f.capture && f.publication == nil && f.completeLocked() {
		f.publication = f.snapshotLocked()
	}
}

func (f *blockFiles) publicationSnapshot() *blockReader {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.publication
}

func (f *blockFiles) takePublicationSnapshot() *blockReader {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot := f.publication
	f.capture, f.publication = false, nil
	return snapshot
}

func (f *blockFiles) snapshotLocked() *blockReader {
	s := &blockReader{owner: f, blocks: make([]*localBlockFile, blockCount(f.size, f.blockSize))}
	for block := range s.blocks {
		b := f.files[int64(block)]
		b.pins++
		s.blocks[block] = b
	}
	return s
}
