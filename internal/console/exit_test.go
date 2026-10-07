package console

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestBeginShutdownCancelsWorkBeforeWaiting(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	batch, cancelBatch := context.WithCancel(context.Background())
	queue, cancelQueue := context.WithCancel(context.Background())
	imports, cancelImport := context.WithCancel(context.Background())
	inventory, cancelInventory := context.WithCancel(context.Background())
	defer cancelBatch()
	defer cancelQueue()
	defer cancelImport()
	defer cancelInventory()
	workDone := make(chan struct{})
	c.batchCancel, c.queueCancel = cancelBatch, cancelQueue
	c.importCancel, c.inventoryCancel = cancelImport, cancelInventory
	c.batchDone, c.queueDone = workDone, workDone
	c.importDone, c.inventoryDone = workDone, workDone
	c.queueDesired = true
	defer c.Close()
	defer close(workDone)
	begun := make(chan struct{})
	go func() {
		c.BeginShutdown()
		c.BeginShutdown()
		close(begun)
	}()
	select {
	case <-begun:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for background work before HTTP could drain")
	}
	for name, ctx := range map[string]context.Context{"batch": batch, "queue": queue, "import": imports, "inventory": inventory} {
		if ctx.Err() == nil {
			t.Errorf("%s was not canceled", name)
		}
	}
	if c.queueDesired {
		t.Fatal("shutdown allowed queue to resume")
	}
	if err := c.startBatch(); err == nil {
		t.Fatal("shutdown accepted new background work")
	}
}

func TestExitAuthenticationAndAcknowledgement(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "config.json"), "127.0.0.1:18081")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, tc := range []struct{ method, host, token, origin string }{
		{"GET", c.address, c.token, ""},
		{"POST", "evil.invalid", c.token, ""},
		{"POST", c.address, "", ""},
		{"POST", c.address, c.token, "https://evil.invalid"},
	} {
		r := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/exit", nil)
		r.Header.Set("X-StepStash-Token", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatalf("invalid exit accepted: %+v", tc)
		}
		select {
		case <-c.ExitRequested():
			t.Fatal("invalid request signaled exit")
		default:
		}
	}
	// Cleanup remains available without terms acceptance and is idempotent.
	for range 2 {
		r := httptest.NewRequest("POST", "http://"+c.address+"/api/exit", nil)
		r.Header.Set("X-StepStash-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 200 || !w.Flushed || w.Body.String() != "{\"ok\":true}\n" {
			t.Fatalf("exit acknowledgement: %d %q flushed=%v", w.Code, w.Body.String(), w.Flushed)
		}
		select {
		case <-c.ExitRequested():
		default:
			t.Fatal("exit not signaled")
		}
	}
}
