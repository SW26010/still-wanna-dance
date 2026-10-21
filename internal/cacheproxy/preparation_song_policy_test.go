package cacheproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestBlockingDoesNotCancelAdmittedPreparation(t *testing.T) {
	for _, kind := range []string{"manual", "queue"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, s := testSession(t)
				s.ctx = context.Background()
				db, err := openStorageDatabase(filepath.Join(t.TempDir(), storageDatabaseName), true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				s.usage = &usageStore{db: db}
				ctx := t.Context()
				if kind == "queue" {
					if err := s.UpdatePrefetchQueue([]int64{1}, false); err != nil {
						t.Fatal(err)
					}
					ctx = s.queueDemandContext(ctx, "1")
				}
				v := video{key: "prepared", songID: "1", size: 1}
				d, release, err := s.addPreparation(ctx, v)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				if err := s.SetSongPolicy(ctx, 1, SongBlocked); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				s.mu.Lock()
				active := s.demands[d] && len(s.useBlocks) == 1
				s.mu.Unlock()
				if !active || context.Cause(d.ctx) != nil {
					t.Fatal("blocking revoked an admitted source", active, context.Cause(d.ctx))
				}
				release()
				if err := s.UpdatePrefetchQueue(nil, true); err != nil {
					t.Fatal(err)
				}
				if kind == "queue" {
					time.Sleep(handoffGrace)
					synctest.Wait()
				}
				s.mu.Lock()
				released := len(s.demands) == 0 && len(s.useBlocks) == 0
				s.mu.Unlock()
				if !released {
					t.Fatal("admitted source did not end through normal release")
				}
			})
		})
	}
}

func TestPreparationRechecksBlockedSongAfterCapacityWait(t *testing.T) {
	for _, kind := range []string{"manual", "queue", "queue-local"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				cfg := fixtureConfig()
				cfg.StorageDir = t.TempDir()
				cfg.CacheUnlimited, cfg.MaxCacheBytes = false, 1024
				cfg.ResolvePlayback = func(context.Context, string, string) (string, error) { return videoURL(payload), nil }
				cfg.ResourceTransports = func(string) []http.RoundTripper {
					return []http.RoundTripper{resourceTransportFunc(func(r *http.Request) (*http.Response, error) {
						calls.Add(1)
						return &http.Response{StatusCode: http.StatusOK, Request: r, Header: make(http.Header),
							ContentLength: int64(len(payload)), Body: io.NopCloser(strings.NewReader(payload))}, nil
					})}
				}
				s, err := New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := s.Close(); err != nil {
						t.Error(err)
					}
				})
				seedPrioritySong(t, s, "1", videoKey(payload))
				if kind == "queue-local" {
					writeTestFile(t, cfg.videoFile(videoKey(payload)), payload)
				}
				if kind != "manual" {
					if err := s.UpdatePrefetchQueue([]int64{1}, false); err != nil {
						t.Fatal(err)
					}
				}
				_, release := addTestDemand(t, s, t.Context(), video{key: "occupied", size: backgroundUseBlocks * rangeBlockSize}, false)
				defer release()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if kind == "manual" {
					ctx = s.WithBatchBudget(ctx)
				}
				ctx, admitted := WithPreparationAdmission(ctx)
				result := make(chan error, 1)
				go func() {
					var err error
					if kind == "manual" {
						_, err = s.PrefetchSong(ctx, "1", videoURL(payload))
					} else {
						_, err = s.PrefetchQueuePlayback(ctx, 1)
					}
					result <- err
				}()
				// All metadata/local verification has finished; the candidate is
				// blocked on capacity, with no registered usage or download.
				synctest.Wait()
				select {
				case err := <-result:
					t.Fatal("preparation did not wait for capacity", err)
				default:
				}
				s.mu.Lock()
				pending := len(s.demands) == 1 && len(s.useBlocks) == backgroundUseBlocks && len(s.flights) == 0
				s.mu.Unlock()
				if !pending || calls.Load() != 0 {
					t.Fatal("waiting candidate created work", pending, calls.Load())
				}
				if err := s.SetSongPolicy(ctx, 1, SongBlocked); err != nil {
					t.Fatal(err)
				}
				release()
				if err := <-result; !errors.Is(err, ErrSongBlocked) {
					t.Errorf("waiting preparation ignored new block: %v", err)
				}
				select {
				case <-admitted:
					t.Error("blocked candidate signalled admission")
				default:
				}
				s.mu.Lock()
				empty := len(s.demands) == 0 && len(s.useBlocks) == 0 && len(s.flights) == 0
				s.mu.Unlock()
				if !empty || calls.Load() != 0 {
					t.Error("blocked candidate created work after capacity release", empty, calls.Load())
				}
				if b, _ := ctx.Value(batchBudgetKey{}).(*batchBudget); b != nil {
					b.mu.Lock()
					reserved := len(b.entries)
					b.mu.Unlock()
					if reserved != 0 {
						t.Fatal("blocked candidate retained batch budget", reserved)
					}
				}
			})
		})
	}
}
