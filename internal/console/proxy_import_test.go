package console

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProxyConfigurationSourcesShareOriginalDraft(t *testing.T) {
	c := testConsole(t)
	const content = "# unchanged original\n" + nodeFixture + "\n"
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.URL.Path == "/error" {
			http.Error(w, "private-source-error", 500)
			return
		}
		if r.URL.Path == "/large" {
			io.WriteString(w, strings.Repeat("x", (2<<20)+1))
			return
		}
		io.WriteString(w, content)
	}))
	defer server.Close()
	for _, source := range []string{"config", "base64", "url"} {
		input := content
		if source == "base64" {
			input = base64.StdEncoding.EncodeToString([]byte(content))
		}
		if source == "url" {
			input = server.URL + "/?private-source-token=secret"
		}
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/proxy/import", strings.NewReader(input))
		r.Header.Set("X-SWD-Token", c.token)
		r.Header.Set("X-SWD-Import-Source", source)
		if source == "config" {
			r.Header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "机场配置.yaml"}))
		}
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		var result proxyImportResult
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Nodes != 1 {
			t.Fatalf("%s source rejected: %d %s", source, w.Code, w.Body.String())
		}
		draft := c.editor.proxyImports[result.ID]
		wantName := map[string]string{"config": "机场配置.yaml", "base64": "Base64 配置", "url": "URL 配置"}[source]
		if result.Name != wantName || result.ImportedAt.IsZero() || draft.Info.Name != wantName || !draft.Info.ImportedAt.Equal(result.ImportedAt) {
			t.Fatal("source display metadata was not staged")
		}
		if draft.Snapshot != content || strings.Contains(w.Body.String(), "private") || c.editor.savedSettings.nodeSnapshot != "" {
			t.Fatal("source was stored or original configuration changed")
		}
	}
	if downloads.Load() != 1 {
		t.Fatal("configuration source downloaded more than once")
	}
	for _, path := range []string{"/error", "/large"} {
		if data, err := proxyConfigurationContent(context.Background(), "url", []byte(server.URL+path+"?private-token=secret")); err == nil || len(data) != 0 || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), server.URL) {
			t.Fatal("download failure exposed source or accepted invalid data")
		}
	}
	before := downloads.Load()
	c.terms = termsReceipt{}
	r := httptest.NewRequest("POST", "http://"+c.address+"/api/proxy/import", strings.NewReader(server.URL))
	r.Header.Set("X-SWD-Token", c.token)
	r.Header.Set("X-SWD-Import-Source", "url")
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != http.StatusPreconditionRequired || downloads.Load() != before {
		t.Fatal("download started before consent")
	}
}

func TestProxyConfigurationRejectsSourceErrorsWithoutChangingSavedData(t *testing.T) {
	c := testConsole(t)
	before, _ := os.ReadFile(c.configPath)
	for _, tc := range []struct{ source, input string }{
		{"base64", "invalid@@"}, {"url", "file:///private"}, {"unknown", nodeFixture},
		{"base64", base64.StdEncoding.EncodeToString([]byte("ss://private.invalid"))},
	} {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/proxy/import", strings.NewReader(tc.input))
		r.Header.Set("X-SWD-Token", c.token)
		r.Header.Set("X-SWD-Import-Source", tc.source)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "settings_validation") || len(c.editor.proxyImports) != 0 {
			t.Fatal("invalid source changed draft state", w.Code)
		}
	}
	after, _ := os.ReadFile(c.configPath)
	if string(before) != string(after) {
		t.Fatal("failed import modified saved settings")
	}
}
