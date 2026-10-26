package console

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHelpEscapesPathsAndOffersReentry(t *testing.T) {
	c := testConsole(t)
	c.terms = termsReceipt{} // Help replaces the README, including before first consent.
	setTestLibraryPaths(&c.settings, `<script>alert("path")</script>`)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/help", nil))
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, testStorageRoot(c.settings)) || !strings.Contains(body, `\u003cscript\u003e`) {
		t.Fatalf("unsafe or unavailable help: %d", w.Code)
	}
	for _, want := range []string{`"page":"help"`, `id="swd-bootstrap"`, `/assets/webui/app.js`, `/assets/webui/app.css`} {
		if !strings.Contains(body, want) {
			t.Fatalf("help missing %q", want)
		}
	}
	w = httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	if w.Code != 428 || c.termsAccepted() {
		t.Fatal("reading help must not unlock business APIs or accept terms")
	}
}
