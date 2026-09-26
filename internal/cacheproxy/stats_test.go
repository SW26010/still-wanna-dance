package cacheproxy

import (
	"net/http"
	"testing"
	"time"
)

func TestTrafficStats(t *testing.T) {
	s := &Server{}
	if v := s.TrafficStats(); v.HitRate != nil || v.ReductionPercent != nil || v.LocalMS != nil || v.UpstreamMS != nil {
		t.Fatalf("empty statistics: %+v", v)
	}
	s.recordUpstream(100 * time.Millisecond)
	s.recordUpstream(300 * time.Millisecond)
	s.recordTraffic("GET", "MISS", "completed", 200, time.Second, 100)
	s.recordTraffic("GET", "HIT", "completed", 206, 50*time.Millisecond, 7)
	for _, sample := range []struct {
		method, outcome string
		status          int
	}{
		{"HEAD", "completed", 200}, {"GET", "failed", 200}, {"GET", "canceled", 206},
		{"GET", "aborted", 200}, {"GET", "completed", 304}, {"GET", "completed", 416},
	} {
		s.recordTraffic(sample.method, "HIT", sample.outcome, sample.status, time.Second, 1000)
	}
	v := s.TrafficStats()
	if v.Requests != 2 || v.Hits != 1 || v.Misses != 1 || v.SavedBytes != 7 || *v.HitRate != 50 || *v.LocalMS != 50 || *v.UpstreamMS != 200 || *v.ReductionPercent != 75 {
		t.Fatalf("unexpected statistics: %+v", v)
	}
	s.recordTraffic("GET", "HIT", "completed", 200, time.Second, 100)
	if *s.TrafficStats().ReductionPercent >= 0 {
		t.Fatal("slower local response must retain a negative reduction")
	}
}

func TestTrafficStatsHTTP(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) })
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	// Wait for the shared download worker to publish before checking a hot hit.
	s.mu.Lock()
	var done []chan struct{}
	for _, f := range s.flights {
		done = append(done, f.done)
	}
	s.mu.Unlock()
	for _, ch := range done {
		<-ch
	}
	assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=2-5"}), 206, payload[2:6])
	request(s, "HEAD", videoURL(payload), nil)
	request(s, "POST", videoURL(payload), nil)
	request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=900-"})
	v := s.TrafficStats()
	if v.Hits != 1 || v.Misses != 1 || v.SavedBytes != 4 || v.UpstreamSamples != 1 || v.LocalSamples != 1 || v.LocalMS == nil || v.UpstreamMS == nil {
		t.Fatalf("HTTP statistics: %+v", v)
	}
}
