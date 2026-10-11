package cacheproxy

import (
	"context"
	"testing"
)

func TestDemandRanksAndLifetimes(t *testing.T) {
	s := &Server{capacityChanged: make(chan struct{})}
	s.UpdatePrefetchQueue([]int64{1, 2, 3}, false)
	queue := context.WithValue(context.Background(), queueDemandKey{}, true)
	_, release1 := s.addDemand(queue, video{key: "shared", songID: "1"}, false)
	_, release2 := s.addDemand(queue, video{key: "shared", songID: "2"}, false)
	check := func(want downloadRank) {
		t.Helper()
		s.mu.Lock()
		got := s.resourceRankLocked("shared")
		s.mu.Unlock()
		if got != want {
			t.Fatalf("rank = %+v, want %+v", got, want)
		}
	}
	check(downloadRank{level: 1})
	release1()
	s.UpdatePrefetchQueue([]int64{3, 2}, false)
	check(downloadRank{level: 2, position: 1})
	ctx, cancel := context.WithCancel(context.Background())
	_, releaseGET := s.addDemand(ctx, video{key: "shared"}, true)
	_, releaseOtherGET := s.addDemand(context.Background(), video{key: "shared"}, true)
	check(downloadRank{})
	s.UpdatePrefetchQueue(nil, true)
	check(downloadRank{})
	cancel() // Rank ignores canceled requests even before AfterFunc executes.
	check(downloadRank{})
	releaseOtherGET()
	check(noDemand)
	releaseGET()
	releaseGET()
	release2()
	_, releaseBatch := s.addDemand(context.Background(), video{key: "shared"}, false)
	check(downloadRank{level: 3})
	releaseBatch()
	check(noDemand)
}
