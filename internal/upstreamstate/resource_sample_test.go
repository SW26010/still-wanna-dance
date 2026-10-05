package upstreamstate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type pacedResource struct {
	remaining  int64
	step       time.Duration
	stallAfter int
	reads      int
	closed     chan struct{}
	once       sync.Once
}

func (b *pacedResource) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	if b.stallAfter != 0 && b.reads >= b.stallAfter {
		<-b.closed
		return 0, io.ErrClosedPipe
	}
	select {
	case <-time.After(b.step):
	case <-b.closed:
		return 0, io.ErrClosedPipe
	}
	n := min(int64(len(p)), b.remaining)
	b.remaining -= n
	b.reads++
	return int(n), nil
}
func (b *pacedResource) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestResourceMeasurementWindow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int64
		step      time.Duration
		stall     int
		cancel    bool
		wantTime  time.Duration
		wantBytes int64
		wantErr   error
	}{
		{"sixteen MiB before time limit", 100 << 20, time.Second / 128, 0, false, 2 * time.Second, 16 << 20, nil},
		{"fast connection stops at sixteen MiB", 200 << 20, time.Millisecond, 0, false, 256 * time.Millisecond, 16 << 20, nil},
		{"sixteen MiB near time limit", 100 << 20, 10 * time.Millisecond, 0, false, 2560 * time.Millisecond, 16 << 20, nil},
		{"slow connection capped at three seconds", 100 << 20, 350 * time.Millisecond, 0, false, 3 * time.Second, 8 * 65536, nil},
		{"no data is timeout", 100 << 20, time.Second, -1, false, 3 * time.Second, 0, context.DeadlineExceeded},
		{"whole song before byte and time limits", 3 * 65536, 250 * time.Millisecond, 0, false, 750 * time.Millisecond, 3 * 65536, nil},
		{"stalled transfer capped at three seconds", 100 << 20, 250 * time.Millisecond, 1, false, 3 * time.Second, 65536, nil},
		{"owner cancellation", 100 << 20, 250 * time.Millisecond, 1, true, time.Second, 65536, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := &pacedResource{remaining: tc.size, step: tc.step, stallAfter: tc.stall, closed: make(chan struct{})}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if tc.cancel {
					time.AfterFunc(time.Second, cancel)
				}
				p := DefaultPolicy()
				n, d, err := readResourceSample(ctx, body, tc.size, p.ResourceMaxBytes, p.ResourceTimeout)
				if n != tc.wantBytes || d != tc.wantTime || !errors.Is(err, tc.wantErr) {
					t.Fatalf("bytes=%d duration=%v err=%v", n, d, err)
				}
				select {
				case <-body.closed:
				default:
					t.Fatal("body survived measurement")
				}
			})
		})
	}
}

func TestResourceThroughputExcludesConnectionTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
			time.Sleep(3 * time.Second)
			if r.Header.Get("Range") != "bytes=0-16777215" {
				t.Fatal(r.Header)
			}
			return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": []string{"bytes 0-16777215/104857600"}}, Body: &pacedResource{remaining: 100 << 20, step: time.Second / 128, closed: make(chan struct{})}}, nil
		}))
		s := videoSample{url: videoFixture, host: "play.udon.dance", size: 100 << 20}
		o := probeResource(context.Background(), client, DefaultPolicy(), 42, s)
		if o.state != "available" || o.transferDuration != 2*time.Second || o.duration != 5*time.Second || o.bytes != 16<<20 {
			t.Fatalf("%+v", o)
		}
		m := testMonitor(t, transportFunc(fixture))
		m.mu.Lock()
		recordFixture(m, o)
		m.mu.Unlock()
		r := resultFor(t, m, Resource, "play.udon.dance")
		if r.EstimatedSpeedBPS == nil || *r.EstimatedSpeedBPS != 8<<20 || r.TransferDurationMS == nil || *r.TransferDurationMS != 2000 {
			t.Fatalf("%+v", r)
		}
	})
}
