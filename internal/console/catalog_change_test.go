package console

import (
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
)

func TestMetadataNotificationsOnlyFollowCommittedChanges(t *testing.T) {
	c := testConsole(t)
	c.mu.Lock()
	c.metadataLocked()
	c.mu.Unlock()
	ctx := t.Context()
	updated := func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.catalogSyncSnapshotLocked().Updated
	}
	catalog := monitorNames("20990101000000", "song")
	if usable, err := c.syncCatalogs(ctx, []upstreamcheck.CatalogResponse{catalog}); err != nil || !usable {
		t.Fatal(usable, err)
	}
	previous := updated()
	if previous.IsZero() {
		t.Fatal("first catalog did not publish a change")
	}
	time.Sleep(time.Millisecond)
	if _, err := c.syncCatalogs(ctx, []upstreamcheck.CatalogResponse{catalog}); err != nil {
		t.Fatal(err)
	}
	if got := updated(); got != previous {
		t.Error("identical catalog published another change", previous, got)
	}
	// Older backups may still add a missing mapping without replacing names.
	time.Sleep(time.Millisecond)
	backup := monitorFull("kiva", "20980101000000", "old name", strings.Repeat("b", 32))
	if usable, err := c.syncCatalogs(ctx, []upstreamcheck.CatalogResponse{backup}); err != nil || !usable {
		t.Fatal(usable, err)
	}
	if got := updated(); !got.After(previous) {
		t.Fatal("older backup filled a mapping without publishing a change", previous, got)
	}
	previous = updated()
	time.Sleep(time.Millisecond)
	if _, err := c.syncCatalogs(ctx, []upstreamcheck.CatalogResponse{backup}); err != nil {
		t.Fatal(err)
	}
	if got := updated(); got != previous {
		t.Error("duplicate older backup published another change", previous, got)
	}
	time.Sleep(time.Millisecond)
	address := upstreamcheck.PlaybackURLResponse{SongID: 42, Node: "cf", URL: "http://play.udon.dance/files/1/42-video.mp4?e=" + strings.Repeat("a", 32) + "&s=1234", ObservedAt: time.Now()}
	c.syncPlaybackURL(ctx, address)
	if got := updated(); !got.After(previous) {
		t.Fatal("first address did not publish a change", previous, got)
	}
	previous = updated()
	for range 3 {
		time.Sleep(time.Millisecond)
		address.ObservedAt = time.Now()
		c.syncPlaybackURL(ctx, address)
		if got := updated(); got != previous {
			t.Error("identical playback address published another change", previous, got)
		}
	}
	address.URL = strings.Replace(address.URL, "video.mp4", "new.mp4", 1)
	time.Sleep(time.Millisecond)
	c.syncPlaybackURL(ctx, address)
	if got := updated(); !got.After(previous) {
		t.Fatal("changed address did not publish a change", previous, got)
	}
	previous = updated()
	address.URL = "invalid"
	c.syncPlaybackURL(ctx, address)
	if got := updated(); got != previous {
		t.Fatal("failed write published a change", previous, got)
	}
}
