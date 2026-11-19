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
