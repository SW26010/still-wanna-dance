package cacheproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// DiscardLoader runs ordinary resource demands through the same range loader
// and scheduler as Prefetch. It has no cache, database, file, publication or
// integrity-check lifecycle, and cannot serve playback. Only small streaming
// buffers and bounded scheduling metadata survive while a load is active.
type DiscardLoader struct{ s *Server }

type DiscardConfig struct {
	Transport          http.RoundTripper
	ResourceTransports func(string) []http.RoundTripper
	ObserveBytes       func(int64)
	Observe            func(blocksched.Event)
}

func NewDiscardLoader(cfg DiscardConfig) (*DiscardLoader, error) {
	if cfg.Transport == nil {
		return nil, errors.New("discard loader requires an upstream transport")
	}
	c := DefaultConfig()
	c.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	c.ObserveDownloadBytes, c.ObserveBlockSchedule = cfg.ObserveBytes, cfg.Observe
	c.ResourceTransports = cfg.ResourceTransports
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{cfg: c, ctx: ctx, cancel: cancel, requests: new(requestActivity),
		capacityChanged: make(chan struct{}),
		client:          &http.Client{Transport: cfg.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.scheduler = newDownloadScheduler(c)
	return &DiscardLoader{s}, nil
}

func (d *DiscardLoader) SetSoftLimit(limit int) bool {
	if limit < 1 || limit > VideoRequestSoftLimit {
		return false
	}
	return d.s.scheduler.SetSoftLimit(limit)
}

// Load admits one ordinary resource preparation, waiting for metadata capacity
// with the same lifetime as Prefetch. Resources share one concurrency ceiling.
func (d *DiscardLoader) Load(ctx context.Context, target string) error {
	s := d.s
	if !s.beginRequest() {
		return context.Canceled
	}
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	v, err := s.parseResolved(r)
	if err != nil {
		return err
	}
	_, release, err := s.addPreparation(ctx, v)
	if err != nil {
		return err
	}
	defer release()
	preparationAdmitted(ctx)
	now := time.Now()
	f := &flight{discard: true, log: s.cfg.Logger, streaming: make(chan struct{}),
		progress: &downloadProgress{log: s.cfg.Logger, start: now, changed: now, lastByte: now, observeBytes: s.observeDownloadBytes}}
	return s.publishScheduled(ctx, "", v, f)
}

func (d *DiscardLoader) Close() {
	s := d.s
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		if s.useTimer != nil {
			s.useTimer.Stop()
		}
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		s.scheduler.Close()
	})
}
