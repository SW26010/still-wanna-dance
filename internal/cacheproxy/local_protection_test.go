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
		start := time.Now()
		clock := &playbackClock{at: start.Add(4 * time.Minute), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
		ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
		ctx = context.WithValue(ctx, httpGETKey{}, true)
		_, release := addTestDemand(t, s, ctx, w.v, true)
		p := protectionFor(s, w.v.key, 0)
		if p.active(start.Add(2*time.Minute-time.Nanosecond)) || !p.active(start.Add(2*time.Minute)) {
			t.Fatal("protection must start at need-by minus PrepareAhead", p)
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
		s.notifyUseChangedLocked(w.v.key)
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

func TestLocalProtectionConsumptionLeavesOnlyIndependentNeeds(t *testing.T) {
	for _, kind := range []string{"future", "manual"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, s := testSession(t)
				ctx := context.WithValue(context.Background(), httpGETKey{}, true)
				d, release := addTestDemand(t, s, ctx, w.v, true)
				defer release()
				clock := &playbackClock{at: time.Now().Add(time.Hour), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
				_, independent := addTestDemand(t, s, context.WithValue(ctx, playbackClockKey{}, clock), w.v, kind == "future")
				defer independent()
				remaining := protectionFor(s, w.v.key, 2)
				g := &getConsumption{s: s, demand: d, size: w.v.size}
				g.start(http.StatusOK, nil)
				g.accept(2 * rangeBlockSize)
				for b := int64(0); b < 2; b++ {
					p := protectionFor(s, w.v.key, b)
					if p.active(time.Now()) || !p.until.IsZero() {
						t.Fatal("consumption retained the departing contributor's protection or added grace", b, p)
					}
					var want time.Time
					if kind == "future" {
						want = clock.ready(b * rangeBlockSize).Add(-s.cfg.prepareAhead())
					}
					if p.start != want {
						t.Fatal("consumption lost the independent protection clock", b, p.start, want)
					}
				}
				if got := protectionFor(s, w.v.key, 2); got != remaining {
					t.Fatal("consumption changed protection of the undelivered suffix", got, remaining)
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
		if !p.start.IsZero() || p.until != ended.Add(handoffGrace+s.cfg.LocalProtectionGrace) {
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
		time.Sleep(handoffGrace - time.Second)
		if p := protectionFor(s, v.key, 0); !p.active(time.Now()) || !p.until.IsZero() {
			t.Fatal("queue handoff lost live protection", p)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if p := protectionFor(s, v.key, 0); !p.start.IsZero() || p.until != ended.Add(handoffGrace+s.cfg.LocalProtectionGrace) {
			t.Fatal("queue protection did not start at handoff end", p)
		}
		time.Sleep(s.cfg.LocalProtectionGrace)
		if protectionFor(s, v.key, 0).active(time.Now()) {
			t.Fatal("queue-only grace survived unified expiry")
		}
	})
}

func TestLocalProtectionGraceConfiguration(t *testing.T) {
	if got := DefaultConfig().LocalProtectionGrace; got != 300*time.Second {
		t.Fatal("incorrect default local protection grace", got)
	}
	for _, seconds := range []int{-1, 0, 1, 300, 10000, 10001} {
		cfg := DefaultConfig()
		cfg.VideoDir = t.TempDir()
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
