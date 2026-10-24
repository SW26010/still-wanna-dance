package console

import (
	"errors"
	"os"
	"testing"
)

func TestQueueCanRunIndependentlyWhenGatewayFollowDisabled(t *testing.T) {
	c := testConsole(t)
	c.settings.QueueWithGateway = false
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
	next.PrepareAheadSeconds++
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

func TestQueueStartsDuringBatch(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	c.batch = Batch{Running: true}
	postTaskAction(t, c, "queue/start")
	if !c.queueDesired || !c.queue.Running {
		t.Fatal("batch prevented queue startup")
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

func TestQueueWithGatewayDoesNotStartWithoutCDN(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	c.settings.QueueWithGateway = true
	c.settings.AutoAccelerate = false
	c.savedSettings = c.settings
	completeStartupSetup(t, c)
	c.AutoStart()
	if c.queue.Running || c.queueDesired || c.httpServer != nil {
		t.Fatal("queue started without its gateway")
	}
}

func TestQueueStartRequiresConsent(t *testing.T) {
	c := testConsole(t)
	c.terms = termsReceipt{}
	if err := c.startQueue(); err == nil || c.queueDesired {
		t.Fatal("queue bypassed consent")
	}
}

func TestQueueFollowsGatewayStopFailureAndActivationRollback(t *testing.T) {
	for _, outcome := range []string{"stop", "listener-failure", "activation-rollback"} {
		t.Run(outcome, func(t *testing.T) {
			c := testConsole(t)
			if err := os.MkdirAll(c.settings.LogDir, 0700); err != nil {
				t.Fatal(err)
			}
			checks := 0
			inspect := func() HostsStatus {
				checks++
				if outcome == "activation-rollback" && checks == 3 {
					if !c.queue.Running {
						t.Fatal("gateway did not start queue")
					}
					return HostsStatus{Message: "fixture final verification failed"}
				}
				return c.hosts.status()
			}
			err := c.enableAcceleration(inspect, c.changeHostsLocked)
			if outcome == "activation-rollback" {
				if err == nil {
					t.Fatal("unexpected success")
				}
			} else {
				if err != nil || !c.queue.Running {
					t.Fatal("queue did not follow gateway", err)
				}
				if outcome == "stop" {
					c.stop()
				} else {
					c.failCDN(c.httpServer, errors.New("fixture listener failed"))
				}
			}
			if c.httpServer != nil || c.queue.Running || c.queueDesired || len(c.queue.Songs) != 0 || c.queueCancel != nil {
				t.Fatal("gateway left queue sources alive")
			}
			select {
			case <-c.queueDone:
			default:
				t.Fatal("queue shutdown was not awaited")
			}
		})
	}
}
