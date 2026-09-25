// Package vrclog reads only queue and lifecycle evidence, never player identities.
package vrclog

import (
	"encoding/json"
	"errors"
	"strings"
)

type Song struct {
	ID       int64   `json:"songId"`
	Title    string  `json:"title"`
	Duration float64 `json:"duration"`
}

type Event struct {
	Reset bool
	Songs []Song
}

// Parse accepts complete queue snapshots, not current-video userData or previews.
func Parse(line string) (Event, bool, error) {
	for _, marker := range []string{"[Behaviour] OnLeftRoom", "[Behaviour] Entering Room:", "[Behaviour] Joining wrld_", "VRCApplication: HandleApplicationQuit"} {
		if strings.Contains(line, marker) {
			return Event{Reset: true}, true, nil
		}
	}
	i := strings.Index(line, "[VideoQueueManager]")
	if i < 0 {
		return Event{}, false, nil
	}
	line = line[i:]
	for _, marker := range []string{"OnPreSerialization: queue info serialized:", "OnDeserialization: syncedQueuedInfoJson ="} {
		i = strings.Index(line, marker)
		if i < 0 {
			continue
		}
		payload := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[i+len(marker):]), "</color>"))
		if !strings.HasPrefix(payload, "[") {
			return Event{}, true, errors.New("队列数据不是数组")
		}
		var songs []Song
		if err := json.Unmarshal([]byte(payload), &songs); err != nil {
			return Event{}, true, errors.New("队列 JSON 无效")
		}
		if len(songs) > 4096 {
			return Event{}, true, errors.New("队列过长")
		}
		for _, s := range songs {
			// Real logs also contain -1 entries; they have no catalog ID.
			// Preserve their queue position but never resolve them for caching.
			if s.ID == 0 || s.ID < -1 {
				return Event{}, true, errors.New("队列歌曲 ID 无效")
			}
		}
		return Event{Songs: songs}, true, nil
	}
	return Event{}, false, nil
}
