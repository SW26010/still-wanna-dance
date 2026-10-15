package blocksched

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestMixedDemandKeepsDeadlineAndOpportunisticAdmission(t *testing.T) {
	for _, earlier := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			s := New(Config{SoftLimit: 1})
			defer s.Close()
			started := make(chan string, 8)
			gate := make(chan struct{})
			run := func(key string) Run {
				return func(ctx context.Context, _ *Progress) Result {
					started <- key
					select {
					case <-gate:
					case <-ctx.Done():
					}
					return Result{}
				}
			}
			hold, _ := s.Submit("hold", 1, run("hold"))
			hold.Add(need("hold", false, 0, time.Hour))
			<-started
			shared, _ := s.Submit("shared", 1, run("shared"))
			d := shared.Add(need("playback", false, 15*time.Minute, time.Hour))
			shared.Add(need("batch", false, 0, time.Hour))
			other, _ := s.Submit("other", 1, run("other"))
			other.Add(need("other", false, 2*time.Second, time.Hour))
			if earlier {
				d.Update(need("playback", false, time.Second, time.Hour))
			}
			synctest.Wait()
			if len(started) != 0 {
				t.Fatal("opportunistic demand bypassed quota")
			}
			s.call(func(st *state) {
				j := st.jobs["shared"]
				if j.need.ReadyBy.IsZero() || !j.need.Opportunistic || j.need.Blocking || j.index[0] < 0 || j.index[1] < 0 {
					t.Error("mixed demand lost an independent attribute", j.need)
				}
			})
			want := []string{"other", "shared"}
			if earlier {
				want = []string{"shared", "other"}
			}
			for _, key := range want {
				gate <- struct{}{}
				if got := <-started; got != key {
					t.Fatal("lost deadline priority or spare-capacity admission", got, key)
				}
			}
			gate <- struct{}{}
			synctest.Wait()
			s.call(func(st *state) {
				if st.active != 0 || st.ready.Len() != 0 || st.untimed.Len() != 0 {
					t.Error("shared execution left a second heap entry")
				}
			})
			if len(started) != 0 {
				t.Fatal("same block executed twice")
			}
		})
	}
}

func TestMixedDemandWithdrawalRemovesOnlyEarlyAdmission(t *testing.T) {
	for _, expire := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			s := New(Config{SoftLimit: 1})
			defer s.Close()
			gate := make(chan struct{})
			hold, _ := s.Submit("hold", 1, func(ctx context.Context, _ *Progress) Result {
				select {
				case <-gate:
				case <-ctx.Done():
				}
				return Result{}
			})
			hold.Add(need("hold", false, 0, time.Hour))
			started := make(chan struct{}, 1)
			j, _ := s.Submit("mixed", 1, func(context.Context, *Progress) Result { started <- struct{}{}; return Result{} })
			playback := j.Add(need("playback", false, 15*time.Minute, time.Hour))
			batch := j.Add(need("batch", false, 0, 100*time.Millisecond))
			if expire {
				time.Sleep(100 * time.Millisecond)
			} else {
				batch.Close()
			}
			close(gate)
			synctest.Wait()
			if len(started) != 0 {
				t.Fatal("withdrawn early demand remained executable")
			}
			s.call(func(st *state) {
				if st.untimed.Len() != 0 || st.ready.Len() != 1 || st.jobs["mixed"].need.Opportunistic {
					t.Error("stale opportunistic membership")
				}
			})
			playback.Update(need("playback", false, time.Second, time.Hour))
			<-started
			<-j.Done()
		})
	}
}
