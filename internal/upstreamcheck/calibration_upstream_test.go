package upstreamcheck

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// A client may expose the legacy direct interface, but diagnostics never use it.
type unselectedTransport struct{ http.RoundTripper }

func (unselectedTransport) Candidates(context.Context, string) ([]upstreamrequest.Candidate, error) {
	panic("diagnostic selected a channel")
}
func (unselectedTransport) Current(string) []upstreamrequest.Candidate {
	panic("diagnostic read channel history")
}
func (unselectedTransport) Changed() <-chan struct{} { panic("diagnostic subscribed to channels") }

func TestCalibrationUsesActiveClientAcrossResourceHosts(t *testing.T) {
	var active atomic.Int32
	var mu sync.Mutex
	hosts := map[string]int{}
	bothHosts := make(chan struct{})
	fixture, _ := calibrationManyVideosSized(t, 2, calibrationBlock, func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		hosts[r.URL.Host]++
		if len(hosts) == 2 && hosts[r.URL.Host] == 1 {
			close(bothHosts)
		}
		mu.Unlock()
		active.Add(1)
		resp := calibrationRangeSized(r, nil, calibrationBlock)
		resp.Body = &pacedCalibrationBody{ctx: r.Context(), remaining: resp.ContentLength, active: &active}
		return resp, nil
	})
	client := unselectedTransport{transportFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := fixture.RoundTrip(r)
		if r.URL.Path == "/Api/Songs/play" && resp != nil {
			var id int
			fmt.Sscanf(r.URL.Query().Get("id"), "%d", &id)
			host := "cf-a.example.com"
			if id > 1 {
				host = "cf-b.example.com"
			}
			location := strings.ReplaceAll(resp.Header.Get("Location"), "play.udon.dance", host)
			resp.Header.Set("Location", location)
		}
		return resp, err
	})}
	c := calibrationForTest(t, client, 2)
	c.limits.reserveVideos = 2
	// Each source is one block; observe both without opening additional lanes.
	c.limits.warmup, c.limits.window = time.Hour, time.Hour
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	<-bothHosts
	c.Cancel()
	s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
	if s.Phase != "canceled" || s.Failures != 0 || active.Load() != 0 {
		t.Fatalf("%+v active=%d", s, active.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if hosts["cf-a.example.com"] < 1 || hosts["cf-b.example.com"] < 1 {
		t.Fatal("resource hosts did not share the active client", hosts)
	}
}

func TestCalibrationCancellationDrainsActiveRequests(t *testing.T) {
	started := make(chan struct{}, 1)
	var active atomic.Int32
	c := calibrationForTest(t, unselectedTransport{transportFunc(func(r *http.Request) (*http.Response, error) {
		active.Add(1)
		defer active.Add(-1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	<-started
	c.Cancel()
	s := awaitCalibration(t, c, func(s CalibrationStatus) bool { return !s.Running })
	if s.Phase != "canceled" || active.Load() != 0 {
		t.Fatal(s.Phase, active.Load())
	}
}
