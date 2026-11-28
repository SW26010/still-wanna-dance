package vrclog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRecoverRoomAndContinueAtPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output_log_test.txt")
	now := time.Now().Truncate(time.Second)
	line := func(age time.Duration, msg string) string {
		return now.Add(-age).Format("2006.01.02 15:04:05") + " Debug - " + msg + "\n"
	}
	const join = "[Behaviour] Successfully joined room"
	const queue = "[VideoQueueManager] OnPreSerialization: queue info serialized: "
	const player = "[USharpVideo (WannaDance)] "
	const route = "http://api.udon.dance/Api/Songs/play?id=4"
	next := line(0, queue+`[{"songId":7,"title":"中文"}]`)
	cut := strings.Index(next, "中") + 1
	log := line(time.Minute, join) + line(time.Minute, queue+`[{"songId":99}]`) + line(40*time.Second, "[Behaviour] OnLeftRoom") +
		line(30*time.Second, join) + line(29*time.Second, queue+`[{"songId":2}]`) +
		line(27*time.Second, queue+`[{"songId":5},{"songId":5}]`) + line(25*time.Second, player+"Playing synced "+route) +
		strings.Repeat("a", 100<<10) + "\n" + next[:cut]
	if err := os.WriteFile(path, []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	tail, room, err := OpenRoomLog(dir, now)
	if err != nil || tail.Offset != int64(len(log)) || !slices.Equal(room.Songs, []Song{{5}, {5}}) || !room.QueueAt.Equal(now.Add(-27*time.Second)) || !room.FirstQueueAt.Equal(now.Add(-29*time.Second)) || room.AwaitFirstQueue || !room.Timeline.Anchor.At.IsZero() {
		t.Fatalf("recovery: %+v, %v", room, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(next[cut:] + line(0, player+"Syncing video to 13.20, current time = 0"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := tail.Poll(now)
	if err != nil || len(events) != 2 || !slices.Equal(events[0].Songs, []Song{{7}}) {
		t.Fatal(events, err)
	}
	room.Timeline.Observe(events[1], now)
	if a := room.Timeline.Anchor; a.SongID != 4 || a.Position != 13.2 {
		t.Fatal("live synchronization lost the recovered identity", a)
	}
	if events, err = tail.Poll(now); err != nil || len(events) != 0 {
		t.Fatal("duplicated startup events", events, err)
	}
}

func TestRecoveryDropsQueueAtOversizedPartialLine(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	stamp := now.Format("2006.01.02 15:04:05") + " Debug - "
	queue := stamp + `[VideoQueueManager] OnDeserialization: syncedQueuedInfoJson = [{"songId":7}]` + "\n"
	data := stamp + "[Behaviour] Successfully joined room\n" + queue +
		stamp + "[USharpVideo (WannaDance)] Playing synced http://api.udon.dance/Api/Songs/play?id=4\n" +
		stamp + "[USharpVideo (WannaDance)] Syncing video to 13.20, current time = 0\n" +
		strings.Repeat("x", maxLine+1)
	dir := t.TempDir()
	path := filepath.Join(dir, "output_log_test.txt")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	tail, room, err := OpenRoomLog(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if room.Songs != nil || !room.QueueAt.IsZero() || room.Timeline.Anchor.SongID != 4 {
		t.Fatal("oversized partial line must clear only the queue", room)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// The first apparent snapshot is still part of the discarded line.
	_, err = f.WriteString(queue + queue)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	events, err := tail.Poll(now)
	if err != nil || len(events) != 2 || !events[0].QueueReset || !slices.Equal(events[1].Songs, []Song{{7}}) {
		t.Fatal(events, err)
	}
}

func TestRecoveryRequiresCurrentRoom(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	entry := now.Format("2006.01.02 15:04:05") + " [Behaviour] Successfully joined room\n"
	snapshot := now.Format("2006.01.02 15:04:05") + ` [VideoQueueManager] OnDeserialization: syncedQueuedInfoJson = [{"songId":2}]` + "\n"
	for _, tc := range []struct {
		name, data string
		at         time.Time
	}{
		{"missing entry", snapshot, now},
		{"quit", entry + snapshot + "VRCApplication: HandleApplicationQuit\n", now},
		{"joining", entry + snapshot + "[Behaviour] Joining wrld_example\n", now},
		{"outside budget", entry + strings.Repeat("x", roomLookback) + "\n" + snapshot, now},
		{"stale session", entry + snapshot, now.Add(IdleTimeout)},
		{"future session", entry + snapshot, now.Add(-time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tail := Tail{Offset: int64(len(tc.data))}
			room, err := tail.recoverRoom(strings.NewReader(tc.data), tc.at)
			if err != nil || !room.LastActivity.IsZero() || room.Songs != nil || room.AwaitFirstQueue {
				t.Fatal(room, err)
			}
		})
	}
}
