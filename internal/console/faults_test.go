package console

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/faults"
)

func TestAutomaticAndTrayFailuresShareIncident(t *testing.T) {
	c := testConsole(t)
	c.settings.AutoOpenWebUI = false
	c.settings.AutoAccelerate = true
	c.settings.QueueWithGateway = false
	center := faults.New()
	c.SetFaultCenter(center)
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c.httpsAddress = occupied.Addr().String()
	c.AutoStart()
	select {
	case <-center.Wake():
		if open, err := center.Take(); !open || err == nil {
			t.Fatal("automatic failure did not request the WebUI", open, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("automatic failure was not reported")
	}
	if err := c.DesktopCommand(desktop.StartAcceleration); err == nil {
		t.Fatal("occupied port unexpectedly accepted")
	}
	select {
	case <-center.Wake():
		t.Fatal("same unresolved fault opened another tab")
	default:
	}
	occupied.Close()
	c.httpsAddress = "127.0.0.1:0"
	if err := c.DesktopCommand(desktop.StartAcceleration); err != nil {
		t.Fatal("manual recovery failed", err)
	}
	if err := c.DesktopCommand(desktop.StopAcceleration); err != nil {
		t.Fatal(err)
	}
	again, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	c.httpsAddress = again.Addr().String()
	if err := c.DesktopCommand(desktop.StartAcceleration); err == nil {
		t.Fatal("second occupied port unexpectedly accepted")
	}
	select {
	case <-center.Wake():
		if open, err := center.Take(); !open || err == nil {
			t.Fatal("a new incident after recovery was not reported", open, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new incident was not reported")
	}
}

func TestManagementFailuresUseSharedFaultBoundary(t *testing.T) {
	c := testConsole(t)
	center := faults.New()
	c.SetFaultCenter(center)
	get := func(path string) int {
		r := httptest.NewRequest("GET", "http://"+c.address+path, nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w.Code
	}
	if code := get("/api/library?offset=-1"); code != 400 {
		t.Fatal("expected invalid query", code)
	}
	if open, err := center.Take(); open {
		t.Fatal("invalid query requested the WebUI", err)
	}
	// A real read failure still reports, independently of its HTTP status.
	database := c.settings.DatabasePath
	c.settings.DatabasePath = filepath.Join(t.TempDir(), "missing.sqlite")
	if code := get("/api/library"); code != 400 {
		t.Fatal("expected database read failure", code)
	}
	open, cause := center.Take()
	if !open || cause == nil {
		t.Fatal("database failure did not reach the shared center")
	}
	if code := get("/api/library?offset=invalid"); code != 400 {
		t.Fatal(code)
	}
	center.Restore(cause)
	if open, _ := center.Take(); !open {
		t.Fatal("invalid query cleared an existing database fault")
	}
	c.settings.DatabasePath = database
	if code := get("/api/library?view=songs"); code != 200 {
		t.Fatal("valid query did not recover", code)
	}
	c.settings.DatabasePath = filepath.Join(t.TempDir(), "missing-again.sqlite")
	if code := get("/api/library"); code != 400 {
		t.Fatal(code)
	}
	if open, _ := center.Take(); !open {
		t.Fatal("new management incident was not delivered")
	}
}

func TestManagementRetainsStorageFailureAcrossSources(t *testing.T) {
	c := testConsole(t)
	center := faults.New()
	c.SetFaultCenter(center)
	watchCtx, cancelWatch := context.WithCancel(t.Context())
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); c.WatchFaults(watchCtx) }()
	defer func() { cancelWatch(); <-watchDone }()

	db, err := sql.Open("sqlite", c.settings.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TRIGGER fail_storage BEFORE INSERT ON songs BEGIN SELECT RAISE(FAIL, 'fixture storage failure'); END"); err != nil {
		t.Fatal(err)
	}
	err = c.store.SyncCatalog(t.Context(), cacheproxy.Catalog{Source: "test", Revision: "20261031000000", Songs: []cacheproxy.CatalogSong{{ID: 42}}})
	if !errors.Is(err, cacheproxy.ErrLocalStorage) {
		t.Fatal("database failure was not retained", err)
	}
	retained := c.store.Error()
	select {
	case <-center.Wake():
		if open, err := center.Take(); !open || !errors.Is(err, retained) {
			t.Fatal("storage watcher did not publish the original failure", open, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("storage watcher did not publish the failure")
	}
	paths := libraryPaths(c.settings)
	source := vrcxFixture(t)
	vrcxBody := fmt.Sprintf(`{"path":%q}`, source)
	const vrcxMessage = "无法读取目标歌曲统计，请检查存储后重试；未导入"
	requests := []struct {
		path, body, message string
		status              int
	}{
		{"/api/songs", fmt.Sprintf(`{"storageID":%q,"id":"42","policy":"favorite"}`, cacheproxy.DatabaseID(paths.DatabasePath)), retained.Error(), http.StatusBadRequest},
		{"/api/cache/delete", fmt.Sprintf(`{"storageID":%q,"entries":[]}`, cacheproxy.CacheStorageID(paths)), retained.Error(), http.StatusBadRequest},
		{"/api/library/preview", fmt.Sprintf(`{"storageID":%q}`, cacheproxy.CacheStorageID(paths)), retained.Error(), http.StatusBadRequest},
		{"/api/vrcx/preview", vrcxBody, vrcxMessage, http.StatusConflict},
	}
	call := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://"+c.address+path, strings.NewReader(body))
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	for _, request := range requests {
		w := call(request.path, request.body)
		message := strings.TrimSpace(w.Body.String())
		if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			var result struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatalf("%s returned invalid JSON: %v", request.path, err)
			}
			message = result.Error
		}
		if w.Code != request.status || message != request.message {
			t.Fatalf("%s changed the existing response: %d %s", request.path, w.Code, w.Body.String())
		}
		select {
		case <-center.Wake():
			if open, err := center.Take(); open {
				t.Errorf("%s published the same storage failure again: %v", request.path, err)
			}
		default:
		}
	}
	c.pathPicker = func(context.Context, desktop.PathDialog) (string, error) {
		return "", errors.New("fixture dialog failure")
	}
	if w := call("/api/path/select", `{"purpose":"database"}`); w.Code != http.StatusBadRequest {
		t.Fatal("unrelated operation did not fail", w.Code)
	}
	select {
	case <-center.Wake():
		if open, err := center.Take(); !open || err == nil || errors.Is(err, retained) {
			t.Fatal("an unrelated fault was incorrectly merged", open, err)
		}
	default:
		t.Fatal("unrelated fault was not published")
	}
	// A fresh observer sees the same public message, with the retained cause
	// still discoverable even though the response deliberately hides it.
	center = faults.New()
	c.SetFaultCenter(center)
	if w := call("/api/vrcx/preview", vrcxBody); w.Code != http.StatusConflict || strings.TrimSpace(w.Body.String()) != vrcxMessage {
		t.Fatal("sanitized response changed", w.Code, w.Body.String())
	}
	select {
	case <-center.Wake():
		if open, err := center.Take(); !open || err == nil || err.Error() != vrcxMessage || !errors.Is(err, retained) {
			t.Fatal("sanitized fault lost its message or original cause", open, err)
		}
	default:
		t.Fatal("sanitized failure was not published")
	}
	// The management response may win the race against the storage watcher.
	c.reportFault("storage", retained)
	select {
	case <-center.Wake():
		t.Fatal("the storage watcher duplicated an already published response fault")
	default:
	}
}

func TestManagementPreservesCancellationCause(t *testing.T) {
	c := testConsole(t)
	c.pathPicker = func(context.Context, desktop.PathDialog) (string, error) { return "", context.Canceled }
	center := faults.New()
	c.SetFaultCenter(center)
	r := httptest.NewRequest(http.MethodPost, "http://"+c.address+"/api/path/select", strings.NewReader(`{"purpose":"database"}`))
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != context.Canceled.Error() {
		t.Fatal("path selection response changed", w.Code, w.Body.String())
	}
	select {
	case <-center.Wake():
		t.Fatal("cancellation was published as a new fault")
	default:
	}
}

func TestFaultCenterIgnoresCancellationAndFindsStartupFault(t *testing.T) {
	c := testConsole(t)
	c.settings.AutoOpenWebUI = false
	c.workOpenError = errors.New("cache unavailable")
	center := faults.New()
	c.SetFaultCenter(center)
	<-center.Wake()
	if open, err := center.Take(); !open || !errors.Is(err, c.workOpenError) {
		t.Fatal("startup fault was not delivered", open, err)
	}
	c.reportFault("test:cancel", context.Canceled)
	c.reportFault("test:uac", errHostsAuthorizationCanceled)
	select {
	case <-center.Wake():
		t.Fatal("cancellation was treated as a fault")
	default:
	}
	rollback := errors.New("rollback failed")
	c.reportFault("test:rollback", errors.Join(errHostsAuthorizationCanceled, rollback))
	<-center.Wake()
	if open, err := center.Take(); !open || !errors.Is(err, rollback) {
		t.Fatal("a failure accompanying cancellation was lost", open, err)
	}
}
