package vrclog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const snapshot = "2026.09.25 11:00:00 Debug - <color=#3EFF00>[VideoQueueManager] [11:00:00] : OnDeserialization: syncedQueuedInfoJson = [{\"songId\":1321,\"title\":\"中文舞曲\",\"playerNames\":[\"ignored\"]}]</color>\r\n"

func TestParseSnapshotsAndLifecycle(t *testing.T) {
	for _, line := range []string{snapshot, "[VideoQueueManager] : OnPreSerialization: queue info serialized: [{\"songId\":1321}]"} {
		e, ok, err := Parse(line)
		if err != nil || !ok || len(e.Songs) != 1 || e.Songs[0].ID != 1321 {
			t.Fatalf("%+v %v %v", e, ok, err)
		}
	}
	for _, line := range []string{"[Behaviour] OnLeftRoom", "[Behaviour] Entering Room: Home", "VRCApplication: HandleApplicationQuit"} {
		e, ok, err := Parse(line)
		if !ok || err != nil || !e.Reset {
			t.Fatal(line)
		}
	}
	for _, payload := range []string{"null", "[", "[{\"songId\":0}]", "[{\"songId\":1}] garbage"} {
		_, ok, err := Parse("[VideoQueueManager] OnPreSerialization: queue info serialized: " + payload)
		if !ok || err == nil {
			t.Fatal("accepted", payload)
		}
	}
	_, ok, _ := Parse("[VideoQueueManager] DeserializeVideoUserData: userData = {\"songId\":1321}")
	if ok {
		t.Fatal("current song is not a queue")
	}
	e, ok, err := Parse("[VideoQueueManager] OnPreSerialization: queue info serialized: []")
	if !ok || err != nil || len(e.Songs) != 0 {
		t.Fatal("empty snapshot")
	}
}

func TestTailEOFPartialRotationTruncation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "output_log_2026-09-24_00-00-00.txt")
	if err := os.WriteFile(p, []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	tail := &Tail{Dir: dir}
	now := time.Now()
	events, err := tail.Poll(now)
	if err != nil || len(events) != 1 || !events[0].Reset {
		t.Fatalf("startup replayed old queue: %+v %v", events, err)
	}
	appendBytes := func(b []byte) {
		t.Helper()
		f, e := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		_, e = f.Write(b)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	// Split inside a multibyte title as real concurrent writes can do.
	cut := strings.Index(snapshot, "中文") + 1
	appendBytes([]byte(snapshot[:cut]))
	events, err = tail.Poll(now.Add(3 * time.Second))
	if err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
	appendBytes([]byte(snapshot[cut:]))
	events, err = tail.Poll(now.Add(6 * time.Second))
	if err != nil || len(events) != 1 || events[0].Songs[0].Title != "中文舞曲" {
		t.Fatal(events, err)
	}
	if err = os.WriteFile(p, []byte("[Behaviour] OnLeftRoom\n"), 0600); err != nil {
		t.Fatal(err)
	}
	events, err = tail.Poll(now.Add(9 * time.Second))
	if err != nil || len(events) != 2 || !events[0].Reset || !events[1].Reset {
		t.Fatal(events, err)
	}
	newPath := filepath.Join(dir, "output_log_2026-09-25_00-00-00.txt")
	if err = os.WriteFile(newPath, []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	events, err = tail.Poll(now.Add(12 * time.Second))
	if err != nil || len(events) != 0 {
		t.Fatal("directory scanned too soon", events, err)
	}
	events, err = tail.Poll(now.Add(15 * time.Second))
	if err != nil || len(events) != 2 || events[1].Songs[0].ID != 1321 {
		t.Fatal(events, err)
	}
	// Old file activity must not move the watcher back to a previous session.
	if err = os.Chtimes(p, now.Add(time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, err = tail.Poll(now.Add(30 * time.Second))
	if err != nil || tail.Path != newPath {
		t.Fatal(tail.Path, err)
	}
}

func TestTailEmptyDirectoryThenNewLog(t *testing.T) {
	tail := &Tail{Dir: t.TempDir()}
	now := time.Now()
	if _, err := tail.Poll(now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tail.Dir, "output_log_2026-09-25.txt"), []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	events, err := tail.Poll(now.Add(DiscoverInterval))
	if err != nil || len(events) != 2 || len(events[1].Songs) != 1 {
		t.Fatal(events, err)
	}
}

func TestTailPartialStartupAndMalformedLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "output_log_2026-09-25.txt")
	cut := strings.Index(snapshot, "[VideoQueueManager]")
	if err := os.WriteFile(p, []byte(snapshot[:cut]), 0600); err != nil {
		t.Fatal(err)
	}
	tail := &Tail{Dir: dir}
	now := time.Now()
	if _, err := tail.Poll(now); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(snapshot[cut:] + snapshot)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	events, err := tail.Poll(now.Add(PollInterval))
	if err != nil || len(events) != 2 || !events[0].Reset || len(events[1].Songs) != 1 {
		t.Fatal(events, err)
	}
	f, err = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("[VideoQueueManager] OnPreSerialization: queue info serialized: broken\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	events, err = tail.Poll(now.Add(2 * PollInterval))
	if err != nil || len(events) != 1 || !events[0].Reset {
		t.Fatal("malformed snapshot must invalidate old queue", events, err)
	}
	if err = os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte(snapshot), 0600); err != nil {
		t.Fatal(err)
	}
	events, err = tail.Poll(now.Add(3 * PollInterval))
	if err != nil || len(events) != 2 || !events[0].Reset || len(events[1].Songs) != 1 {
		t.Fatal(events, err)
	}
}
