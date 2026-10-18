package blocksched

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestSetSoftLimitPreservesGrowthAndRunningTasks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 16, GrowInterval: 250 * time.Millisecond})
		defer s.Close()
		if !s.SetSoftLimit(1) || s.SetSoftLimit(0) {
			t.Fatal("invalid limit handling")
		}
		started := make(chan time.Time, 20)
		for i := range 20 {
			_, _, err := s.Schedule(string(rune('a'+i)), 1<<20, func(ctx context.Context, _ *Progress) Result {
				started <- time.Now()
				<-ctx.Done()
				return Result{Err: ctx.Err()}
			}, need("video", false, 0, time.Minute))
			if err != nil {
				t.Fatal(err)
			}

		}
		<-started
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if len(started) != 0 {
			t.Fatal("single ceiling leaked")
		}
		released := time.Now()
		if !s.SetSoftLimit(16) {
			t.Fatal("release failed")
		}
		for i := 1; i < 16; i++ {
			if at := <-started; at.Sub(released) != time.Duration(i)*250*time.Millisecond {
				t.Fatalf("growth %d: %s", i, at.Sub(released))
			}
		}
		s.SetSoftLimit(1)
		s.call(func(st *state) {
			if st.active != 16 || st.control.open != 1 {
				t.Fatal("lowering canceled running work or failed to cap")
			}
		})
		time.Sleep(time.Second)
		synctest.Wait()
		if len(started) != 0 {
			t.Fatal("ceiling exceeded")
		}
		s.Close()
		if s.SetSoftLimit(16) {
			t.Fatal("closed scheduler accepted update")
		}
	})
}
