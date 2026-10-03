package upstreamstate

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

func awaitCondition(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestChannelAvailabilityAndAutomaticRefresh(t *testing.T) {
	ch := upstreamrequest.NewChannel()
	m, err := newMonitor(Options{}, ch)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Check(context.Background()); !errors.Is(err, upstreamrequest.ErrUnavailable) {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	ch.Publish(transportFunc(fixture))
	awaitCondition(t, func() bool { return !m.Snapshot().Finished.IsZero() })
	if m.Best(Resource).State != "available" {
		t.Fatal(m.Snapshot())
	}
	ch.Publish(transportFunc(func(*http.Request) (*http.Response, error) { return response(524, ""), nil }))
	// A reader must never observe a recommendation from the previous channel.
	if r := m.Best(Resource); r.Entry != "" || r.EstimatedSpeedBPS != nil {
		t.Fatal(r)
	}
	awaitCondition(t, func() bool { return m.Best(Catalog).Reason == "origin_timeout" })
	if m.Best(PlaybackURL).State != "unknown" {
		t.Fatal("retained old song seed")
	}
	snapshot := ch.Snapshot()
	ch.Release(snapshot.Revision)
	if m.Best(Catalog).State != "unknown" {
		t.Fatal("retained released channel results")
	}
}

func TestChannelChangeCancelsAndDiscardsLateResults(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m := testMonitor(t, func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release
		return response(200, catalogFixture), nil
	})
	defer close(release)
	m.Start()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	m.channel.Publish(transportFunc(func(*http.Request) (*http.Response, error) { return response(503, ""), nil }))
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("old request not canceled")
	}
	if r := m.Best(Catalog); r.State != "unknown" || r.Entry != "" {
		t.Fatal(r)
	}
	// Let a misbehaving old transport finish successfully after cancellation.
	release <- struct{}{}
	awaitCondition(t, func() bool { return m.Best(Catalog).HTTP == 503 })
	if m.Best(Resource).State != "unknown" {
		t.Fatal("late results published")
	}
}
