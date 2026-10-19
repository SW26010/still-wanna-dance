package cacheproxy

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestDownloadSettingsValidation(t *testing.T) {
	for _, tc := range []struct {
		limit    int
		interval time.Duration
		valid    bool
	}{
		{1, 0, true}, {64, 2 * time.Second, true}, {16, 250 * time.Millisecond, true},
		{0, 0, false}, {65, 0, false}, {1, -time.Millisecond, false}, {1, 2001 * time.Millisecond, false},
	} {
		cfg := DefaultConfig()
		cfg.DownloadSoftLimit, cfg.DownloadGrowInterval = tc.limit, tc.interval
		if err := cfg.validate(); (err == nil) != tc.valid {
			t.Fatalf("limit=%d interval=%s: %v", tc.limit, tc.interval, err)
		}
	}
}

func TestConfiguredDownloadSchedulerAdmission(t *testing.T) {
	for _, tc := range []struct {
		limit    int
		interval time.Duration
	}{
		{16, 250 * time.Millisecond}, {1, 0}, {64, 0}, {64, time.Millisecond}, {64, 2 * time.Second},
	} {
		t.Run(fmt.Sprintf("%d/%s", tc.limit, tc.interval), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := DefaultConfig()
				cfg.DownloadSoftLimit, cfg.DownloadGrowInterval = tc.limit, tc.interval
				s := newDownloadScheduler(cfg)
				defer s.Close()
				started := make(chan time.Time, tc.limit+1)
				release := make(chan struct{})
				now := time.Now()
				for i := range tc.limit + 1 {
					_, _, err := s.Schedule(fmt.Sprint(i), 1, func(ctx context.Context, _ *blocksched.Progress) blocksched.Result {
						started <- time.Now()
						select {
						case <-release:
						case <-ctx.Done():
						}
						return blocksched.Result{}
					}, blocksched.Need{Owner: "background", LeaseUntil: now.Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
				}
				for i := range tc.limit {
					at := <-started
					if at.Sub(now) != time.Duration(i)*tc.interval {
						t.Fatalf("start %d at %s, interval=%s", i, at.Sub(now), tc.interval)
					}
				}
				synctest.Wait()
				if len(started) != 0 {
					t.Fatal("ordinary execution exceeded configured limit")
				}
				reuseAt := time.Now()
				release <- struct{}{}
				if at := <-started; !at.Equal(reuseAt) {
					t.Fatal("slot reuse waited for growth interval")
				}
			})
		})
	}
}
