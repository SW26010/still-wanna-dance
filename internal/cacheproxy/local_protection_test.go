package cacheproxy

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func protectTestVideo(s *Server, key string) {
	s.retentionMu.Lock()
	if s.protection == nil {
		s.protection = make(map[string]map[int64]blockProtection)
	}
	s.protection[key] = map[int64]blockProtection{0: {start: time.Now()}}
	s.retentionMu.Unlock()
}

func clearTestProtection(s *Server) {
	s.retentionMu.Lock()
	clear(s.protection)
	s.retentionMu.Unlock()
}

func protectionFor(s *Server, key string, block int64) blockProtection {
	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()
	return s.protection[key][block]
}

func TestLocalProtectionWindowAndFixedTail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		s.cfg.PrepareAhead = 2 * time.Minute
		s.cfg.LocalProtectAhead = 3 * time.Minute
		start := time.Now()
		clock := &playbackClock{at: start.Add(4 * time.Minute), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
		ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
		ctx = context.WithValue(ctx, httpGETKey{}, true)
		_, release := addTestDemand(t, s, ctx, w.v, true)
		p := protectionFor(s, w.v.key, 0)
		if p.active(start) || !p.active(start.Add(time.Minute)) {
			t.Fatal("independent protection window", p)
		}
		time.Sleep(5 * time.Minute)
		if !protectionFor(s, w.v.key, 0).active(time.Now()) {
			t.Fatal("need-by ended live protection")
		}
		ended := time.Now()
		release()
		if got := protectionFor(s, w.v.key, 0); !got.start.IsZero() || got.until != ended.Add(s.cfg.LocalProtectionGrace) {
			t.Fatal("wrong final-source grace", got)
		}
		time.Sleep(30 * time.Second)
		release()
		s.mu.Lock()
		s.notifyCapacityLocked()
		s.mu.Unlock()
		if protectionFor(s, w.v.key, 0).until != ended.Add(s.cfg.LocalProtectionGrace) {
			t.Fatal("duplicate event refreshed grace")
		}
		time.Sleep(s.cfg.LocalProtectionGrace - 30*time.Second)
		if protectionFor(s, w.v.key, 0).active(time.Now()) {
			t.Fatal("grace did not expire")
		}
	})
}

func TestLocalProtectionConsumptionAndSharing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		ctx := context.WithValue(context.Background(), httpGETKey{}, true)
		d, release := addTestDemand(t, s, ctx, w.v, true)
		defer release()
		_, other := addTestDemand(t, s, ctx, w.v, true)
		g := &getConsumption{s: s, demand: d, size: w.v.size}
		g.start(http.StatusOK, nil)
		g.accept(rangeBlockSize)
		if !protectionFor(s, w.v.key, 0).until.IsZero() {
			t.Fatal("one consumer ended shared protection")
		}
		time.Sleep(10 * time.Second)
		ended := time.Now()
		other()
		if protectionFor(s, w.v.key, 0).until != ended.Add(s.cfg.LocalProtectionGrace) {
			t.Fatal("consumed block did not enter grace")
		}
		if !protectionFor(s, w.v.key, 1).until.IsZero() {
			t.Fatal("remaining GET block lost protection")
		}
	})
}

func TestLocalProtectionFarFutureAndUntimedDoNotExtendTail(t *testing.T) {
	for _, kind := range []string{"future", "manual"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, s := testSession(t)
				ctx := context.WithValue(context.Background(), httpGETKey{}, true)
				_, release := addTestDemand(t, s, ctx, w.v, true)
				release()
				until := protectionFor(s, w.v.key, 0).until
				time.Sleep(10 * time.Second)
				clock := &playbackClock{at: time.Now().Add(time.Hour), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
				ctx = context.WithValue(ctx, playbackClockKey{}, clock)
				_, release = addTestDemand(t, s, ctx, w.v, kind == "future")
				if protectionFor(s, w.v.key, 0).until != until {
					t.Fatal("new source renewed old tail")
				}
				time.Sleep(s.cfg.LocalProtectionGrace)
				if protectionFor(s, w.v.key, 0).active(time.Now()) {
					t.Fatal("future/untimed source provided immediate protection")
				}
				release()
				if protectionFor(s, w.v.key, 0).until.After(until) {
					t.Fatal("future/untimed exit added protection")
				}
			})
		})
	}
}

func TestLocalProtectionNoExtraGraceWhenOnlyFutureSourceRemains(t *testing.T) {
	w, s := testSession(t)
	ctx := context.WithValue(context.Background(), httpGETKey{}, true)
	_, release := addTestDemand(t, s, ctx, w.v, true)
	clock := &playbackClock{at: time.Now().Add(time.Hour), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
	_, future := addTestDemand(t, s, context.WithValue(ctx, playbackClockKey{}, clock), w.v, true)
	defer future()
	release()
	p := protectionFor(s, w.v.key, 0)
	if p.active(time.Now()) || !p.until.IsZero() {
		t.Fatal("extra grace for in-window contributor loss", p)
	}
}

func TestLocalProtectionHandoffUsesRealExpiryDespiteDelayedCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		d, release := addTestDemand(t, s, ctx, w.v, true)
		defer release()
		ended := time.Now()
		d.exit.recordReason(withdrawalSourceGone)
		time.Sleep(10 * time.Second) // Deliver the frozen exit after its handoff expired.
		s.mu.Lock()
		cancel()
		s.removeDemandLocked(d)
		s.mu.Unlock()
		synctest.Wait()
		p := protectionFor(s, w.v.key, 0)
		if !p.start.IsZero() || p.until != ended.Add(getHandoff+s.cfg.LocalProtectionGrace) {
			t.Fatal("delayed callback extended handoff protection", p)
		}
	})
}

func TestLocalProtectionNewInWindowSourceSurvivesOldExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		ctx := context.WithValue(context.Background(), httpGETKey{}, true)
		_, release := addTestDemand(t, s, ctx, w.v, true)
		release()
		time.Sleep(30 * time.Second)
		_, release = addTestDemand(t, s, ctx, w.v, true)
		defer release()
		time.Sleep(s.cfg.LocalProtectionGrace)
		s.retentionMu.Lock()
		s.nextProtectionExpiryLocked(time.Now())
		s.retentionMu.Unlock()
		if !protectionFor(s, w.v.key, 0).active(time.Now()) {
			t.Fatal("old tail expiry removed new protection")
		}
	})
}

func TestLocalProtectionQueueHandoffThenConfiguredTail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		s.UpdatePrefetchQueue([]int64{1}, false)
		v := w.v
		v.songID = "1"
		_, release := addTestDemand(t, s, s.queueDemandContext(context.Background(), "1"), v, false)
		release() // Completion of preparation does not end queue usage.
		ended := time.Now()
		s.UpdatePrefetchQueue(nil, false)
		time.Sleep(queueHandoff - time.Second)
		if p := protectionFor(s, v.key, 0); !p.active(time.Now()) || !p.until.IsZero() {
			t.Fatal("queue handoff lost live protection", p)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if p := protectionFor(s, v.key, 0); !p.start.IsZero() || p.until != ended.Add(queueHandoff+s.cfg.LocalProtectionGrace) {
			t.Fatal("queue protection did not start at handoff end", p)
		}
		time.Sleep(s.cfg.LocalProtectionGrace)
		if protectionFor(s, v.key, 0).active(time.Now()) {
			t.Fatal("queue-only grace survived unified expiry")
		}
	})
}

func TestLocalProtectionConfiguration(t *testing.T) {
	defaults := DefaultConfig()
	if (&Server{cfg: defaults}).protectionAhead() < defaults.PrepareAhead {
		t.Fatal("default protection window violates the design convention")
	}
	// Shorter windows are accepted and used as supplied, even though normal
	// configurations and acceptance fixtures must follow the design convention.
	for _, tc := range []struct {
		name             string
		prepare, protect time.Duration
		want             time.Duration // Zero marks an invalid configuration.
	}{
		{name: "follow default", want: 500 * time.Second},
		{name: "follow explicit", prepare: 10 * time.Minute, want: 10 * time.Minute},
		{name: "equal default", protect: 500 * time.Second, want: 500 * time.Second},
		{name: "below default", protect: 499 * time.Second, want: 499 * time.Second},
		{name: "equal explicit", prepare: 2 * time.Minute, protect: 2 * time.Minute, want: 2 * time.Minute},
		{name: "longer", prepare: 2 * time.Minute, protect: 3 * time.Minute, want: 3 * time.Minute},
		{name: "shorter", prepare: 3 * time.Minute, protect: 2 * time.Minute, want: 2 * time.Minute},
		{name: "negative", protect: -time.Second},
		{name: "upper bound", prepare: 16000 * time.Second, protect: 24 * time.Hour, want: 24 * time.Hour},
		{name: "above upper bound", protect: 24*time.Hour + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixtureConfig()
			cfg.PrepareAhead, cfg.LocalProtectAhead = tc.prepare, tc.protect
			if err := cfg.validate(); (err == nil) != (tc.want != 0) {
				t.Fatal("unexpected configuration result", err)
			}
			if tc.want != 0 {
				s := &Server{cfg: cfg}
				if got := s.protectionAhead(); got != tc.want {
					t.Fatalf("protection lead = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestLocalProtectionGraceConfiguration(t *testing.T) {
	if got := DefaultConfig().LocalProtectionGrace; got != 300*time.Second {
		t.Fatal("incorrect default local protection grace", got)
	}
	for _, seconds := range []int{-1, 0, 1, 300, 10000, 10001} {
		cfg := DefaultConfig()
		cfg.LocalProtectionGrace = time.Duration(seconds) * time.Second
		if err := cfg.validate(); (err == nil) != (seconds >= 0 && seconds <= 10000) {
			t.Fatalf("grace=%d: %v", seconds, err)
		}
	}
}

func TestLocalProtectionConfiguredGraceReclaimsFromRealEnd(t *testing.T) {
	for _, grace := range []time.Duration{0, time.Second, 300 * time.Second, 10000 * time.Second} {
		t.Run(grace.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, s := testSession(t)
				s.cfg.LocalProtectionGrace = grace
				w.v.size = rangeBlockSize
				local := reclamationResource(t, s, w.v)
				// Early delivery/cancellation must not retain through need-by.
				clock := &playbackClock{at: time.Now().Add(240 * time.Second), limit: w.v.size}
				ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
				ctx = context.WithValue(ctx, httpGETKey{}, true)
				_, release := addTestDemand(t, s, ctx, w.v, true)
				s.reclaimLocalBlocks()
				if !local.has(0) || !protectionFor(s, w.v.key, 0).active(time.Now()) {
					t.Fatal("configured grace changed live protection")
				}
				ended := time.Now()
				release()
				if got := protectionFor(s, w.v.key, 0); got.until != ended.Add(grace) || !got.start.IsZero() {
					t.Fatal("tail did not use actual end plus configured grace", got)
				}
				if grace > 0 {
					time.Sleep(grace - time.Nanosecond)
					s.reclaimLocalBlocks()
					if !local.has(0) {
						t.Fatal("block reclaimed before configured expiry")
					}
					release()
					time.Sleep(time.Nanosecond)
				}
				s.reclaimLocalBlocks()
				if local.has(0) || protectionFor(s, w.v.key, 0).active(time.Now()) {
					t.Fatal("block protected after expiry or duplicate release renewed tail")
				}
			})
		})
	}
}
