package console

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

func TestBusinessRetryDoesNotResetBudgetOrRewriteMonitor(t *testing.T) {
	for _, scenario := range []string{"fallback", "all-fail", "parent-canceled"} {
		t.Run(scenario, func(t *testing.T) {
			c := testConsole(t)
			const target = "https://play.udon.dance/files/1/42-video.mp4?e=0123456789abcdef0123456789abcdef&s=12"
			business := false
			attempts := 0
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var deadlines []time.Time
			measured := catalogTransport(func(r *http.Request) (*http.Response, error) {
				if business {
					attempts++
					deadline, ok := r.Context().Deadline()
					if !ok {
						t.Error("missing shared deadline")
					}
					deadlines = append(deadlines, deadline)
					if scenario == "parent-canceled" {
						cancel()
						return nil, ctx.Err()
					}
					if scenario == "all-fail" || attempts == 1 {
						return nil, io.EOF
					}
				}
				resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"time":"20261004235822","groups":{"contents":[{"songInfos":[{"id":42}]}]}}`))}
				if r.URL.Path == "/Api/Songs/play" {
					resp.StatusCode = 302
					resp.Header.Set("Location", target)
				}
				return resp, nil
			})
			c.requestRevision = upstreamrequest.Default.Publish(monitorTestChannels{measured})
			m, err := upstreamstate.NewMonitor(upstreamstate.Options{})
			if err != nil {
				t.Fatal(err)
			}
			c.monitor = m
			if err = m.CheckSelected(context.Background(), upstreamstate.CheckPlayback); err != nil {
				t.Fatal(err)
			}
			before := m.Results(upstreamstate.PlaybackURL)
			if len(c.operationClients(context.Background(), upstreamstate.PlaybackURL, upstreamstate.Constraints{Route: "cf", Entry: c.apiBase + "/Api/Songs/play?node=cf"})) != 2 {
				t.Fatal("expected two candidates")
			}
			business = true
			got, err := c.resolveSongURL(ctx, 42)
			switch scenario {
			case "fallback":
				if err != nil || got != target || attempts != 2 {
					t.Fatalf("got=%q attempts=%d err=%v", got, attempts, err)
				}
			case "all-fail":
				if err == nil || attempts != 2 { // Each fixed-HTTP candidate is attempted once.
					t.Fatalf("attempts=%d err=%v", attempts, err)
				}
			case "parent-canceled":
				if !errors.Is(err, context.Canceled) || attempts != 1 {
					t.Fatalf("attempts=%d err=%v", attempts, err)
				}
			}
			parentDeadline, _ := ctx.Deadline()
			for _, deadline := range deadlines {
				if deadline.After(parentDeadline) {
					t.Fatal("fallback reset operation deadline")
				}
			}
			if !reflect.DeepEqual(before, m.Results(upstreamstate.PlaybackURL)) {
				t.Fatal("business failure rewrote independent Monitor observations")
			}
		})
	}
}
