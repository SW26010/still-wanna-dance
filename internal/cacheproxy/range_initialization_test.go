package cacheproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// The source context is inspected during planning. Gate the 206 initialization
// there once a real HTTP reader is waiting, without adding a production hook.
type initializeDuringPlanContext struct {
	context.Context
	scheduler  *blocksched.Scheduler
	tailKey    string
	preflight  <-chan struct{}
	preplanned chan struct{}
	preOnce    sync.Once
	armed      <-chan struct{}
	entered    chan struct{}
	initialize func()
	firstRead  <-chan struct{}
	raceOnce   sync.Once
}

func (c *initializeDuringPlanContext) Err() error {
	select {
	case <-c.preflight:
		c.preOnce.Do(func() { close(c.preplanned) })
	default:
	}
	select {
	case <-c.armed:
		if c.scheduler.Observation().Tasks[c.tailKey].Readers > 0 {
			c.raceOnce.Do(func() {
				close(c.entered)
				c.initialize()
				select {
				case <-c.firstRead:
				case <-c.Done():
				}
			})
		}
	default:
	}
	return c.Context.Err()
}

type rangeInitializationGate struct {
	attached     chan struct{}
	stageEntered chan struct{}
	release      <-chan struct{}
	once         sync.Once
}

// Hold only the range stage message; other handlers can log while it waits.
type rangeInitializationHandler struct {
	slog.Handler
	gate *rangeInitializationGate
}

func (h rangeInitializationHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "cache_task_attached" {
		select {
		case h.gate.attached <- struct{}{}:
		default:
		}
	}
	if record.Message == "download_stage" {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "stage" && attr.Value.String() == "range_download" {
				h.gate.once.Do(func() {
					close(h.gate.stageEntered)
					<-h.gate.release
				})
			}
			return true
		})
	}
	return h.Handler.Handle(ctx, record)
}

func (h rangeInitializationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return rangeInitializationHandler{Handler: h.Handler.WithAttrs(attrs), gate: h.gate}
}

func (h rangeInitializationHandler) WithGroup(name string) slog.Handler {
	return rangeInitializationHandler{Handler: h.Handler.WithGroup(name), gate: h.gate}
}

type blockedInitialRange struct {
	ctx     context.Context
	started chan struct{}
	release <-chan struct{}
	reader  io.Reader
	once    sync.Once
}

func (b *blockedInitialRange) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return b.reader.Read(p)
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}

func (b *blockedInitialRange) Close() error { return nil }

func TestHTTPSeekDuringRangeInitialization(t *testing.T) {
	const blockSize = 8 * 1024
	body := bytes.Repeat([]byte("01234567"), 3*blockSize/8)
	initialRequest, headers := make(chan struct{}), make(chan struct{})
	stageRelease, firstRead, prefixRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseHeaders := sync.OnceFunc(func() { close(headers) })
	releaseStage := sync.OnceFunc(func() { close(stageRelease) })
	releasePrefix := sync.OnceFunc(func() { close(prefixRelease) })
	defer releaseHeaders()
	defer releaseStage()
	defer releasePrefix()
	gate := &rangeInitializationGate{attached: make(chan struct{}, 2), stageEntered: make(chan struct{}), release: stageRelease}
	s, _ := setup(t, nil, func(c *Config) {
		c.BlockSize = blockSize
		c.Logger = slog.New(rangeInitializationHandler{Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}), gate: gate})
	})
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			var start, end int64
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
				return nil, err
			}
			var responseBody io.ReadCloser = io.NopCloser(bytes.NewReader(body[start : end+1]))
			if start == 0 {
				close(initialRequest)
				select {
				case <-headers:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				responseBody = &blockedInitialRange{ctx: r.Context(), started: firstRead, release: prefixRelease, reader: bytes.NewReader(body[start : end+1])}
			}
			return &http.Response{StatusCode: http.StatusPartialContent, ContentLength: end - start + 1, Request: r,
				Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}, "ETag": {`"stable"`}}, Body: responseBody}, nil
		})}
	}
	target := videoURL(string(body))
	r, _ := http.NewRequest(http.MethodGet, target, nil)
	v, err := s.parseResolved(r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	demand, release := addTestDemand(t, s, ctx, v, false)
	defer release()
	preflight, armed := make(chan struct{}), make(chan struct{})
	probe := &initializeDuringPlanContext{Context: demand.ctx, scheduler: s.scheduler,
		tailKey: downloadTaskKey(v.key, v.size, 2), preflight: preflight, preplanned: make(chan struct{}),
		armed: armed, entered: make(chan struct{}), initialize: releaseStage, firstRead: firstRead}
	s.mu.Lock()
	demand.ctx = probe
	s.mu.Unlock()
	background := context.WithValue(ctx, sourceDemandKey{}, demand)
	go func() { _, _, _ = s.obtainMode(background, v, true) }()
	wait := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	wait(initialRequest, "initial request")
	wait(gate.attached, "background attachment")
	responses := make(chan *responseRecorder, 1)
	go func() {
		responses <- request(s, http.MethodGet, target, map[string]string{"Range": fmt.Sprintf("bytes=%d-", len(body)-128)})
	}()
	wait(gate.attached, "HTTP attachment")
	// Let the coordinator consume the already-attached HTTP demand before the
	// response headers expose the stream and its tail reader registers.
	close(preflight)
	s.mu.Lock()
	s.notifyResourceChangedLocked(v.key)
	s.mu.Unlock()
	wait(probe.preplanned, "pre-initialization plan")
	// The notification is emitted under this lock; wait until the plan has
	// captured its replacement change channel before exposing the stream.
	s.mu.Lock()
	s.mu.Unlock()
	close(armed)
	releaseHeaders()
	wait(gate.stageEntered, "range initialization stage")
	wait(probe.entered, "tail read during planning")
	wait(firstRead, "initialization completion")
	select {
	case response := <-responses:
		if response.aborted || response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), body[len(body)-128:]) {
			t.Fatalf("tail response: status=%d bytes=%d aborted=%t", response.Code, response.Body.Len(), response.aborted)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("tail read did not complete while the first range was blocked")
	}
}
