package console

import (
	"errors"
	"os"
	"testing"
)

func TestQueueAndCDNAreIndependent(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	postTaskAction(t, c, "queue/start")
	engine := c.service
	if !c.queue.Running || c.httpServer != nil {
		t.Fatal("queue requires CDN")
	}
	postTaskAction(t, c, "queue/start")
	postTaskAction(t, c, "start")
	postTaskAction(t, c, "stop")
	if !c.queue.Running || c.service != engine {
		t.Fatal("CDN stop affected queue")
	}
	postTaskAction(t, c, "start")
	postTaskAction(t, c, "queue/stop")
	postTaskAction(t, c, "queue/stop")
	if c.queue.Running || c.queueDesired || c.httpServer == nil {
		t.Fatal("queue stop affected CDN")
	}
	postTaskAction(t, c, "queue/start")
	c.failCDN(c.httpServer, errors.New("listener failed"))
	if !c.queue.Running || c.httpServer != nil {
		t.Fatal("CDN failure stopped queue")
	}
	next := c.settings
	next.QueuePrefetchCount++
	if err := c.save(next); err != nil {
		t.Fatal(err)
	}
	if c.service != engine || !c.queue.Running {
		t.Fatal("save invalidated standalone queue")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.queue.Running || c.queueDesired {
		t.Fatal("queue survived application close")
	}
}

func TestQueueStartupMigration(t *testing.T) {
	for _, tc := range []struct {
		config string
		want   bool
	}{
		{`{}`, false},
		{`{"queuePrefetchEnabled":true}`, false},
		{`{"autoStartCDN":true}`, true},
		{`{"autoStartCDN":true,"queuePrefetchEnabled":false}`, false},
		{`{"autoStartCDN":true,"queuePrefetchEnabled":true}`, true},
		{`{"autoStartCDN":true,"queuePrefetchEnabled":true,"autoStartQueue":false}`, false},
		{`{"autoStartCDN":false,"autoStartQueue":true}`, true},
	} {
		t.Run(tc.config, func(t *testing.T) {
			c := testConsole(t)
			if err := os.WriteFile(c.configPath, []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			c = restartTestConsole(t, c)
			if c.settings.AutoStartQueue != tc.want {
				t.Fatal("incorrect migration", c.settings)
			}
		})
	}
}

func TestQueueStartDuringBatchRecordsIntent(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	c.batch = Batch{Running: true}
	postTaskAction(t, c, "queue/start")
	if !c.queueDesired || c.queue.Running {
		t.Fatal("queue competed with batch")
	}
	c.lifecycleMu.Lock()
	c.mu.Lock()
	c.batch.Running = false
	c.mu.Unlock()
	c.resumeQueueLocked()
	c.lifecycleMu.Unlock()
	if !c.queue.Running {
		t.Fatal("queue did not resume without CDN")
	}
}

func TestQueueFailureRetainsIntentAndCanRetry(t *testing.T) {
	c := testConsole(t)
	if err := c.startQueue(); err == nil || !c.queueDesired || c.queue.Running {
		t.Fatal("missing failure state")
	}
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	postTaskAction(t, c, "queue/start")
	if !c.queue.Running {
		t.Fatal("retry failed")
	}
}

func TestAutoStartQueueDoesNotRequireCDN(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	c.settings.AutoStartQueue = true
	c.AutoStart()
	if !c.queue.Running || !c.queueDesired || c.httpServer != nil {
		t.Fatal("queue startup depends on CDN")
	}
}

func TestQueueStartRequiresConsent(t *testing.T) {
	c := testConsole(t)
	c.terms = termsReceipt{}
	if err := c.startQueue(); err == nil || c.queueDesired {
		t.Fatal("queue bypassed consent")
	}
}
