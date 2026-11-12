package cacheproxy

import (
	"errors"
	"io"
	"os"
	"sync"
)

// blockReader owns at most one open file, reused across small reads. Ordinary
// readers pin only that file; complete snapshots pin blocks until Close. The
// caller keeps the storage reference alive for an ordinary reader's lifetime.
type blockReader struct {
	owner   *blockFiles
	blocks  []*localBlockFile // Non-nil only for a pinned, complete snapshot.
	mu      sync.Mutex        // Serializes file I/O with cancellation/Close.
	file    *os.File
	block   int64
	current *localBlockFile
	closed  bool
}

func (f *blockFiles) reader() *blockReader { return &blockReader{owner: f} }

func (r *blockReader) open(block int64) error {
	if r.file != nil && r.block == block {
		return nil
	}
	if err := r.closeFile(); err != nil {
		return err
	}
	var b *localBlockFile
	if r.blocks != nil {
		b = r.blocks[block]
	} else {
		var err error
		b, err = r.owner.pin(block)
		if err != nil {
			return err
		}
	}
	file, err := os.Open(b.path)
	if err != nil {
		if r.blocks == nil {
			r.owner.unpin(b)
		}
		return err
	}
	r.file, r.block, r.current = file, block, b
	return nil
}

// Close the handle before dropping its pin, so retired-file removal also works
// on Windows. The caller holds mu for both open and closeFile.
func (r *blockReader) closeFile() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	if r.blocks == nil {
		r.owner.unpin(r.current)
	}
	r.file, r.current = nil, nil
	return err
}

func (r *blockReader) ReadAt(p []byte, offset int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if offset < 0 {
		return 0, errors.New("negative block read offset")
	}
	n := 0
	for len(p) > 0 && offset < r.owner.size {
		if err := r.open(offset / r.owner.blockSize); err != nil {
			return n, err
		}
		within := offset % r.owner.blockSize
		length := min(int64(len(p)), r.current.size-within)
		read, err := r.file.ReadAt(p[:length], within)
		n += read
		offset += int64(read)
		p = p[read:]
		if err != nil || within+int64(read) == r.current.size {
			err = errors.Join(err, r.closeFile())
		}
		if err != nil {
			return n, err
		}
	}
	if len(p) > 0 {
		return n, io.EOF
	}
	return n, nil
}

// Seeking away must release an old block even if the next Read waits for data.
// This only supports offset bookkeeping, including after Close. Read reports
// cancellation so ServeContent does not commit a generic Seek failure first.
func (r *blockReader) seek(offset int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if offset >= r.owner.size || r.block != offset/r.owner.blockSize {
		return r.closeFile()
	}
	return nil
}

func (r *blockReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.closeFile()
	for _, b := range r.blocks {
		r.owner.unpin(b)
	}
	return err
}
