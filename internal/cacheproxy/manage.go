package cacheproxy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

type CacheSong struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type CacheEntry struct {
	Key         string      `json:"key"`
	Stamp       string      `json:"stamp"`
	Bytes       int64       `json:"bytes"`
	LastRequest int64       `json:"lastRequest"`
	Songs       []CacheSong `json:"songs"`
	SongCount   int         `json:"songCount"`
	State       string      `json:"state"`
	Protected   bool        `json:"protected"`
	Known       bool        `json:"known"`
}
type CacheSelection struct {
	Key   string `json:"key"`
	Stamp string `json:"stamp"`
}
type CacheRemoval struct {
	Key    string `json:"key"`
	Result string `json:"result"`
}

func readCacheEntry(ctx context.Context, paths LibraryPaths, key string, db *sql.DB) (CacheEntry, error) {
	e := CacheEntry{Key: key, Songs: []CacheSong{}, State: "未知文件 · 未校验"}
	if !cacheVideoName.MatchString(key + ".mp4") {
		return e, errors.New("无效资源标识")
	}
	path := filepath.Join(paths.VideoDir, key+".mp4")
	info, err := os.Lstat(path)
	if err != nil {
		return e, err
	}
	if !info.Mode().IsRegular() {
		return e, errors.New("不是普通本地视频文件")
	}
	e.Bytes = info.Size()
	var expected int64
	var songs, associations string
	if db != nil {
		err = db.QueryRowContext(ctx, `SELECT COALESCE(byte_size,0),
 max(COALESCE((SELECT last_requested_at FROM media_access WHERE md5=v.md5),0),
 COALESCE((SELECT max(requested_at) FROM request_events WHERE resource_md5=v.md5),0)),
 (SELECT count(*) FROM song_media WHERE md5=v.md5),
 (SELECT json_group_array(json_object('id',song_id,'title',title)) FROM
  (SELECT CAST(s.song_id AS TEXT) song_id,COALESCE(substr(s.name,1,300),'') title
   FROM song_media sv JOIN songs s ON s.song_id=sv.song_id WHERE sv.md5=v.md5 ORDER BY s.song_id LIMIT 20)),
 (SELECT COALESCE(group_concat(song_id),'') FROM
  (SELECT sv.song_id
   FROM song_media sv WHERE sv.md5=v.md5 ORDER BY sv.song_id))
 FROM media v WHERE md5=?`, key).Scan(&expected, &e.LastRequest, &e.SongCount, &songs, &associations)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return e, fmt.Errorf("read local video metadata: %w", err)
		}
		if err == nil {
			e.Known = true
			if err = json.Unmarshal([]byte(songs), &e.Songs); err != nil {
				return e, err
			}
			e.State = "文件存在 · 按 MD5 复用"
			if expected > 0 && e.Bytes != expected {
				e.State = "大小与记录不同 · 本地文件按不可变内容使用"
			}
		}
	}
	// Only open for stable identity metadata; never read or hash video contents.
	f, err := os.Open(path)
	if err != nil {
		return e, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return e, err
	}
	if !os.SameFile(info, opened) || !sameVerifiedFile(info, opened) {
		return e, errors.New("文件已变化，请刷新")
	}
	identity, err := fileIdentity(f)
	if err != nil {
		return e, fmt.Errorf("read local video identity: %w", err)
	}
	if identity == "" {
		return e, errors.New("平台无法识别文件身份")
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d|%d|%d|%s", CacheStorageID(paths), key, identity, info.Size(), info.ModTime().UnixNano(), info.Mode(), associations)))
	e.Stamp = hex.EncodeToString(hash[:])
	return e, nil
}

func (s *Server) MarkCacheProtection(entries []*CacheEntry) {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	for _, e := range entries {
		e.Protected = s.versionPins[e.Key] > 0 || s.videoProtectedLocked(e.Key) || s.deletingVideos[e.Key] != nil
	}
}

func (s *Server) DeleteCache(ctx context.Context, selected []CacheSelection) ([]CacheRemoval, error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	return s.deleteCache(ctx, selected)
}

// Offline maintenance owns the database and video leases, starts no workers,
// and shares file-identity checks with the engine. No missing resource is created.
func DeleteCacheOffline(ctx context.Context, paths LibraryPaths, selected []CacheSelection) (result []CacheRemoval, resultErr error) {
	if len(selected) < 1 || len(selected) > 50 {
		return nil, errors.New("每次请选择 1～50 个本地视频")
	}
	paths, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(paths.DatabasePath)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
	return store.DeleteCache(ctx, paths.VideoDir, selected)
}

// DeleteCache handles explicit video deletion while the application's database
// is open and no engine is attached. It does not start retention or cleanup.
func (store *Store) DeleteCache(ctx context.Context, videoDir string, selected []CacheSelection) (result []CacheRemoval, resultErr error) {
	if !store.beginRequest() {
		return nil, context.Canceled
	}
	defer store.wg.Done()
	store.retentionRunMu.Lock()
	defer store.retentionRunMu.Unlock()
	if store.engine.Load() != nil {
		return nil, errors.New("视频引擎正在使用数据库，请通过当前引擎删除")
	}
	paths, err := (LibraryPaths{DatabasePath: store.path, VideoDir: videoDir}).Resolve()
	if err != nil {
		return nil, err
	}
	unlock, err := lockVideoDirectory(paths.VideoDir)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, unlock()) }()
	s := &Server{Store: store, cfg: Config{Store: store, VideoDir: paths.VideoDir, Logger: slog.Default()}}
	return s.deleteCache(ctx, selected)
}

func (s *Server) deleteCache(ctx context.Context, selected []CacheSelection) ([]CacheRemoval, error) {
	if len(selected) < 1 || len(selected) > 50 {
		return nil, errors.New("每次请选择 1～50 个本地视频")
	}
	if _, err := CacheDirectory(s.cfg.videosDir()); err != nil {
		return nil, err
	}
	db, err := readDatabaseFile(s.Store.path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	results := make([]CacheRemoval, 0, len(selected))
	for _, item := range selected {
		results = append(results, CacheRemoval{item.Key, s.removeCacheEntry(ctx, db, item)})
	}
	return results, nil
}

func (s *Server) removeCacheEntry(ctx context.Context, db *sql.DB, item CacheSelection) string {
	s.retentionMu.Lock()
	if s.videoProtectedLocked(item.Key) || !s.beginVideoRemovalLocked(item.Key) {
		s.retentionMu.Unlock()
		return "protected"
	}
	s.retentionMu.Unlock()
	removed := false
	defer func() { s.finishVideoRemoval(item.Key, removed) }()
	// Independent local scanners share this gate. Do not wait for a check.
	slot := verificationLocks.slot(pathIdentity(s.cfg.videosDir()) + item.Key)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	default:
		return "protected"
	}
	if _, err := CacheDirectory(s.cfg.videosDir()); err != nil {
		return "unsafe"
	}
	e, err := readCacheEntry(ctx, s.cfg.libraryPaths(), item.Key, db)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "failed"
	}
	if !e.Known {
		return "unknown"
	}
	if item.Stamp == "" || e.Stamp != item.Stamp {
		return "changed"
	}
	if ctx.Err() != nil {
		return "failed"
	}
	if err = os.Remove(s.cfg.videoFile(item.Key)); err != nil {
		return "failed"
	}
	removed = true
	s.cfg.Logger.Info("cache_manually_removed", "key", item.Key, "bytes", e.Bytes)
	return "deleted"
}

func CacheFile(ctx context.Context, paths LibraryPaths, item CacheSelection) (string, error) {
	paths, err := paths.Resolve()
	if err != nil {
		return "", err
	}
	if _, err := CacheDirectory(paths.VideoDir); err != nil {
		return "", err
	}
	db, err := readDatabaseFile(paths.DatabasePath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	e, err := readCacheEntry(ctx, paths, item.Key, db)
	if err != nil {
		return "", err
	}
	if !e.Known || e.Stamp != item.Stamp {
		return "", errors.New("本地视频条目已变化，请刷新")
	}
	return filepath.Join(paths.VideoDir, item.Key+".mp4"), nil
}
