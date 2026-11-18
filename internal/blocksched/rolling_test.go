package blocksched

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestRollingDeadlinesReorderAgainstFixedWorkWithoutUpdates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		base := time.Now()
		lease := base.Add(time.Hour)
		release := make(chan struct{})
		_, _, err := s.Schedule("occupied", 1, func(ctx context.Context, _ *Progress) Result {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return Result{}
		}, Need{LeaseUntil: lease})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		started := make(chan string, 7)
		submit := func(key string, fixed time.Time, rolling RollingDeadline) *Task {
			t.Helper()
			task, _, err := s.Schedule(key, 1, func(context.Context, *Progress) Result {
				started <- key
				return Result{}
			}, Need{ReadyBy: fixed, Rolling: rolling, LeaseUntil: lease})
			if err != nil {
				t.Fatal(err)
			}
			return task
		}
		future := base.Add(6 * time.Second)
		submit("head", time.Time{}, RollingDeadline{Base: future})
		submit("later", time.Time{}, RollingDeadline{Base: base, Offset: 9 * time.Second})
		fixed := base.Add(10 * time.Second)
		submit("fixed", fixed, RollingDeadline{})
		mixed := submit("mixed", time.Time{}, RollingDeadline{Base: base, Offset: 6 * time.Second})
		cap := base.Add(11 * time.Second)
		mixed.Add(Need{ReadyBy: cap, LeaseUntil: lease})
		// At release these three and head all have the same need-by.
		submit("fixed-tie", base.Add(12*time.Second), RollingDeadline{})
		submit("other-origin", time.Time{}, RollingDeadline{Base: base})
		submit("same-origin", time.Time{}, RollingDeadline{Base: future})

		time.Sleep(3 * time.Second)
		if got := s.TaskNeedBy("head"); got != future {
			t.Fatal("future estimate moved before its start", got)
		}
		if got := s.TaskNeedBy("mixed"); got != base.Add(9*time.Second) {
			t.Fatal("fixed source hid the earlier rolling source", got)
		}
		time.Sleep(9 * time.Second)
		if got := s.TaskNeedBy("head"); got != time.Now() {
			t.Fatal("head time did not move with now", got)
		}
		if got := s.TaskNeedBy("later"); got != time.Now().Add(9*time.Second) {
			t.Fatal("offset drifted from the rolling clock", got)
		}
		if got := s.TaskNeedBy("mixed"); got != cap {
			t.Fatal("rolling source hid the earlier fixed source", got)
		}
		close(release)
		want := []string{"fixed", "mixed", "head", "fixed-tie", "other-origin", "same-origin", "later"}
		var got []string
		for range want {
			got = append(got, <-started)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("execution order %v, want %v", got, want)
		}
	})
}

func TestRollingDeadlineUsesFutureEstimateForWindowEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{PrepareAhead: 120 * time.Second})
		defer s.Close()
		origin := time.Now().Add(time.Minute)
		started := make(chan time.Time, 1)
		task, _, err := s.Schedule("future", 1, func(context.Context, *Progress) Result {
			started <- time.Now()
			return Result{}
		}, Need{Rolling: RollingDeadline{Base: origin, Offset: 90 * time.Second}, LeaseUntil: origin.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := <-started, origin.Add(-30*time.Second); got != want {
			t.Fatalf("started at %v, want %v", got, want)
		}
		if err := task.Err(); err != nil {
			t.Fatal(err)
		}
	})
}
