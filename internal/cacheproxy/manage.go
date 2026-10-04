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
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type CacheSong struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Current bool   `json:"current"`
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
type CachePage struct {
	StorageID string       `json:"storageID"`
	Entries   []CacheEntry `json:"entries"`
	Total     int          `json:"total"`
}
type CacheSelection struct {
	Key   string `json:"key"`
	Stamp string `json:"stamp"`
}
type CacheRemoval struct {
	Key    string `json:"key"`
	Result string `json:"result"`
}

func CacheStorageID(root string) string {
	h := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(h[:])
}

// Refuse redirected directories as well as symlink files. No caller supplies paths.
func CacheDirectory(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "videos")
	// Inspect every component without following links. Comparing EvalSymlinks
	// strings also rejects ordinary Windows 8.3 aliases (such as RUNNER~1).
	for path := dir; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("inspect cache directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("缓存目录包含符号链接或目录重定向")
		}
		if !info.IsDir() {
			return "", errors.New("不是普通缓存目录")
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	return dir, nil
}

func cacheReadDB(root string) (*sql.DB, error) {
	path := filepath.Join(root, "stepstash.sqlite")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("缓存数据库不是普通文件")
	}
	path = filepath.ToSlash(path)
	if len(path) > 1 && path[1] == ':' {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(1000)"}
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func readCacheEntry(ctx context.Context, root, key string, db *sql.DB) (CacheEntry, error) {
	e := CacheEntry{Key: key, Songs: []CacheSong{}, State: "未知文件 · 未校验"}
	if !cacheVideoName.MatchString(key + ".mp4") {
		return e, errors.New("无效资源标识")
	}
	path := filepath.Join(root, "videos", key+".mp4")
	info, err := os.Lstat(path)
	if err != nil {
		return e, err
	}
	if !info.Mode().IsRegular() {
		return e, errors.New("不是普通缓存文件")
	}
	e.Bytes = info.Size()
	var expected int64
	var songs, associations string
	if db != nil {
		err = db.QueryRowContext(ctx, `SELECT file_bytes,
 max(COALESCE((SELECT last_requested_at FROM resource_usage WHERE resource_key=v.version_key),0),
 COALESCE((SELECT max(requested_at) FROM request_events WHERE resource_key=v.version_key AND source='http'),0)),
 (SELECT count(*) FROM song_videos WHERE version_key=v.version_key),
 (SELECT json_group_array(json_object('id',song_id,'title',title,'current',is_current)) FROM
  (SELECT s.song_id,substr(s.title,1,300) title,EXISTS(SELECT 1 FROM current_videos c WHERE c.song_id=s.song_id AND c.version_key=v.version_key) is_current
   FROM song_videos sv JOIN songs s ON s.song_id=sv.song_id WHERE sv.version_key=v.version_key ORDER BY s.song_id LIMIT 20)),
 (SELECT COALESCE(group_concat(song_id || ':' || is_current),'') FROM
  (SELECT sv.song_id,EXISTS(SELECT 1 FROM current_videos c WHERE c.song_id=sv.song_id AND c.version_key=v.version_key) is_current
   FROM song_videos sv WHERE sv.version_key=v.version_key ORDER BY sv.song_id))
 FROM video_versions v WHERE version_key=?`, key).Scan(&expected, &e.LastRequest, &e.SongCount, &songs, &associations)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return e, fmt.Errorf("read cache metadata: %w", err)
		}
		if err == nil {
			e.Known = true
			// SQLite json_object emits numeric booleans.
			var raw []struct {
				ID      string `json:"id"`
				Title   string `json:"title"`
				Current int    `json:"current"`
			}
			if err = json.Unmarshal([]byte(songs), &raw); err != nil {
				return e, err
			}
			for _, s := range raw {
				e.Songs = append(e.Songs, CacheSong{s.ID, s.Title, s.Current != 0})
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
		return e, fmt.Errorf("read cache identity: %w", err)
	}
	if identity == "" {
		return e, errors.New("平台无法识别文件身份")
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d|%d|%d|%s", root, key, identity, info.Size(), info.ModTime().UnixNano(), info.Mode(), associations)))
	e.Stamp = hex.EncodeToString(hash[:])
	return e, nil
}

// Local metadata only. Response and song associations are bounded; enumeration
// and sorting inspect directory metadata, not video bodies or upstream catalogs.
func ReadCachePage(ctx context.Context, root, query, order string, offset int) (CachePage, error) {
	root, err := filepath.Abs(root)
	result := CachePage{StorageID: CacheStorageID(root), Entries: []CacheEntry{}}
	if err != nil {
		return result, err
	}
	if offset < 0 {
		return result, errors.New("无效分页")
	}
	dir, err := CacheDirectory(root)
	if errors.Is(err, os.ErrNotExist) {
		if _, dbErr := os.Lstat(filepath.Join(root, "stepstash.sqlite")); errors.Is(dbErr, os.ErrNotExist) {
			return result, nil
		}
		return result, err
	}
	if err != nil {
		return result, err
	}
	db, err := cacheReadDB(root)
	if err != nil && !os.IsNotExist(err) {
		return result, err
	}
	if db != nil {
		defer db.Close()
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return result, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	var candidates []cacheListCandidate
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !cacheVideoName.MatchString(f.Name()) || f.Type()&os.ModeSymlink != 0 || f.IsDir() {
			continue
		}
		info, err := f.Info()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, err
		}
		if !info.Mode().IsRegular() {
			return result, errors.New("不是普通缓存文件")
		}
		key := strings.TrimSuffix(f.Name(), ".mp4")
		candidates = append(candidates, cacheListCandidate{
			key: key, bytes: info.Size(), match: query == "" || strings.Contains(key, query),
		})
	}
	if err := loadCacheListMetadata(ctx, db, candidates, query, order); err != nil {
		return result, err
	}
	candidates = sortCacheList(candidates, order)
	result.Total = len(candidates)
	// Full song details and identity stamps are needed only for the visible page.
	// Re-read each selected file; directory metadata may have changed meanwhile.
	for i := offset; i < len(candidates) && len(result.Entries) < 50; i++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		e, err := readCacheEntry(ctx, root, candidates[i].key, db)
		if os.IsNotExist(err) {
			result.Total--
			continue
		}
		if err != nil {
			return result, err
		}
		result.Entries = append(result.Entries, e)
	}
	return result, nil
}

func (s *Server) MarkCacheProtection(page *CachePage) {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	for i := range page.Entries {
		e := &page.Entries[i]
		e.Protected = s.versionPins[e.Key] > 0 || s.queueReservedLocked(e.Key) || s.deletingVideos[e.Key] != nil
	}
}

func (s *Server) DeleteCache(ctx context.Context, selected []CacheSelection) ([]CacheRemoval, error) {
	if !s.beginRequest() {
		return nil, context.Canceled
	}
	defer s.wg.Done()
	return s.deleteCache(ctx, selected)
}

// Offline maintenance shares the engine removal implementation and exclusive
// store lock, but starts no service, cleanup worker, network or usage pruning.
func DeleteCacheOffline(ctx context.Context, root string, selected []CacheSelection) ([]CacheRemoval, error) {
	if len(selected) < 1 || len(selected) > 50 {
		return nil, errors.New("每次请选择 1～50 个缓存")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err = CacheDirectory(root); err != nil {
		return nil, err
	}
	for _, name := range []string{".lock", "verification.sqlite"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil && !info.Mode().IsRegular() {
			return nil, errors.New("缓存管理文件不是普通文件")
		}
	}
	unlock, err := lockDirectory(filepath.Join(root, ".lock"))
	if err != nil {
		return nil, errors.New("缓存目录正由其他进程使用")
	}
	defer unlock()
	store, err := openVerificationStore(root)
	if err != nil {
		return nil, err
	}
	defer store.db.Close()
	s := &Server{cfg: Config{StorageDir: root, Logger: slog.Default()}, verifications: store}
	return s.deleteCache(ctx, selected)
}

func (s *Server) deleteCache(ctx context.Context, selected []CacheSelection) ([]CacheRemoval, error) {
	if len(selected) < 1 || len(selected) > 50 {
		return nil, errors.New("每次请选择 1～50 个缓存")
	}
	if _, err := CacheDirectory(s.cfg.StorageDir); err != nil {
		return nil, err
	}
	db, err := cacheReadDB(s.cfg.StorageDir)
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
	if s.queueReservedLocked(item.Key) || !s.beginVideoRemovalLocked(item.Key) {
		s.retentionMu.Unlock()
		return "protected"
	}
	s.retentionMu.Unlock()
	removed := false
	defer func() { s.finishVideoRemoval(item.Key, removed) }()
	// Independent local scanners share this gate. Do not wait for a check.
	slot := verificationLocks.slot(s.cfg.StorageDir + item.Key)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	default:
		return "protected"
	}
	if _, err := CacheDirectory(s.cfg.StorageDir); err != nil {
		return "unsafe"
	}
	e, err := readCacheEntry(ctx, s.cfg.StorageDir, item.Key, db)
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
	if err = s.verifications.forget(ctx, item.Key); err != nil {
		return "failed"
	}
	if err = os.Remove(s.cfg.videoFile(item.Key)); err != nil {
		return "failed"
	}
	removed = true
	s.cfg.Logger.Info("cache_manually_removed", "key", item.Key, "bytes", e.Bytes)
	return "deleted"
}

func CacheFile(ctx context.Context, root string, item CacheSelection) (string, error) {
	if _, err := CacheDirectory(root); err != nil {
		return "", err
	}
	db, err := cacheReadDB(root)
	if err != nil {
		return "", err
	}
	defer db.Close()
	e, err := readCacheEntry(ctx, root, item.Key, db)
	if err != nil {
		return "", err
	}
	if !e.Known || e.Stamp != item.Stamp {
		return "", errors.New("缓存条目已变化，请刷新")
	}
	return filepath.Join(root, "videos", item.Key+".mp4"), nil
}
