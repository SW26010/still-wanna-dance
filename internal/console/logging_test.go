package console

import (
	"bytes"
	"log/slog"
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
