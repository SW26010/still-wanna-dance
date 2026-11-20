package upstreamstate

import (
	"context"
	"net/http"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestApplicationMonitorFullCheckOnlyReadsCatalogAndPlayback(t *testing.T) {
	channel := upstreamrequest.NewChannel()
	channel.Publish(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") != "" {
			t.Errorf("application Monitor requested a resource block")
		}
		return fixture(r)
	}))
	m, err := NewMonitor(Options{Channel: channel})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	seen := make(map[Operation]bool)
	for _, row := range m.Snapshot().Results {
		if row.State == "available" {
			seen[row.Operation] = true
		}
	}
	if !seen[Catalog] || !seen[PlaybackURL] || seen[Resource] {
		t.Fatal(seen)
	}
}

func TestScopedMonitorMeasuresThroughputWithoutPublishing(t *testing.T) {
	other := upstreamrequest.NewChannel()
	other.Publish(&upstreamrequest.Pool{})
	before := other.Snapshot()
	channel := upstreamrequest.NewChannel()
	channel.Publish(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") != "" {
			time.Sleep(time.Millisecond)
		}
		return fixture(r)
	}))
	m, err := NewExperimentMonitor(Options{Channel: channel})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for i := 0; i < 2; i++ {
		if err := m.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		r := resultFor(t, m, Resource, "play.udon.dance")
		if r.EstimatedSpeedBPS == nil || *r.EstimatedSpeedBPS <= 0 || r.TransferredBytes == 0 {
			t.Fatalf("missing speed: %+v", r)
		}
	}
	after := other.Snapshot()
	if after.Revision != before.Revision || after.Candidates != before.Candidates {
		t.Fatal("scoped monitor changed another channel")
	}
}
