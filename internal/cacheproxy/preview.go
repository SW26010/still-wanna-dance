package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// PreviewTarget is the click-time resource snapshot. AdmissionSongID is only
// for checking song policy, never for scoring, mapping updates or queue timing.
type PreviewTarget struct {
	Resource        string `json:"resource"`
	Size            int64  `json:"size,omitempty"`
	AdmissionSongID string `json:"songID,omitempty"`
}

// PreparePreview reads local metadata only: no engine, pin, download or API call.
func (s *Store) PreparePreview(ctx context.Context, paths LibraryPaths, input DownloadTarget) (PreviewTarget, error) {
	if (input.SongID == "") == (input.Resource == "") {
		return PreviewTarget{}, errors.New("请选择一首歌曲或一个资源")
	}
	if !s.beginRequest() {
		return PreviewTarget{}, context.Canceled
	}
	defer s.wg.Done()
	target := PreviewTarget{Resource: input.Resource, AdmissionSongID: input.SongID}
	if input.SongID != "" {
		id, err := strconv.ParseInt(input.SongID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != input.SongID {
			return PreviewTarget{}, errors.New("无效歌曲 ID")
		}
		if err := s.CheckSongAllowed(ctx, id); err != nil {
			return PreviewTarget{}, err
		}
		err = s.db.QueryRowContext(ctx, "SELECT md5 FROM song_media WHERE song_id=?", id).Scan(&target.Resource)
		if errors.Is(err, sql.ErrNoRows) {
			return PreviewTarget{}, errors.New("当前尚不知道这首歌曲对应的资源，请先更新曲库")
		}
		if err != nil {
			return PreviewTarget{}, s.storageError(err)
		}
	}
	return s.PreviewMetadata(ctx, paths, target)
}

// PreviewMetadata rechecks admission and the fixed resource without following a
// changed song mapping. A missing size remains unknown until GET resolves it.
func (s *Store) PreviewMetadata(ctx context.Context, paths LibraryPaths, target PreviewTarget) (PreviewTarget, error) {
	if !validMD5(target.Resource) || target.Size < 0 {
		return PreviewTarget{}, errors.New("无效资源")
	}
	if !s.beginRequest() {
		return PreviewTarget{}, context.Canceled
	}
	defer s.wg.Done()
	if target.AdmissionSongID != "" {
		id, err := strconv.ParseInt(target.AdmissionSongID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != target.AdmissionSongID {
			return PreviewTarget{}, errors.New("无效歌曲 ID")
		}
		if err := s.CheckSongAllowed(ctx, id); err != nil {
			return PreviewTarget{}, err
		}
	}
	var size sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT byte_size FROM media WHERE md5=?", target.Resource).Scan(&size)
	known := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PreviewTarget{}, s.storageError(err)
	}
	info, statErr := os.Stat(filepath.Join(paths.VideoDir, target.Resource+".mp4"))
	if statErr == nil && info.Mode().IsRegular() {
		known = true
		if size.Valid && size.Int64 != info.Size() {
			return PreviewTarget{}, errors.New("本地视频大小与资源记录不一致")
		}
		size = sql.NullInt64{Int64: info.Size(), Valid: true}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return PreviewTarget{}, statErr
	}
	if !known {
		return PreviewTarget{}, errors.New("资源已不存在，请刷新曲库")
	}
	if size.Valid {
		if target.Size != 0 && target.Size != size.Int64 {
			return PreviewTarget{}, errors.New("资源大小已变化，请重新预览")
		}
		target.Size = size.Int64
	}
	return target, nil
}

// ServePreview shares GET serving and demand ownership with game playback.
// Its caller authenticates the local media capability; HEAD uses metadata only.
func (s *Server) ServePreview(w http.ResponseWriter, r *http.Request, target PreviewTarget) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	checked, err := s.PreviewMetadata(r.Context(), s.cfg.libraryPaths(), target)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrSongBlocked) {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.serveVideo(w, r, &previewRequest{target: checked})
}

// Resolver state belongs to this request. In particular, its API lookup song
// must not become video.songID or a property of a shared download.
type previewRequest struct {
	target    PreviewTarget
	addresses *resourceAddresses
}

func (p *previewRequest) video(ctx context.Context, s *Server) (video, error) {
	v := video{key: p.target.Resource, size: p.target.Size}
	if shared, ok := s.previewRoute(v); ok {
		v = shared
		p.target.Size = v.size
	}
	if v.size <= 0 {
		return p.nextAddress(ctx, s, v)
	}
	if v.size > s.cfg.MaxFileBytes {
		return video{}, errors.New("资源超过允许的大小")
	}
	if info, err := os.Stat(s.cfg.videoFile(v.key)); err == nil && info.Mode().IsRegular() && info.Size() == v.size {
		v.localOnly = true
	}
	return v, nil
}

func (s *Server) previewRoute(v video) (video, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flights[v.key]
	if f != nil && !f.preempted && (v.size == 0 || f.size == v.size) && f.route.host != "" {
		return f.route, true
	}
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	if local := s.localResources[v.key]; local != nil && (v.size == 0 || local.video.size == v.size) {
		return resourceVideo(local.video), true
	}
	return video{}, false
}

func (p *previewRequest) nextAddress(ctx context.Context, s *Server, failed video) (video, error) {
	if p.addresses == nil {
		p.addresses = &resourceAddresses{key: p.target.Resource, size: p.target.Size}
	}
	v, err := p.addresses.next(ctx, s, failed)
	if err == nil {
		p.target.Size = v.size
	}
	return v, err
}

// HEAD is intentionally outside ServeHTTP: that legacy game path registers a
// whole-resource demand even for HEAD. No engine is needed for preview metadata.
func PreviewHeaders(w http.ResponseWriter, target PreviewTarget) {
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("ETag", `"`+target.Resource+`"`)
	w.Header().Set("Accept-Ranges", "bytes")
	if target.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(target.Size, 10))
	}
}
