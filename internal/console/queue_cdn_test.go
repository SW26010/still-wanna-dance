package console

import (
	"errors"
	"os"
	"testing"
)

func TestGatewayWithQueueFollowingDisabled(t *testing.T) {
	c := testConsole(t)
	c.settings.QueueWithGateway = false
	if err := os.MkdirAll(c.settings.VRChatLogDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if c.queue.snapshot().Running || c.queueDesired || c.httpServer == nil {
		t.Fatal("disabled queue started")
	}
	c.failCDN(c.httpServer, errors.New("listener failed"))
	if c.queue.snapshot().Running || c.queueDesired || c.httpServer != nil {
		t.Fatal("failed gateway left work running")
	}
}

func TestQueueStartsDuringBatch(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.VRChatLogDir, 0700); err != nil {
		t.Fatal(err)
	}
	c.batch = Batch{Running: true}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if !c.queueDesired || !c.queue.snapshot().Running {
		t.Fatal("batch prevented queue startup")
	}
}

func TestQueueFailureRetainsIntentAndCanRetry(t *testing.T) {
	c := testConsole(t)
	if err := c.start(); err != nil || !c.queueDesired || c.queue.snapshot().Running || c.actionErrors["queue"] == "" {
		t.Fatal("missing failure state")
	}
	if err := os.MkdirAll(c.settings.VRChatLogDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if !c.queue.snapshot().Running {
		t.Fatal("retry failed")
	}
}

func TestQueueWithGatewayDoesNotStartWithoutCDN(t *testing.T) {
	c := testConsole(t)
	if err := os.MkdirAll(c.settings.VRChatLogDir, 0700); err != nil {
		t.Fatal(err)
	}
	c.settings.QueueWithGateway = true
	c.settings.AutoAccelerate = false
	c.editor.initializeSettings(c.settings)

	c.AutoStart()
	if c.queue.snapshot().Running || c.queueDesired || c.httpServer != nil {
		t.Fatal("queue started without its gateway")
	}
}

func TestQueueStartRequiresConsent(t *testing.T) {
	c := testConsole(t)
	c.terms = termsReceipt{}
	if err := c.start(); err == nil || c.queueDesired {
		t.Fatal("queue bypassed consent")
	}
}

func TestQueueFollowsGatewayStopFailureAndActivationRollback(t *testing.T) {
	for _, outcome := range []string{"stop", "listener-failure", "activation-rollback"} {
		t.Run(outcome, func(t *testing.T) {
			c := testConsole(t)
			if err := os.MkdirAll(c.settings.VRChatLogDir, 0700); err != nil {
				t.Fatal(err)
			}
			checks := 0
			inspect := func() HostsStatus {
				checks++
				if outcome == "activation-rollback" && checks == 3 {
					if !c.queue.snapshot().Running {
						t.Fatal("gateway did not start queue")
					}
					return HostsStatus{Message: "fixture final verification failed"}
				}
				return c.editor.hosts.status()
			}
			err := c.enableAcceleration(inspect, c.changeHostsLocked)
			if outcome == "activation-rollback" {
				if err == nil {
					t.Fatal("unexpected success")
				}
			} else {
				if err != nil || !c.queue.snapshot().Running {
					t.Fatal("queue did not follow gateway", err)
				}
				if outcome == "stop" {
					c.stop()
				} else {
					c.failCDN(c.httpServer, errors.New("fixture listener failed"))
				}
			}
			if c.httpServer != nil || c.queue.snapshot().Running || c.queueDesired || len(c.queue.snapshot().Songs) != 0 || queueHasCancelForTest(&c.queue) {
				t.Fatal("gateway left queue sources alive")
			}
			select {
			case <-queueDoneForTest(&c.queue):
			default:
				t.Fatal("queue shutdown was not awaited")
			}
		})
	}
}
