package console

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"still-wanna-dance/internal/buildinfo"
)

type updateTransport func(*http.Request) (*http.Response, error)

func (f updateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func releaseResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestCheckLatestRelease(t *testing.T) {
	for _, tc := range []struct {
		name, build, kind, body, result, reason, version string
		status                                           int
	}{
		{"beta to stable", "v1.0.0-beta.4", "formal", `[{"tag_name":"v1.0.0"}]`, "available", "", "v1.0.0", 200},
		{"same version", "v1.0.0", "formal", `[{"tag_name":"v1.0.0"}]`, "current", "", "", 200},
		{"newer local beta", "v1.1.0-beta.1", "formal", `[{"tag_name":"v1.0.0"}]`, "current", "", "", 200},
		{"numeric comparison", "v1.0.9", "formal", `[{"tag_name":"v1.0.10"}]`, "available", "", "v1.0.10", 200},
		{"development", "v0.1.0", "development", `[{"tag_name":"v1.0.0"}]`, "unknown", "version", "", 200},
		{"invalid local version", "dev", "rebuild", `[{"tag_name":"v1.0.0"}]`, "unknown", "version", "", 200},
		{"no release", "v1.0.0", "formal", `not found`, "unknown", "not_found", "", 404},
		{"rate limited", "v1.0.0", "formal", `rate limited`, "unknown", "failed", "", 403},
		{"malformed JSON", "v1.0.0", "formal", `{`, "unknown", "failed", "", 200},
		{"unversioned release", "v1.0.0", "formal", `[{"tag_name":"evidence-1"}]`, "unknown", "not_found", "", 200},
		{"prerelease", "v1.0.0", "formal", `[{"tag_name":"v2.0.0-beta.1","prerelease":true}]`, "unknown", "not_found", "", 200},
		{"draft", "v1.0.0", "formal", `[{"tag_name":"v2.0.0","draft":true}]`, "unknown", "not_found", "", 200},
		{"mixed releases", "v1.0.0", "formal", `[{"tag_name":"v2.0.0-beta.1","prerelease":true},{"tag_name":"v1.1.0"},{"tag_name":"evidence-1"},{"tag_name":"v1.0.2"}]`, "available", "", "v1.1.0", 200},
		{"oversized response", "v1.0.0", "formal", strings.Repeat("x", maxReleaseBytes+1), "unknown", "failed", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != releasesAPI+"&page=1" || r.Header.Get("User-Agent") == "" || r.Header.Get("Accept") != "application/vnd.github+json" {
					t.Errorf("unexpected request: %v %v", r.URL, r.Header)
				}
				return releaseResponse(tc.status, tc.body), nil
			})}
			got := checkLatestRelease(context.Background(), client, buildinfo.Info{Version: tc.build, Kind: tc.kind})
			if got.Result != tc.result || got.Reason != tc.reason || got.LatestVersion != tc.version {
				t.Fatalf("result = %+v", got)
			}
			if tc.version != "" && got.ReleaseURL != releasePageBase+tc.version {
				t.Fatalf("unexpected release link: %q", got.ReleaseURL)
			}
		})
	}
}

func TestUpdatePaginatesAndChoosesNewestFormalVersion(t *testing.T) {
	for _, failSecond := range []bool{false, true} {
		var calls int
		client := &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.String() != releasesAPI+"&page="+strconv.Itoa(calls) {
				t.Errorf("unexpected page URL: %s", r.URL)
			}
			if calls == 1 {
				resp := releaseResponse(200, `[{"tag_name":"v1.9.0"}]`)
				resp.Header.Set("Link", `<https://untrusted.invalid/other>; rel="next"`)
				return resp, nil
			}
			if failSecond {
				return releaseResponse(500, ""), nil
			}
			return releaseResponse(200, `[{"tag_name":"v1.10.0"},{"tag_name":"v2.0.0-beta.1","prerelease":true}]`), nil
		})}
		got := checkLatestRelease(context.Background(), client, buildinfo.Info{Version: "v1.0.0", Kind: "formal"})
		if calls != 2 || (failSecond && (got.Result != "unknown" || got.Reason != "failed")) || (!failSecond && (got.Result != "available" || got.LatestVersion != "v1.10.0")) {
			t.Fatalf("failSecond=%t, calls=%d, result=%+v", failSecond, calls, got)
		}
	}
}

func TestUpdateStartupSharesCheckAndNotice(t *testing.T) {
	c := testConsole(t)
	previousVersion, previousKind, previousRevision := buildinfo.Version, buildinfo.Kind, buildinfo.SourceRevision
	buildinfo.Version, buildinfo.Kind, buildinfo.SourceRevision = "v1.0.0-beta.4", "rebuild", strings.Repeat("a", 40)
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Kind, buildinfo.SourceRevision = previousVersion, previousKind, previousRevision
	})
	oldClient := c.client
	t.Cleanup(func() { c.client = oldClient })
	started, release := make(chan struct{}, 2), make(chan struct{})
	var calls atomic.Int32
	c.client = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return releaseResponse(200, `[{"tag_name":"v1.0.0"}]`), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})}

	c.StartUpdateCheck()
	<-started
	c.StartUpdateCheck()
	request := func(path string, token bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://"+c.address+path, strings.NewReader("{}"))
		if token {
			r.Header.Set("X-SWD-Token", c.token)
		}
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	if got := request("/api/update/notice", false); got.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated notice claim = %d", got.Code)
	}
	c.mu.Lock()
	joined := c.startUpdateCheckLocked(false)
	if joined != c.updateDone {
		c.mu.Unlock()
		t.Fatal("manual check did not join the startup check")
	}
	c.mu.Unlock()
	close(release)
	<-joined
	c.mu.Lock()
	result := c.updateSnapshotLocked()
	c.mu.Unlock()
	if result.Result != "available" || !result.StartupNotice {
		t.Fatalf("startup result = %+v", result)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent checks sent %d requests", got)
	}
	first, second := request("/api/update/notice", true), request("/api/update/notice", true)
	if !strings.Contains(first.Body.String(), "v1.0.0") || strings.Contains(second.Body.String(), "v1.0.0") {
		t.Fatalf("notice claims = %s, %s", first.Body.String(), second.Body.String())
	}
	response := request("/api/update/check", true)
	if response.Code != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("manual retry = %d, calls=%d", response.Code, calls.Load())
	}
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Result != "available" || result.CheckedAt == "" {
		t.Fatalf("manual result = %s", response.Body.String())
	}
	if got := request("/api/update/notice", true); strings.Contains(got.Body.String(), "v1.0.0") {
		t.Fatalf("manual check created a notice: %s", got.Body.String())
	}
}

func TestUpdateWaitsForConsentAndCancelsOnShutdown(t *testing.T) {
	c := testConsole(t)
	oldClient := c.client
	t.Cleanup(func() { c.client = oldClient })
	started, canceled := make(chan struct{}), make(chan struct{})
	c.client = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		return nil, r.Context().Err()
	})}
	c.mu.Lock()
	c.terms = termsReceipt{}
	c.mu.Unlock()
	c.StartUpdateCheck()
	select {
	case <-started:
		t.Fatal("checked before terms acceptance")
	default:
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("check did not start after consent")
	}
	c.BeginShutdown()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel the check")
	}
}

func TestUpdatePublicGitHub(t *testing.T) {
	if os.Getenv("SWD_UPDATE_GITHUB_LIVE") == "" {
		t.Skip("set SWD_UPDATE_GITHUB_LIVE to check the public GitHub endpoint")
	}
	c := &Console{}
	_, client, err := c.networkFor(Settings{UpstreamMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if transport, ok := client.Transport.(interface{ Retire() }); ok {
			transport.Retire()
		}
		client.CloseIdleConnections()
		c.dns.Close()
	}()
	ctx, cancel := context.WithTimeout(t.Context(), updateTimeout)
	defer cancel()
	result := checkLatestRelease(ctx, client, buildinfo.Info{Version: "v0.0.1", Kind: "rebuild"})
	if result.Reason == "failed" {
		t.Fatalf("GitHub request failed through the active direct transport: %+v", result)
	}
	t.Logf("public GitHub result: %s (%s)", result.Result, result.Reason)
}
