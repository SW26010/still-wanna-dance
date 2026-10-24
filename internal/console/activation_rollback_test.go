package console

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"
)

func TestAccelerationRollbackRetainsHelperForRetry(t *testing.T) {
	for _, initial := range []string{hostsBaseline, hostsBaseline + "\r\n127.0.0.1 nya.xin.moe " + marker + "\r\n"} {
		c := testConsole(t)
		h, launches := fixtureHostsController(t, initial)
		c.hosts = h
		occupied, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()
		c.httpsAddress = occupied.Addr().String()
		for range 2 {
			if err := c.enableAcceleration(h.status, c.changeHostsLocked); err == nil {
				t.Fatal("occupied gateway reported success")
			}
			if readHostsFixture(t, h.path) != initial || c.httpServer != nil || c.activation.Phase != "failed" || *launches != 1 {
				t.Fatal("failed activation did not restore its original state or reuse the helper")
			}
		}
		occupied.Close()
		if err := c.enableAcceleration(h.status, c.changeHostsLocked); err != nil || !h.status().Ready || c.httpServer == nil || *launches != 1 {
			t.Fatal("retry failed to reuse authorized helper", err)
		}
	}
}

func TestAccelerationFailurePreservesExistingGatewayAndConnection(t *testing.T) {
	for _, running := range []bool{false, true} {
		for _, ready := range []bool{false, true} {
			for _, failAt := range []int{2, 3} {
				c := testConsole(t)
				initial := hostsBaseline
				if ready {
					initial, _ = transformHosts(initial, "enable")
				}
				h, _ := fixtureHostsController(t, initial)
				c.hosts = h
				if running {
					if err := c.start(); err != nil {
						t.Fatal(err)
					}
				}
				server := c.httpServer
				checks := 0
				inspect := func() HostsStatus {
					checks++
					if checks >= failAt {
						return HostsStatus{Message: "fixture failed verification"}
					}
					return h.status()
				}
				if err := c.enableAcceleration(inspect, c.changeHostsLocked); err == nil || c.httpServer != server || readHostsFixture(t, h.path) != initial {
					t.Fatal("failure changed preexisting state", running, ready, failAt, err)
				}
			}
		}
	}
}

func TestAccelerationReportsRollbackFailureAndKeepsConcurrentHostsEdit(t *testing.T) {
	c := testConsole(t)
	h, launches := fixtureHostsController(t, hostsBaseline)
	c.hosts = h
	var edited string
	change := func(action string) error {
		if err := c.changeHostsLocked(action); err != nil {
			return err
		}
		if action == "enable" {
			edited = readHostsFixture(t, h.path) + "# external edit\r\n"
			if err := os.WriteFile(h.path, []byte(edited), 0600); err != nil {
				t.Fatal(err)
			}
			return errors.New("fixture post-write failure")
		}
		return nil
	}
	err := c.enableAcceleration(h.status, change)
	if err == nil || !strings.Contains(err.Error(), "未能撤销") || !strings.Contains(c.activation.Error, "未能撤销") || c.activation.Phase != "failed" || c.httpServer != nil || readHostsFixture(t, h.path) != edited || *launches != 1 {
		t.Fatal("rollback failure hidden or concurrent hosts edit overwritten", err, c.activation)
	}
}
