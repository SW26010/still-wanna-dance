// Package vrclog reads selected queue, lifecycle and playback facts,
// never retaining player identities.
package vrclog

import (
	"encoding/json"
	"errors"
	"strings"
)

type Song struct {
	ID int64 `json:"songId"`
}

type Event struct {
	Reset      bool
	QueueReset bool // Invalid input clears the queue while retaining the playback timeline.
	Songs      []Song
	Playback   *PlaybackEvent
}

func roomReset(line string) bool {
	for _, marker := range []string{"[Behaviour] OnLeftRoom", "[Behaviour] Entering Room:", "[Behaviour] Joining wrld_", "VRCApplication: HandleApplicationQuit"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// Parse accepts complete queue snapshots, not current-video userData or previews.
func Parse(line string) (Event, bool, error) {
	if raw, found := queueSnapshotPayload(line); found {
		payload := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "</color>"))
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
	if roomReset(line) {
		return Event{Reset: true}, true, nil
	}
	return Event{}, false, nil
}

func queueSnapshotPayload(line string) (string, bool) {
	_, line, ok := strings.Cut(line, "[VideoQueueManager]")
	if ok {
		for _, marker := range []string{"OnPreSerialization: queue info serialized:", "OnDeserialization: syncedQueuedInfoJson ="} {
			if _, payload, found := strings.Cut(line, marker); found {
				return payload, true
			}
		}
	}
	return "", false
}
