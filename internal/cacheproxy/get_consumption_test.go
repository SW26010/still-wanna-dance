package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestGETConsumptionWithdrawsOnlyDeliveredBlocks(t *testing.T) {
	w, s := testSession(t)
	clock := &playbackClock{at: time.Now(), offset: rangeBlockSize - 2, limit: 2*rangeBlockSize + 3, media: estimateMediaTiming(w.v.size, 0)}
	ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
	d, closeGET := addTestDemand(t, s, ctx, w.v, true)
	defer closeGET()
	other, closeOther := addTestDemand(t, s, ctx, w.v, true)
	defer closeOther()
	g := &getConsumption{s: s, demand: d, size: w.v.size}
	g.start(http.StatusPartialContent, http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", clock.offset, clock.limit-1, w.v.size)}})
	original := clock.ready(2 * rangeBlockSize)
	for _, step := range []struct{ bytes, first int64 }{{1, 0}, {1, 1}, {rangeBlockSize, 2}, {2, 2}, {1, 3}} {
		g.accept(step.bytes)
		plan, _, _ := w.plan(time.Now(), true)
		for block := int64(0); block < 3; block++ {
			_, own := plan[block][blockSource{"get", d}]
			_, shared := plan[block][blockSource{"get", other}]
			if own != (block >= step.first) || !shared {
				t.Fatalf("block %d after delivery: own=%v shared=%v first=%d", block, own, shared, step.first)
			}
		}
		if !clock.ready(2*rangeBlockSize).Equal(original) || !s.demands[d] {
			t.Fatal("consumption changed the clock or ended source identity")
		}
	}
}

type acceptedWriter struct {
	*httptest.ResponseRecorder
	acceptBytes int
	err         error
}

func (w *acceptedWriter) Write(p []byte) (int, error) {
	n := min(len(p), w.acceptBytes)
	w.ResponseRecorder.Write(p[:n])
	return n, w.err
}

func TestGETConsumptionCountsAcceptedBytesOnShortAndFailedWrites(t *testing.T) {
	for _, failure := range []error{nil, io.ErrClosedPipe} {
		w, s := testSession(t)
		clock := &playbackClock{at: time.Now(), offset: rangeBlockSize - 2, limit: rangeBlockSize + 2, media: estimateMediaTiming(w.v.size, 0)}
		d, release := addTestDemand(t, s, context.WithValue(context.Background(), playbackClockKey{}, clock), w.v, true)
		defer release()
		out := &acceptedWriter{httptest.NewRecorder(), 2, failure}
		out.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", clock.offset, clock.limit-1, w.v.size))
		response := &responseWriter{ResponseWriter: out, consumption: &getConsumption{s: s, demand: d, size: w.v.size}}
		response.WriteHeader(http.StatusPartialContent)
		n, err := response.Write([]byte("abcd"))
		if n != 2 || err == nil || d.firstBlock != 1 || response.outcome(context.Background(), nil) != "failed" {
			t.Fatalf("short/failed write accepted as complete: n=%d err=%v first=%d", n, err, d.firstBlock)
		}
		if failure == nil && !errors.Is(err, io.ErrShortWrite) {
			t.Fatal("missing short write error", err)
		}
	}
}

type inspectGETWriter struct {
	*httptest.ResponseRecorder
	inspect func()
}

func (w *inspectGETWriter) Write(p []byte) (int, error) {
	w.inspect()
	return w.ResponseRecorder.Write(p)
}

func TestHTTPGETSharesOneDemandOnColdAndLocalReads(t *testing.T) {
	body := bytes.Repeat([]byte("x"), int(rangeBlockSize+23))
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[start : end+1])
	})
	for range 2 {
		calls := 0
		out := &inspectGETWriter{ResponseRecorder: httptest.NewRecorder(), inspect: func() {
			calls++
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.demands) != 1 {
				t.Errorf("one HTTP request has %d demand sources", len(s.demands))
			}
		}}
		s.ServeHTTP(out, httptest.NewRequest("GET", videoURL(string(body)), nil))
		if calls == 0 || out.Code != http.StatusOK || !bytes.Equal(out.Body.Bytes(), body) {
			t.Fatal("response failed", out.Code, out.Body.Len())
		}
		s.mu.Lock()
		if len(s.demands) != 0 {
			t.Error("HTTP exit retained its source")
		}
		s.mu.Unlock()
		s.wg.Wait() // Publication is independent of response completion.
	}
}

func TestHTTPSourceExitOnlyHandsOffClientCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		w, s := testSession(t)
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), httpGETKey{}, true))
		d, release := addTestDemand(t, s, ctx, w.v, true)
		if canceled {
			cancel()
		}
		release()
		want := withdrawalNeedEnded
		if canceled {
			want = withdrawalSourceGone
		}
		if d.exit.at.Load().reason != want {
			t.Fatal("server completion/failure was treated as a client close")
		}
		cancel()
	}
}

func TestFullGETCompletesWhilePublicationWaitsForValidation(t *testing.T) {
	body := bytes.Repeat([]byte("v"), int(2*rangeBlockSize))
	last := make(chan struct{})
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if start != 0 {
			select {
			case <-last:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[start : end+1])
	})
	var first sync.Once
	out := &inspectGETWriter{ResponseRecorder: httptest.NewRecorder(), inspect: func() {
		first.Do(func() {
			for range cap(s.localChecks) {
				s.localChecks <- struct{}{}
			}
			close(last)
		})
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ServeHTTP(out, httptest.NewRequest("GET", videoURL(string(body)), nil).WithContext(ctx))
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Error("GET waited for whole-video validation")
		<-done
	}
	if out.Code != http.StatusOK || !bytes.Equal(out.Body.Bytes(), body) {
		t.Error("response did not deliver all requested bytes")
	}
	v := parsedVideo(t, s, string(body))
	if _, err := os.Stat(cfg.videoFile(v.key)); !os.IsNotExist(err) {
		t.Error("publication bypassed validation", err)
	}
	for range len(s.localChecks) {
		<-s.localChecks
	}
	s.wg.Wait()
	got, err := os.ReadFile(cfg.videoFile(v.key))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatal("request completion canceled independent publication", err)
	}
}
