package console

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBatchResultsBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_BATCH_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_BATCH_BROWSER_EVIDENCE for real Console batch result integration")
	}
	c := testConsole(t)
	result := Batch{ID: "large-result", Total: 10575, Checked: 10575, Failed: 10575, Finished: time.Now(), Phase: "检查完成，部分歌曲失败；再次检查可重试"}
	for i := 0; i < result.Failed; i++ {
		result.Failures = append(result.Failures, Failure{ID: int64(i + 1), Name: fmt.Sprintf("失败歌曲 %d", i+1), Error: "上游不可用"})
	}
	if err := c.writeSnapshot("batch-last-attempt", libraryPaths(c.settings), result); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if len(c.batch.Failures) != 10575 {
		t.Fatal("restart truncated full result")
	}
	ui := httptest.NewUnstartedServer(c)
	c.address = ui.Listener.Addr().String()
	ui.Start()
	defer ui.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "batch-results-live.mjs"), ui.URL, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("batch browser: %v\n%s", err, output)
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("viewing batch results started media services")
	}
}

func TestBatchStatusOmitsFailureDetails(t *testing.T) {
	c := testConsole(t)
	c.batch = Batch{Failed: 10575}
	for i := 0; i < c.batch.Failed; i++ {
		c.batch.Failures = append(c.batch.Failures, Failure{ID: int64(i + 1), Name: "failed song", Error: "upstream unavailable"})
	}
	// Both current and saved results must pass through the summary boundary.
	c.lastBatch = c.batch
	r := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	var response struct {
		Batch, LastBatch struct {
			Failed   int
			Failures []Failure
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || response.Batch.Failed != 10575 || len(response.Batch.Failures) != 0 || len(response.LastBatch.Failures) != 0 {
		t.Fatalf("status=%d bytes=%d failure rows=%d/%d", w.Code, w.Body.Len(), len(response.Batch.Failures), len(response.LastBatch.Failures))
	}
	if len(c.batch.Failures) != 10575 {
		t.Fatal("summary discarded the stored result")
	}
	t.Logf("status with 10,575 failures: %d bytes", w.Body.Len())
}

func TestBatchFailurePagesPreserveCompleteSnapshot(t *testing.T) {
	c := testConsole(t)
	// No ID models a snapshot written before paging was introduced.
	want := Batch{Failed: 125, Finished: time.Now()}
	for i := 0; i < want.Failed; i++ {
		want.Failures = append(want.Failures, Failure{ID: int64(i + 1), Name: fmt.Sprintf("song %d", i+1), Error: "unavailable"})
	}
	if err := c.writeSnapshot("batch-last-attempt", libraryPaths(c.settings), want); err != nil {
		t.Fatal(err)
	}
	c = restartTestConsole(t, c)
	if c.batch.ID == "" || c.batch.Failed != want.Failed || !reflect.DeepEqual(c.batch.Failures, want.Failures) {
		t.Fatal("restart lost the full failure record")
	}
	var all []Failure
	for offset := 0; offset < want.Failed; offset += batchFailurePageSize {
		r := httptest.NewRequest("GET", fmt.Sprintf("http://%s/api/batch/failures?id=%s&offset=%d", c.address, c.batch.ID, offset), nil)
		r.Header.Set("X-SWD-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		var page struct {
			Offset, Total int
			Failures      []Failure
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || page.Offset != offset || page.Total != want.Failed || len(page.Failures) != min(batchFailurePageSize, want.Failed-offset) {
			t.Fatalf("page %d: status=%d body=%s", offset, w.Code, w.Body.String())
		}
		all = append(all, page.Failures...)
	}
	if !reflect.DeepEqual(all, want.Failures) {
		t.Fatal("paging dropped, duplicated or reordered failures")
	}
	for _, tc := range []struct {
		query, token, origin string
		status               int
	}{
		{"?id=" + c.batch.ID + "&offset=9223372036854775807", c.token, "", 200},
		{"?id=" + c.batch.ID + "&offset=-1", c.token, "", 400},
		{"?id=" + c.batch.ID + "&offset=bad", c.token, "", 400},
		{"", c.token, "", 400},
		{"?id=previous-run", c.token, "", 409},
		{"?id=" + c.batch.ID, "", "", 403},
		{"?id=" + c.batch.ID, c.token, "http://other.invalid", 403},
	} {
		r := httptest.NewRequest("GET", "http://"+c.address+"/api/batch/failures"+tc.query, nil)
		r.Header.Set("X-SWD-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.query, w.Code, tc.status)
		}
	}
	if c.service != nil || c.batch.Running {
		t.Fatal("reading the result started a download")
	}
	// A fresh scan retains the existing retry entry point and replaces the result.
	id := c.batch.ID
	if err := c.startBatchMode(true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.batchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("scan stalled")
	}
	if c.batch.ID == id || len(c.batch.Failures) != 0 {
		t.Fatal("new run retained previous failures")
	}
}
