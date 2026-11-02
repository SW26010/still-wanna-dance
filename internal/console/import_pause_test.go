package console

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImportPauseStatusSurvivesProgress(t *testing.T) {
	c := testConsole(t)
	task := videoImportTask{ID: 42, DatabasePath: c.settings.DatabasePath, VideoDir: c.settings.VideoDir, Enumerated: true, Files: []videoImportItem{{}}}
	c.publishImportTask(task)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.mu.Lock()
	c.importStatus.Running = true
	c.importStatus.Paused = false
	c.importStatus.Phase = "正在校验并导入"
	c.importCancel = cancel
	c.mu.Unlock()

	request := httptest.NewRequest("POST", "http://"+c.address+"/api/import/pause", strings.NewReader(`{"id":42}`))
	request.Header.Set("X-SWD-Token", c.token)
	response := httptest.NewRecorder()
	c.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("pause request: %d %s", response.Code, response.Body)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("pause did not cancel the worker")
	}
	assertPausing := func() {
		t.Helper()
		request := httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil)
		request.Header.Set("X-SWD-Token", c.token)
		response := httptest.NewRecorder()
		c.ServeHTTP(response, request)
		var status struct {
			Import struct {
				Running bool   `json:"running"`
				Pausing bool   `json:"pausing"`
				Paused  bool   `json:"paused"`
				Phase   string `json:"phase"`
			} `json:"import"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || !status.Import.Pausing || !status.Import.Running || status.Import.Paused || status.Import.Phase != "正在暂停" {
			t.Fatalf("pause status lost while worker is running: %d %s", response.Code, response.Body)
		}
	}
	assertPausing()
	c.setImportPhase("正在枚举视频文件")
	assertPausing()
	c.publishImportTask(task)
	assertPausing()
}
