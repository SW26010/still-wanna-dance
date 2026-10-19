package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func TestUniqueUseCapacityReservesGETHeadroom(t *testing.T) {
	_, s := testSession(t)
	ctx := context.WithValue(context.Background(), httpGETKey{}, true)
	background := video{key: "background", size: backgroundUseBlocks * rangeBlockSize}
	_, closeBackground := addTestDemand(t, s, ctx, background, false)
	defer closeBackground()
	if _, _, err := s.addDemand(ctx, video{key: "new", size: 1}, false); !errors.Is(err, ErrDemandCapacity) {
		t.Fatal("non-GET exceeded its unique block threshold", err)
	}
	_, closeGET := addTestDemand(t, s, ctx, video{key: "GET", size: (maxUseBlocks - backgroundUseBlocks) * rangeBlockSize}, true)
	defer closeGET()
	_, closeShared := addTestDemand(t, s, ctx, background, false)
	if useCount(s) != maxUseBlocks {
		t.Fatal("shared source changed unique usage", useCount(s))
	}
	closeShared()
	if _, _, err := s.addDemand(ctx, video{key: "new", size: 1}, true); !errors.Is(err, ErrDemandCapacity) || useCount(s) != maxUseBlocks {
		t.Fatal("GET pressure damaged existing sources", err, useCount(s))
	}
	changed := s.DemandCapacityChanged()
	closeGET()
	select {
	case <-changed:
	default:
		t.Fatal("source exit did not wake admission")
	}
	if useCount(s) != backgroundUseBlocks {
		t.Fatal("GET completion retained demand slots", useCount(s))
	}
	closeBackground()
	if useCount(s) != 0 {
		t.Fatal("last contribution did not release unique capacity")
	}
}

func TestReadableBlocksRetainUseUntilConsumptionOrSourceExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		d, closeGET := addTestDemand(t, s, context.Background(), w.v, true)
		for b := int64(0); b < 20; b++ {
			w.f.ranges.done[b] = true
		}
		w.plan(time.Now(), true)
		if useCount(s) != 20 {
			t.Fatal("local completion released usage capacity")
		}
		g := &getConsumption{s: s, demand: d, size: w.v.size}
		g.start(http.StatusOK, nil)
		g.accept(2 * rangeBlockSize)
		if useCount(s) != 18 {
			t.Fatal("delivered blocks still occupy usage capacity")
		}
		closeGET()
		if useCount(s) != 18 {
			t.Fatal("handoff lost readable but unconsumed usage")
		}
		time.Sleep(getHandoff)
		synctest.Wait()
		if useCount(s) != 0 {
			t.Fatal("fixed handoff expiry retained capacity")
		}
	})
}

func TestQueueUseOutlivesPreparationAndSharesWithGET(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, s := testSession(t)
		s.UpdatePrefetchQueue([]int64{1, 2}, false)
		v := w.v
		v.songID = "1"
		_, prepared := addTestDemand(t, s, s.queueDemandContext(context.Background(), "1"), v, false)
		prepared()
		if useCount(s) != 20 {
			t.Fatal("queue preparation ended queue usage")
		}
		ctx := context.WithValue(context.Background(), httpGETKey{}, true)
		_, closeGET := addTestDemand(t, s, ctx, v, true)
		s.UpdatePrefetchQueue([]int64{2}, false)
		time.Sleep(queueHandoff)
		synctest.Wait()
		if useCount(s) != 20 {
			t.Fatal("queue expiry removed independent GET usage")
		}
		closeGET()
		if useCount(s) != 0 {
			t.Fatal("expired queue handoff revived after GET exit")
		}
	})
}

func TestCapacityRefusalRejectsGETBeforeUpstream(t *testing.T) {
	s, _ := setup(t, func(http.ResponseWriter, *http.Request) { t.Error("rejected GET reached upstream") })
	s.mu.Lock()
	err := s.reserveUseLocked(useSpan{"occupied", 0, maxUseBlocks}, true)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	response := request(s, "GET", videoURL(payload), nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatal("capacity refusal was not explicit", response.Code)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.useBlocks) != maxUseBlocks || len(s.demands) != 0 {
		t.Fatal("rejected GET changed admitted usage")
	}
}

func TestUsageReplacementIsAtomicAndNonGrowingChangesRemainAllowed(t *testing.T) {
	_, s := testSession(t)
	old := useSpan{"same", 0, maxUseBlocks}
	if err := s.reserveUseLocked(old, true); err != nil {
		t.Fatal(err)
	}
	if err := s.replaceUseLocked(old, old, false); err != nil {
		t.Fatal("unchanged source blocked above background threshold", err)
	}
	if err := s.replaceUseLocked(old, useSpan{"same", 0, maxUseBlocks - 1}, false); err != nil {
		t.Fatal("source shrink blocked", err)
	}
	old.last--
	if err := s.replaceUseLocked(old, useSpan{"different", 0, maxUseBlocks}, false); !errors.Is(err, ErrDemandCapacity) {
		t.Fatal("replacement exceeded background threshold", err)
	}
	if useCount(s) != maxUseBlocks-1 || s.useBlocks[useBlockID{"same", 0}] != 1 {
		t.Fatal("refused replacement damaged old usage")
	}
	for _, replacement := range []useSpan{{"equal", 0, old.last}, {"smaller", 0, old.last - 1}} {
		if err := s.replaceUseLocked(old, replacement, false); err != nil {
			t.Fatal("non-growing resource replacement blocked above background threshold", err)
		}
		if useCount(s) != int(replacement.last) || s.useBlocks[useBlockID{old.key, 0}] != 0 || s.useBlocks[useBlockID{replacement.key, 0}] != 1 {
			t.Fatal("replacement did not transfer unique usage")
		}
		old = replacement
	}
	// Another source keeps the old blocks in use, so this same-size resource
	// replacement would now increase unique usage and must remain blocked.
	if err := s.reserveUseLocked(old, true); err != nil {
		t.Fatal(err)
	}
	if err := s.replaceUseLocked(old, useSpan{"shared", 0, old.last}, false); !errors.Is(err, ErrDemandCapacity) {
		t.Fatal("replacement ignored the remaining source's usage", err)
	}
	if useCount(s) != int(old.last) || s.useBlocks[useBlockID{old.key, 0}] != 2 {
		t.Fatal("refused shared replacement damaged existing usage")
	}
}

func useCount(s *Server) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.useBlocks)
}
