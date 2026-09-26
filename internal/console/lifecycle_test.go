package console

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Block a real lifecycle operation at a synchronous log write, without sleeps
// or filesystem timing assumptions. Derived engine loggers share the same gate.
type lifecycleLogGate struct {
	message string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *lifecycleLogGate) Enabled(context.Context, slog.Level) bool { return true }
func (g *lifecycleLogGate) WithAttrs([]slog.Attr) slog.Handler       { return g }
func (g *lifecycleLogGate) WithGroup(string) slog.Handler            { return g }
func (g *lifecycleLogGate) Handle(_ context.Context, r slog.Record) error {
	if r.Message == g.message {
		g.once.Do(func() { close(g.entered); <-g.release })
	}
	return nil
}

func TestLifecycleIOLeavesStateResponsive(t *testing.T) {
	for _, operation := range []string{"start", "save", "close"} {
		t.Run(operation, func(t *testing.T) {
			c := testConsole(t)
			message := "cache_engine_ready"
			if operation != "start" {
				if err := c.start(); err != nil {
					t.Fatal(err)
				}
				if err := c.stop(); err != nil {
					t.Fatal(err)
				}
				message = "settings_saved"
			}
			if operation == "close" {
				message = "cache_engine_stopped"
			}
			gate := &lifecycleLogGate{message: message, entered: make(chan struct{}), release: make(chan struct{})}
			oldLogger := slog.Default()
			slog.SetDefault(slog.New(gate))
			defer slog.SetDefault(oldLogger)
			var release sync.Once
			defer release.Do(func() { close(gate.release) })
			// Close uses the logger captured when the engine was created.
			if operation == "close" {
				if err := c.save(c.settings); err != nil {
					t.Fatal(err)
				}
				if err := c.start(); err != nil {
					t.Fatal(err)
				}
			}
			settings := c.settings
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "start":
					done <- c.start()
				case "save":
					done <- c.save(settings)
				case "close":
					done <- c.Close()
				}
			}()
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("operation did not reach the I/O gate")
			}
			read := make(chan struct{})
			go func() {
				c.DesktopState()
				w := httptest.NewRecorder()
				c.ServeHTTP(w, httptest.NewRequest("GET", "http://"+c.address+"/api/status", nil))
				close(read)
			}()
			select {
			case <-read:
			case <-time.After(3 * time.Second):
				t.Fatal("state readers blocked behind lifecycle I/O")
			}
			var saved chan error
			if operation == "start" {
				// A concurrent save must observe the fully published CDN and reject
				// the change, rather than invalidate an engine still being created.
				saved = make(chan error, 1)
				go func() { saved <- c.save(settings) }()
			}
			release.Do(func() { close(gate.release) })
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if saved != nil {
				if err := <-saved; err == nil {
					t.Fatal("save changed configuration during CDN startup")
				}
			}
		})
	}
}
