package blocksched

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestBlockingBypassesSoftLimitAndRamp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 16, GrowInterval: time.Hour})
		defer s.Close()
		started := make(chan string, 32)
		run := func(key string, release <-chan struct{}) Run {
			return func(ctx context.Context, _ *Progress) Result {
				started <- key
				select {
				case <-release:
				case <-ctx.Done():
				}
				return Result{}
			}
		}
		now := time.Now()
		ordinaryRelease, blockingRelease := make(chan struct{}), make(chan struct{})
		ordinary, _ := s.Submit("ordinary", 1, run("ordinary", ordinaryRelease))
		ordinary.Add(need("ordinary", false, time.Minute, 24*time.Hour))
		<-started
		var blocked []*Task
		for i := range 20 {
			key := fmt.Sprint(i)
			j, _ := s.Submit(key, 1, run(key, blockingRelease))
			j.Add(need(key, true, 0, 24*time.Hour))
			blocked = append(blocked, j)
		}
		// More readers of an already blocked block still share its execution.
		duplicate, _ := s.Submit("0", 1, nil)
		duplicate.Add(need("another-reader", true, 0, 24*time.Hour))
		queued, _ := s.Submit("queued", 1, run("queued", nil))
		queued.Add(need("queued", false, time.Minute, 24*time.Hour))
		synctest.Wait()
		if len(started) != 20 || !time.Now().Equal(now) || duplicate != blocked[0] {
			t.Fatal("blocked reads waited, hit a cap, or duplicated work", len(started), time.Since(now))
		}
		s.call(func(st *state) {
			if st.active != 21 || st.control.open != 1 {
				t.Error("blocking borrowed or inflated ordinary capacity", st.active, st.control.open)
			}
		})
		for range 20 {
			<-started
		}
		close(blockingRelease)
		for _, j := range blocked {
			<-j.Done()
		}
		synctest.Wait()
		if len(started) != 0 {
			t.Fatal("overflow created a reusable ordinary quota")
		}
		close(ordinaryRelease)
		if key := <-started; key != "queued" || !time.Now().Equal(now) {
			t.Fatal("ordinary slot did not become immediately reusable", key)
		}
	})
}

func TestOrdinaryRequestsRampToFullSoftLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 16, GrowInterval: 250 * time.Millisecond})
		defer s.Close()
		started := make(chan time.Time, 20)
		release := make(chan struct{})
		now := time.Now()
		for i := range 20 {
			j, _ := s.Submit(fmt.Sprint(i), 1<<20, func(ctx context.Context, _ *Progress) Result {
				started <- time.Now()
				select {
				case <-release:
				case <-ctx.Done():
				}
				return Result{}
			})
			j.Add(need("same", false, time.Second, time.Hour))
		}
		time.Sleep(15 * 250 * time.Millisecond)
		synctest.Wait()
		if len(started) != 16 {
			t.Fatal("ordinary admission did not reach exactly the soft limit", len(started))
		}
		for i := range 16 {
			if at := <-started; at.Sub(now) != time.Duration(i)*250*time.Millisecond {
				t.Fatal("ordinary growth skipped pacing", i, at.Sub(now))
			}
		}
		release <- struct{}{}
		if at := <-started; !at.Equal(time.Now()) {
			t.Fatal("released capacity waited for another growth interval")
		}
		s.call(func(st *state) {
			if st.active != 16 || st.control.open != 16 {
				t.Error("ordinary work exceeded soft limit", st.active, st.control.open)
			}
		})
	})
}

func TestBlockingPromotionBypassesResumeCooldown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1, PreemptAfter: time.Second, Cooldown: time.Hour})
		defer s.Close()
		started := make(chan time.Time, 2)
		old, _ := s.Submit("old", 1<<20, func(ctx context.Context, _ *Progress) Result {
			started <- time.Now()
			<-ctx.Done()
			return Result{Err: ErrPaused, Remaining: 1 << 20}
		})
		d := old.Add(need("old", false, time.Minute, 24*time.Hour))
		<-started
		front, _ := s.Submit("front", 1, func(ctx context.Context, _ *Progress) Result { <-ctx.Done(); return Result{} })
		front.Add(need("front", true, 0, 24*time.Hour))
		time.Sleep(time.Second)
		synctest.Wait()
		now := time.Now()
		if !d.Update(need("old", true, 0, 24*time.Hour)) {
			t.Fatal("promotion rejected")
		}
		if at := <-started; !at.Equal(now) {
			t.Fatal("blocked read waited for resume cooldown", at.Sub(now))
		}
	})
}

func TestBlockingContentionDoesNotRefillBackgroundSlots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, paused := 0, 0
		s := New(Config{SoftLimit: 16, GrowInterval: 250 * time.Millisecond,
			PreemptAfter: 200 * time.Millisecond, Cooldown: 500 * time.Millisecond,
			Observe: func(e Event) {
				if !e.Blocking && e.Kind == "started" {
					started++
				}
				if e.Kind == "paused" {
					paused++
				}
			}})
		defer s.Close()
		for i := range 24 {
			j, _ := s.Submit(fmt.Sprint(i), 1<<20, func(ctx context.Context, _ *Progress) Result {
				<-ctx.Done()
				return Result{Err: ErrPaused, Remaining: 1 << 20}
			})
			j.Add(need("background", false, time.Second, time.Hour))
		}
		time.Sleep(15 * 250 * time.Millisecond)
		synctest.Wait()
		if started != 16 {
			t.Fatal("background did not fill ordinary capacity", started)
		}
		front, _ := s.Submit("front", 1<<20, func(ctx context.Context, _ *Progress) Result {
			<-ctx.Done()
			return Result{}
		})
		d := front.Add(need("reader", true, 0, time.Hour))
		time.Sleep(1200 * time.Millisecond)
		synctest.Wait()
		if paused != 6 || started != 16 {
			t.Fatalf("yielded bandwidth was refilled: paused=%d background starts=%d", paused, started)
		}
		s.call(func(st *state) {
			if st.active != 11 {
				t.Error("background competition did not decrease", st.active)
			}
		})
		d.Close()
		synctest.Wait()
		if started <= 16 {
			t.Fatal("ordinary admission did not resume after Blocking ended")
		}
	})
}
