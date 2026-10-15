package upstreamstate

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestConnectivitySequentialOneByteAndNoThroughput(t *testing.T) {
	channel := upstreamrequest.NewChannel()
	var events []string
	var resourceRequests int
	channel.Publish(calibrationTransport(func(r *http.Request) (*http.Response, error) {
		resourceRequests++
		if r.Header.Get("Range") != "bytes=0-0" {
			t.Fatalf("connectivity downloaded more than one byte: %s", r.Header.Get("Range"))
		}
		if events[len(events)-1] != "resource/checking" {
			t.Fatal("missing progress before request")
		}
		return calibrationRange(r, io.NopCloser(strings.NewReader("x"))), nil
	}))
	CheckConnectivity(context.Background(), channel, "direct", func(row ConnectivityResult) {
		events = append(events, string(row.Operation)+"/"+row.State)
		if row.State == "available" && row.LatencyMS == nil {
			t.Fatal("successful request missing latency")
		}
	})
	want := []string{"catalog/checking", "catalog/available", "playback_url/checking", "playback_url/available", "resource/checking", "resource/available"}
	if !reflect.DeepEqual(events, want) || resourceRequests != 1 {
		t.Fatalf("events=%v requests=%d", events, resourceRequests)
	}
}

func TestConnectivityCancelBetweenStepsAndMissingCatalog(t *testing.T) {
	channel := upstreamrequest.NewChannel()
	channel.Publish(calibrationTransport(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected media request"); return nil, nil }))
	ctx, cancel := context.WithCancel(context.Background())
	var events []string
	CheckConnectivity(ctx, channel, "direct", func(row ConnectivityResult) {
		events = append(events, string(row.Operation)+"/"+row.State)
		if row.State == "available" {
			cancel()
		}
	})
	if !reflect.DeepEqual(events, []string{"catalog/checking", "catalog/available"}) {
		t.Fatal(events)
	}
	channel.Publish(transportFunc(func(*http.Request) (*http.Response, error) { return response(503, ""), nil }))
	events = nil
	CheckConnectivity(context.Background(), channel, "direct", func(row ConnectivityResult) { events = append(events, string(row.Operation)+"/"+row.State) })
	if !reflect.DeepEqual(events, []string{"catalog/checking", "catalog/failed", "playback_url/skipped", "resource/skipped"}) {
		t.Fatal(events)
	}
}
