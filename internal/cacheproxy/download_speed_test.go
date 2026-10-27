package cacheproxy

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDownloadSpeedSharesReadsWithoutCapturingBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, s := testSession(t)
		s.ctx = context.Background()
		var observed atomic.Int64
		s.cfg.ObserveDownloadBytes = func(n int64) { observed.Add(n) }
		read := func(n int) {
			p := &downloadProgress{log: slog.New(slog.NewTextHandler(io.Discard, nil)), observeBytes: s.observeDownloadBytes}
			if count, err := io.Copy(io.Discard, progressReader{Reader: strings.NewReader(strings.Repeat("x", n)), progress: p}); err != nil || count != int64(n) {
				t.Errorf("read %d bytes: %v; want %d", count, err, n)
			}
		}
		read(100) // No observation state is created by download traffic.
		if s.blockObserver.Load() != nil {
			t.Fatal("traffic created observer")
		}
		sub, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		other, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		snapshot := observedSnapshot(t, sub)
		_, blockChanged, _ := sub.Snapshot()
		// Two short-lived readers can finish before the first speed tick. Their
		// bytes must still be included once, even with multiple display subscribers.
		go read(1024)
		go read(2048)
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		speed, speedChanged := sub.DownloadSpeed()
		if speed != 3072 {
			t.Fatal("wrong one-second aggregate", speed)
		}
		shared, _ := other.DownloadSpeed()
		if shared != speed {
			t.Fatal("subscribers disagree")
		}
		if current, _, _ := sub.Snapshot(); current != snapshot {
			t.Fatal("speed rebuilt block snapshot")
		}
		select {
		case <-blockChanged:
			t.Fatal("speed woke block observation")
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if speed, _ := sub.DownloadSpeed(); speed != 0 {
			t.Fatal("speed did not return to zero", speed)
		}
		select {
		case <-speedChanged:
		default:
			t.Fatal("zero was not published")
		}
		_, idle := sub.DownloadSpeed()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		select {
		case <-idle:
			t.Fatal("idle speed kept publishing")
		default:
		}
		read(512)
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if speed, _ := sub.DownloadSpeed(); speed != 512 {
			t.Fatal("speed did not restart", speed)
		}
		other.Close()
		sub.Close()
		if s.blockObserver.Load() != nil {
			t.Fatal("observer retained after close")
		}
		read(200)
		if observed.Load() != 3884 {
			t.Fatal("experiment observer lost or duplicated bytes", observed.Load())
		}
		resumed, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer resumed.Close()
		synctest.Wait()
		if speed, _ := resumed.DownloadSpeed(); speed != 0 {
			t.Fatal("new subscription inherited old traffic", speed)
		}
	})
}
