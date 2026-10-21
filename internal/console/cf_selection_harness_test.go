//go:build windows && cf_block_experiment

package console

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptrace"
	"slices"
	"sync/atomic"
	"testing"
)

func TestCFSelectionHarness(t *testing.T) {
	dns := []string{"104.26.4.28", "104.26.5.28", "172.67.69.58", "2606:4700:20::681a:41c", "2606:4700:20::681a:51c", "2606:4700:20::ac43:453a"}
	cs := cfSelectionCandidates(dns)
	if len(cs) != 14 || !slices.Equal(cs, cfSelectionCandidates(dns)) {
		t.Fatal("finite reproducible candidate generation")
	}
	if len(cfSelectionCandidates(append(dns, "104.24.0.1"))) != 14 {
		t.Fatal("candidate sources must deduplicate")
	}
	probes := []cfSelectionProbe{
		{IP: dns[0], Kind: "single-8388608", ElapsedMS: 2000},
		{IP: "104.24.0.1", Kind: "single-8388608", ElapsedMS: 1000},
		{IP: "104.16.0.1", Kind: "single-8388608", ElapsedMS: 10, Error: "incomplete"},
		{IP: dns[1], Kind: "single-1048576", ElapsedMS: 1},
	}
	if cfSelectionPick(cs, probes, true) != dns[0] || cfSelectionPick(cs, probes, false) != "104.24.0.1" {
		t.Fatal("selection mixed failures, sizes or candidate sources")
	}
	if cfSelectionPick(cs, nil, true) != "" {
		t.Fatal("missing measurements cannot select a winner")
	}
}

func TestCFRecorderFailureDoesNotAttributeDNSRemote(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var total atomic.Int64
	c := &cfexpCapture{total: &total, cancel: cancel, tr: cfexpFunc(func(r *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(r.Context()).GotConn(httptrace.GotConnInfo{Conn: cfexpConn{}})
		return nil, errors.New("DNS fixture failure")
	})}
	r, _ := http.NewRequestWithContext(ctx, "GET", "https://"+cfexpHost, nil)
	r.Header.Set("Range", "bytes=0-1048575")
	c.target.Size = 1 << 20
	_, err := c.RoundTrip(r)
	if err == nil || len(c.rows) != 1 || c.rows[0].Remote != "" || c.rows[0].ErrorDetail != "DNS fixture failure" {
		t.Fatalf("ambiguous failed trace must not be a media remote: %+v", c.rows)
	}
}
