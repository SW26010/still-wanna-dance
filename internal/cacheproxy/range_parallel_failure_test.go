package cacheproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestParallelRangeEntityValidation(t *testing.T) {
	for _, validator := range []string{"missing", "weak", "strong-missing", "strong-weak", "strong-changed", "weak-corrupt"} {
		t.Run(validator, func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), int(5*rangeBlockSize+137))
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				var start, end int64
				fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
				strong := strings.HasPrefix(validator, "strong-")
				if strong {
					w.Header().Set("ETag", `"original"`)
					if start > 0 {
						if r.Header.Get("If-Match") != `"original"` {
							t.Error("strong condition lost")
						}
						switch validator {
						case "strong-missing":
							w.Header().Del("ETag")
						case "strong-weak":
							w.Header().Set("ETag", `W/"original"`)
						case "strong-changed":
							w.Header().Set("ETag", `"changed"`)
						}
					}
				} else {
					if validator != "missing" {
						w.Header().Set("ETag", fmt.Sprintf(`W/"block-%d"`, start))
					}
					if r.Header.Get("If-Match") != "" {
						t.Error("weak entity used as a strong condition")
					}
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
				w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
				w.WriteHeader(206)
				if validator == "weak-corrupt" && start > 0 {
					w.Write(bytes.Repeat([]byte("y"), int(end-start+1)))
				} else {
					w.Write(body[start : end+1])
				}
			})
			target := videoURL(string(body))
			_, err := s.Prefetch(context.Background(), target)
			wantFailure := strings.HasPrefix(validator, "strong-") || validator == "weak-corrupt"
			if (err != nil) != wantFailure {
				t.Fatalf("failure=%v, want %v", err, wantFailure)
			}
			r, _ := http.NewRequest("GET", target, nil)
			v, _ := s.parseResolved(r)
			got, statErr := os.ReadFile(s.cfg.videoFile(v.key))
			if wantFailure {
				if !os.IsNotExist(statErr) {
					t.Fatalf("published failed content: %v", statErr)
				}
			} else if statErr != nil || !bytes.Equal(got, body) {
				t.Fatal("published bytes differ", statErr)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(s.cfg.tempDir())
			if err != nil || len(entries) != 0 {
				t.Fatal("temporary files remain", entries, err)
			}
		})
	}
}

type rangeClosingPeer struct {
	ctx      context.Context
	readers  *sync.WaitGroup
	readOnce sync.Once
	closing  chan<- struct{}
	release  <-chan struct{}
}

func (b *rangeClosingPeer) Read([]byte) (int, error) {
	b.readOnce.Do(b.readers.Done)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *rangeClosingPeer) Close() error {
	b.closing <- struct{}{}
	<-b.release
	return nil
}

func TestParallelRangeFailureJoinsClosingPeersBeforeCleanup(t *testing.T) {
	s, _ := setup(t, nil)
	fixedBlockScheduler(s)
	body := bytes.Repeat([]byte("x"), int(5*rangeBlockSize))
	var readers sync.WaitGroup
	readers.Add(2)
	closing, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.cfg.ResourceTransports = func(string) []http.RoundTripper {
		return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
			var start, end int64
			fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
			if start >= 2*rangeBlockSize {
				readers.Wait()
				return nil, errors.New("controlled failed block")
			}
			return &http.Response{StatusCode: 206,
				Header:        http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", start, end, len(body))}},
				ContentLength: end - start + 1, Request: r,
				Body: &rangeClosingPeer{ctx: r.Context(), readers: &readers, closing: closing, release: release},
			}, nil
		})}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequest("GET", videoURL(string(body)), nil)
	v, _ := s.parseResolved(r)
	f, _, err := s.obtainMode(ctx, v, true)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-closing:
		case <-ctx.Done():
			t.Fatal("peer body was not closed")
		}
	}
	select {
	case <-f.done:
		t.Fatal("flight ended before peers closed")
	default:
	}
	s.requests.mu.Lock()
	active := s.requests.active
	s.requests.mu.Unlock()
	// Additional failing attempts may still be unwinding; both gated bodies
	// must remain counted, regardless of that independent completion ordering.
	if active < 2 || active > videoRequestSoftLimit {
		t.Fatalf("closing requests released early: %d", active)
	}
	unblock()
	select {
	case <-f.done:
	case <-ctx.Done():
		t.Fatal("flight did not finish cleanup")
	}
	if f.err == nil || !strings.Contains(f.err.Error(), "controlled failed block") {
		t.Fatal("root failure lost", f.err)
	}
	if _, err := os.Stat(s.cfg.videoFile(v.key)); !os.IsNotExist(err) {
		t.Fatal("failed file was published")
	}
	entries, err := os.ReadDir(s.cfg.tempDir())
	if err != nil || len(entries) != 0 {
		t.Fatal("partial files survived cleanup", entries, err)
	}
	s.requests.mu.Lock()
	defer s.requests.mu.Unlock()
	if s.requests.active != 0 {
		t.Fatal("request quota leaked")
	}
}

// Ensure the test peer retains the same ownership contract as an HTTP body.
var _ io.ReadCloser = (*rangeClosingPeer)(nil)

func TestParallelRangeCleanupPreservesBlockWriteFailure(t *testing.T) {
	s, _ := setup(t, nil)
	s.scheduler.Close()
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 2})
	f, v := completedRangeFlight(t, s, strings.Repeat("x", int(2*rangeBlockSize)))
	f.spool = f.ranges.makeSpool(nil)
	w := newDownloadSession(s, context.Background(), video{}, f)
	started := make(chan struct{}, 2)
	upstream := fmt.Errorf("%w: controlled failed peer", errUpstreamDownload)
	// Invalid temporary destination forces an actual filesystem write failure.
	f.ranges.storage.dir = localBlockPath(t, f.ranges.storage, 1)
	f.ranges.storage.removeFiles(f.ranges.storage.retire(func(block int64) bool { return block == 0 }))
	for block := int64(0); block < 2; block++ {
		task, _, err := s.scheduler.Schedule(fmt.Sprint(block), rangeBlockSize, func(ctx context.Context, p *blocksched.Progress) blocksched.Result {
			started <- struct{}{}
			<-ctx.Done()
			if block == 1 {
				return blocksched.Result{Err: upstream}
			}
			return blocksched.Result{Err: f.ranges.storage.store(0, bytes.Repeat([]byte("x"), int(rangeBlockSize)))}
		}, blocksched.Need{Owner: "background", LeaseUntil: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		w.jobs[block] = &scheduledBlock{task: task}

	}
	defer w.close(nil)
	for range 2 {
		receiveDemandEvent(t, started)
	}
	err := s.storageError(w.close(upstream))
	if !errors.Is(w.jobs[1].task.Err(), errUpstreamDownload) || !errors.Is(err, ErrLocalStorage) || errors.Is(err, errUpstreamDownload) || s.StorageError() == nil {
		t.Fatal("upstream cleanup masked block write failure", err, s.StorageError())
	}
	if _, reader, err := s.obtain(context.Background(), v); reader != nil || !errors.Is(err, ErrLocalStorage) {
		t.Fatal("write failure did not stop subsequent downloads", err)
	}
}
