package console

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"still-wanna-dance/internal/desktop"
)

func TestDesktopSharesWebRuntime(t *testing.T) {
	c := testConsole(t)
	if err := c.DesktopCommand(desktop.StartAcceleration); err != nil {
		t.Fatal(err)
	}
	if s := c.DesktopState(); !s.CDN || !s.Ready || s.Activating || s.NeedsExitPreparation || c.activation.Phase != "waiting" || !c.queueDesired {
		t.Fatal("tray did not complete acceleration", s, c.activation)
	}
	c.beginVideoRequest(time.Now())()
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
	var status struct {
		Running    bool
		Hosts      HostsStatus
		Activation Activation
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || !status.Running || !status.Hosts.Ready || status.Activation.FirstRequest.IsZero() {
		t.Fatal("web status did not observe tray activation and request", status, err)
	}
	server := c.httpServer
	if err := c.DesktopCommand(desktop.StartAcceleration); err != nil || c.httpServer != server {
		t.Fatal("repeated tray action stopped or replaced the gateway", err)
	}
	if err := c.stop(); err != nil {
		t.Fatal(err)
	}
	if s := c.DesktopState(); s.CDN || s.Ready {
		t.Fatal("tray state stale after web stop")
	}
	if err := c.DesktopCommand(desktop.StartAcceleration); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readHostsFixture(t, c.hosts.path); got != hostsBaseline {
		t.Fatal("exit did not restore tray activation", got)
	}
	if err := c.DesktopCommand(desktop.StartAcceleration); err == nil {
		t.Fatal("restarted while closing")
	}
}

func TestDesktopTermsStateTracksValidConsent(t *testing.T) {
	c := testConsole(t)
	valid := c.terms
	staleVersion, staleHash := valid, valid
	staleVersion.Version = "outdated"
	staleHash.Hash = "outdated"
	for _, receipt := range []termsReceipt{{}, staleVersion, staleHash} {
		c.mu.Lock()
		c.terms = receipt
		c.mu.Unlock()
		s := c.DesktopState()
		if s.TermsAccepted || !desktop.Menu(s, false)[2].Disabled {
			t.Fatal("missing or invalid consent allowed acceleration", s)
		}
		if err := c.DesktopCommand(desktop.StartAcceleration); err == nil {
			t.Fatal("backend consent gate bypassed")
		}
	}
	if err := c.acceptTerms(); err != nil {
		t.Fatal(err)
	}
	if s := c.DesktopState(); !s.TermsAccepted || desktop.Menu(s, false)[2].Disabled {
		t.Fatal("consent did not update the existing tray state", s)
	}
	if c.httpServer != nil || !c.activation.Started.IsZero() || c.queueDesired {
		t.Fatal("reading consent state or accepting terms started acceleration")
	}
}

func TestDesktopRepairsRunningGateway(t *testing.T) {
	owned, _ := transformHosts(hostsBaseline, "enable")
	for _, tc := range []struct {
		name, initial string
		restore       bool
		ready         bool
		needsRestore  bool
	}{
		{"missing hosts", hostsBaseline, true, false, false},
		{"unprepared exit", owned, true, true, true},
		{"persistent hosts", owned, false, true, false},
		{"user mapping", hostsBaseline + "127.0.0.1 api.udon.dance # user\r\n", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConsole(t)
			c.hosts, _ = fixtureHostsController(t, tc.initial)
			c.settings.RestoreHostsOnExit = tc.restore
			if err := c.start(); err != nil {
				t.Fatal(err)
			}
			if s := c.DesktopState(); !s.CDN || s.Ready != tc.ready || s.NeedsExitPreparation != tc.needsRestore {
				t.Fatal("wrong repair availability", s)
			}
			server := c.httpServer
			if err := c.DesktopCommand(desktop.StartAcceleration); err != nil {
				t.Fatal(err)
			}
			if s := c.DesktopState(); !s.Ready || s.NeedsExitPreparation || c.httpServer != server || c.activation.Phase != "waiting" {
				t.Fatal("repair did not reuse the running gateway", s, c.activation)
			}
		})
	}
}

func TestDesktopActivationRollsBackAndRetries(t *testing.T) {
	c := testConsole(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c.httpsAddress = listener.Addr().String()
	if err := c.DesktopCommand(desktop.StartAcceleration); err == nil {
		t.Fatal("occupied HTTPS port accepted")
	}
	if s := c.DesktopState(); s.CDN || s.Ready || s.Activating || c.activation.Phase != "failed" || c.activation.Error == "" || readHostsFixture(t, c.hosts.path) != hostsBaseline {
		t.Fatal("failed tray activation was not rolled back", s, c.activation)
	}
	c.httpsAddress = "127.0.0.1:0"
	if err := c.DesktopCommand(desktop.StartAcceleration); err != nil || !c.DesktopState().Ready {
		t.Fatal("tray retry failed", err)
	}
}

func TestDesktopActivationRejectsConflictAndDeniedAuthorization(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		c := testConsole(t)
		initial := hostsBaseline
		if conflict {
			initial += "192.0.2.1 api.udon.dance\r\n"
		}
		if err := os.WriteFile(c.hosts.path, []byte(initial), 0600); err != nil {
			t.Fatal(err)
		}
		calls := 0
		c.hosts.launch = func(context.Context) (hostsConnection, error) {
			calls++
			return nil, errors.New("UAC canceled")
		}
		if err := c.DesktopCommand(desktop.StartAcceleration); err == nil || c.DesktopState().CDN || c.activation.Phase != "failed" {
			t.Fatal("failed tray preflight started the gateway", err)
		}
		if calls != map[bool]int{true: 0, false: 1}[conflict] || readHostsFixture(t, c.hosts.path) != initial {
			t.Fatal("conflict/denied authorization changed hosts or elevated unexpectedly", calls)
		}
	}
}

func TestDesktopReflectsWebActivationAndRejectsDuplicates(t *testing.T) {
	c := testConsole(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	defer once.Do(func() { close(release) })
	launch := c.hosts.launch
	c.hosts.launch = func(ctx context.Context) (hostsConnection, error) {
		close(entered)
		<-release
		return launch(ctx)
	}
	go func() { done <- c.enableAcceleration(c.hosts.status, c.changeHostsLocked) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("activation did not reach authorization")
	}
	state := make(chan desktop.State, 1)
	go func() { state <- c.DesktopState() }()
	select {
	case s := <-state:
		if !s.Activating || s.Ready || !desktop.Menu(s, false)[2].Disabled {
			t.Fatal("tray did not reflect pending web activation", s)
		}
	case <-time.After(time.Second):
		t.Fatal("tray status blocked on authorization")
	}
	if err := c.DesktopCommand(desktop.StartAcceleration); err == nil {
		t.Fatal("duplicate activation accepted")
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil || !c.DesktopState().Ready || c.DesktopState().Activating {
		t.Fatal("tray state did not follow completed web activation", err)
	}
}

func TestConsoleIdentityIsHostProtected(t *testing.T) {
	c := testConsole(t)
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/identity", nil))
	var id desktop.Identity
	if err := json.Unmarshal(w.Body.Bytes(), &id); err != nil || id != c.identity || id.Config != c.configPath || id.PID == 0 || id.Started == "" {
		t.Fatal(id, err)
	}
	w = httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "http://untrusted.test/api/identity", nil))
	if w.Code != 403 {
		t.Fatal("identity accepted foreign Host")
	}
}
