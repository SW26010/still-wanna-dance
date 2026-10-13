//go:build !external_only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// Only the loopback fixture control plane is used here. This is a diagnostic
// command, not an additional product backend or protocol implementation.
func (r *fullRuntime) directed(cmd command, pool *upstreamrequest.Pool) map[string]any {
	out := map[string]any{}
	checks := []map[string]any{}
	check := func(name string, ok bool, details any) {
		checks = append(checks, map[string]any{"name": name, "passed": ok, "details": details})
	}
	defer func() { out["checks"] = checks }()
	u, err := url.Parse(cmd.ControlURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || r.mode != "B2" {
		check("fixture-control", false, nil)
		return out
	}
	control := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer control.CloseIdleConnections()
	stats := func(path string) map[string]int {
		method := "GET"
		if path != "/stats" {
			method = "POST"
		}
		req, _ := http.NewRequest(method, cmd.ControlURL+path, nil)
		resp, e := control.Do(req)
		if e != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil
		}
		var s map[string]int
		if json.NewDecoder(resp.Body).Decode(&s) != nil {
			return nil
		}
		return s
	}
	wait := func(f func() bool) bool {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if f() {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return f()
	}
	request := func(ctx context.Context, host string) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+host+"/api", nil)
		cs := pool.Current(req.URL.String())
		if len(cs) != 1 {
			return upstreamrequest.ErrUnavailable
		}
		resp, e := cs[0].Transport.RoundTrip(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(resp.Body)
		if e == nil && (resp.StatusCode != 200 || string(b) != "ok") {
			e = errors.New("body-mismatch")
		}
		return e
	}
	healthy := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return request(ctx, "business.invalid") == nil
	}
	check("warm-business", healthy(), nil)
	initial := stats("/stats")
	out["initialPeer"] = initial
	if initial == nil || initial["peers"] != 1 || initial["handshakes"] != 1 {
		check("warm-session-observed", false, initial)
		return out
	}
	sameSession := func() bool {
		s := stats("/stats")
		return s != nil && s["peers"] == initial["peers"] && s["handshakes"] == initial["handshakes"]
	}
	pool.CloseIdleConnections()
	if cmd.Directed == "credit" {
		// OpenStream is nonblocking. Hold actual streams until peer credit is
		// exhausted; never replace it with OpenStreamSync or an injected mutex.
		var held []net.Conn
		defer func() {
			for _, c := range held {
				c.Close()
			}
		}()
		failed := false
		var failureMS float64
		for i := 0; i < 8; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			start := time.Now()
			c, e := r.proxies[0].Dial(ctx, "tcp", "hold.invalid:80")
			cancel()
			if e != nil {
				failed = true
				failureMS = float64(time.Since(start).Microseconds()) / 1000
				break
			}
			held = append(held, c)
			c.SetWriteDeadline(time.Now().Add(time.Second))
			_, e = io.WriteString(c, "GET /api HTTP/1.1\r\nHost: hold.invalid\r\n\r\n")
			if e != nil {
				break
			}
			if !wait(func() bool { s := stats("/stats"); return s != nil && s["held"] == len(held) }) {
				break
			}
		}
		out["creditBlockedState"] = r.snapshot()
		out["creditPeer"] = stats("/stats")
		check("credit-exhaustion-fails-without-waiting", failed && len(held) > 0 && failureMS < 500, map[string]any{"held": len(held), "failureMS": failureMS})
		for _, c := range held {
			c.Close()
		}
		check("credit-workers-and-peer-drain", wait(func() bool {
			s := stats("/stats")
			return s != nil && s["held"] == 0 && r.admission.snapshot()["occupied"] == 0
		}), r.snapshot())
		attempts := 0
		ok := wait(func() bool { attempts++; return healthy() })
		check("credit-recovery-on-same-session", ok && sameSession(), map[string]any{"attempts": attempts, "peer": stats("/stats")})
		return out
	}
	// One released handshake proves the fixture is capable of successful
	// service, followed by saturation held until explicit cancellation/retire.
	batch := func(n int) (context.CancelFunc, <-chan []error) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan []error, 1)
		go func() {
			var wg sync.WaitGroup
			results := make([]error, n)
			for i := range results {
				wg.Add(1)
				go func(i int) { defer wg.Done(); results[i] = request(ctx, "hold.invalid") }(i)
			}
			wg.Wait()
			done <- results
		}()
		return cancel, done
	}
	collect := func(done <-chan []error) ([]error, bool) {
		select {
		case es := <-done:
			return es, true
		case <-time.After(4 * time.Second):
			return nil, false
		}
	}
	cancel, done := batch(1)
	blocked := wait(func() bool { s := stats("/stats"); return s != nil && s["held"] == 1 })
	check("warm-response-gate-entered", blocked, map[string]any{"peer": stats("/stats"), "runtime": r.snapshot()})
	stats("/release")
	es, returned := collect(done)
	cancel()
	check("release-restores-request", blocked && returned && len(es) == 1 && es[0] == nil, nil)
	if !wait(func() bool {
		s := stats("/stats")
		return s != nil && s["held"] == 0 && r.admission.snapshot()["occupied"] == 0
	}) {
		check("release-drained", false, r.snapshot())
		return out
	}
	if stats("/arm") == nil {
		check("gate-rearmed", false, nil)
		return out
	}
	cancel, done = batch(64)
	defer cancel()
	blocked = wait(func() bool { s := stats("/stats"); return s != nil && s["held"] == 64 })
	out["blockedPeer"] = stats("/stats")
	out["blockedRuntime"] = r.snapshot()
	check("all-64-responses-deterministically-blocked", blocked && sameSession() && r.admission.snapshot()["occupied"] == 64, out["blockedRuntime"])
	if !blocked {
		cancel()
		collect(done)
		return out
	}
	stableStart := time.Now()
	select {
	case <-done:
		check("gate-stays-blocked-until-intervention", false, nil)
		return out
	case <-time.After(100 * time.Millisecond):
		check("gate-stays-blocked-until-intervention", stats("/stats")["held"] == 64,
			map[string]any{"heldMS": float64(time.Since(stableStart).Microseconds()) / 1000})
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	start := time.Now()
	c, capacityErr := r.proxies[0].Dial(ctx, "tcp", "business.invalid:80")
	stop()
	if c != nil {
		c.Close()
	}
	check("saturation-rejects-without-new-work", capacityErr != nil && capacityErr.Error() == "dial-capacity" && time.Since(start) < 500*time.Millisecond, r.snapshot())
	start = time.Now()
	if cmd.Directed == "retire" {
		pool.Close()
		r.Close()
	} else {
		cancel()
	}
	es, returned = collect(done)
	failed := 0
	for _, e := range es {
		if e != nil {
			failed++
		}
	}
	check("blocked-callers-return", returned && len(es) == 64 && failed == 64 && time.Since(start) < time.Second, map[string]any{"elapsedMS": float64(time.Since(start).Microseconds()) / 1000, "errors": failed})
	check("blocked-work-drains-without-gate-release", wait(func() bool {
		s := stats("/stats")
		return s != nil && s["held"] == 0 && r.admission.snapshot()["occupied"] == 0 && r.admission.snapshot()["pending"] == 0
	}), map[string]any{"peer": stats("/stats"), "runtime": r.snapshot()})
	if cmd.Directed == "retire" {
		c, e := r.proxies[0].Dial(context.Background(), "tcp", "business.invalid:80")
		if c != nil {
			c.Close()
		}
		check("retired-generation-stays-unavailable", errors.Is(e, upstreamrequest.ErrUnavailable), r.snapshot())
	} else {
		check("healthy-after-saturation-on-same-session", healthy() && sameSession(), map[string]any{"peer": stats("/stats"), "runtime": r.snapshot()})
		// Re-enter the gate to show that cancellation did not disable it; then
		// release it and demand a successful response on the original session.
		cancel, done = batch(1)
		defer cancel()
		blocked = wait(func() bool { s := stats("/stats"); return s != nil && s["held"] == 1 })
		stats("/release")
		es, returned = collect(done)
		check("new-held-request-recovers-after-release", blocked && returned && len(es) == 1 && es[0] == nil && sameSession(), stats("/stats"))
	}
	out["finalPeer"] = stats("/stats")
	out["finalRuntime"] = r.snapshot()
	return out
}
