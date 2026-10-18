package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestDownloadedPublicationKeepsIndependentTimeout(t *testing.T) {
	s, cfg := setup(t, nil)
	s.cfg.DownloadTimeout = 30 * time.Millisecond
	f, v := completedRangeFlight(t, s, payload)
	for range cap(s.localChecks) {
		s.localChecks <- struct{}{}
	}
	defer func() {
		for range cap(s.localChecks) {
			<-s.localChecks
		}
	}()
	err := s.publishScheduled(context.Background(), cfg.videoFile(v.key), v, f)
	if !errors.Is(err, context.DeadlineExceeded) || s.StorageError() != nil {
		t.Fatal("local publication lost its bounded wait or tripped storage fuse", err)
	}
	if _, err := os.Stat(cfg.videoFile(v.key)); !os.IsNotExist(err) {
		t.Fatal("published without validation", err)
	}
}

func TestSequentialPublicationOutlivesDownloadDeadline(t *testing.T) {
	s, cfg := setup(t, nil)
	s.cfg.DownloadTimeout = 3 * time.Second
	finished := make(chan struct{}, 1)
	s.scheduler.Close()
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1, Observe: func(e blocksched.Event) {
		if e.Kind == "finished" {
			finished <- struct{}{}
		}
	}})
	defer func() {
		for len(s.localChecks) > 0 {
			<-s.localChecks
		}
	}()
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			for range cap(s.localChecks) {
				s.localChecks <- struct{}{}
			}
			return &http.Response{StatusCode: 200, ContentLength: int64(len(payload)), Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(payload)), Request: r}, nil
		})}
	}
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, _ := s.parseResolved(r)
	deadline := time.Now().Add(time.Second)
	ctx := context.WithValue(context.Background(), playbackClockKey{}, &playbackClock{at: deadline.Add(-blocksched.DownloadGrace), limit: v.size, media: estimateMediaTiming(v.size, 0)})
	f, reader, err := s.obtain(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if got, err := io.ReadAll(reader); err != nil || string(got) != payload {
		t.Fatal("sequential bytes were not readable before publication", err)
	}
	receiveDemandEvent(t, finished) // Body and task have drained; merge still waits.
	time.Sleep(max(0, time.Until(deadline)))
	select {
	case <-f.done:
		t.Fatal("download deadline canceled independent publication", f.err)
	default:
	}
	for range cap(s.localChecks) {
		<-s.localChecks
	}
	receiveDemandEvent(t, f.done)
	if got, err := os.ReadFile(cfg.videoFile(v.key)); f.err != nil || err != nil || string(got) != payload {
		t.Fatal("sequential publication did not survive its former download deadline", f.err, err)
	}
}

func TestExecutionTimeoutExcludesInitialQueueWindow(t *testing.T) {
	s, _ := setup(t, nil)
	var active atomic.Int32
	s.cfg.BeginResourceLoad = func() func() {
		active.Add(1)
		return func() { active.Add(-1) }
	}
	s.cfg.DownloadTimeout = 150 * time.Millisecond
	queued := make(chan struct{}, 8)
	s.scheduler.Close()
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 1, PrepareAhead: 30 * time.Second, Observe: func(e blocksched.Event) {
		if e.Kind == "queued" {
			queued <- struct{}{}
		}
	}})
	var requests atomic.Int32
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			requests.Add(1)
			return &http.Response{StatusCode: 200, ContentLength: int64(len(payload)), Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(payload)), Request: r}, nil
		})}
	}
	if err := s.UpdatePrefetchQueue([]int64{1, 2}, false); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", videoURL(payload), nil)
	v, _ := s.parseResolved(r)
	v.songID = "2"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = s.queueDemandContext(ctx, "2")
	done := make(chan error, 1)
	go func() {
		f, _, err := s.obtainDemand(ctx, v, true, false)
		if err == nil {
			<-f.done
			err = f.err
		}
		done <- err
	}()
	receiveDemandEvent(t, queued)
	time.Sleep(2 * s.cfg.DownloadTimeout)
	select {
	case err := <-done:
		t.Fatal("window wait consumed execution timeout", err)
	default:
	}
	if requests.Load() != 0 {
		t.Fatal("future queue requested upstream before activation")
	}
	if active.Load() != 0 {
		t.Fatal("waiting for the download window suppressed automatic probes")
	}
	if err := s.UpdatePrefetchQueue([]int64{2}, false); err != nil {
		t.Fatal(err)
	}
	if err := receiveDemandEvent(t, done); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("promotion restarted or skipped execution", requests.Load())
	}
}

func TestExecutionTimeoutExcludesWaitBetweenPlaybackBlocks(t *testing.T) {
	s, _ := setup(t, nil)
	s.cfg.DownloadTimeout = 300 * time.Millisecond
	body := bytes.Repeat([]byte("b"), int(rangeBlockSize+123))
	var requests atomic.Int32
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			requests.Add(1)
			var start, end int64
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			return &http.Response{StatusCode: 206, ContentLength: end - start + 1, Request: r,
				Header: http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}},
				Body:   io.NopCloser(bytes.NewReader(body[start : end+1]))}, nil
		})}
	}
	r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
	v, _ := s.parseResolved(r)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, playbackClockKey{}, &playbackClock{at: time.Now(), limit: v.size, media: mediaTiming{v.size, 15 * time.Minute}})
	f, reader, err := s.obtain(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := io.ReadFull(reader, make([]byte, rangeBlockSize)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * s.cfg.DownloadTimeout)
	select {
	case <-f.done:
		t.Fatal("between-block wait timed out", f.err)
	default:
	}
	if requests.Load() != 1 {
		t.Fatal("tail started outside its window")
	}
	got, err := io.ReadAll(reader) // Actual Blocking admits the future tail immediately.
	if err != nil || !bytes.Equal(got, body[rangeBlockSize:]) {
		t.Fatal("tail did not resume", err)
	}
	receiveDemandEvent(t, f.done)
	if f.err != nil {
		t.Fatal(f.err)
	}
	got, err = os.ReadFile(s.cfg.videoFile(v.key))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("incomplete publication", err)
	}
}
