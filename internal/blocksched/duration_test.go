package blocksched

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestCompletedBlockDurationDecayAndComparableSamples(t *testing.T) {
	now := time.Now()
	d := new(durationEstimate)
	if d.value() != time.Second {
		t.Fatal("missing initial estimate")
	}
	sample := func(work int64, elapsed time.Duration, readable bool) bool {
		p := &Progress{work: work, admitted: now.Add(-elapsed)}
		if readable {
			p.ready = now
		}
		return d.observe(p, now)
	}
	if !sample(standardBlockSize, time.Second, true) || d.value() != time.Second {
		t.Fatal("full block not sampled")
	}
	for _, work := range []int64{32 << 10, standardBlockSize - 1, 8 << 20} {
		if sample(work, time.Minute, true) {
			t.Fatal("incomparable work sampled", work)
		}
	}
	if sample(standardBlockSize, time.Minute, false) {
		t.Fatal("unreadable/sequential execution sampled")
	}
	now = now.Add(sampleHalfLife)
	if !sample(standardBlockSize, 4*time.Second, true) || d.value() != 3*time.Second {
		t.Fatalf("slow sample or decay missing: %v", d.value())
	}
	// The same completion batch has equal new-sample weights.
	sample(standardBlockSize, 8*time.Second, true)
	if d.value() != 5*time.Second {
		t.Fatal("batch mean", d.value())
	}
	// Long idle gaps discard old influence without a timer or accumulated credit.
	now = now.Add(24 * time.Hour)
	sample(standardBlockSize, 2*time.Second, true)
	if d.value() != 2*time.Second {
		t.Fatal("stale history survived idle decay", d.value())
	}
}

func TestDurationUsesAdmissionToReadableAndHoldsSlotThroughCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{SoftLimit: 1})
		defer s.Close()
		gate := make(chan struct{})
		j, _ := s.Submit("standard", standardBlockSize, func(_ context.Context, p *Progress) Result {
			time.Sleep(time.Second) // headers and checks count, even without body progress
			p.Invalidate()          // a discarded attempt still costs execution time
			time.Sleep(2 * time.Second)
			p.Ready()
			<-gate
			p.Ready() // a late duplicate cannot extend the readable timestamp
			return Result{}
		})
		time.Sleep(10 * time.Second) // queued without a demand: excluded
		j.Add(need("reader", true, 0, time.Hour))
		time.Sleep(20 * time.Second)
		synctest.Wait()
		s.call(func(st *state) {
			if st.active != 1 || st.durations.weight != 0 {
				t.Error("cleanup released slot or reported completion early")
			}
		})
		close(gate)
		<-j.Done()
		s.call(func(st *state) {
			if st.active != 0 || st.durations.value() != 3*time.Second {
				t.Error("queue/cleanup time entered sample", st.durations.value())
			}
		})
	})
}

func TestFailedAndPausedExecutionsDoNotSample(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, err := range []error{ErrPaused, context.Canceled} {
			s := New(Config{SoftLimit: 1})
			j, _ := s.Submit("failed", standardBlockSize, func(ctx context.Context, p *Progress) Result {
				time.Sleep(time.Second)
				p.Ready()
				<-ctx.Done()
				return Result{Err: err, Remaining: standardBlockSize / 2}
			})
			d := j.Add(need("reader", true, 0, time.Hour))
			time.Sleep(2 * time.Second)
			d.Close()
			synctest.Wait()
			s.call(func(st *state) {
				if st.durations.weight != 0 {
					t.Error("failed or paused execution sampled")
				}
			})
			s.Close()
		}
	})
}
