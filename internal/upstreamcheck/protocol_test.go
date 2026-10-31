package upstreamcheck

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestProbesUseFixedProtocolsAndPreserveEvidence(t *testing.T) {
	var calls []string
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Scheme)
		return fixture(r)
	}))
	for _, route := range routeIDs(Catalog) {
		o, id, _ := probeCatalogData(context.Background(), []*http.Client{client}, DefaultPolicy(), route)
		want := "https"
		if route == "api" {
			want = "http"
		}
		if o.state != "available" || o.scheme != want || id <= 0 {
			t.Fatal(o, id)
		}
	}
	o, sample, result := probePlaybackResponse(context.Background(), client, DefaultPolicy(), 42, playbackRoutes[0])
	if o.state != "available" || o.scheme != "http" || sample == nil || result.API != apiBase+"/Api/Songs/play" || result.URL != videoFixture {
		t.Fatal(o, sample, result)
	}
	o = probeResourceMode(context.Background(), client, DefaultPolicy(), 42, *sample, false)
	if o.state != "available" || o.scheme != "http" {
		t.Fatal(o)
	}
	if strings.Join(calls, ",") != "http,https,https,http,http" {
		t.Fatal(calls)
	}
}

func TestResourceProbeUsesHTTPAtRedirectTarget(t *testing.T) {
	var calls []string
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Scheme+"/"+r.URL.Query().Get("next"))
		if r.URL.Query().Get("next") == "" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {strings.Replace(videoFixture, "https://", "http://", 1) + "&next=1"}}, Body: http.NoBody, Request: r}, nil
		}
		return fixture(r)
	}))
	sample, _ := parseSample(videoFixture)
	o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
	if o.state != "available" || o.scheme != "http" || strings.Join(calls, ",") != "http/,http/1" {
		t.Fatal(o, calls)
	}
}

func TestResourceProbeRejectsUpgradeAndRedirectLoops(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls []string
			client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.URL.Scheme)
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {videoFixture}}, Body: http.NoBody, Request: r}, nil
			}))
			sample, _ := parseSample(videoFixture)
			o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
			if o.state == "available" || strings.Join(calls, ",") != "http" {
				t.Fatal(o, calls)
			}
		})
	}
	t.Run("revisited endpoint", func(t *testing.T) {
		var calls []string
		client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.URL.Scheme+"/"+r.URL.Query().Get("next"))
			location := videoFixture
			if r.URL.Query().Get("next") == "" {
				location += "&next=1"
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {location}}, Body: http.NoBody, Request: r}, nil
		}))
		sample, _ := parseSample(videoFixture)
		o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
		if o.state != "invalid" || strings.Join(calls, ",") != "http/,http/1" {
			t.Fatal(o, calls)
		}
	})
}
