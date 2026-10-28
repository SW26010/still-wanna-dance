package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/desktop"
)

func pathPickerRequest(c *Console, purpose, path, token, origin string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"purpose": purpose, "path": path})
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/path/select", strings.NewReader(string(body)))
	r.Header.Set("X-SWD-Token", token)
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	return w
}

func TestPathPickerAuthorizationAndReadOnlySelection(t *testing.T) {
	c := testConsole(t)
	before, _ := os.ReadFile(c.configPath)
	selected := filepath.Join(t.TempDir(), "新 资料.sqlite")
	calls := 0
	for _, tc := range []struct{ purpose, mode string }{
		{"database", "database"}, {"vrcx-database", "open"},
		{"video-directory", "folder"}, {"import-directory", "folder"}, {"log-directory", "folder"},
	} {
		t.Run(tc.purpose, func(t *testing.T) {
			c.pathPicker = func(_ context.Context, dialog desktop.PathDialog) (string, error) {
				calls++
				if dialog.Title == "" || dialog.Initial != selected || dialog.Mode != tc.mode {
					t.Fatal("invalid dialog input", dialog)
				}
				return selected, nil
			}
			previous := calls
			for _, auth := range []struct{ token, origin string }{{"", ""}, {c.token, "http://untrusted.invalid"}} {
				if w := pathPickerRequest(c, tc.purpose, selected, auth.token, auth.origin); w.Code != 403 {
					t.Fatalf("unauthorized picker: %d", w.Code)
				}
			}
			if calls != previous {
				t.Fatal("unauthorized request opened a dialog")
			}
			if w := pathPickerRequest(c, tc.purpose, selected, c.token, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"canceled":false`) {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	after, _ := os.ReadFile(c.configPath)
	if string(before) != string(after) || c.service != nil || c.importStatus.Running || c.vrcxPreview != nil {
		t.Fatal("selecting changed configuration or started work")
	}
	if _, err := os.Stat(selected); !os.IsNotExist(err) {
		t.Fatal("selection created a file", err)
	}
	c.terms = termsReceipt{}
	if w := pathPickerRequest(c, "log-directory", selected, c.token, ""); w.Code != 428 || calls != 5 {
		t.Fatal("picker bypassed consent", w.Code, calls)
	}
}

func TestPathPickerCancelBoundaryAndRetry(t *testing.T) {
	c := testConsole(t)
	c.pathPicker = func(context.Context, desktop.PathDialog) (string, error) { return "", nil }
	if w := pathPickerRequest(c, "database", "", c.token, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"canceled":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	c.pathPicker = func(context.Context, desktop.PathDialog) (string, error) {
		t.Fatal("invalid request opened dialog")
		return "", nil
	}
	for _, input := range []struct{ purpose, path string }{{"unknown", ""}, {"database", "bad\x00path"}, {"video-directory", strings.Repeat("x", 4097)}} {
		if w := pathPickerRequest(c, input.purpose, input.path, c.token, ""); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	for _, result := range []string{"relative.sqlite3", "bad\x00path"} {
		c.pathPicker = func(context.Context, desktop.PathDialog) (string, error) { return result, nil }
		if _, err := c.selectPath(context.Background(), "database", ""); err == nil {
			t.Fatal("accepted invalid path")
		}
	}
	c.pathPicker = func(context.Context, desktop.PathDialog) (string, error) { return "", errors.New("dialog unavailable") }
	if _, err := c.selectPath(context.Background(), "database", ""); err == nil {
		t.Fatal("lost picker error")
	}
	dir := t.TempDir()
	c.pathPicker = func(_ context.Context, dialog desktop.PathDialog) (string, error) {
		if dialog.Initial != filepath.Join(filepath.Dir(c.configPath), "videos") {
			t.Fatal(dialog.Initial)
		}
		return dir, nil
	}
	if path, err := c.selectPath(context.Background(), "video-directory", " videos "); err != nil || path != dir {
		t.Fatal(path, err)
	}
}

func TestPathPickerSingleDialogAndCancellation(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		c := testConsole(t)
		started, done := make(chan struct{}), make(chan error, 1)
		c.pathPicker = func(ctx context.Context, _ desktop.PathDialog) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() { _, err := c.selectPath(ctx, "database", ""); done <- err }()
		<-started
		if _, err := c.selectPath(context.Background(), "import-directory", ""); err == nil {
			t.Fatal("opened two dialogs across purposes")
		}
		if shutdown {
			c.BeginShutdown()
		} else {
			cancel()
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		cancel()
		if !shutdown {
			c.pathPicker = func(context.Context, desktop.PathDialog) (string, error) { return "", nil }
			if _, err := c.selectPath(context.Background(), "log-directory", ""); err != nil {
				t.Fatal("cancellation retained dialog lock", err)
			}
		}
	}
}
