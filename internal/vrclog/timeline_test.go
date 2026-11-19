package vrclog

import (
	"testing"
	"time"
)

func TestRoomClockFromFormalPlayerLog(t *testing.T) {
	const player = "[<color=#9C6994>USharpVideo (WannaDance)</color>] "
	const song = "http://api.udon.dance/Api/Songs/play?id=3214"
	at := time.Date(2026, 11, 18, 20, 0, 0, 0, time.Local)
	var p PlayerTimeline
	read := func(offset time.Duration, body string) {
		t.Helper()
		stamp := at.Add(offset)
		e, ok, err := ParseWithPlayback(stamp.Format("2006.01.02 15:04:05") + " Debug - " + body)
		if !ok || err != nil {
			t.Fatal(body, err)
		}
		p.Observe(e, stamp.Add(2*time.Second))
	}
	read(0, player+"LoadRoutedURL: "+song+" routed to "+song)
	read(0, player+"Syncing video to 76.12, current time = 0.00")
	sync := p.Anchor
	if sync.SongID != 3214 || sync.Position != 76.12 || sync.At != at {
		t.Fatal(sync)
	}
	read(time.Second, player+"OnVideoStart: Started video: "+song+", since owner is playing")
	read(time.Second, player+"OnVideoStart: Started video: "+song)
	if p.Anchor != sync {
		t.Fatal("start erased late join target", p.Anchor)
	}
	read(2*time.Second, player+"Paused video: "+song)
	read(3*time.Second, player+"OnVideoError: "+song)
	read(4*time.Second, player+"Retrying load: "+song)
	if p.Anchor != sync {
		t.Fatal("room clock stopped during local interruption")
	}
	read(5*time.Second, player+"OnVideoEnd: "+song)
	if p.Anchor.SongID != 0 {
		t.Fatal(p.Anchor)
	}
	read(6*time.Second, player+"Started video: "+song+", since I'm the owner")
	if !p.Anchor.CatalogStart || p.Role != Owner {
		t.Fatal(p)
	}
	read(7*time.Second, player+"Syncing video to 8.25, current time = 0.00")
	if p.Anchor.Position != 8.25 || p.Anchor.CatalogStart {
		t.Fatal(p)
	}
	read(8*time.Second, player+"Started video: "+song+", since I'm the owner")
	if !p.Anchor.CatalogStart || p.Anchor.At != at.Add(8*time.Second) {
		t.Fatal("same-song restart", p)
	}
	read(9*time.Second, "[Behaviour] OnLeftRoom")
	if p.Anchor.SongID != 0 || p.Role != UnknownRole {
		t.Fatal(p)
	}
	read(10*time.Second, player+"Syncing video to 58.12, current time = 0")
	if p.Anchor.SongID != 0 {
		t.Fatal("anonymous sync revived old song")
	}
	read(10*time.Second, player+"Started video: "+song)
	if p.Anchor.SongID != 0 {
		t.Fatal("start claimed a preceding unidentified sync", p)
	}
	read(10*time.Second, player+"Syncing video to 59.45")
	if p.Anchor.Position != 59.45 || p.Anchor.SongID != 3214 {
		t.Fatal("start did not identify the subsequent sync", p)
	}
	read(11*time.Second, "VRCApplication: HandleApplicationQuit")
	read(12*time.Second, player+"Started video: "+song+", since owner is playing")
	if p.Anchor.SongID != 0 {
		t.Fatal("late join fabricated zero position")
	}
}

func TestRoomClockLateStartPreservesConfirmedRole(t *testing.T) {
	const start = "Started video: http://api.udon.dance/Api/Songs/play?id="
	at := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		first, late string
		role        PlayerRole
	}{
		{", since I'm the owner", ", since I'm the owner", Owner},
		{", since I'm the owner", "", Owner},
		{", since I'm the owner", ", since owner is playing", UnknownRole},
		{", since owner is playing", ", since owner is playing", Follower},
	} {
		var p PlayerTimeline
		observePlayerLine(t, &p, at, 0, start+"1"+tc.first)
		observePlayerLine(t, &p, at.Add(time.Second), 5*time.Second, start+"2"+tc.late)
		if p.Role != tc.role || p.Anchor.SongID != 0 {
			t.Fatalf("late start %q: role=%v, anchor=%v", tc.late, p.Role, p.Anchor)
		}
		syncAt := at.Add(7 * time.Second)
		observePlayerLine(t, &p, syncAt, 0, "Syncing video to 12.5")
		if p.Anchor.SongID != 2 || p.Anchor.At != syncAt || p.Anchor.Position != 12.5 || p.Role != tc.role {
			t.Fatal("fresh sync lost the song identified by the late start", p)
		}
	}
}

func observePlayerLine(t *testing.T, timeline *PlayerTimeline, stamp time.Time, delay time.Duration, text string) {
	t.Helper()
	e, ok, err := ParseWithPlayback(stamp.Format("2006.01.02 15:04:05") + " Debug - [USharpVideo (WannaDance)] " + text)
	if !ok || err != nil {
		t.Fatal(text, err)
	}
	timeline.Observe(e, stamp.Add(delay))
}

func TestRoomClockRoleChangeAndDelayedLocalEvents(t *testing.T) {
	var timeline PlayerTimeline
	at := time.Now().Truncate(time.Second)
	observe := func(offset, delay time.Duration, text string) {
		t.Helper()
		observePlayerLine(t, &timeline, at.Add(offset), delay, text)
	}
	const start = "Started video: http://api.udon.dance/Api/Songs/play?id="
	observe(0, 0, start+"1, since I'm the owner")
	observe(time.Second, 0, start+"2, since owner is playing")
	if timeline.Role != Follower || timeline.Anchor.SongID != 0 {
		t.Fatal("follower inherited the previous owner's start", timeline)
	}
	observe(2*time.Second, 0, "Syncing video to 30.13, current time = 0")
	anchor := timeline.Anchor
	for _, local := range []string{"OnVideoError:", "Retrying load:", "Paused video:"} {
		observe(3*time.Second, 5*time.Second, local)
		if anchor.SongID != 2 || timeline.Anchor != anchor || timeline.Role != Follower {
			t.Fatal(local, timeline)
		}
	}
	observe(4*time.Second, 5*time.Second, "LoadRoutedURL: http://api.udon.dance/Api/Songs/play?id=3")
	observe(10*time.Second, 0, "Syncing video to 5.00, current time = 0")
	if timeline.Anchor != anchor || timeline.Role != Follower {
		t.Fatal("late route or unassociated sync changed the room clock", timeline)
	}
	observe(11*time.Second, 0, "LoadRoutedURL: http://api.udon.dance/Api/Songs/play?id=3")
	observe(12*time.Second, 0, "Syncing video to 7.50, current time = 0")
	if timeline.Anchor.SongID != 3 || timeline.Anchor.Position != 7.5 {
		t.Fatal("fresh route failed to identify the new song", timeline)
	}
	observe(13*time.Second, 5*time.Second, "OnVideoEnd:")
	if timeline.Anchor.SongID != 0 {
		t.Fatal("delayed end retained the room clock")
	}
}

func TestRoomClockLateRolePreservesSong(t *testing.T) {
	at := time.Now().Truncate(time.Second)
	var p PlayerTimeline
	observePlayerLine(t, &p, at, 0, "Started video: http://api.udon.dance/Api/Songs/play?id=2, since owner is playing")
	observePlayerLine(t, &p, at, 0, "Syncing video to 12.5")
	anchor := p.Anchor
	for _, owner := range []bool{false, true} {
		p.Observe(Event{Playback: &PlaybackEvent{Kind: "role", At: at, Owner: &owner}}, at.Add(5*time.Second))
		if p.Anchor != anchor || (!owner && p.Role != Follower) || (owner && p.Role != UnknownRole) {
			t.Fatal("late role changed the clock or authorized a stale decision", p)
		}
	}
	observePlayerLine(t, &p, at.Add(6*time.Second), 0, "Syncing video to 18.5")
	if p.Anchor.SongID != 2 || p.Anchor.Position != 18.5 {
		t.Fatal("lost route after delayed role report", p)
	}
}

func TestRoomClockLateAnonymousStopKeepsOwner(t *testing.T) {
	const start = "Started video: http://api.udon.dance/Api/Songs/play?id="
	for _, end := range []string{"OnVideoEnd:", "StopVideo:"} {
		t.Run(end, func(t *testing.T) {
			var p PlayerTimeline
			at := time.Now().Truncate(time.Second)
			observePlayerLine(t, &p, at, 0, start+"1, since I'm the owner")
			observePlayerLine(t, &p, at.Add(time.Second), 5*time.Second, end)
			observePlayerLine(t, &p, at.Add(7*time.Second), 0, "Syncing video to 18.5")
			if p.Role != Owner || p.Anchor.SongID != 0 {
				t.Fatal("late stop lost owner or retained the old song", p)
			}
			observePlayerLine(t, &p, at.Add(8*time.Second), 0, start+"2")
			if p.Role != Owner || p.Anchor.SongID != 2 || !p.Anchor.CatalogStart {
				t.Fatal("next start lost the room's owner", p)
			}
		})
	}
}

func TestRoomClockIgnoresFutureTimestamp(t *testing.T) {
	var p PlayerTimeline
	at := time.Now().Truncate(time.Second)
	observePlayerLine(t, &p, at, 0, "Started video: http://api.udon.dance/Api/Songs/play?id=1, since I'm the owner")
	anchor := p.Anchor
	observePlayerLine(t, &p, at.Add(time.Hour), -time.Hour, "Started video: http://api.udon.dance/Api/Songs/play?id=2, since owner is playing")
	if p.Anchor != anchor || p.Role != Owner {
		t.Fatal("future start changed the trusted timeline", p)
	}
	observePlayerLine(t, &p, at.Add(time.Second), 0, "Started video: http://api.udon.dance/Api/Songs/play?id=3, since owner is playing")
	observePlayerLine(t, &p, at.Add(2*time.Second), 0, "Syncing video to 18.5")
	if p.Role != Follower || p.Anchor.SongID != 3 || p.Anchor.Position != 18.5 {
		t.Fatal("future timestamp suppressed valid playback", p)
	}
}

func TestRoomClockUnidentifiedStartPreservesRoleAndClearsRoute(t *testing.T) {
	var p PlayerTimeline
	at := time.Now().Truncate(time.Second)
	observePlayerLine(t, &p, at, 0, "Started video: http://api.udon.dance/Api/Songs/play?id=1, since I'm the owner")
	anchor := p.Anchor
	const unknown = "Started video: https://example.invalid/video"
	for _, delta := range []time.Duration{-time.Second, time.Hour} {
		observePlayerLine(t, &p, at.Add(delta), -delta, unknown+", since owner is playing")
		if p.Anchor != anchor || p.Role != Owner {
			t.Fatal("untrusted start reset the room", p)
		}
	}
	observePlayerLine(t, &p, at.Add(time.Second), 0, unknown)
	observePlayerLine(t, &p, at.Add(2*time.Second), 0, "Syncing video to 18.5")
	if p.Role != Owner || p.Anchor.SongID != 0 {
		t.Fatal("unidentified start lost owner or retained old song", p)
	}
	observePlayerLine(t, &p, at.Add(3*time.Second), 0, unknown+", since owner is playing")
	if p.Role != Follower || p.Anchor.SongID != 0 {
		t.Fatal("unidentified start lost its explicit role", p)
	}
}

func TestRoomClockLateSyncPreservesOwnerAndRoute(t *testing.T) {
	var p PlayerTimeline
	at := time.Now().Truncate(time.Second)
	observePlayerLine(t, &p, at, 0, "Started video: http://api.udon.dance/Api/Songs/play?id=2, since I'm the owner")
	anchor := p.Anchor
	observePlayerLine(t, &p, at.Add(time.Second), 5*time.Second, "Syncing video to 12.5")
	if p.Role != Owner || p.Anchor != anchor {
		t.Fatal("late sync changed owner or playback", p)
	}
	observePlayerLine(t, &p, at.Add(7*time.Second), 0, "Syncing video to 18.5")
	if p.Role != Owner || p.Anchor.SongID != 2 || p.Anchor.Position != 18.5 || p.Anchor.Started != anchor.Started {
		t.Fatal("fresh anonymous sync lost its playback", p)
	}
}

func TestRoomClockLateNamedEnd(t *testing.T) {
	const url = "http://api.udon.dance/Api/Songs/play?id="
	for _, end := range []string{"OnVideoEnd:", "StopVideo:"} {
		t.Run(end, func(t *testing.T) {
			var p PlayerTimeline
			at := time.Now().Truncate(time.Second)
			observePlayerLine(t, &p, at, 0, "Started video: "+url+"1, since I'm the owner")
			observePlayerLine(t, &p, at.Add(time.Second), 0, "Started video: "+url+"2")
			anchor := p.Anchor
			observePlayerLine(t, &p, at.Add(2*time.Second), 5*time.Second, end+" "+url+"1")
			if anchor.SongID != 2 || p.Anchor != anchor || p.Role != Owner {
				t.Fatal("old song ended the current playback", p)
			}
			observePlayerLine(t, &p, at.Add(8*time.Second), 0, "Syncing video to 12.5")
			if p.Anchor.SongID != 2 || p.Anchor.Position != 12.5 {
				t.Fatal("old end cleared the current route", p)
			}
			observePlayerLine(t, &p, at.Add(9*time.Second), 5*time.Second, end+" "+url+"2")
			observePlayerLine(t, &p, at.Add(15*time.Second), 0, "Syncing video to 15.0")
			if p.Anchor.SongID != 0 || p.Role != Owner {
				t.Fatal("matching end did not clear the song and its route", p)
			}
		})
	}
}

func TestRoomClockDuplicateStartConfirmsFollower(t *testing.T) {
	const start = "Started video: http://api.udon.dance/Api/Songs/play?id="
	var p PlayerTimeline
	at := time.Now().Truncate(time.Second)
	observePlayerLine(t, &p, at, 0, start+"1, since I'm the owner")
	at = at.Add(time.Second)
	observePlayerLine(t, &p, at, 0, start+"2")
	if !p.Anchor.CatalogStart || p.Anchor.SongID != 2 {
		t.Fatal("unqualified start did not inherit owner role", p)
	}
	withSync := p
	observePlayerLine(t, &p, at, 0, start+"2, since owner is playing")
	if p.Role != Follower || p.Anchor.SongID != 0 {
		t.Fatal("confirmed follower kept the inferred owner anchor", p)
	}
	observePlayerLine(t, &p, at, 0, "Syncing video to 11.2")
	if p.Anchor.SongID != 2 || p.Anchor.Position != 11.2 || p.Anchor.CatalogStart {
		t.Fatal("new sync did not restore the room clock", p)
	}
	observePlayerLine(t, &withSync, at, 0, "Syncing video to 11.2")
	synced := withSync.Anchor
	observePlayerLine(t, &withSync, at, 0, start+"2, since owner is playing")
	if synced != p.Anchor || withSync.Role != Follower || withSync.Anchor != synced {
		t.Fatal("explicit follower start lost the preceding synchronization", withSync)
	}
}

func TestRoomClockSameSongRerouteBeforeDelayedStart(t *testing.T) {
	const url = "http://api.udon.dance/Api/Songs/play?id="
	for _, owner := range []bool{false, true} {
		var p PlayerTimeline
		base := time.Now().Truncate(time.Second)
		read := func(sec int, line string) {
			t.Helper()
			observePlayerLine(t, &p, base.Add(time.Duration(sec)*time.Second), 0, line)
		}
		start := "Started video: " + url + "1"
		role := ", since owner is playing"
		if owner {
			role = ", since I'm the owner"
		}
		read(0, start+role)
		read(1, "LoadRoutedURL: "+url+"1")
		read(2, "Syncing video to 76, current time = 0")
		anchor := p.Anchor
		read(3, start)
		read(3, start)
		if p.Anchor != anchor || anchor.SongID != 1 || anchor.Position != 76 || anchor.CatalogStart || anchor.Started != base.Add(2*time.Second) {
			t.Fatal("same-song start overwrote its routed synchronization", owner, anchor, p.Anchor)
		}
		read(4, "LoadRoutedURL: "+url+"1")
		read(5, start)
		if (owner && p.Role != Owner) || (!owner && p.Role != Follower) {
			t.Fatal("unqualified callbacks lost the confirmed role", p.Role)
		}
		if owner && !p.Anchor.CatalogStart || !owner && p.Anchor.SongID != 0 {
			t.Fatal("new start reused the preceding route's sync", owner, p.Anchor)
		}
	}
}

func TestRoomClockIgnoresGamePreview(t *testing.T) {
	const mainURL = "http://api.udon.dance/Api/Songs/play?id=139"
	const previewURL = "http://api.udon.dance/Api/Songs/play?id=10980"
	const player = "[USharpVideo (WannaDance)] "
	at := time.Date(2026, 11, 19, 19, 55, 34, 0, time.Local)

	for _, delay := range []time.Duration{0, 5 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			var timeline PlayerTimeline
			observeTimelineLine(t, &timeline, at, 0, player+"LoadRoutedURL: "+mainURL)
			observeTimelineLine(t, &timeline, at, 0, player+"OnVideoStart: Started video: "+mainURL+", since owner is playing")
			observeTimelineLine(t, &timeline, at, 0, player+"Syncing video to 0.16")
			anchor := timeline.Anchor

			preview := at.Add(144 * time.Second)
			observeTimelineLine(t, &timeline, preview, delay, "[VideoListManager] PreviewVideo: 10980 "+previewURL+", time 30 - 193")
			observeTimelineLine(t, &timeline, preview, delay, player+"LoadRoutedURL: "+previewURL+" routed to "+previewURL)
			observeTimelineLine(t, &timeline, preview.Add(5*time.Second), delay, player+"OnVideoStart: Started video: "+previewURL+", since I'm the owner")
			observeTimelineLine(t, &timeline, preview.Add(5*time.Second), 0, player+"Syncing video to 30.00, current time = 0.00")
			observeTimelineLine(t, &timeline, preview.Add(9*time.Second), 0, player+"Syncing video to 33.37, current time = 30.43")
			observeTimelineLine(t, &timeline, preview.Add(10*time.Second), 0, player+"StopVideo:")
			observeTimelineLine(t, &timeline, preview.Add(11*time.Second), 0, player+"OnVideoEnd: Video "+previewURL+" ended")
			if timeline.Anchor != anchor || timeline.Role != Follower {
				t.Fatalf("preview changed room playback: anchor=%+v role=%v", timeline.Anchor, timeline.Role)
			}

			observeTimelineLine(t, &timeline, preview.Add(12*time.Second), 0, player+"Syncing video to 156.25")
			if timeline.Anchor.SongID != 139 || timeline.Anchor.Position != 156.25 {
				t.Fatal("ended preview suppressed a room sync", timeline.Anchor)
			}

			observeTimelineLine(t, &timeline, preview.Add(13*time.Second), 0, player+"OnVideoEnd: Video "+mainURL+" ended")
			if timeline.Anchor.SongID != 0 || timeline.Role != Follower {
				t.Fatal("preview hid the formal song's end", timeline)
			}
		})
	}
}

func TestRoomClockAcceptsFormalPlayAfterSameSongPreview(t *testing.T) {
	const songURL = "http://api.udon.dance/Api/Songs/play?id=10980"
	const player = "[USharpVideo (WannaDance)] "
	at := time.Date(2026, 11, 19, 19, 57, 58, 0, time.Local)

	for _, signal := range []struct {
		name   string
		marker string
		role   PlayerRole
	}{
		{"owner queue", "[VideoQueueManager] [19:57:59] : PlayQueueVideo: index = 0, userData = {\"songId\":10980}, videoDuration = 193", Owner},
		{"owner random", "[VideoQueueManager] [19:57:59] : PlayRandomVideo: userData = {\"songId\":10980}, videoDuration = 193", Owner},
		{"follower sync", player + "Playing synced " + songURL, Follower},
	} {
		t.Run(signal.name, func(t *testing.T) {
			var timeline PlayerTimeline
			observeTimelineLine(t, &timeline, at, 0, "[VideoListManager] PreviewVideo: 10980 "+songURL)
			observeTimelineLine(t, &timeline, at, 0, `[VideoQueueManager] [19:57:58] : DeserializeVideoUserData: userData = {"songId":10980,"videoTitle":"PlayQueueVideo: title"}`)
			observeTimelineLine(t, &timeline, at, 0, player+"LoadRoutedURL: "+songURL)
			observeTimelineLine(t, &timeline, at, 0, player+"OnVideoStart: Started video: "+songURL+", since I'm the owner")
			if timeline.Anchor.SongID != 0 || timeline.Role != UnknownRole {
				t.Fatal("preview established room playback", timeline)
			}

			start := at.Add(time.Second)
			observeTimelineLine(t, &timeline, start, 0, signal.marker)
			if signal.role == Owner {
				observeTimelineLine(t, &timeline, start, 0, player+"LoadRoutedURL: "+songURL)
				observeTimelineLine(t, &timeline, start, 0, player+"OnVideoStart: Started video: "+songURL+", since I'm the owner")
				if !timeline.Anchor.CatalogStart {
					t.Fatal("owner did not establish the catalog start", timeline)
				}
				observeTimelineLine(t, &timeline, start, 0, player+"Syncing video to 1.25, current time = 0.00")
			} else {
				observeTimelineLine(t, &timeline, start, 0, player+"Syncing video to 1.25, current time = 0.00")
				observeTimelineLine(t, &timeline, start, 0, player+"OnVideoStart: Started video: "+songURL+", since owner is playing")
			}
			if timeline.Anchor.SongID != 10980 || timeline.Anchor.Position != 1.25 || timeline.Role != signal.role {
				t.Fatal("formal playback did not take over after preview", timeline)
			}
		})
	}
}

func observeTimelineLine(t *testing.T, timeline *PlayerTimeline, at time.Time, delay time.Duration, line string) {
	t.Helper()
	event, ok, err := ParseWithPlayback(at.Format("2006.01.02 15:04:05") + " Debug - " + line)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		timeline.Observe(event, at.Add(delay))
	}
}

func TestRoomClockIgnoresLatePreviewSeek(t *testing.T) {
	const player = "[USharpVideo (WannaDance)] "
	const roomURL = "http://api.udon.dance/Api/Songs/play?id=3136"
	const previewURL = "http://api.udon.dance/Api/Songs/play?id=4923"
	at := time.Date(2026, 8, 31, 20, 27, 16, 0, time.Local)
	var timeline PlayerTimeline
	observeTimelineLine(t, &timeline, at, 0, player+"Playing synced "+roomURL)
	observeTimelineLine(t, &timeline, at, 0, player+"OnVideoStart: Started video: "+roomURL+", since owner is playing")
	observeTimelineLine(t, &timeline, at, 0, player+"Syncing video to 0.19")
	anchor := timeline.Anchor
	preview := at.Add(15 * time.Second)
	observeTimelineLine(t, &timeline, preview, 0, "[VideoListManager] PreviewVideo: 4923 "+previewURL+", time 30 - 259")
	observeTimelineLine(t, &timeline, preview, 0, player+"LoadRoutedURL: "+previewURL+" routed to "+previewURL)
	observeTimelineLine(t, &timeline, preview.Add(2*time.Second), 0, player+"OnVideoStart: Started video: "+previewURL+", since I'm the owner")

	late := preview.Add(101 * time.Second)
	observeTimelineLine(t, &timeline, late, 0, player+"SeekTo: call SyncVideo() since newTargetTime is 193.5573")
	observeTimelineLine(t, &timeline, late, 0, player+"Syncing video to 193.56, current time = 201.17, offset = -7.61, threshold = 0.85")
	if timeline.Anchor != anchor || timeline.Role != Follower {
		t.Fatal("late preview seek recalibrated the room clock", timeline.Anchor)
	}

	observeTimelineLine(t, &timeline, late, 0, player+"Syncing video to 116.19, current time = 115.00, offset = 1.19, threshold = 0.4999999")
	if timeline.Anchor.SongID != 3136 || timeline.Anchor.Position != 116.19 {
		t.Fatal("unpaired room sync did not recalibrate the clock", timeline.Anchor)
	}

	observeTimelineLine(t, &timeline, late, 0, player+"Playing synced "+roomURL)
	observeTimelineLine(t, &timeline, late, 0, player+"SeekTo: call SyncVideo() since newTargetTime is 118")
	observeTimelineLine(t, &timeline, late, 0, player+"Syncing video to 118.00, current time = 0.00")
	if timeline.Anchor.SongID != 3136 || timeline.Anchor.Position != 118 {
		t.Fatal("identified room playback lost its seek", timeline.Anchor)
	}
}

func TestRoomClockEndsFormalPlaybackDuringSameSongPreview(t *testing.T) {
	const player = "[USharpVideo (WannaDance)] "
	const song = "http://api.udon.dance/Api/Songs/play?id=10980"
	const end = player + "OnVideoEnd: Video " + song + " ended"
	at := time.Date(2026, 11, 19, 19, 55, 34, 0, time.Local)
	for _, source := range []struct {
		name string
		line string
	}{
		{"queue manager", "[VideoQueueManager] [19:55:54] : OnUSharpVideoEnd: PlayNextVideo()"},
		{"follower", end + ", but I am not the video owner, waiting for video owner [owner] to act"},
	} {
		t.Run(source.name, func(t *testing.T) {
			var timeline PlayerTimeline
			observeTimelineLine(t, &timeline, at, 0, player+"Playing synced "+song)
			observeTimelineLine(t, &timeline, at, 0, player+"OnVideoStart: Started video: "+song+", since owner is playing")
			observeTimelineLine(t, &timeline, at, 0, player+"Syncing video to 1.25")
			anchor := timeline.Anchor
			preview := at.Add(5 * time.Second)
			observeTimelineLine(t, &timeline, preview, 0, "[VideoListManager] PreviewVideo: 10980 "+song)
			observeTimelineLine(t, &timeline, preview, 0, player+"LoadRoutedURL: "+song)
			observeTimelineLine(t, &timeline, preview, 0, player+"OnVideoStart: Started video: "+song+", since I'm the owner")

			stopped := at.Add(20 * time.Second)
			observeTimelineLine(t, &timeline, stopped, 0, end)
			observeTimelineLine(t, &timeline, stopped, 0, "[VideoQueueManager] [19:55:54] : OnUSharpVideoEndInCaseVideoOwnerFailure: waiting 5 seconds")
			if timeline.Anchor != anchor || timeline.Role != Follower {
				t.Fatal("ambiguous end changed room playback", timeline.Anchor)
			}
			observeTimelineLine(t, &timeline, stopped, 5*time.Second, source.line)
			if timeline.Anchor.SongID != 0 || timeline.Role != Follower {
				t.Fatal("formal end did not clear room playback", timeline.Anchor)
			}

			observeTimelineLine(t, &timeline, stopped.Add(time.Second), 0, player+"LoadRoutedURL: "+song)
			observeTimelineLine(t, &timeline, stopped.Add(time.Second), 0, player+"Syncing video to 23.00")
			if timeline.Anchor.SongID != 0 || timeline.Role != Follower {
				t.Fatal("ended room playback was revived by the ongoing preview", timeline.Anchor)
			}
		})
	}
}

func TestRoomClockPreviewEndOnlyReleasesItsOwnCallbacks(t *testing.T) {
	const player = "[USharpVideo (WannaDance)] "
	const room = "http://api.udon.dance/Api/Songs/play?id=139"
	const first = "http://api.udon.dance/Api/Songs/play?id=4923"
	const second = "http://api.udon.dance/Api/Songs/play?id=10980"
	at := time.Date(2026, 11, 19, 19, 55, 34, 0, time.Local)
	var timeline PlayerTimeline
	observeTimelineLine(t, &timeline, at, 0, player+"Playing synced "+room)
	observeTimelineLine(t, &timeline, at, 0, player+"OnVideoStart: Started video: "+room+", since owner is playing")
	observeTimelineLine(t, &timeline, at, 0, player+"Syncing video to 1.25")
	anchor := timeline.Anchor
	observeTimelineLine(t, &timeline, at.Add(time.Second), 0, "[VideoListManager] PreviewVideo: 10980 "+second)
	observeTimelineLine(t, &timeline, at.Add(2*time.Second), 0, "[VideoListManager] PreviewVideo: 4923 "+first)
	observeTimelineLine(t, &timeline, at.Add(4*time.Second), 0, "[VideoListManager] PreviewVideo: 10980 "+second)
	observeTimelineLine(t, &timeline, at.Add(3*time.Second), time.Second, player+"OnVideoEnd: Video "+second+" ended")
	observeTimelineLine(t, &timeline, at.Add(5*time.Second), 0, player+"OnVideoEnd: Video "+first+" ended")
	observeTimelineLine(t, &timeline, at.Add(6*time.Second), 0, player+"Syncing video to 32.50")
	if timeline.Anchor != anchor {
		t.Fatal("an older preview end released the current preview's callbacks", timeline.Anchor)
	}

	observeTimelineLine(t, &timeline, at.Add(20*time.Second), 0, player+"OnVideoEnd: Video "+second+" ended")
	observeTimelineLine(t, &timeline, at.Add(21*time.Second), 0, player+"LoadRoutedURL: "+second)
	observeTimelineLine(t, &timeline, at.Add(22*time.Second), 0, player+"Syncing video to 23.00")
	if timeline.Anchor.SongID != 139 || timeline.Anchor.Position != 23 {
		t.Fatal("a callback from the ended preview suppressed room sync again", timeline.Anchor)
	}
}

func TestRoomClockKeepsInterleavedPreviewSyncOutOfRoom(t *testing.T) {
	const player = "[USharpVideo (WannaDance)] "
	const song = "http://api.udon.dance/Api/Songs/play?id=10938"
	at := time.Date(2026, 10, 14, 16, 40, 15, 0, time.Local)
	var timeline PlayerTimeline
	observeTimelineLine(t, &timeline, at, 0, player+"LoadRoutedURL: "+song)
	observeTimelineLine(t, &timeline, at.Add(11*time.Second), 0, player+"OnVideoStart: Started video: "+song+", since I'm the owner")
	preview := at.Add(68 * time.Second)
	observeTimelineLine(t, &timeline, preview, 0, "[VideoListManager] PreviewVideo: 10938 "+song)
	observeTimelineLine(t, &timeline, preview, 0, player+"LoadRoutedURL: "+song)

	observeTimelineLine(t, &timeline, preview.Add(90*time.Second), 0, player+"Syncing video to 147.27, current time = 4.53, offset = 142.73, threshold = 0.4999999")
	if timeline.Anchor.SongID != 10938 || timeline.Anchor.Position != 147.27 {
		t.Error("preview suppressed the room calibration", timeline.Anchor)
	}

	interleaved := preview.Add(141 * time.Second)
	observeTimelineLine(t, &timeline, interleaved, 0, player+"Syncing video to 188.07, current time = 4.53, offset = 183.53, threshold = 0.4999999")
	formal := timeline.Anchor
	if formal.Position != 188.07 || formal.At != interleaved {
		t.Fatal("room sync did not reach 188.07", formal)
	}
	observeTimelineLine(t, &timeline, interleaved, 0, player+"Syncing video to 125.85, current time = 43.47, offset = 82.38, threshold = 0.85")
	if timeline.Anchor != formal {
		t.Fatal("preview rewound the room clock", timeline.Anchor)
	}
}

func TestRoomClockKeepsPreviewIdentityAcrossLateReload(t *testing.T) {
	const player = "[USharpVideo (WannaDance)] "
	const song = "http://api.udon.dance/Api/Songs/play?id=3214"
	at := time.Date(2026, 10, 14, 16, 22, 26, 0, time.Local)
	var timeline PlayerTimeline
	observeTimelineLine(t, &timeline, at, 0, "[VideoListManager] PreviewVideo: 3214 "+song)
	observeTimelineLine(t, &timeline, at, 0, player+"LoadRoutedURL: "+song)
	observeTimelineLine(t, &timeline, at.Add(21*time.Second), 0, player+"OnVideoStart: Started video: "+song+", since I'm the owner")
	if timeline.Anchor.SongID != 0 || timeline.Role != UnknownRole {
		t.Fatal("initial preview established room playback", timeline.Anchor)
	}

	observeTimelineLine(t, &timeline, at.Add(134*time.Second), 0, player+"OnVideoError: "+song)
	retry := at.Add(139 * time.Second)
	observeTimelineLine(t, &timeline, retry, 0, player+"Retrying load: "+song)
	observeTimelineLine(t, &timeline, retry, 0, player+"LoadRoutedURL: "+song)
	observeTimelineLine(t, &timeline, retry.Add(2*time.Second), 0, player+"OnVideoStart: Started video: "+song+", since I'm the owner")
	if timeline.Anchor.SongID != 0 || timeline.Role != UnknownRole {
		t.Fatal("late preview retry established room playback", timeline.Anchor, timeline.Role)
	}

	official := at.Add(150 * time.Second)
	observeTimelineLine(t, &timeline, official, 0, player+"Playing synced "+song)
	observeTimelineLine(t, &timeline, official, 0, player+"Syncing video to 1.25, current time = 0.00, offset = 1.25, threshold = 0.4999999")
	if timeline.Anchor.SongID != 3214 || timeline.Anchor.Position != 1.25 {
		t.Fatal("confirmed room route did not supersede the preview", timeline.Anchor)
	}
	formal := timeline.Anchor
	observeTimelineLine(t, &timeline, official, 0, player+"Syncing video to 160.00, current time = 30.00, offset = 130.00, threshold = 0.85")
	if timeline.Anchor != formal {
		t.Fatal("a room route lost the overlapping preview context", timeline.Anchor)
	}
}

func TestRoomClockAcceptsLocalThresholdWithoutPreview(t *testing.T) {
	const player = "[USharpVideo (WannaDance)] "
	at := time.Date(2026, 6, 19, 19, 16, 16, 0, time.Local)
	var timeline PlayerTimeline
	observeTimelineLine(t, &timeline, at, 0, player+"Playing synced http://api.udon.dance/Api/Songs/play?id=1234")
	observeTimelineLine(t, &timeline, at, 0, player+"Syncing video to 208.05, current time = 0.00, offset = 208.05, threshold = 0.85")
	if timeline.Anchor.SongID != 1234 || timeline.Anchor.Position != 208.05 {
		t.Fatal("a startup sync was lost outside a preview", timeline.Anchor)
	}
}
