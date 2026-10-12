package console

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHelpEscapesPathsAndOffersReentry(t *testing.T) {
	c := testConsole(t)
	c.settings.StorageDir = `<script>alert("path")</script>`
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/help", nil))
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, c.settings.StorageDir) || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("unsafe or unavailable help: %d", w.Code)
	}
	for _, want := range []string{`href="/?setup=1#/setup"`, "恢复 hosts", "覆盖升级", "MD5", "强制代理"} {
		if !strings.Contains(body, want) {
			t.Fatalf("help missing %q", want)
		}
	}
}
