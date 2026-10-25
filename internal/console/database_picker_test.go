package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDatabasePickerAuthorizationAndReadOnlySelection(t *testing.T) {
	c := testConsole(t)
	before, _ := os.ReadFile(c.configPath)
	dir := t.TempDir()
	calls := 0
	c.databasePicker = func(_ context.Context, title, initial string) (string, error) {
		calls++
		if title == "" || initial != dir {
			t.Fatal("invalid dialog input")
		}
		return dir, nil
	}
	body, _ := json.Marshal(map[string]string{"path": dir})
	request := func(token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/vrcx/select", strings.NewReader(string(body)))
		r.Header.Set("X-SWD-Token", token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct{ token, origin string }{{"", ""}, {c.token, "http://untrusted.invalid"}} {
		if w := request(tc.token, tc.origin); w.Code != 403 {
			t.Fatalf("unauthorized picker: %d", w.Code)
		}
	}
	if calls != 0 {
		t.Fatal("unauthorized request opened a dialog")
	}
	if w := request(c.token, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"canceled":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(c.configPath)
	if string(before) != string(after) || c.service != nil || c.importStatus.Running {
		t.Fatal("selecting changed configuration or started work")
	}
	c.mu.Lock()
	c.terms = termsReceipt{}
	c.mu.Unlock()
	if w := request(c.token, ""); w.Code != 428 || calls != 1 {
		t.Fatal("picker bypassed consent", w.Code, calls)
	}
}

func TestDatabasePickerCancelBoundaryAndRetry(t *testing.T) {
	c := testConsole(t)
	c.databasePicker = func(context.Context, string, string) (string, error) { return "", nil }
	if path, err := c.selectVRCXDatabase(context.Background(), ""); err != nil || path != "" {
		t.Fatal(path, err)
	}
	c.databasePicker = func(context.Context, string, string) (string, error) {
		return "relative.sqlite3", nil
	}
	if _, err := c.selectVRCXDatabase(context.Background(), ""); err == nil {
		t.Fatal("accepted relative database path")
	}
	c.databasePicker = func(context.Context, string, string) (string, error) { return "", errors.New("dialog unavailable") }
	if _, err := c.selectVRCXDatabase(context.Background(), ""); err == nil {
		t.Fatal("lost picker error")
	}
	dir := t.TempDir()
	c.databasePicker = func(context.Context, string, string) (string, error) { return dir, nil }
	if path, err := c.selectVRCXDatabase(context.Background(), ""); err != nil || path != dir {
		t.Fatal(path, err)
	}
}

func TestDatabasePickerSingleDialogAndShutdownCancellation(t *testing.T) {
	c := testConsole(t)
	started, done := make(chan struct{}), make(chan error, 1)
	c.databasePicker = func(ctx context.Context, _, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	go func() { _, err := c.selectVRCXDatabase(context.Background(), ""); done <- err }()
	<-started
	if _, err := c.selectVRCXDatabase(context.Background(), ""); err == nil {
		t.Fatal("opened two dialogs")
	}
	c.BeginShutdown()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
