package cacheproxy

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestResourceLoadHookCoversDownloadAndReleasesOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			var active, begins atomic.Int32
			released := make(chan struct{}, 2)
			s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if active.Load() != 1 {
					t.Error("business activity not marked before upstream request")
				}
				if fail {
					w.WriteHeader(503)
					return
				}
				fmt.Fprint(w, payload)
			})
			s.cfg.BeginResourceLoad = func() func() {
				begins.Add(1)
				active.Add(1)
				return func() { active.Add(-1); released <- struct{}{} }
			}
			w := request(s, "GET", videoURL(payload), nil)
			if !fail {
				assertResponse(t, w, 200, payload)
			}
			select {
			case <-released:
			case <-time.After(time.Second):
				t.Fatal("activity not released")
			}
			if active.Load() != 0 || begins.Load() != 1 {
				t.Fatal("unbalanced activity")
			}
			if !fail {
				assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
				if begins.Load() != 1 {
					t.Fatal("local hit suppressed automatic probes")
				}
			}
		})
	}
}
