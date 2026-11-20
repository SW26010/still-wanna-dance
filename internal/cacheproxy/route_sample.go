package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrNoSampleDue means no identifiable route needs an automatic sample.
var ErrNoSampleDue = errors.New("no resource route due for sampling")

// Proxies have one upstream across all resource hosts. Reserve once for the
// entire sample, including redirects. A subsequent business observation also
// shifts the next eligible time.
func (h *DownloadRoutes) reserveProxySample() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if now.Before(h.proxySampleAfter) {
		return false
	}
	h.proxySampleAfter = now.Add(sampleInterval)
	return true
}

// SampleBlock samples one due route independently of local block demand and storage.
func SampleBlock(ctx context.Context, cfg Config, client *http.Client, target string, block int64, manual bool) error {
	return sampleBlock(ctx, cfg, client, target, block, manual, "")
}

// SampleBlockOnIP uses only the requested resource host's current direct candidate.
func SampleBlockOnIP(ctx context.Context, cfg Config, client *http.Client, target string, block int64, ip string) error {
	if ip == "" || cfg.DownloadRoutes == nil || !cfg.DownloadRoutes.direct {
		return ErrNoSampleDue
	}
	return sampleBlock(ctx, cfg, client, target, block, true, ip)
}

func sampleBlock(ctx context.Context, cfg Config, client *http.Client, target string, block int64, manual bool, ip string) error {
	sampleClient := *client
	sampleClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s := Server{cfg: cfg, client: &sampleClient, requests: new(requestActivity)}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	v, err := s.parseResolved(r)
	if err != nil {
		return err
	}
	size := blockBytes(v.size, block, DefaultBlockSize)
	if size == 0 {
		return errors.New("invalid resource sample block")
	}
	if h := cfg.DownloadRoutes; h != nil && !h.direct && !h.reserveProxySample() {
		return ErrNoSampleDue
	}
	ctx, cancel := context.WithTimeoutCause(ctx, 10*time.Second, errSampleTimeout)
	defer cancel()
	req, err := s.routeRequest(ctx, v)
	if err != nil {
		return err
	}
	start := block * DefaultBlockSize
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+size-1))
	resp, err := s.videoResponse(req, v, &resourceAttempts{sample: &blockSample{visited: make(map[routeKey]bool), manual: manual, target: routeKey{host: v.host, ip: ip}}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := validateRangeResponse(resp, start, start+size, v.size); err != nil {
		return err
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, size+1))
	if err == nil && n < size {
		err = io.ErrUnexpectedEOF
	}
	observation := resp.Body.(*resourceBody).observation
	if err != nil {
		observation.finish(0, err)
		return err
	}
	if n != size {
		return errors.New("oversized resource sample")
	}
	observation.finish(n, nil)
	return nil
}

var errSampleTimeout = fmt.Errorf("resource sampling timed out: %w", context.DeadlineExceeded)
