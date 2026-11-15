package cacheproxy

import (
	"cmp"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"modernc.org/sqlite"
)

func init() {
	// SQLite's built-in lower() folds ASCII only. Keep the library's existing
	// Unicode substring semantics while filtering before rows cross into Go.
	sqlite.MustRegisterDeterministicScalarFunction("swd_library_contains", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if strings.Contains(strings.ToLower(args[0].(string)), args[1].(string)) {
			return int64(1), nil
		}
		return int64(0), nil
	})
}

type librarySelection struct {
	with, from, where string
	args              []any
}

func (s librarySelection) selectSQL(columns string) string {
	return s.with + " SELECT " + columns + " FROM " + s.from + " WHERE " + s.where
}

func queryLibraryPage(ctx context.Context, paths LibraryPaths, q LibraryQuery, now int64, inventory func(context.Context, LibraryPaths) (libraryInventory, error)) (LibraryPage, error) {
	search, err := ParseLibrarySearch(q.Search)
	if err != nil {
		return LibraryPage{}, err
	}
	paths, err = paths.Resolve()
	page := LibraryPage{StorageID: CacheStorageID(paths), DatabaseID: DatabaseID(paths.DatabasePath), Songs: []LibrarySong{}, Keys: []string{}, Resources: map[string]*LibraryResource{}}
	if err != nil {
		return page, err
	}
	if q.Direction != "" && q.Direction != "ascending" && q.Direction != "descending" {
		return page, errors.New("无效排序方向")
	}
	if q.Offset < 0 {
		return page, errors.New("无效分页")
	}
	db, err := readDatabaseFile(paths.DatabasePath)
	if err != nil {
		return page, err
	}
	defer db.Close()
	stock := libraryInventory{}
	if _, err = CacheDirectory(paths.VideoDir); err == nil {
		stock, err = inventory(ctx, paths)
	}
	if err != nil {
		if ctx.Err() != nil {
			return page, ctx.Err()
		}
		page.VideoError = err.Error()
		if q.Filter == "saved" || q.Filter == "missing" {
			return page, errors.New("视频目录不可用，无法按本地视频状态筛选：" + page.VideoError)
		}
		stock = libraryInventory{}
	}
	page.FileCount, page.TotalBytes = len(stock.files), stock.bytes
	if err := prepareLibraryInventory(ctx, db, q, search, stock); err != nil {
		return page, err
	}
	selection := selectLibrary(q, search)
	var keys []string
	if q.Sort == "score" {
		keys, page.Total, err = rankLibraryScores(ctx, db, selection, q, now)
	} else {
		if err = db.QueryRowContext(ctx, selection.selectSQL("count(*)"), selection.args...).Scan(&page.Total); err == nil {
			if q.Offset >= page.Total {
				return page, ctx.Err()
			}
			identity := "CAST(s.id AS TEXT)"
			if q.View == "resources" {
				identity = "r.md5"
			}
			args := append(append([]any{}, selection.args...), q.Offset)
			var rows *sql.Rows
			rows, err = db.QueryContext(ctx, selection.selectSQL(identity)+" ORDER BY "+libraryOrder(q)+" LIMIT 50 OFFSET ?", args...)
			if err == nil {
				for rows.Next() {
					var key string
					if err = rows.Scan(&key); err != nil {
						break
					}
					keys = append(keys, key)
				}
				if err == nil {
					err = rows.Err()
				}
				rows.Close()
			}
		}
	}
	if err != nil || len(keys) == 0 {
		return page, err
	}
	if q.View != "resources" {
		page.Songs, err = loadLibrarySongs(ctx, db, keys, now)
		if err != nil {
			return page, err
		}
		keys = nil
		for _, song := range page.Songs {
			if song.Resource != "" && !slices.Contains(keys, song.Resource) {
				keys = append(keys, song.Resource)
			}
		}
	}
	if err := loadLibraryResources(ctx, db, paths, keys, stock, page.Resources); err != nil {
		return page, err
	}
	if q.View == "resources" {
		for _, key := range keys {
			if page.Resources[key] != nil {
				page.Keys = append(page.Keys, key)
			} else {
				page.Total-- // An unknown file disappeared after the inventory snapshot.
			}
		}
	}
	return page, ctx.Err()
}

// The selected connection owns an indexed, temporary projection of local stock.
// It never changes the business database and is discarded when the reader closes.
func prepareLibraryInventory(ctx context.Context, db *sql.DB, q LibraryQuery, search LibrarySearch, stock libraryInventory) error {
	if q.View != "resources" && q.Filter != "saved" && q.Filter != "missing" {
		return nil
	}
	if _, err := db.ExecContext(ctx, "CREATE TEMP TABLE library_files(md5 TEXT PRIMARY KEY,byte_size INTEGER NOT NULL) WITHOUT ROWID"); err != nil {
		return err
	}
	if len(stock.files) == 0 {
		return nil
	}
	files := stock.files
	if search.songID != "" || search.md5 != "" {
		key := search.md5
		if search.songID != "" {
			err := db.QueryRowContext(ctx, "SELECT md5 FROM song_media WHERE song_id=?", search.songID).Scan(&key)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		files = map[string]int64{}
		if size, saved := stock.files[key]; saved {
			files[key] = size
		}
	}
	encoded, err := json.Marshal(files)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO library_files SELECT key,value FROM json_each(?)", string(encoded))
	return err
}

func selectLibrary(q LibraryQuery, search LibrarySearch) librarySelection {
	s := librarySelection{where: "1"}
	add := func(condition string, args ...any) {
		s.where += " AND (" + condition + ")"
		s.args = append(s.args, args...)
	}
	if q.View != "resources" {
		s.from = "songs s"
		if search.md5 != "" || search.text != "" || q.Filter == "saved" || q.Filter == "missing" {
			s.from += " LEFT JOIN song_media m ON m.song_id=s.id"
		}
		if q.Filter == "normal" || q.Filter == "favorite" || q.Filter == "blocked" {
			s.from += " LEFT JOIN song_preferences p ON p.song_id=s.id"
		}
		condition, args := librarySongSearch(search)
		add(condition, args...)
		if q.Filter == "saved" || q.Filter == "missing" {
			condition = "EXISTS (SELECT 1 FROM library_files WHERE md5=m.md5)"
			if q.Filter == "missing" {
				condition = "NOT (" + condition + ")"
			}
			add(condition)
		} else if q.Filter == "normal" || q.Filter == "favorite" || q.Filter == "blocked" {
			add("COALESCE(p.policy,'normal')=?", q.Filter)
		}
		return s
	}
	s.with = `WITH resource_keys AS (SELECT md5 FROM media UNION ALL
 SELECT md5 FROM library_files f WHERE NOT EXISTS (SELECT 1 FROM media WHERE md5=f.md5))`
	s.from = "resource_keys r"
	switch {
	case search.md5 != "":
		add("r.md5=?", search.md5)
	case search.songID != "":
		add("r.md5=(SELECT md5 FROM song_media WHERE song_id=?)", search.songID)
	case search.text != "":
		condition, args := librarySongSearch(search)
		add("instr(r.md5,?)>0 OR EXISTS (SELECT 1 FROM song_media m JOIN songs s ON s.id=m.song_id WHERE m.md5=r.md5 AND ("+condition+"))", append([]any{search.text}, args...)...)
	}
	switch q.Filter {
	case "saved":
		add("EXISTS (SELECT 1 FROM library_files WHERE md5=r.md5)")
	case "missing":
		add("NOT EXISTS (SELECT 1 FROM library_files WHERE md5=r.md5)")
	case "favorite", "blocked":
		add("0") // Resource views have no song policy of their own.
	}
	return s
}

func librarySongSearch(search LibrarySearch) (string, []any) {
	switch {
	case search.songID != "":
		return "s.id=?", []any{search.songID}
	case search.md5 != "":
		return "m.md5=?", []any{search.md5}
	case search.text != "":
		return `CAST(s.id AS TEXT)=? OR swd_library_contains(
 substr(COALESCE(s.name,''),1,300)||char(10)||substr(COALESCE(s.artist,''),1,300)||char(10)||
 substr(COALESCE(s.dancer,''),1,300)||char(10)||substr(COALESCE(s.groupName,''),1,300)||char(10)||COALESCE(m.md5,''),?)`, []any{search.text, search.text}
	default:
		return "1", nil
	}
}

func libraryDirection(q LibraryQuery, descending bool) string {
	if q.Direction == "descending" || q.Direction == "" && descending {
		return " DESC"
	}
	return " ASC"
}

func libraryOrder(q LibraryQuery) string {
	if q.View != "resources" {
		switch q.Sort {
		case "title":
			return "substr(COALESCE(s.name,''),1,300)" + libraryDirection(q, false) + ",s.id DESC"
		case "id", "":
			return "s.id" + libraryDirection(q, true)
		default:
			return "s.id DESC"
		}
	}
	switch q.Sort {
	case "title":
		return `COALESCE((SELECT substr(COALESCE(s.name,''),1,300) FROM song_media m JOIN songs s ON s.id=m.song_id WHERE m.md5=r.md5 ORDER BY s.id LIMIT 1),'')` + libraryDirection(q, false) + ",r.md5"
	case "size":
		size := "COALESCE((SELECT byte_size FROM library_files WHERE md5=r.md5),(SELECT byte_size FROM media WHERE md5=r.md5))"
		return size + " IS NULL," + size + libraryDirection(q, true) + ",r.md5"
	case "recent":
		return `CASE WHEN NOT EXISTS (SELECT 1 FROM media WHERE md5=r.md5) THEN 0 ELSE max(
 COALESCE((SELECT last_requested_at FROM media_access WHERE md5=r.md5),0),
 COALESCE((SELECT max(requested_at) FROM request_events WHERE resource_md5=r.md5),0)) END` + libraryDirection(q, true) + ",r.md5"
	case "resource":
		return "r.md5" + libraryDirection(q, false)
	default:
		return "r.md5"
	}
}

type libraryRank struct {
	key    string
	id     int64
	score  float64
	demand bool
}

// Dynamic priority stays in Go and uses the scheduler's canonical calculation.
// Only filtered identities and usage accumulators are loaded before pagination.
func rankLibraryScores(ctx context.Context, db *sql.DB, s librarySelection, q LibraryQuery, now int64) ([]string, int, error) {
	columns := "s.id,COALESCE(u.demand_count,0),COALESCE(u.demand_score,0),COALESCE(u.last_demand_at,0)"
	if q.View == "resources" {
		s.from += " LEFT JOIN song_media m ON m.md5=r.md5 LEFT JOIN songs s ON s.id=m.song_id LEFT JOIN song_usage u ON u.song_id=s.id"
		columns = "r.md5,COALESCE(s.id,0),COALESCE(u.demand_count,0),COALESCE(u.demand_score,0),COALESCE(u.last_demand_at,0)"
	} else {
		s.from += " LEFT JOIN song_usage u ON u.song_id=s.id"
	}
	rows, err := db.QueryContext(ctx, s.selectSQL(columns), s.args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var ranks []libraryRank
	resources := map[string]int{}
	for rows.Next() {
		var r libraryRank
		var count, last int64
		var score float64
		if q.View == "resources" {
			err = rows.Scan(&r.key, &r.id, &count, &score, &last)
		} else {
			err = rows.Scan(&r.id, &count, &score, &last)
			r.key = strconv.FormatInt(r.id, 10)
		}
		if err != nil {
			return nil, 0, err
		}
		if r.id > 0 {
			r.score = effectiveSongPriority(r.id, count, score, last, now)
		}
		r.demand = count > 0
		if q.View == "resources" {
			if i, ok := resources[r.key]; ok {
				ranks[i].score += r.score
				continue
			}
			resources[r.key] = len(ranks)
		}
		ranks = append(ranks, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	descending := libraryDirection(q, true) == " DESC"
	sort.Slice(ranks, func(i, j int) bool {
		a, b := ranks[i], ranks[j]
		if q.View != "resources" && a.demand != b.demand {
			return a.demand
		}
		if order := cmp.Compare(a.score, b.score); order != 0 {
			return (order > 0) == descending
		}
		if q.View == "resources" {
			return a.key < b.key
		}
		return a.id > b.id
	})
	keys := make([]string, 0, 50)
	for i := min(q.Offset, len(ranks)); i < len(ranks) && len(keys) < 50; i++ {
		keys = append(keys, ranks[i].key)
	}
	return keys, len(ranks), ctx.Err()
}

func loadLibrarySongs(ctx context.Context, db *sql.DB, keys []string, now int64) ([]LibrarySong, error) {
	encoded, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT s.id,substr(COALESCE(s.name,''),1,300),
 substr(COALESCE(s.artist,''),1,300),substr(COALESCE(s.dancer,''),1,300),substr(COALESCE(s.groupName,''),1,300),
 COALESCE(p.policy,'normal'),COALESCE(m.md5,''),COALESCE(u.demand_count,0),COALESCE(u.demand_score,0),COALESCE(u.last_demand_at,0)
 FROM songs s LEFT JOIN song_media m ON m.song_id=s.id LEFT JOIN song_preferences p ON p.song_id=s.id
 LEFT JOIN song_usage u ON u.song_id=s.id WHERE s.id IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	songs := make(map[string]LibrarySong, len(keys))
	for rows.Next() {
		var song LibrarySong
		var id, count, last int64
		var score float64
		if err := rows.Scan(&id, &song.Title, &song.Artist, &song.Dancer, &song.Group, &song.Policy, &song.Resource, &count, &score, &last); err != nil {
			return nil, err
		}
		song.ID, song.HasDemand = strconv.FormatInt(id, 10), count > 0
		song.Score = effectiveSongPriority(id, count, score, last, now)
		songs[song.ID] = song
	}
	result := make([]LibrarySong, 0, len(keys))
	for _, key := range keys {
		if song, ok := songs[key]; ok {
			result = append(result, song)
		}
	}
	return result, rows.Err()
}

func loadLibraryResources(ctx context.Context, db *sql.DB, paths LibraryPaths, keys []string, stock libraryInventory, result map[string]*LibraryResource) error {
	encoded, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT v.md5,v.byte_size,
 (SELECT count(*) FROM song_media WHERE md5=v.md5),
 (SELECT json_group_array(json_object('id',song_id,'title',title)) FROM
  (SELECT CAST(s.id AS TEXT) song_id,substr(COALESCE(s.name,''),1,300) title
   FROM song_media m JOIN songs s ON s.id=m.song_id WHERE m.md5=v.md5 ORDER BY s.id LIMIT 20))
 FROM media v WHERE v.md5 IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return err
	}
	for rows.Next() {
		r := &LibraryResource{}
		var size sql.NullInt64
		var songs string
		if err = rows.Scan(&r.Key, &size, &r.SongCount, &songs); err != nil {
			break
		}
		if size.Valid {
			r.Size = &size.Int64
		}
		if err = json.Unmarshal([]byte(songs), &r.Songs); err != nil {
			break
		}
		result[r.Key] = r
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if size, saved := stock.files[key]; saved {
			r := result[key]
			if r == nil {
				r = &LibraryResource{Key: key, Songs: []CacheSong{}}
			}
			r.Size = &size
			local, err := readCacheEntry(ctx, paths, key, db)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil {
				r.Local, r.Size = &local, &local.Bytes
				result[key] = r
			}
		}
	}
	return ctx.Err()
}
