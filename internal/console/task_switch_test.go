package console

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
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
