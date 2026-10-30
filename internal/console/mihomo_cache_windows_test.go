package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleStartsWithoutMihomoSelectionCache(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(map[bool]string{true: "saved-consent", false: "first-consent"}[accepted], func(t *testing.T) {
			if !consoleMihomoProcess(t) {
				return
			}
			c := testConsole(t)
			s := runtimeNodeSettings(t, c, 3)
			if err := saveConfigSettings(c.configPath, s); err != nil {
				t.Fatal(err)
			}
			if !accepted {
				if err := os.Remove(c.files().Terms); err != nil {
					t.Fatal(err)
				}
			}
			blockedCache := filepath.Join(c.files().StateDir, "mihomo", "cache.db")
			if err := os.MkdirAll(blockedCache, 0700); err != nil {
				t.Fatal(err)
			}
			c = restartTestConsole(t, c)
			if !accepted {
				if c.client.Transport.(*mihomoTransport).core != nil {
					t.Fatal("core started before consent")
				}
				if err := c.acceptTerms(); err != nil {
					t.Fatal("cache failure blocked startup after consent", err)
				}
			}
			c.mu.Lock()
			status := c.proxyNodesStatusLocked()
			c.mu.Unlock()
			if !c.termsAccepted() || !status.Dashboard || !strings.Contains(status.ActiveError, "选择缓存不可用") || !strings.Contains(status.ActiveError, "节点选择不会保存") {
				t.Fatal("cache failure did not preserve a usable dashboard and warning", status)
			}
			request := func(method, body string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				r := httptest.NewRequest(method, "http://"+c.address+"/api/mihomo/proxies/SWD", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+c.token)
				c.ServeHTTP(w, r)
				return w
			}
			var group struct {
				All []string `json:"all"`
				Now string   `json:"now"`
			}
			w := request("GET", "")
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &group) != nil || len(group.All) != 4 {
				t.Fatal("dashboard unavailable", w.Code, w.Body.String())
			}
			selected := group.All[1]
			body, _ := json.Marshal(map[string]string{"name": selected})
			if w := request("PUT", string(body)); w.Code != http.StatusNoContent {
				t.Fatal("cache failure prevented manual selection", w.Code, w.Body.String())
			}
			w = request("GET", "")
			if json.Unmarshal(w.Body.Bytes(), &group) != nil || group.Now != selected {
				t.Fatal("manual selection was not retained in memory", w.Body.String())
			}
			if evidence := os.Getenv("SWD_CACHE_FAILURE_BROWSER_EVIDENCE"); accepted && evidence != "" {
				ui := httptest.NewUnstartedServer(c)
				c.address = ui.Listener.Addr().String()
				ui.Start()
				defer ui.Close()
				cmd := exec.CommandContext(t.Context(), "node", filepath.Join("..", "..", "webui", "test-support", "dashboard-live.mjs"), ui.URL, evidence, status.ActiveError)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("browser: %v\n%s", err, output)
				} else {
					t.Log(string(output))
				}
			}
		})
	}
}
