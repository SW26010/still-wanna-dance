package upstreamcheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProbeProtocolFallbackPreservesEvidence(t *testing.T) {
	var calls []string
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Scheme)
		if r.URL.Scheme == "https" {
			return nil, io.EOF
		}
		return fixture(r)
	}))
	for _, route := range routeIDs(Catalog) {
		o, id, _ := probeCatalogData(context.Background(), []*http.Client{client}, DefaultPolicy(), route)
		if o.state != "available" || o.scheme != "http" || id <= 0 {
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
	if strings.Join(calls, ",") != "https,http,https,http,https,http,https,http,https,http" {
		t.Fatal(calls)
	}
}

func TestResourceProbeRetriesProtocolAtRedirectTarget(t *testing.T) {
	var calls []string
	client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Scheme+"/"+r.URL.Query().Get("next"))
		if r.URL.Scheme == "https" {
			return nil, io.EOF
		}
		if r.URL.Query().Get("next") == "" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {strings.Replace(videoFixture, "https://", "http://", 1) + "&next=1"}}, Body: http.NoBody, Request: r}, nil
		}
		return fixture(r)
	}))
	sample, _ := parseSample(videoFixture)
	o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
	if o.state != "available" || o.scheme != "http" || strings.Join(calls, ",") != "https/,http/,https/1,http/1" {
		t.Fatal(o, calls)
	}
}

func TestResourceProbeProtocolRedirectsKeepEndpointBudget(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls []string
			client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.URL.Scheme)
				if r.URL.Scheme == "https" {
					return nil, io.EOF
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {videoFixture}}, Body: http.NoBody, Request: r}, nil
			}))
			sample, _ := parseSample(videoFixture)
			o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
			if o.state == "available" || strings.Join(calls, ",") != "https,http,https" {
				t.Fatal(o, calls)
			}
		})
	}
	t.Run("revisited endpoint", func(t *testing.T) {
		var calls []string
		client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.URL.Scheme+"/"+r.URL.Query().Get("next"))
			if r.URL.Scheme == "https" {
				return nil, io.EOF
			}
			location := videoFixture
			if r.URL.Query().Get("next") == "" {
				location += "&next=1"
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {location}}, Body: http.NoBody, Request: r}, nil
		}))
		sample, _ := parseSample(videoFixture)
		o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
		if o.state != "invalid" || strings.Join(calls, ",") != "https/,http/,https/1,http/1" {
			t.Fatal(o, calls)
		}
	})
}
