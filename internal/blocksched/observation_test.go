package blocksched

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestObservationTracksExecutionAndIndependentReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		wake := make(chan struct{}, 1)
		stop := s.WatchChanges(wake)
		defer stop()
		reads := s.NewReadRequests("owner", func(int64) string { return "key" }, nil)
		defer reads.Close()
		task, _, err := s.Schedule("key", 1, func(ctx context.Context, p *Progress) Result { <-ctx.Done(); return Result{Err: ctx.Err()} }, Need{Owner: "owner", ReadyBy: time.Now().Add(time.Hour), LeaseUntil: time.Now().Add(2 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if s.Observation().Tasks["key"].Running {
			t.Fatal("observer admitted future work")
		}
		select {
		case <-wake:
		default:
			t.Fatal("missing registration event")
		}
		firstWait := time.Now()
		one := reads.Wait(0)
		time.Sleep(time.Second)
		secondWait := time.Now()
		two := reads.Wait(0)
		synctest.Wait()
		view := s.Observation()
		if !view.Tasks["key"].Running || view.Tasks["key"].Readers != 2 || view.Active != 1 || view.Tasks["key"].WaitSince != firstWait || view.Tasks["key"].Deadline != firstWait.Add(time.Hour+DownloadGrace) {
			t.Fatal(view)
		}
		one()
		if got := s.Observation().Tasks["key"]; got.Readers != 1 || got.WaitSince != secondWait {
			t.Fatal(got)
		}
		two()
		if got := s.Observation().Tasks["key"]; got.Readers != 0 || !got.Running || !got.WaitSince.IsZero() {
			t.Fatal(got)
		}
		task.Close()
		synctest.Wait()
		if len(s.Observation().Tasks) != 0 {
			t.Fatal("finished task remained")
		}
		stop()
		stop()
		s.call(func(st *state) {
			if st.observers != nil {
				t.Fatal("observer retained after unsubscribe")
			}
		})
	})
}

func TestObservationReportsReadBeforeTaskExists(t *testing.T) {
	s := New(Config{SoftLimit: 1})
	defer s.Close()
	wake := make(chan struct{}, 1)
	stop := s.WatchChanges(wake)
	defer stop()
	reads := s.NewReadRequests("owner", func(int64) string { return "key" }, nil)
	defer reads.Close()
	end := reads.Wait(0)
	if got := s.Observation().Tasks["key"]; got.Readers != 1 || got.Running {
		t.Fatal(got)
	}
	end()
	if len(s.Observation().Tasks) != 0 {
		t.Fatal("stale read")
	}
}
