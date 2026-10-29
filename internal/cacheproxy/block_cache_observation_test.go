package cacheproxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestBlockCacheObservationCoalescesWithoutCapturingBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, s := testSession(t)
		s.ctx = context.Background()
		s.observeBlockCacheBytes(17)
		if s.blockObserver.Load() != nil {
			t.Fatal("cache accounting created observation")
		}
		sub, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		snapshot := observedSnapshot(t, sub)
		_, blocksChanged, _ := sub.Snapshot()
		_, speedChanged := sub.DownloadSpeed()
		bytes, changed := sub.BlockCacheBytes()
		if bytes != 17 {
			t.Fatal("initial subscription lost existing cache", bytes)
		}
		for range 100 {
			go s.observeBlockCacheBytes(1)
		}
		synctest.Wait()
		time.Sleep(49 * time.Millisecond)
		synctest.Wait()
		select {
		case <-changed:
			t.Fatal("file changes bypassed the batch window")
		default:
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		select {
		case <-changed:
		default:
			t.Fatal("file changes were not published")
		}
		bytes, idle := sub.BlockCacheBytes()
		if bytes != 117 {
			t.Fatal("concurrent file writes lost bytes", bytes)
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		select {
		case <-idle:
			t.Fatal("idle cache kept publishing")
		case <-blocksChanged:
			t.Fatal("cache changes woke demand capture")
		case <-speedChanged:
			t.Fatal("cache changes woke speed sampling")
		default:
		}
		if current, _, _ := sub.Snapshot(); current != snapshot {
			t.Fatal("cache changes rebuilt demand snapshot")
		}
		sub.Close()
		s.observeBlockCacheBytes(-117)
		if s.blockObserver.Load() != nil {
			t.Fatal("closed view retained observer")
		}
		resumed, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer resumed.Close()
		if bytes, _ := resumed.BlockCacheBytes(); bytes != 0 {
			t.Fatal("reconnect retained stale cache size", bytes)
		}
	})
}
