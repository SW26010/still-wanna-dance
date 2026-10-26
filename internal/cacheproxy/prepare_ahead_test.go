package cacheproxy

import (
	"context"
	"testing"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func TestConfiguredPrepareAheadControlsEngineWindow(t *testing.T) {
	for _, tc := range []struct {
		name string
		lead time.Duration
		wait bool
	}{
		{"default", DefaultConfig().PrepareAhead, false},
		{"zero_default", 0, false},
		{"custom_short_window", 100 * time.Second, true},
		{"maximum_window", 16000 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtureConfig()
			prepareFixtureResources(t, &cfg, t.TempDir(), true)
			cfg.PrepareAhead = tc.lead
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			started := make(chan struct{}, 1)
			now := time.Now()
			n := blocksched.Need{Owner: "window", ReadyBy: now.Add(200 * time.Second), LeaseUntil: now.Add(time.Hour)}
			_, demand, err := s.scheduler.Schedule("window", 1, func(context.Context, *blocksched.Progress) blocksched.Result {
				started <- struct{}{}
				return blocksched.Result{}
			}, n)
			if err != nil {
				t.Fatal(err)
			}

			if tc.wait {
				select {
				case <-started:
					t.Fatal("custom lead was not forwarded to scheduler")
				case <-time.After(30 * time.Millisecond):
				}
				n.ReadyBy = now
				if !demand.Update(n) {
					t.Fatal("window wait lost demand")
				}
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("eligible task did not start")
			}
		})
	}
}

func TestPrepareAheadRejectsOutOfRange(t *testing.T) {
	for _, lead := range []time.Duration{-time.Second, 100*time.Second - time.Nanosecond, 16000*time.Second + time.Nanosecond, 24 * time.Hour} {
		cfg := fixtureConfig()
		cfg.PrepareAhead = lead
		if err := cfg.validate(); err == nil {
			t.Errorf("accepted out-of-range lead %v", lead)
		}
	}
}
