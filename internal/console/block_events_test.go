package console

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestBlockDeltaResetChangeRemovalAndClockOnly(t *testing.T) {
	one := cacheproxy.BlockView{ID: "a/1", Block: 1, State: "waiting", At: 100, NeedBy: 100, Deadline: 30100}
	initial := &cacheproxy.BlockSnapshot{At: 1, Capacity: 4096, Used: 1, Blocks: map[string]cacheproxy.BlockView{one.ID: one}}
	first, changed := blockDelta(nil, initial)
	if !changed || !first.Reset || len(first.Blocks) != 1 || first.Blocks[0] != one {
		t.Fatal(first)
	}
	next := *initial
	next.At = 2
	if _, changed := blockDelta(initial, &next); changed {
		t.Fatal("clock tick produced event")
	}
	two := one
	two.State = "available"
	next.Blocks = map[string]cacheproxy.BlockView{two.ID: two}
	if event, changed := blockDelta(initial, &next); !changed || event.Reset || len(event.Blocks) != 1 {
		t.Fatal(event)
	}
	next.Blocks = map[string]cacheproxy.BlockView{}
	next.Used = 0
	if event, changed := blockDelta(initial, &next); !changed || len(event.Removed) != 1 || len(event.Blocks) != 0 {
		t.Fatal(event)
	}
}

func TestBlockEventsRefreshClockForSharedSnapshot(t *testing.T) {
	c := testConsole(t)
	c.lifecycleMu.Lock()
	_, err := c.ensureEngineLocked()
	c.lifecycleMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// One window keeps the observer alive while a second connects/reconnects.
	keeper, err := c.service.ObserveBlocks()
	if err != nil {
		t.Fatal(err)
	}
	defer keeper.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var initial *cacheproxy.BlockSnapshot
	for initial == nil {
		current, changed, closed := keeper.Snapshot()
		if closed {
			t.Fatal("observer closed")
		}
		if current != nil {
			initial = current
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("initial snapshot not published")
		}
	}
	initialAt := initial.At
	for _, name := range []string{"new-window", "reconnect"} {
		t.Run(name, func(t *testing.T) {
			// Cross a clock tick without a state change or a new snapshot.
			time.Sleep(10 * time.Millisecond)
			before := time.Now().UnixMilli()
			streamCtx, stop := context.WithCancel(ctx)
			defer stop()
			w := &diagnosticEventWriter{ResponseRecorder: httptest.NewRecorder(), t: t, afterFlush: stop}
			r := httptest.NewRequest("GET", "http://"+c.address+"/api/block-demands/events", nil).WithContext(streamCtx)
			c.ServeHTTP(w, r)
			after := time.Now().UnixMilli()
			if w.Code != 200 || w.writes != 1 {
				t.Fatal("expected one initial event", w.Code, w.Body.String())
			}
			_, body, ok := strings.Cut(w.Body.String(), "data: ")
			var event blockEvent
			if !ok || json.Unmarshal([]byte(body), &event) != nil || !event.Running || !event.Reset {
				t.Fatal("invalid initial event", w.Body.String())
			}
			if event.At < before || event.At > after {
				t.Errorf("event clock = %d, want send time in [%d, %d]; shared snapshot = %d", event.At, before, after, initialAt)
			}
			current, changed, _ := keeper.Snapshot()
			if current != initial || initial.At != initialAt {
				t.Fatal("connecting rebuilt or mutated the shared snapshot")
			}
			select {
			case <-changed:
				t.Fatal("connecting triggered observation")
			default:
			}
		})
	}
}

func TestBlockDeltaDemandMembershipAndOrdering(t *testing.T) {
	demand := cacheproxy.BlockDemandView{ID: "1/a", SourceID: 1, Kind: "queue", Position: 2, Blocks: []string{"a/0", "a/1"}}
	initial := &cacheproxy.BlockSnapshot{Demands: map[string]cacheproxy.BlockDemandView{demand.ID: demand}}
	if event, changed := blockDelta(nil, initial); !changed || len(event.Demands) != 1 {
		t.Fatal("missing demand reset", event)
	}
	next := *initial
	next.At++
	if _, changed := blockDelta(initial, &next); changed {
		t.Fatal("unchanged demand produced event")
	}
	for _, update := range []cacheproxy.BlockDemandView{
		{ID: demand.ID, SourceID: 1, Kind: "queue", Position: 1, Blocks: demand.Blocks},
		{ID: demand.ID, SourceID: 1, Kind: "queue", Position: 2, Blocks: []string{"a/1"}},
	} {
		next.Demands = map[string]cacheproxy.BlockDemandView{update.ID: update}
		if event, changed := blockDelta(initial, &next); !changed || len(event.Demands) != 1 || len(event.Blocks) != 0 {
			t.Fatal("lost demand-only update", event)
		}
	}
	next.Demands = nil
	if event, changed := blockDelta(initial, &next); !changed || len(event.RemovedDemands) != 1 || event.RemovedDemands[0] != demand.ID {
		t.Fatal("lost ended demand", event)
	}
}

func TestBlockEventsOriginAndOffline(t *testing.T) {
	c := testConsole(t)
	for _, tc := range []struct {
		host, origin, site string
		code               int
	}{
		{c.address, "", "same-origin", 200},
		{"evil.example", "", "", 403},
		{c.address, "http://evil.example", "", 403},
		{c.address, "", "cross-site", 403},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/block-demands/events", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Sec-Fetch-Site", tc.site)
		w := httptest.NewRecorder()
		if tc.code == 200 {
			ctx, cancel := context.WithCancel(r.Context())
			writer := &diagnosticEventWriter{ResponseRecorder: w, t: t, afterFlush: cancel}
			c.ServeHTTP(writer, r.WithContext(ctx))
			cancel()
		} else {
			c.ServeHTTP(w, r)
		}
		if w.Code != tc.code {
			t.Fatal(w.Code, w.Body.String())
		}
		if tc.code == 200 && !strings.Contains(w.Body.String(), `"running":false`) {
			t.Fatal(w.Body.String())
		}
	}
	if c.service != nil {
		t.Fatal("subscription started engine")
	}
	if c.blockEngineWaiters != nil {
		t.Fatal("disconnected page retained engine waiters")
	}
}

func TestBlockEventsWaitForEngine(t *testing.T) {
	c := testConsole(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	w := &diagnosticEventWriter{ResponseRecorder: httptest.NewRecorder(), t: t}
	w.afterFlush = func() {
		if w.writes == 1 {
			c.lifecycleMu.Lock()
			_, err := c.ensureEngineLocked()
			c.lifecycleMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
		} else {
			cancel()
		}
	}
	r := httptest.NewRequest("GET", "http://"+c.address+"/api/block-demands/events", nil).WithContext(ctx)
	c.ServeHTTP(w, r)
	if ctx.Err() == context.DeadlineExceeded || w.writes != 2 || !strings.Contains(w.Body.String(), `"running":false`) || !strings.Contains(w.Body.String(), `"running":true`) {
		t.Fatal("engine creation did not continue the original stream", w.Body.String())
	}
}

func TestBlockEngineWaiterCancellationAndShutdown(t *testing.T) {
	for _, action := range []string{"cancel", "start", "shutdown"} {
		c := testConsole(t)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan *cacheproxy.Server, 1)
		go func() { done <- c.waitBlockEngine(ctx) }()
		deadline := time.Now().Add(time.Second)
		for {
			c.mu.Lock()
			waiting := len(c.blockEngineWaiters) > 0
			c.mu.Unlock()
			if waiting {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("waiter not registered")
			}
			time.Sleep(time.Millisecond)
		}
		if action == "shutdown" {
			c.BeginShutdown()
		} else if action == "start" {
			c.lifecycleMu.Lock()
			_, err := c.ensureEngineLocked()
			c.lifecycleMu.Unlock()
			if err != nil {
				cancel()
				t.Fatal(err)
			}
		} else {
			cancel()
		}
		select {
		case service := <-done:
			if (service != nil) != (action == "start") || c.blockEngineWaiters != nil {
				t.Fatal("wrong engine or retained waiter after", action)
			}
		case <-time.After(time.Second):
			t.Fatal("waiter did not finish")
		}
		cancel()
	}
}

func TestBlockEventsDisconnect(t *testing.T) {
	for _, failure := range []string{"write", "flush", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			c := testConsole(t)
			c.lifecycleMu.Lock()
			_, err := c.ensureEngineLocked()
			c.lifecycleMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			r := httptest.NewRequest("GET", "http://"+c.address+"/api/block-demands/events", nil).WithContext(ctx)
			w := &diagnosticEventWriter{ResponseRecorder: httptest.NewRecorder(), t: t, writeError: failure == "write", flushError: failure == "flush"}
			if failure == "cancel" {
				w.afterFlush = cancel
			}
			c.ServeHTTP(w, r)
			if ctx.Err() == context.DeadlineExceeded {
				t.Fatal("failed to release stream")
			}
			if w.writes != 1 {
				t.Fatal("continued after disconnect", w.writes)
			}
		})
	}
}
