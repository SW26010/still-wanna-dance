// Package applog provides bounded, synchronous application logs without dependencies.
package applog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const MaxBytes int64 = 5 << 20
const Backups = 3

// Writer must have one process owner (the console's config lock supplies this).
type Writer struct {
	mu          sync.Mutex
	path        string
	file        *os.File
	size, limit int64
	backups     int
	closed      bool
	onError     func(error)
	reported    sync.Once
}

func Open(path string, limit int64, backups int, onError func(error)) (*Writer, error) {
	if limit <= 0 || backups < 1 {
		return nil, errors.New("invalid log rotation limits")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	w := &Writer{path: path, limit: limit, backups: backups, onError: onError}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file, w.size = f, info.Size()
	return nil
}

func (w *Writer) rotate() error {
	if err := w.file.Close(); err != nil {
		w.file = nil
		return err
	}
	w.file = nil
	oldest := fmt.Sprintf("%s.%d", w.path, w.backups)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := w.backups - 1; i >= 0; i-- {
		source := w.path
		if i > 0 {
			source = fmt.Sprintf("%s.%d", w.path, i)
		}
		if err := os.Rename(source, fmt.Sprintf("%s.%d", w.path, i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return w.open()
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
		if err = w.open(); err != nil {
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
	w.closed = true
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
