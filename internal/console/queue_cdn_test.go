package console

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestQueueFollowsCDNAndUnlocksSettings(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		c := testConsole(t)
		c.settings.QueuePrefetchEnabled = enabled
		if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := c.startQueue(); err == nil {
			t.Fatal("queue started without CDN")
		}
		if err := c.start(); err != nil {
			t.Fatal(err)
		}
		if c.queue.Running != enabled {
			t.Fatalf("queue running=%v, enabled=%v", c.queue.Running, enabled)
		}
		if err := c.stop(); err != nil {
			t.Fatal(err)
		}
		if c.queue.Running {
			t.Fatal("queue survived CDN stop")
		}
		if err := c.save(c.settings); err != nil {
			t.Fatal(err)
		}
		reloaded, err := New(c.configPath, c.address)
		if err != nil {
			t.Fatal(err)
		}
		defer reloaded.Close()
		if reloaded.settings.QueuePrefetchEnabled != enabled {
			t.Fatal("preference not persisted")
		}
	}
}

func TestQueuePreferenceDefaultsForOldConfig(t *testing.T) {
	c := testConsole(t)
	if !c.settings.QueuePrefetchEnabled {
		t.Fatal("new configuration disabled queue")
	}
	if err := os.WriteFile(c.configPath, []byte(`{"storageDir":"data"}`), 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if !reloaded.settings.QueuePrefetchEnabled {
		t.Fatal("old configuration disabled queue")
	}
}

func TestQueueFailureDoesNotFailCDN(t *testing.T) {
	c := testConsole(t)
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if c.httpServer == nil || c.queue.Running || c.actionErrors["queue"] == "" {
		t.Fatal("missing logs must only report queue error")
	}
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
}

func TestCDNFailureStopsQueue(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	c.failCDN(c.httpServer, errors.New("listener failed"))
	if c.queue.Running || c.httpServer != nil {
		t.Fatal("failed CDN left queue running")
	}
	if err := c.save(c.settings); err != nil {
		t.Fatal(err)
	}
}

func TestDisablingQueueClearsPreviousStartError(t *testing.T) {
	c := testConsole(t)
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if c.actionErrors["queue"] == "" {
		t.Fatal("missing log directory did not report a queue start error")
	}
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	c.recordActionError("/api/batch/start", errors.New("unrelated batch error"))
	settings := c.settings
	settings.QueuePrefetchEnabled = false
	if err := c.save(settings); err != nil {
		t.Fatal(err)
	}
	if c.actionErrors["queue"] == "" {
		t.Fatal("save erased active queue error")
	}
	if c.actionErrors["batch"] == "" {
		t.Fatal("disabling queue cleared an unrelated error")
	}
	c = restartTestConsole(t, c)
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if c.queue.Running || c.actionErrors["queue"] != "" {
		t.Fatal("disabled queue restarted or displayed its previous error")
	}
}

func TestCDNStartedDuringBatchRestoresQueue(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Model a batch that owns the background download slot before CDN starts.
	c.batch = Batch{Running: true}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if c.queue.Running {
		t.Fatal("queue competed with batch")
	}
	c.lifecycleMu.Lock()
	c.mu.Lock()
	c.batch.Running = false
	c.mu.Unlock()
	c.resumeQueueLocked()
	c.lifecycleMu.Unlock()
	if !c.queue.Running {
		t.Fatal("queue did not resume")
	}
}

func TestQueueHasNoIndependentAPI(t *testing.T) {
	c := testConsole(t)
	for _, action := range []string{"queue/start", "queue/stop", "queue/switch"} {
		r := httptest.NewRequest(http.MethodPost, "http://"+c.address+"/api/"+action, nil)
		r.Header.Set("X-StepStash-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s returned %d", action, w.Code)
		}
	}
}
