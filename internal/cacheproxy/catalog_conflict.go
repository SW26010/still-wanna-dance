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
	rows, err := tx.QueryContext(ctx, "SELECT song_id FROM catalog_members")
	if err != nil {
		return "", err
	}
	current := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		current[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	added := 0
	var examples []int64
	for _, song := range c.Songs {
		if !current[song.ID] {
			added++
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
	kind := "歌曲成员变化"
	if added == 0 && len(current) == 0 {
		kind = "元数据或规范化表示变化（成员相同）"
	}
	return fmt.Sprintf("%s；新增 %d，移除 %d；示例 ID %v；摘要 %s → %s", kind, added, len(current), examples, previous, digest), nil
}
