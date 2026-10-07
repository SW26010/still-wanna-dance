package console

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActionLoggingDoesNotLogPollingOrToken(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	c := testConsole(t)
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
		c.ServeHTTP(httptest.NewRecorder(), r)
	}
	if output.Len() != 0 {
		t.Fatal("polling logged", output.String())
	}
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/settings", strings.NewReader(`{"storageDir":""}`))
	r.Header.Set("X-StepStash-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(output.String(), "console_action_failed") {
		t.Fatal(w.Code, output.String())
	}
	if strings.Contains(output.String(), c.token) {
		t.Fatal("token logged")
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "cdn_started") || !strings.Contains(output.String(), "cdn_stopped") {
		t.Fatal(output.String())
	}
}

func TestResolveLogsMalformedLocationIsRedacted(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	c := testConsole(t)
	defer c.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://nya.xin.moe/%zz?token=secret")
		w.WriteHeader(http.StatusFound)
	}))
	defer api.Close()
	c.apiBase, c.client.Transport = api.URL, http.DefaultTransport
	// Resolution fails before an engine is needed, including the Auto fallback.
	if _, err := c.prefetchSong(context.Background(), nil, 1343); err == nil {
		t.Fatal("expected resolution failure")
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "token") {
		t.Fatalf("Location leaked: %s", output.String())
	}
	if !strings.Contains(output.String(), "prefetch_upstream_failed") {
		t.Fatal("missing resolution failure")
	}
}
