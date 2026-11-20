package upstreamstate

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestAutomaticMonitorChecksPlaybackWithoutResourceRanges(t *testing.T) {
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") != "" {
			t.Error("automatic monitor fetched resource")
		}
		return fixture(r)
	})
	if err := m.checkRequest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if resultFor(t, m, Catalog, "api").State != "available" || resultFor(t, m, PlaybackURL, "cf").State != "available" {
		t.Fatal(m.Snapshot())
	}
}

func TestManualResourceCheckBypassesBusyBusiness(t *testing.T) {
	m := testMonitor(t, fixture)
	end := m.channel.BeginResourceLoad()
	defer end()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Check(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Results(Resource) {
		if r.State != "available" {
			t.Fatal(r)
		}
	}
}

func TestCloseCancelsAutomaticProbeWaitingForBusiness(t *testing.T) {
	m := testMonitor(t, fixture)
	end := m.channel.BeginResourceLoad()
	defer end()
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	awaitCondition(t, func() bool { return resultFor(t, m, Catalog, "api").State == "available" })
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close blocked on busy business")
	}
}
