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
		s.cfg.LocalProtectAhead = time.Minute
		start := time.Now()
		clock := &playbackClock{at: start.Add(2 * time.Minute), limit: w.v.size, media: mediaTiming{w.v.size, time.Minute}}
		ctx := context.WithValue(context.Background(), playbackClockKey{}, clock)
		ctx = context.WithValue(ctx, httpGETKey{}, true)
		_, release := addTestDemand(t, s, ctx, w.v, true)
		p := protectionFor(s, w.v.key, 0)
		if p.active(start) || !p.active(start.Add(time.Minute)) {
			t.Fatal("independent protection window", p)
		}
		time.Sleep(3 * time.Minute)
		if !protectionFor(s, w.v.key, 0).active(time.Now()) {
			t.Fatal("need-by ended live protection")
		}
		ended := time.Now()
		release()
		if got := protectionFor(s, w.v.key, 0); !got.start.IsZero() || got.until != ended.Add(time.Minute) {
			t.Fatal("wrong final-source grace", got)
		}
		time.Sleep(30 * time.Second)
		release()
		s.mu.Lock()
		s.notifyCapacityLocked()
		s.mu.Unlock()
		if protectionFor(s, w.v.key, 0).until != ended.Add(time.Minute) {
			t.Fatal("duplicate event refreshed grace")
		}
		time.Sleep(30 * time.Second)
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
		if protectionFor(s, w.v.key, 0).until != ended.Add(time.Minute) {
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
				time.Sleep(time.Minute)
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
		if !p.start.IsZero() || p.until != ended.Add(getHandoff+time.Minute) {
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
		time.Sleep(time.Minute)
		s.retentionMu.Lock()
		s.nextProtectionExpiryLocked(time.Now())
		s.retentionMu.Unlock()
		if !protectionFor(s, w.v.key, 0).active(time.Now()) {
			t.Fatal("old tail expiry removed new protection")
		}
	})
}

func TestLocalProtectionQueueHandoffThenSixtySeconds(t *testing.T) {
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
		if p := protectionFor(s, v.key, 0); !p.start.IsZero() || p.until != ended.Add(queueHandoff+time.Minute) {
			t.Fatal("queue protection did not start at handoff end", p)
		}
		time.Sleep(time.Minute)
		if protectionFor(s, v.key, 0).active(time.Now()) {
			t.Fatal("queue-only grace survived unified expiry")
		}
	})
}

func TestLocalProtectionConfiguration(t *testing.T) {
	for _, lead := range []time.Duration{-time.Second, 0, time.Second, 24 * time.Hour, 24*time.Hour + time.Second} {
		cfg := fixtureConfig()
		cfg.LocalProtectAhead = lead
		if err := cfg.validate(); (err == nil) != (lead >= 0 && lead <= 24*time.Hour) {
			t.Fatal(lead, err)
		}
	}
	s := &Server{cfg: Config{PrepareAhead: 10 * time.Minute}}
	if s.protectionAhead() != 10*time.Minute {
		t.Fatal("default local window did not follow download lead")
	}
	s.cfg.LocalProtectAhead = time.Minute
	if s.protectionAhead() != time.Minute {
		t.Fatal("local and download windows cannot differ")
	}
}
