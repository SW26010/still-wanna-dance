package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBodyProbeRangeGateResumeAndCancel(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 8192)
	file := filepath.Join(t.TempDir(), "sample.mp4")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	gate := &bodyGate{limit: 1024}
	s := httptest.NewServer(bodyHandler(map[string]bodyMedia{"1": {Song: "1", File: file, MD5: "fixture"}}, gate, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/Api/Songs/play?id=1", nil)
	req.Header.Set("Range", "bytes=13-4099")
	req.Header.Set("User-Agent", "NSPlayer/test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 206 || resp.ContentLength != 4087 || resp.Header.Get("Content-Range") != "bytes 13-4099/131072" {
		t.Fatalf("bad range: %v", resp.Header)
	}
	first := make([]byte, 1024)
	if _, err = io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, data[13:1037]) {
		t.Fatal("wrong offset")
	}
	done := make(chan []byte, 1)
	go func() { rest, _ := io.ReadAll(resp.Body); done <- rest }()
	select {
	case <-done:
		t.Fatal("gate manufactured EOF")
	case <-time.After(60 * time.Millisecond):
	}
	gate.mu.Lock()
	gate.limit = 4087
	gate.mu.Unlock()
	select {
	case rest := <-done:
		if !bytes.Equal(rest, data[1037:4100]) {
			t.Fatal("resume corrupted body")
		}
	case <-ctx.Done():
		t.Fatal("resume blocked")
	}
	gate.mu.Lock()
	delivered := gate.delivered
	gate.mu.Unlock()
	if delivered != 4087 {
		t.Fatal(delivered)
	}
	// A fresh waiting request must leave promptly when its client disconnects.
	blocked, cancelBlocked := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { _, err := gate.take(blocked, 16); finished <- err }()
	cancelBlocked()
	select {
	case err := <-finished:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
	// HEAD does not consume allowance; non-player resolver requests are separately logged and ungated.
	head, _ := http.NewRequestWithContext(ctx, "HEAD", s.URL+"/Api/Songs/play?id=1", nil)
	hr, err := http.DefaultClient.Do(head)
	if err != nil {
		t.Fatal(err)
	}
	hr.Body.Close()
	if hr.ContentLength != int64(len(data)) {
		t.Fatal(hr.ContentLength)
	}
}

func TestBodyGateSharedBudgetAndShortWrite(t *testing.T) {
	g := &bodyGate{limit: 100}
	a, err := g.take(context.Background(), 80)
	if err != nil || a != 80 {
		t.Fatal(a, err)
	}
	b, err := g.take(context.Background(), 80)
	if err != nil || b != 20 {
		t.Fatal(b, err)
	}
	g.finish(a, 50)
	g.finish(b, 20)
	c, err := g.take(context.Background(), 80)
	if err != nil || c != 30 {
		t.Fatal(c, err)
	}
	if total := g.finish(c, c); total != 100 {
		t.Fatal(total)
	}
}

func TestBodyProbeSendsHeadersBeforeWaitingForBodyQuota(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		name := "zero initial quota"
		if exhausted {
			name = "quota exhausted by previous GET"
		}
		t.Run(name, func(t *testing.T) {
			data := []byte("0123456789abcdef")
			file := filepath.Join(t.TempDir(), "sample.mp4")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			gate := &bodyGate{}
			if exhausted {
				gate.limit = 1
			}
			s := httptest.NewServer(bodyHandler(map[string]bodyMedia{"1": {Song: "1", File: file, MD5: "fixture"}}, gate, slog.New(slog.NewTextHandler(io.Discard, nil))))
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.ResponseHeaderTimeout = 500 * time.Millisecond
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			request := func(rng string) *http.Request {
				r, err := http.NewRequestWithContext(ctx, "GET", s.URL+"/Api/Songs/play?id=1", nil)
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("User-Agent", "NSPlayer/test")
				if rng != "" {
					r.Header.Set("Range", rng)
				}
				return r
			}
			if exhausted {
				first, err := client.Do(request("bytes=0-0"))
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(first.Body)
				first.Body.Close()
				if err != nil || !bytes.Equal(body, data[:1]) {
					t.Fatalf("first GET: %q, %v", body, err)
				}
			}
			rng, wantStatus, want := "", http.StatusOK, data
			if exhausted {
				rng, wantStatus, want = "bytes=8-15", http.StatusPartialContent, data[8:]
			}
			resp, err := client.Do(request(rng))
			if err != nil {
				t.Fatalf("response headers blocked by body quota: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != wantStatus || resp.ContentLength != int64(len(want)) {
				t.Fatalf("unexpected headers: status=%d length=%d", resp.StatusCode, resp.ContentLength)
			}
			type readResult struct {
				body []byte
				err  error
			}
			done := make(chan readResult, 1)
			go func() {
				body, err := io.ReadAll(resp.Body)
				done <- readResult{body, err}
			}()
			select {
			case result := <-done:
				t.Fatalf("body finished before quota release: %q, %v", result.body, result.err)
			case <-time.After(60 * time.Millisecond):
			}
			gate.mu.Lock()
			if gate.delivered != gate.limit {
				t.Errorf("body exceeded quota: %d != %d", gate.delivered, gate.limit)
			}
			gate.limit += int64(len(want))
			gate.mu.Unlock()
			select {
			case result := <-done:
				if result.err != nil || !bytes.Equal(result.body, want) {
					t.Fatalf("released body: %q, %v", result.body, result.err)
				}
			case <-ctx.Done():
				t.Fatal("body did not resume")
			}
		})
	}
}

func TestPacerRestoreBandwidthWakesExistingConnection(t *testing.T) {
	p := &pacer{rate: 1}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.wait(ctx, 16384) }()
	time.Sleep(20 * time.Millisecond)
	p.setRate(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("rate restoration failed")
	}
}
