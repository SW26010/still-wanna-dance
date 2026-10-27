package upstreamstate

import (
	"context"
	"net/http"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestScopedMonitorMeasuresThroughputWithoutPublishing(t *testing.T) {
	before := upstreamrequest.Default.Snapshot()
	channel := upstreamrequest.NewChannel()
	channel.Publish(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") != "" {
			time.Sleep(time.Millisecond)
		}
		return fixture(r)
	}))
	m, err := NewMonitor(Options{Channel: channel})
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
	after := upstreamrequest.Default.Snapshot()
	if after.Revision != before.Revision || after.Candidates != before.Candidates {
		t.Fatal("scoped monitor published to default")
	}
}
