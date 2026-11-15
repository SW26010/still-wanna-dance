package cacheproxy

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

type LibraryQuery struct {
	View, Search, Sort, Direction, Filter string
	Offset                                int
}

// LibrarySearch uses the same exact fields in both library views.
type LibrarySearch struct {
	text, songID, md5 string
}

func ParseLibrarySearch(raw string) (LibrarySearch, error) {
	text := strings.ToLower(strings.TrimSpace(raw))
	field, value, found := strings.Cut(text, ":")
	switch {
	case found && field == "id":
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
			return LibrarySearch{}, errors.New("id: 后请输入正整数歌曲 ID，例如 id:2367")
		}
		return LibrarySearch{songID: value}, nil
	case found && field == "md5":
		if len(value) != 32 || strings.IndexFunc(value, func(c rune) bool { return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') }) >= 0 {
			return LibrarySearch{}, errors.New("md5: 后请输入完整的 32 位资源标识")
		}
		return LibrarySearch{md5: value}, nil
	default:
		return LibrarySearch{text: text}, nil
	}
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

// ReadLibraryPage inspects an independently selected library without starting
// an engine or reading video contents. Only the visible page gets full metadata
// and file-identity stamps. The engine method reuses its maintained inventory.
func ReadLibraryPage(ctx context.Context, paths LibraryPaths, q LibraryQuery) (LibraryPage, error) {
	return readLibraryPage(ctx, paths, q, time.Now().UnixMilli())
}

func readLibraryPage(ctx context.Context, paths LibraryPaths, q LibraryQuery, now int64) (LibraryPage, error) {
	return queryLibraryPage(ctx, paths, q, now, scanLibraryInventory)
}

func (s *Server) ReadLibraryPage(ctx context.Context, q LibraryQuery) (LibraryPage, error) {
	if !s.beginRequest() {
		return LibraryPage{}, context.Canceled
	}
	defer s.wg.Done()
	return queryLibraryPage(ctx, s.cfg.libraryPaths(), q, time.Now().UnixMilli(), s.libraryInventory)
}

// RefreshLibraryInventory explicitly reconciles external file changes without
// running retention, probing media contents or starting downloads.
func (s *Server) RefreshLibraryInventory(ctx context.Context) error {
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := CacheDirectory(s.cfg.VideoDir); err != nil {
		return err
	}
	return s.reconcileRetentionContext(ctx)
}
