// Package applog provides bounded, synchronous application logs without dependencies.
package applog

import (
	"errors"
	"os"
	"sync"
	"time"
)

const MaxBytes int64 = 1 << 20
const MaxTotalBytes int64 = 20 << 20

// Writer must have one process owner (the console's config lock supplies this).
type Writer struct {
	mu          sync.Mutex
	root        *os.Root
	name        string
	file        *os.File
	size, limit int64
	budget      int64
	closed      bool
	onError     func(error)
	reported    sync.Once
}

func Open(dir string, limit, budget int64, onError func(error)) (*Writer, error) {
	if limit <= 0 || limit > MaxBytes || budget < limit {
		return nil, errors.New("invalid log rotation limits")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	w := &Writer{root: root, limit: limit, budget: budget, onError: onError}
	if err := w.open(true); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

func (w *Writer) open(reuse bool) error {
	files, err := logSegments(w.root)
	if err != nil {
		return err
	}
	if reuse && len(files) != 0 {
		last := files[len(files)-1]
		if _, _, ok := segmentID(last.name); ok && last.info.Size() < w.limit {
			w.file, err = w.root.OpenFile(last.name, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			w.name, w.size = last.name, last.info.Size()
		}
	}
	if w.file == nil {
		at := time.Now().UTC().Truncate(time.Second)
		var sequence uint64
		if len(files) != 0 {
			if last, previous, ok := segmentID(files[len(files)-1].name); ok && !at.After(last) {
				if previous == ^uint64(0) {
					return errors.New("log segment sequence exhausted")
				}
				at, sequence = last, previous+1
			}
		}
		for {
			w.name = segmentName(at, sequence)
			w.file, err = w.root.OpenFile(w.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if !os.IsExist(err) {
				break
			}
			if sequence == ^uint64(0) {
				return errors.New("log segment sequence exhausted")
			}
			sequence++
		}
		if err != nil {
			return err
		}
		w.size = 0
	}
	// Reserve a full current segment, so ordinary appends need no directory scan.
	total := w.limit
	for _, file := range files {
		if file.name != w.name {
			total += file.info.Size()
		}
	}
	for _, file := range files {
		if total <= w.budget {
			break
		}
		if file.name == w.name {
			continue
		}
		if err := w.root.Remove(file.name); err != nil && !os.IsNotExist(err) {
			w.file.Close()
			w.file = nil
			return err
		}
		total -= file.info.Size()
	}
	return nil
}

func (w *Writer) rotate() error {
	err := w.file.Close()
	w.file = nil
	if err != nil {
		return err
	}
	return w.open(false)
}

func (w *Writer) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer func() {
		w.mu.Unlock()
		if err != nil && w.onError != nil {
			w.reported.Do(func() { w.onError(err) })
		}
	}()
	if w.closed {
		return 0, os.ErrClosed
	}
	// Never split a JSON record or allow an oversized entry to bypass the cap.
	if int64(len(p)) > w.limit {
		return 0, errors.New("log record exceeds file size limit")
	}
	if w.file == nil {
		if err = w.open(true); err != nil {
			return 0, err
		}
	}
	if w.size+int64(len(p)) > w.limit {
		if err = w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err = w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var err error
	if w.file != nil {
		err = w.file.Close()
		w.file = nil
	}
	return errors.Join(err, w.root.Close())
}
