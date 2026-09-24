package cacheproxy

import (
	"context"
	"crypto/md5" // The observed protocol supplies MD5; this is integrity checking, not authentication.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

var errBusy = errors.New("download capacity reached")

func (s *Server) obtain(ctx context.Context, v video) (*flight, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	f := s.flights[v.key]
	if f == nil {
		// Bound all workers (including validation); callers sharing a key do not consume another slot.
		select {
		case s.slots <- struct{}{}:
		default:
			s.mu.Unlock()
			return nil, errBusy
		}
		f = &flight{done: make(chan struct{})}
		s.flights[v.key] = f
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			workerCtx, cancel := context.WithTimeout(s.ctx, s.cfg.DownloadTimeout)
			defer cancel()
			f.path, f.source, f.err = s.prepare(workerCtx, v)
			s.mu.Lock()
			delete(s.flights, v.key)
			<-s.slots
			close(f.done)
			s.mu.Unlock()
		}()
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		return f, f.err
	}
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func checkFile(ctx context.Context, path string, v video) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return checkOpenFile(ctx, f, v)
}

func checkOpenFile(ctx context.Context, f *os.File, v video) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != v.size {
		return errors.New("cached size mismatch")
	}
	h := md5.New()
	if _, err := io.Copy(h, contextReader{ctx, f}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != v.checksum {
		return errors.New("cached checksum mismatch")
	}
	return nil
}

func (s *Server) prepare(ctx context.Context, v video) (string, string, error) {
	libraryPath := filepath.Join(s.cfg.SongsDir, v.id, "video.mp4")
	if err := checkFile(ctx, libraryPath, v); err == nil {
		s.inspectMetadata(v)
		return libraryPath, "HIT", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		s.cfg.Logger.Warn("library_mismatch", "song_id", v.id, "error", err)
	}
	path := filepath.Join(s.cfg.CacheDir, v.key+".mp4")
	if err := checkFile(ctx, path, v); err == nil {
		if err := s.publishLibrary(ctx, path, v); err != nil {
			return "", "", err
		}
		return path, "HIT", nil
	} else if ctx.Err() != nil {
		return "", "", ctx.Err()
	} else if !errors.Is(err, os.ErrNotExist) {
		s.cfg.Logger.Warn("cache_invalid", "key", v.key, "error", err)
		if err := os.Remove(path); err != nil {
			return "", "", err
		}
	}
	s.cfg.Logger.Info("download_started", "key", v.key, "host", v.host, "size", v.size)
	// Dial the configured origin while preserving the externally observed Host.
	u := &url.URL{Scheme: "http", Host: s.cfg.Origins[v.host], Path: v.path, RawQuery: v.query}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Host = v.host
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
		return "", "", errors.New("unexpected upstream encoding")
	}
	if resp.ContentLength >= 0 && resp.ContentLength != v.size {
		return "", "", errors.New("upstream content length mismatch")
	}
	if err := s.publish(ctx, resp.Body, path, v); err != nil {
		return "", "", err
	}
	if err := s.publishLibrary(ctx, path, v); err != nil {
		return "", "", err
	}
	s.cfg.Logger.Info("download_published", "key", v.key, "size", v.size)
	return path, "MISS", nil
}

func (s *Server) publish(ctx context.Context, src io.Reader, path string, v video) error {
	f, err := os.CreateTemp(s.cfg.CacheDir, "download-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := md5.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, src}, v.size+1))
	if err != nil {
		return fmt.Errorf("download read/write: %w", err)
	}
	if n != v.size || hex.EncodeToString(h.Sum(nil)) != v.checksum {
		return errors.New("download integrity mismatch")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
