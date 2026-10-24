package console

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBatchLeavesQueueRunning(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	requested := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requested)
		<-r.Context().Done()
	}))
	defer api.Close()
	defer c.Close()
	c.apiBase = api.URL
	c.checksumURL = api.URL
	c.client.Transport = http.DefaultTransport
	if err := c.startQueue(); err != nil {
		t.Fatal(err)
	}
	queueDone := c.queueDone
	if err := c.startBatch(); err != nil {
		t.Fatal(err)
	}
	<-requested
	c.mu.Lock()
	batch, queue := c.batch.Running, c.queue.Running
	c.mu.Unlock()
	if !batch || !queue {
		t.Fatal("batch interrupted queue")
	}
	c.mu.Lock()
	done := c.batchDone
	c.mu.Unlock()
	postTaskAction(t, c, "batch/cancel")
	<-done
	c.mu.Lock()
	batch, queue = c.batch.Running, c.queue.Running
	c.mu.Unlock()
	if batch || !queue || c.queueDone != queueDone {
		t.Fatal("batch cancellation interrupted queue")
	}
}

func TestBatchDoesNotChangeQueueLifecycle(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, outcome := range []string{"cancel", "complete", "failure", "cdn-stop", "queue-stop", "close"} {
			name := outcome + "/queue-off"
			if enabled {
				name = outcome + "/queue-on"
			}
			t.Run(name, func(t *testing.T) {
				c := testConsole(t)
				if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
					t.Fatal(err)
				}
				requested, release := make(chan struct{}), make(chan struct{})
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if outcome == "complete" {
						fmt.Fprint(w, `{"code":200,"data":{"time":"20261001000000","groups":[{"entries":[{"id":1,"checksum":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}}`)
						return
					}
					close(requested)
					select {
					case <-r.Context().Done():
					case <-release:
						w.WriteHeader(http.StatusServiceUnavailable)
					}
				}))
				defer api.Close()
				defer c.Close()
				c.apiBase = api.URL
				c.checksumURL = api.URL
				c.client.Transport = http.DefaultTransport
				if enabled {
					if err := c.startQueue(); err != nil {
						t.Fatal(err)
					}
				}
				queueDone := c.queueDone
				// An already saved resource needs no video download.
				if outcome == "complete" {
					c.checksumURL = api.URL
					if !enabled {
						initializeTestLibrary(t, c)
					}
					os.MkdirAll(filepath.Join(c.settings.StorageDir, "videos"), 0700)
					os.WriteFile(filepath.Join(c.settings.StorageDir, "videos", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.mp4"), []byte("trusted"), 0600)
				}
				if err := c.startBatch(); err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				done := c.batchDone
				c.mu.Unlock()
				if outcome != "complete" {
					select {
					case <-requested:
					case <-time.After(5 * time.Second):
						t.Fatal("batch did not request catalog")
					}
					switch outcome {
					case "failure":
						close(release)
					case "close":
						if err := c.Close(); err != nil {
							t.Fatal(err)
						}
					default:
						if outcome == "queue-stop" {
							postTaskAction(t, c, "queue/stop")
						}
						if outcome == "cdn-stop" {
							postTaskAction(t, c, "stop")
						}
						postTaskAction(t, c, "batch/cancel")
					}
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("batch did not finish")
				}
				c.mu.Lock()
				defer c.mu.Unlock()
				wantRunning := enabled && outcome != "queue-stop" && outcome != "close"
				if c.queue.Running != wantRunning || c.batch.Running {
					t.Fatalf("queue running=%v, want %v; batch running=%v", c.queue.Running, wantRunning, c.batch.Running)
				}
				if wantRunning && c.queueDone != queueDone {
					t.Fatal("batch restarted the queue log reader")
				}
			})
		}
	}
}

func postTaskAction(t *testing.T, c *Console, action string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://"+c.address+"/api/"+action, nil)
	r.Header.Set("X-SWD-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
	}
}
