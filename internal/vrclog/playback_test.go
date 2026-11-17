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

func TestPlaybackTailOptInAndRotation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "output_log_2000-01-01_00-00-00.txt")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ordinary, enabled := &Tail{Dir: dir}, &Tail{Dir: dir, Playback: true}
	now := time.Now()
	ordinary.Poll(now)
	enabled.Poll(now)
	line := "2000.01.01 00:00:01 Debug - [USharpVideo (WannaDance)] OnVideoStart: Started video: http://api.udon.dance/Api/Songs/play?id=3214, since owner is playing\n"
	if err := os.WriteFile(file, []byte(line[:40]), 0600); err != nil {
		t.Fatal(err)
	}
	if e, err := enabled.Poll(now); err != nil || len(e) != 0 {
		t.Fatalf("partial line: %v %v", e, err)
	}
	if err := os.WriteFile(file, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	e, err := enabled.Poll(now)
	if err != nil || len(e) != 1 || e[0].Playback.SongID != 3214 {
		t.Fatalf("%v %v", e, err)
	}
	if e, err := ordinary.Poll(now); err != nil || len(e) != 0 {
		t.Fatalf("default changed: %v %v", e, err)
	}
	newFile := filepath.Join(dir, "output_log_2000-01-02_00-00-00.txt")
	if err := os.WriteFile(newFile, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	e, err = enabled.Poll(now.Add(DiscoverInterval))
	if err != nil || len(e) != 2 || !e[0].Reset || e[1].Playback == nil {
		t.Fatalf("rotation order: %v %v", e, err)
	}
}
