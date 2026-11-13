package cacheproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// The existing demand event is emitted after the scheduler accepts the merged
// update. Observe it so the body retry is tested under the closed contribution.
type handoffLogCapture struct {
	logCapture
	closed chan struct{}
	once   sync.Once
}

func (c *handoffLogCapture) Write(p []byte) (int, error) {
	n, err := c.logCapture.Write(p)
	var record struct {
		Message      string `json:"msg"`
		Block        int64  `json:"block"`
		WindowClosed bool   `json:"window_closed"`
	}
	if json.Unmarshal(p, &record) == nil && record.Message == "block_demand" && record.Block == 1 && record.WindowClosed {
		c.once.Do(func() { close(c.closed) })
	}
	return n, err
}

func TestExpiredGETKeepsFutureBlockExecution(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "share-partial-block"
		if retry {
			name = "retry-within-running-block"
		}
		t.Run(name, func(t *testing.T) {
			const body = "0123456789abcdef"
			logs := &handoffLogCapture{closed: make(chan struct{})}
			s, cfg := setup(t, nil, func(cfg *Config) {
				cfg.BlockSize = 8
				cfg.Logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			})
			var calls, runs atomic.Int32
			s.scheduler.Close()
			s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 2, Observe: func(e blocksched.Event) {
				if _, _, block, ok := parseDownloadTaskKey(e.Key); ok && block == 1 && e.Kind == "started" {
					runs.Add(1)
				}
			}})
			prefix, retryStarted := make(chan struct{}), make(chan struct{})
			gate, secondGate := make(chan struct{}), make(chan struct{})
			open := sync.OnceFunc(func() { close(gate) })
			openSecond := sync.OnceFunc(func() { close(secondGate) })
			defer open()
			defer openSecond()
			s.cfg.ResourceTransports = func(string) []http.RoundTripper {
				return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
					resp := fixtureRangeResponse(r, body)
					if r.Header.Get("Range") == "bytes=0-7" {
						resp.Body = io.NopCloser(publishReaderFunc(func([]byte) (int, error) {
							<-r.Context().Done()
							return 0, r.Context().Err()
						}))
						return resp, nil
					}
					attempt := calls.Add(1)
					if attempt == 2 {
						close(retryStarted)
					}
					data := strings.NewReader(body[8:])
					first := true
					resp.Body = io.NopCloser(publishReaderFunc(func(p []byte) (int, error) {
						if first && attempt == 1 {
							first = false
							n, err := data.Read(p[:1])
							close(prefix)
							return n, err
						}
						wait := gate
						if attempt > 1 {
							wait = secondGate
						}
						select {
						case <-r.Context().Done():
							return 0, r.Context().Err()
						case <-wait:
						}
						if retry && attempt == 1 {
							return 0, io.ErrUnexpectedEOF
						}
						return data.Read(p)
					}))
					return resp, nil
				})}
			}
			r, _ := http.NewRequest("GET", videoURL(body), nil)
			v, _ := s.parseResolved(r)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			response := httptest.NewRecorder()
			getResult := make(chan any, 1)
			go func() {
				defer func() { getResult <- recover() }()
				s.ServeHTTP(response, httptest.NewRequest("GET", videoURL(body), nil).WithContext(ctx))
			}()
			receiveDemandEvent(t, prefix)
			// Pre-age only the source input, after both real HTTP bodies start.
			// The real scheduler timer, GET exit and persisted history remain in
			// the loop without adding 30 seconds to every fixture invocation.
			clock := &playbackClock{at: time.Now().Add(time.Second - blocksched.DownloadGrace), limit: v.size, media: mediaTiming{v.size, 2 * time.Minute}}
			s.mu.Lock()
			var old *downloadDemand
			for d := range s.demands {
				if d.playback && d.key == v.key {
					old = d
					d.clock = clock
				}
			}
			s.notifyUseChangedLocked(v.key)
			s.mu.Unlock()
			if old == nil {
				t.Fatal("HTTP request did not register its GET source")
			}
			if crash := receiveDemandEvent(t, getResult); crash != nil || response.Code != http.StatusGatewayTimeout || !errors.Is(context.Cause(old.ctx), blocksched.ErrExpired) || ctx.Err() != nil {
				t.Fatal("GET lost its service expiry response", crash, response.Code, context.Cause(old.ctx))
			}
			f := old.flight
			s.mu.Lock()
			shared, handoffs := s.flights[v.key] == f && !f.preempted, len(s.useHandoffs)
			s.mu.Unlock()
			if !shared || handoffs != 1 || calls.Load() != 1 || runs.Load() != 1 {
				t.Fatal("expired GET retired the shared flight or future block", shared, handoffs, calls.Load(), runs.Load())
			}
			receiveDemandEvent(t, logs.closed)
			if retry {
				// No replacement GET exists yet. A valid Run may still use its
				// bounded body retry under the closed handoff contribution.
				open()
				receiveDemandEvent(t, retryStarted)
				if runs.Load() != 1 {
					t.Fatal("HTTP retry recreated the block execution")
				}
			}
			nextClock := &playbackClock{at: time.Now(), offset: 8, limit: 16, media: clock.media}
			nextCtx := context.WithValue(context.WithValue(ctx, httpGETKey{}, true), playbackClockKey{}, nextClock)
			next, resumed, err := s.obtain(nextCtx, v)
			if err != nil || resumed == nil || next != f {
				t.Fatal("replacement GET did not share the existing flight", err)
			}
			defer resumed.Close()
			if _, err := resumed.Seek(8, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			open()
			openSecond()
			got := make([]byte, 8)
			if _, err := io.ReadFull(resumed, got); err != nil || string(got) != body[8:] {
				t.Fatal("replacement GET lost the in-flight block", string(got), err)
			}
			wantCalls := int32(1)
			if retry {
				wantCalls = 2
			}
			if calls.Load() != wantCalls || runs.Load() != 1 || s.StorageError() != nil {
				t.Fatal("handoff duplicated an execution or poisoned storage", calls.Load(), runs.Load(), s.StorageError())
			}
			resumed.Close()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			page, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 10})
			if err != nil || len(page.Requests) != 1 || page.Requests[0].Outcome != "expired" || errors.Is(f.err, blocksched.ErrExpired) {
				t.Fatal("GET history inherited the independent resource result", page.Requests, err, f.err)
			}
		})
	}
}
