package cacheproxy

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// Compare membership and content identity inside the rejected update's snapshot.
// Report bounded identifiers/digests, never full catalog data or resource URLs.
func catalogConflictDetails(ctx context.Context, tx *sql.Tx, c Catalog, digest string) (string, error) {
	var previous string
	if err := tx.QueryRowContext(ctx, "SELECT digest FROM catalog_state WHERE catalog_key='songs'").Scan(&previous); err != nil {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, "SELECT cm.song_id,sm.md5 FROM catalog_members cm JOIN song_media sm ON sm.song_id=cm.song_id")
	if err != nil {
		return "", err
	}
	current := make(map[int64]string)
	for rows.Next() {
		var id int64
		var md5 string
		if err := rows.Scan(&id, &md5); err != nil {
			rows.Close()
			return "", err
		}
		current[id] = md5
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	added, changed := 0, 0
	var examples []int64
	for _, song := range c.Songs {
		md5, exists := current[song.ID]
		if !exists {
			added++
			examples = append(examples, song.ID)
		} else if md5 != song.MD5 {
			changed++
			examples = append(examples, song.ID)
		}
		delete(current, song.ID)
	}
	for id := range current {
		examples = append(examples, id)
	}
	sort.Slice(examples, func(i, j int) bool { return examples[i] < examples[j] })
	if len(examples) > 8 {
		examples = examples[:8]
	}
	kind := "歌曲映射或成员变化"
	if added == 0 && changed == 0 && len(current) == 0 {
		kind = "元数据或规范化表示变化（映射相同）"
	}
	return fmt.Sprintf("%s；新增 %d，移除 %d，MD5 改变 %d；示例 ID %v；摘要 %s → %s", kind, added, len(current), changed, examples, previous, digest), nil
}
