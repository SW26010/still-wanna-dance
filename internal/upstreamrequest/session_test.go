package upstreamrequest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSessionReusesValidatedPathAndDiscardsFailedPreference(t *testing.T) {
	var calls []string
	var clients []*http.Client
	broken := false
	for i := range 3 {
		clients = append(clients, &http.Client{Transport: attemptTransport(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, fmt.Sprintf("%s/%d", r.URL.Scheme, i))
			if i != 1 || r.URL.Scheme == "https" {
				return nil, io.EOF
			}
			body := "valid"
			if broken {
				body = "invalid"
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
		})})
	}
	session := NewSession(clients)
	read := func(target string, want []string, wantError bool) {
		t.Helper()
		calls = nil
		err := session.Read(t.Context(), target, time.Second, func(ctx context.Context, client *http.Client, target string) error {
			_, err := readAttempt(ctx, client, target)
			return err
		})
		if (err != nil) != wantError || !slices.Equal(calls, want) {
			t.Fatalf("calls=%v want=%v error=%v", calls, want, err)
		}
	}
	fresh := []string{"https/0", "http/0", "https/1", "http/1"}
	read("https://example.com/play?id=1", fresh, false)
	read("https://example.com/play?id=2", []string{"http/1"}, false)
	// A response is not reusable until the caller validates its complete body.
	// A business error also forbids further plaintext attempts in this read.
	broken = true
	read("https://example.com/play?id=3", []string{"http/1", "https/0", "https/2"}, true)
	broken = false
	read("https://example.com/play?id=4", fresh, false)
	read("https://example.com/list", fresh, false)
	read("https://other.example.com/list", fresh, false)
	session = NewSession(clients)
	read("https://other.example.com/list", fresh, false)
}

func TestSessionHonorsEndpointUpgradeAfterReusingHTTP(t *testing.T) {
	var calls []string
	upgrade := false
	client := &http.Client{Transport: attemptTransport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Scheme)
		if r.URL.Scheme == "https" && !upgrade {
			return nil, io.EOF
		}
		if r.URL.Scheme == "http" && upgrade {
			return &http.Response{StatusCode: 301, Header: http.Header{"Location": {"https://example.com/list"}}, Body: http.NoBody, Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("valid")), Header: make(http.Header), Request: r}, nil
	})}
	session := NewSession([]*http.Client{client})
	for _, want := range [][]string{{"https", "http"}, {"http", "https"}, {"https"}} {
		calls = nil
		err := session.Read(t.Context(), "https://example.com/list", time.Second, func(ctx context.Context, client *http.Client, target string) error {
			_, err := readAttempt(ctx, client, target)
			return err
		})
		if err != nil || !slices.Equal(calls, want) {
			t.Fatal(calls, err)
		}
		upgrade = true
	}
}
