package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"still-wanna-dance/internal/workfiles"
)

const maxSongCoverBytes = 1 << 20

// Only an authenticated cover-view page demand enters this cache. It has no
// scheduler, channel candidates, probes or background refresh.
type songCoverCache struct {
	mu      sync.Mutex
	pending map[string]*songCoverLoad
	checked map[string]struct{} // Completed upstream attempts in this program session.
	slots   chan struct{}
	closed  bool
	cancel  map[string]context.CancelFunc
	wg      sync.WaitGroup
}

type songCoverLoad struct {
	done   chan struct{}
	data   []byte
	status int
	err    error
	retry  bool // The owner was canceled; a live waiter can take over.
}

func (s *songCoverCache) read(ctx context.Context, client *http.Client, dir, id, mode string) ([]byte, int, error) {
	// Published files are atomic and can be displayed even while another tab
	// is waiting for this same image's update check to finish.
	if mode != "refresh" {
		data, err := readSongCover(dir, id)
		if err != nil {
			return nil, 500, err
		}
		if len(data) > 0 {
			return data, 200, nil
		}
		if mode == "cached" {
			return nil, 404, errors.New("暂无封面")
		}
	}
	// Share the result of an overlapping request, including upstream failures.
	// A canceled owner lets another still-visible caller make its own request.
	for {
		s.mu.Lock()
		if s.closed || ctx.Err() != nil {
			s.mu.Unlock()
			return nil, 503, context.Canceled
		}
		if pending := s.pending[id]; pending != nil {
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, 503, ctx.Err()
			case <-pending.done:
				if err := ctx.Err(); err != nil {
					return nil, 503, err
				}
				if pending.retry {
					continue
				}
				return pending.data, pending.status, pending.err
			}
		}
		if s.pending == nil {
			s.pending = make(map[string]*songCoverLoad)
			s.cancel = make(map[string]context.CancelFunc)
			s.slots = make(chan struct{}, 4)
		}
		loadCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		pending := &songCoverLoad{done: make(chan struct{})}
		s.pending[id], s.cancel[id] = pending, cancel
		s.wg.Add(1)
		s.mu.Unlock()
		defer func() {
			cancel()
			s.mu.Lock()
			close(pending.done)
			delete(s.pending, id)
			delete(s.cancel, id)
			s.mu.Unlock()
			s.wg.Done()
		}()
		pending.data, pending.status, pending.err = s.load(loadCtx, client, dir, id, mode)
		pending.retry = loadCtx.Err() != nil
		return pending.data, pending.status, pending.err
	}
}

func (s *songCoverCache) close() {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.cancel {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func validSongCover(data []byte) bool {
	if len(data) == 0 || len(data) > maxSongCoverBytes {
		return false
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width > 2048 || config.Height > 2048 {
		return false
	}
	_, err = jpeg.Decode(bytes.NewReader(data))
	return err == nil
}

func readSongCover(dir, id string) ([]byte, error) {
	if err := workfiles.CheckFutureDir(dir); err != nil {
		return nil, errors.New("封面缓存目录不可用")
	}
	// OpenRoot confines file access to the cache. Reject links and non-files.
	if root, err := os.OpenRoot(dir); err == nil {
		defer root.Close()
		info, statErr := root.Lstat(id + ".jpg")
		if statErr == nil && !workfiles.Regular(info) {
			return nil, errors.New("封面缓存文件不可用")
		}
		if statErr == nil && info.Size() <= maxSongCoverBytes {
			file, err := root.Open(id + ".jpg")
			if err != nil {
				return nil, errors.New("无法读取封面缓存")
			}
			data, err := io.ReadAll(io.LimitReader(file, maxSongCoverBytes+1))
			file.Close()
			if err != nil {
				return nil, errors.New("无法读取封面缓存")
			}
			if validSongCover(data) {
				return data, nil
			}
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return nil, errors.New("无法读取封面缓存")
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("封面缓存目录不可用")
	}
	return nil, nil
}

func (s *songCoverCache) load(ctx context.Context, client *http.Client, dir, id, mode string) ([]byte, int, error) {
	filename := filepath.Join(dir, id+".jpg")
	cached, err := readSongCover(dir, id)
	if err != nil {
		return nil, 500, err
	}
	if mode != "refresh" && len(cached) > 0 {
		return cached, 200, nil
	}
	if mode == "refresh" && len(cached) > 0 {
		s.mu.Lock()
		_, checked := s.checked[id]
		s.mu.Unlock()
		if checked {
			return cached, 200, nil
		}
	}
	if mode == "cached" {
		return nil, 404, errors.New("暂无封面")
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, 503, ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://aya.kiva.moe/images/small"+id+".jpg", nil)
	if err != nil {
		return nil, 502, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	// A completed download is already fresh. Failed update checks also count,
	// keeping the old image for this session. Canceled work and missing files
	// remain eligible for a later demand; this state is never persisted.
	defer func() {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if s.checked == nil {
			s.checked = make(map[string]struct{})
		}
		s.checked[id] = struct{}{}
		s.mu.Unlock()
	}()
	// The active client's ordinary RoundTripper uses the built-in DNS or the
	// selected proxy upstream. Do not ask its video channel pool for candidates.
	resp, err := client.Do(req)
	if err != nil {
		return nil, 502, errors.New("暂时无法获取封面")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, 404, errors.New("暂无封面")
	}
	if resp.StatusCode != 200 {
		return nil, 502, errors.New("暂时无法获取封面")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSongCoverBytes+1))
	if err != nil || !validSongCover(data) {
		return nil, 502, errors.New("封面图片无效")
	}
	if err := ctx.Err(); err != nil {
		return nil, 503, err
	}
	if bytes.Equal(data, cached) {
		return cached, 200, nil
	}
	// Same-ID writes are serialized under the configuration lease. Reclaim only
	// this image's known staging slot left by an interrupted previous process.
	if _, err := workfiles.Clean(filename); err != nil {
		return nil, 500, errors.New("无法保存封面缓存")
	}
	if err := workfiles.Write(filename, data); err != nil {
		return nil, 500, errors.New("无法保存封面缓存")
	}
	return data, 200, nil
}

func (c *Console) songCoverAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	query := r.URL.Query()
	id := query.Get("id")
	n, err := strconv.ParseInt(id, 10, 64)
	mode := query.Get("mode")
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id || len(query) > 2 || len(query["id"]) != 1 || len(query["mode"]) > 1 || mode != "" && mode != "cached" && mode != "refresh" || len(query) == 2 && query["mode"] == nil {
		http.Error(w, "无效歌曲 ID", 400)
		return
	}
	c.mu.Lock()
	client, closing := c.client, c.closing
	c.mu.Unlock()
	if closing || client == nil {
		http.Error(w, "程序正在退出", 503)
		return
	}
	data, status, err := c.covers.read(r.Context(), client, filepath.Join(c.files().Root, "data", "covers"), id, mode)
	if err != nil {
		// Optional artwork failure is local to the card, never a desktop fault.
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Write(data)
}

// A local-only batch inspection lets the cover view order its explicit page
// demands. It never creates the cache directory or contacts an upstream.
func (c *Console) songCoverStateAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	q := r.URL.Query()
	ids := strings.Split(q.Get("ids"), ",")
	if len(q) != 1 || len(q["ids"]) != 1 || len(ids) > 100 {
		http.Error(w, "无效歌曲 ID", 400)
		return
	}
	cached := make([]string, 0, len(ids))
	refresh := make([]string, 0, len(ids))
	dir := filepath.Join(c.files().Root, "data", "covers")
	for _, id := range ids {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
			http.Error(w, "无效歌曲 ID", 400)
			return
		}
	}
	for _, id := range ids {
		data, err := readSongCover(dir, id)
		// A bad cache entry must not prevent displaying other valid images.
		// Its individual image request will report the actual local error.
		if err == nil && len(data) > 0 {
			cached = append(cached, id)
			c.covers.mu.Lock()
			_, checked := c.covers.checked[id]
			c.covers.mu.Unlock()
			if !checked {
				refresh = append(refresh, id)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(struct {
		Cached  []string `json:"cached"`
		Refresh []string `json:"refresh"`
	}{cached, refresh})
}
