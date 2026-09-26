package console

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestSwitchStopsOldTaskBeforeStartingNewTask(t *testing.T) {
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
	c.client.Transport = http.DefaultTransport
	if err := c.startQueue(); err != nil {
		t.Fatal(err)
	}
	if err := c.switchTask(true); err != nil {
		t.Fatal(err)
	}
	<-requested
	c.mu.Lock()
	batch, queue := c.batch.Running, c.queue.Running
	c.mu.Unlock()
	if !batch || queue {
		t.Fatal("did not switch to batch")
	}
	if err := c.switchTask(false); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	batch, queue = c.batch.Running, c.queue.Running
	c.mu.Unlock()
	if batch || !queue {
		t.Fatal("did not switch back to queue")
	}
}

func TestBatchRestoresOnlyTemporarilyStoppedQueue(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, outcome := range []string{"cancel", "complete", "failure", "queue-stop", "close", "resume-failure"} {
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
				c.client.Transport = http.DefaultTransport
				if enabled {
					if err := c.startQueue(); err != nil {
						t.Fatal(err)
					}
				}
				// An empty reusable plan finishes successfully without network work.
				if outcome == "complete" {
					c.scanPlan = &scanPlan{settings: c.settings}
				}
				if err := c.switchTask(true); err != nil {
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
						if outcome == "resume-failure" {
							if err := os.Remove(c.settings.LogDir); err != nil {
								t.Fatal(err)
							}
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
				wantRunning := enabled && outcome != "queue-stop" && outcome != "close" && outcome != "resume-failure"
				if c.queue.Running != wantRunning || c.batch.Running || c.batchResumeQueue {
					t.Fatalf("queue running=%v, want %v; batch running=%v, resume pending=%v", c.queue.Running, wantRunning, c.batch.Running, c.batchResumeQueue)
				}
				if enabled && outcome == "resume-failure" && c.actionErrors["queue"] == "" {
					t.Fatal("queue resume failure was not exposed to the user")
				}
			})
		}
	}
}

func postTaskAction(t *testing.T, c *Console, action string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://"+c.address+"/api/"+action, nil)
	r.Header.Set("X-StepStash-Token", c.token)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
	}
}
