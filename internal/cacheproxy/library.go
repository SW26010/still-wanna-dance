package cacheproxy

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type LibraryQuery struct {
	View, Search, Sort, Filter string
	SongID, Resource           string // Exact object links, independent of text search.
	Offset                     int
}

type LibrarySong struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Artist    string     `json:"artist"`
	Dancer    string     `json:"dancer"`
	Group     string     `json:"group"`
	Policy    SongPolicy `json:"policy"`
	Score     float64    `json:"score"`
	HasDemand bool       `json:"hasDemand"`
	Resource  string     `json:"resource"`
}

type LibraryResource struct {
	Key       string      `json:"key"`
	Size      *int64      `json:"size"`
	Duration  *float64    `json:"duration"` // No measured duration is currently persisted.
	Songs     []CacheSong `json:"songs"`
	SongCount int         `json:"songCount"`
	Local     *CacheEntry `json:"local"`
}

type LibraryPage struct {
	DatabaseID string                      `json:"databaseID"`
	VideoError string                      `json:"videoError,omitempty"`
	StorageID  string                      `json:"storageID"`
	Songs      []LibrarySong               `json:"songs"`
	Resources  map[string]*LibraryResource `json:"resources"`
	Keys       []string                    `json:"keys"`
	Total      int                         `json:"total"`
	FileCount  int                         `json:"fileCount"`
	TotalBytes int64                       `json:"totalBytes"`
}

type libraryCandidate struct {
	LibraryResource
	saved, match bool
	title        string
	score        float64
	lastRequest  int64
}

// ReadLibraryPage reads current metadata and complete-file directory entries.
// It neither starts an engine nor probes upstream or video contents. Only the
// visible page gets file-identity stamps; shared resources are materialized once.
func ReadLibraryPage(ctx context.Context, paths LibraryPaths, q LibraryQuery) (LibraryPage, error) {
	return readLibraryPage(ctx, paths, q, time.Now().UnixMilli())
}

func readLibraryPage(ctx context.Context, paths LibraryPaths, q LibraryQuery, now int64) (LibraryPage, error) {
	paths, err := paths.Resolve()
	page := LibraryPage{StorageID: CacheStorageID(paths), DatabaseID: DatabaseID(paths.DatabasePath), Songs: []LibrarySong{}, Keys: []string{}, Resources: map[string]*LibraryResource{}}
	if err != nil {
		return page, err
	}
	if q.Offset < 0 {
		return page, errors.New("无效分页")
	}
	db, err := readDatabaseFile(paths.DatabasePath)
	if err != nil {
		return page, err
	}
	if db != nil {
		defer db.Close()
	}
	search := strings.ToLower(strings.TrimSpace(q.Search))
	resources := map[string]*libraryCandidate{}
	resource := func(key string) *libraryCandidate {
		if resources[key] == nil {
			resources[key] = &libraryCandidate{LibraryResource: LibraryResource{Key: key, Songs: []CacheSong{}}, match: search == "" || strings.Contains(key, search)}
		}
		return resources[key]
	}
	dir, err := CacheDirectory(paths.VideoDir)
	if err != nil {
		page.VideoError = err.Error()
	}
	if err == nil {
		files, err := os.ReadDir(dir)
		if err != nil {
			page.VideoError = err.Error()
		}
		for _, file := range files {
			if err := ctx.Err(); err != nil {
				return page, err
			}
			if !cacheVideoName.MatchString(file.Name()) || !file.Type().IsRegular() {
				continue
			}
			info, err := file.Info()
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return page, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			r := resource(strings.TrimSuffix(file.Name(), ".mp4"))
			size := info.Size()
			r.Size, r.saved = &size, true
			page.FileCount++
			page.TotalBytes += size
		}
	}
	if page.VideoError != "" && (q.Filter == "saved" || q.Filter == "missing") {
		return page, errors.New("视频目录不可用，无法按本地视频状态筛选：" + page.VideoError)
	}
	var songs []LibrarySong
	if db != nil {
		rows, err := db.QueryContext(ctx, `SELECT s.song_id, substr(COALESCE(s.name,s.composed_title,''),1,300),
 substr(COALESCE(s.artist,''),1,300),substr(COALESCE(s.dancer,''),1,300),substr(COALESCE(s.group_name,''),1,300),
 COALESCE(p.policy,'normal'),COALESCE(m.md5,''),v.byte_size,
 COALESCE(u.demand_count,0),COALESCE(u.demand_score,0),COALESCE(u.last_demand_at,0)
 FROM songs s LEFT JOIN song_media m ON m.song_id=s.song_id LEFT JOIN media v ON v.md5=m.md5
 LEFT JOIN song_preferences p ON p.song_id=s.song_id LEFT JOIN song_usage u ON u.song_id=s.song_id ORDER BY s.song_id`)
		if err != nil {
			return page, err
		}
		for rows.Next() {
			var song LibrarySong
			var id, count, last int64
			var score float64
			var size sql.NullInt64
			if err := rows.Scan(&id, &song.Title, &song.Artist, &song.Dancer, &song.Group, &song.Policy, &song.Resource, &size, &count, &score, &last); err != nil {
				rows.Close()
				return page, err
			}
			song.ID, song.Score = strconv.FormatInt(id, 10), effectiveSongPriority(id, count, score, last, now)
			song.HasDemand = count > 0
			match := search == "" || song.ID == search || strings.Contains(strings.ToLower(song.Title+"\n"+song.Artist+"\n"+song.Dancer+"\n"+song.Group+"\n"+song.Resource), search)
			saved := false
			if song.Resource != "" {
				r := resource(song.Resource)
				if r.SongCount == 0 {
					r.title = song.Title
				}
				r.SongCount++
				if len(r.Songs) < 20 {
					r.Songs = append(r.Songs, CacheSong{ID: song.ID, Title: song.Title, Current: true})
				}
				r.match = r.match || match
				r.score += song.Score
				if !r.saved && size.Valid && size.Int64 > 0 {
					bytes := size.Int64
					r.Size = &bytes
				}
				saved = r.saved
			}
			if match && (q.SongID == "" || q.SongID == song.ID) && (q.Resource == "" || q.Resource == song.Resource) && libraryFilter(q.Filter, saved, song.Policy) {
				songs = append(songs, song)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return page, err
		}
	}
	if q.Sort == "recent" {
		entries := make([]cacheListCandidate, 0, len(resources))
		for key := range resources {
			entries = append(entries, cacheListCandidate{key: key})
		}
		if err := loadCacheListMetadata(ctx, db, entries, "", "recent"); err != nil {
			return page, err
		}
		for _, entry := range entries {
			resources[entry.key].lastRequest = entry.lastRequest
		}
	}
	materialize := func(key string) error {
		if key == "" || page.Resources[key] != nil {
			return nil
		}
		r := resources[key]
		if r.saved {
			local, err := readCacheEntry(ctx, paths, key, db)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil {
				r.Local = &local
			}
		}
		page.Resources[key] = &r.LibraryResource
		return nil
	}
	if q.View != "resources" {
		// Group the song list before pagination; scheduling still uses numeric priorities.
		sort.Slice(songs, func(i, j int) bool {
			a, b := songs[i], songs[j]
			if q.Sort == "score" && a.HasDemand != b.HasDemand {
				return a.HasDemand
			}
			if q.Sort == "score" && a.Score != b.Score {
				return a.Score > b.Score
			}
			if q.Sort == "title" && a.Title != b.Title {
				return a.Title < b.Title
			}
			ai, _ := strconv.ParseInt(a.ID, 10, 64)
			bi, _ := strconv.ParseInt(b.ID, 10, 64)
			return ai > bi
		})
		page.Total = len(songs)
		for i := q.Offset; i < len(songs) && len(page.Songs) < 50; i++ {
			page.Songs = append(page.Songs, songs[i])
			if err := materialize(songs[i].Resource); err != nil {
				return page, err
			}
		}
	} else {
		var candidates []*libraryCandidate
		for _, r := range resources {
			if r.match && (q.Resource == "" || r.Key == q.Resource) && libraryFilter(q.Filter, r.saved, SongNormal) {
				candidates = append(candidates, r)
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if q.Sort == "recent" && a.lastRequest != b.lastRequest {
				return a.lastRequest > b.lastRequest
			}
			if q.Sort == "score" && a.score != b.score {
				return a.score > b.score
			}
			if q.Sort == "size" {
				if (a.Size == nil) != (b.Size == nil) {
					return a.Size != nil
				}
				if a.Size != nil && b.Size != nil && *a.Size != *b.Size {
					return *a.Size > *b.Size
				}
			}
			if q.Sort == "title" && a.title != b.title {
				return a.title < b.title
			}
			return a.Key < b.Key
		})
		page.Total = len(candidates)
		for i := q.Offset; i < len(candidates) && len(page.Keys) < 50; i++ {
			r := candidates[i]
			if err := materialize(r.Key); err != nil {
				return page, err
			}
			if r.SongCount == 0 && r.Local == nil {
				delete(page.Resources, r.Key)
				page.Total--
				continue
			}
			page.Keys = append(page.Keys, r.Key)
		}
	}
	return page, ctx.Err()
}

func libraryFilter(filter string, saved bool, policy SongPolicy) bool {
	switch filter {
	case "saved":
		return saved
	case "missing":
		return !saved
	case "normal", "favorite", "blocked":
		return string(policy) == filter
	default:
		return true
	}
}
