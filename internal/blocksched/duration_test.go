package blocksched

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

const standardBlockSize = 1 << 20 // Fixture size; the scheduler has no built-in block size.

func TestComparableCompletions(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		work     int64
		readable bool
		want     bool
	}{
		{standardBlockSize, true, true}, {32 << 10, true, false},
		{standardBlockSize - 1, true, false}, {8 << 20, true, false},
		{standardBlockSize, false, false},
	} {
		p := &Progress{work: tc.work, admitted: now.Add(-time.Second)}
		if tc.readable {
			p.ready = now
		}
		if p.comparable(standardBlockSize) != tc.want {
			t.Fatalf("completion classification: %+v", tc)
		}
	}
}

func TestDurationUsesAdmissionToReadableAndHoldsSlotThroughCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var samples []Event
		s := New(Config{SampleBytes: standardBlockSize, SoftLimit: 1, Observe: func(e Event) {
			if e.Kind == "sample" {
				samples = append(samples, e)
			}
		}})
		defer s.Close()
		gate := make(chan struct{})
		j, _, _ := s.Schedule("standard", standardBlockSize, func(_ context.Context, p *Progress) Result {
			time.Sleep(time.Second) // headers and checks count, even without body progress
			time.Sleep(2 * time.Second)
			p.Ready()
			<-gate
			p.Ready() // a late duplicate cannot extend the readable timestamp
			return Result{}
		}, need("reader", 510*time.Second, time.Hour))
		time.Sleep(10 * time.Second) // queued outside the preparation window: excluded

		time.Sleep(20 * time.Second)
		synctest.Wait()
		s.call(func(st *state) {
			if st.active != 1 || len(samples) != 0 {
				t.Error("cleanup released slot or reported completion early")
			}
		})
		close(gate)
		<-j.Done()
		s.call(func(st *state) {
			if st.active != 0 || len(samples) != 1 || samples[0].BlockTime != 3*time.Second {
				t.Error("queue/cleanup time entered sample", samples)
			}
		})
	})
}

func TestFailedAndCanceledExecutionsDoNotSample(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
			var samples []Event
			s := New(Config{SampleBytes: standardBlockSize, SoftLimit: 1, Observe: func(e Event) {
				if e.Kind == "sample" {
					samples = append(samples, e)
				}
			}})
			_, d, _ := s.Schedule("failed", standardBlockSize, func(ctx context.Context, p *Progress) Result {
				time.Sleep(time.Second)
				p.Ready()
				<-ctx.Done()
				return Result{Err: err}
			}, need("reader", 0, time.Hour))

			time.Sleep(2 * time.Second)
			d.Close()
			synctest.Wait()
			s.call(func(st *state) {
				if len(samples) != 0 {
					t.Error("failed or canceled execution sampled")
				}
			})
			s.Close()
		}
	})
}
