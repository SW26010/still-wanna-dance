package vrclog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlaybackParserSelectsFormalFacts(t *testing.T) {
	line := "2026.11.16 17:08:03 Debug - [<color=#9C6994>USharpVideo (WannaDance)</color>] Syncing video to 76.12, current time = 0.00, offset = 76.12"
	e, ok, err := ParseWithPlayback(line)
	if err != nil || !ok || e.Playback == nil || e.Playback.Position != 76.12 {
		t.Fatalf("%+v %v", e, err)
	}
	if _, ok, _ := Parse(line); ok {
		t.Fatal("ordinary queue parser accepted playback")
	}
	if _, ok, _ := ParseWithPlayback(strings.ReplaceAll(line, "WannaDance", "Preview")); ok {
		t.Fatal("preview accepted")
	}
	role := "2026.11.16 17:08:03 Debug - [VideoQueueManager] [LogOtherPlayerAndDeviceType] Player PRIVATE_NAME, isLocal = True, isOwner = False, isMaster = False"
	e, ok, err = ParseWithPlayback(role)
	if err != nil || !ok || e.Playback.Owner == nil || *e.Playback.Owner {
		t.Fatal("local nonowner missing")
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "PRIVATE_NAME") {
		t.Fatal("identity retained")
	}
	if _, ok, _ := ParseWithPlayback(strings.Replace(role, "isLocal = True", "isLocal = False", 1)); ok {
		t.Fatal("remote owner accepted as local")
	}
}

func TestTailReadsQueueAndPlaybackInFileOrder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "output_log_2000-01-01_00-00-00.txt")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	tail := &Tail{Dir: dir}
	now := time.Now()
	if _, err := tail.Poll(now); err != nil {
		t.Fatal(err)
	}
	line := "2000.01.01 00:00:01 Debug - [USharpVideo (WannaDance)] OnVideoStart: Started video: http://api.udon.dance/Api/Songs/play?id=3214, since owner is playing\n"
	if err := os.WriteFile(file, []byte(line[:40]), 0600); err != nil {
		t.Fatal(err)
	}
	if e, err := tail.Poll(now); err != nil || len(e) != 0 {
		t.Fatalf("partial line: %v %v", e, err)
	}
	queue := "[VideoQueueManager] OnDeserialization: syncedQueuedInfoJson = [{\"songId\":42}]\n"
	if err := os.WriteFile(file, []byte(line+queue), 0600); err != nil {
		t.Fatal(err)
	}
	e, err := tail.Poll(now)
	if err != nil || len(e) != 2 || e[0].Playback == nil || e[0].Playback.SongID != 3214 || len(e[1].Songs) != 1 || e[1].Songs[0].ID != 42 {
		t.Fatalf("mixed playback and queue events lost file order: %v %v", e, err)
	}
	// Resume after the observed start, even if the file temporarily disappears.
	if err := os.Rename(file, file+".offline"); err != nil {
		t.Fatal(err)
	}
	if events, err := tail.Poll(now.Add(PollInterval)); err != nil || len(events) != 0 || tail.Path != "" {
		t.Fatal("missing file should leave the tail idle", events, err, tail.Path)
	}
	if err := os.Rename(file+".offline", file); err != nil {
		t.Fatal(err)
	}
	badQueue := "[VideoQueueManager] OnDeserialization: syncedQueuedInfoJson = [bad [Behaviour] OnLeftRoom]\n"
	syncLine := "2000.01.01 00:00:02 Debug - [USharpVideo (WannaDance)] Syncing video to 45.5, current time = 0\n"
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(badQueue + syncLine)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if events, err := tail.Poll(now.Add(2 * PollInterval)); err != nil || len(events) != 0 || tail.Path != "" {
		t.Fatal("tail reopened the returned log before discovery", events, err, tail.Path)
	}
	var timeline PlayerTimeline
	timeline.Observe(e[0], e[0].Playback.At)
	now = now.Add(DiscoverInterval)
	e, err = tail.Poll(now)
	if err != nil || len(e) != 2 || !e[0].QueueReset || e[0].Reset || e[1].Playback == nil {
		t.Fatal("malformed snapshot must clear only the queue", e, err)
	}
	timeline.Observe(e[0], e[1].Playback.At)
	timeline.Observe(e[1], e[1].Playback.At)
	if timeline.Anchor.SongID != 3214 || timeline.Anchor.Position != 45.5 || timeline.Role != Follower {
		t.Fatal("anonymous sync lost its routed song or role", timeline)
	}
	// Once the snapshot exceeds the line limit, its suffix arrives in a
	// later poll. Neither half is evidence that playback ended.
	longQueue := "[VideoQueueManager] OnDeserialization: syncedQueuedInfoJson = " + strings.Repeat("x", maxLine)
	contents := line + queue + badQueue + syncLine + longQueue
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if e, err := tail.Poll(now); err != nil || len(e) != 1 || !e[0].QueueReset || e[0].Reset {
		t.Fatal("oversized queue snapshot changed playback", e, err)
	}
	longOther := "[OtherComponent] " + strings.Repeat("x", maxLine)
	if err := os.WriteFile(file, []byte(contents+"\n"+longOther+"\nVRCApplication: HandleApplicationQuit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if e, err := tail.Poll(now); err != nil || len(e) != 3 || !e[0].QueueReset || !e[1].QueueReset || !e[2].Reset {
		t.Fatal("only the actual exit should reset playback", e, err)
	}
	newFile := filepath.Join(dir, "output_log_2000-01-02_00-00-00.txt")
	if err := os.WriteFile(newFile, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	e, err = tail.Poll(now.Add(DiscoverInterval))
	if err != nil || len(e) != 2 || !e[0].Reset || e[1].Playback == nil {
		t.Fatalf("rotation order: %v %v", e, err)
	}
}
