package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/faults"
)

// The only consumer of actionable incidents. Sources publish state rather than
// opening a browser themselves; normal startup uses the same coalescing path.
func watchFaultOpenings(ctx context.Context, center *faults.Center, url string, connected func() bool, open func(string) error, failed func(error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-center.Wake():
			if ctx.Err() != nil {
				return
			}
			if want, cause := center.Take(); want {
				// The existing page already presents faults. Opening the root URL
				// again can replace its current route and discard an unsaved draft.
				if connected != nil && connected() {
					continue
				}
				result := make(chan error, 1)
				go func() { result <- open(url) }()
				select {
				case <-ctx.Done():
					center.Restore(cause)
					return
				case err := <-result:
					if ctx.Err() != nil {
						center.Restore(cause)
						return
					}
					if err != nil {
						failed(errors.Join(cause, err))
					}
				}
			}
		}
	}
}

func flagValue(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// Startup may fail before any Console, log writer or configuration is usable.
// The isolated server serves the same embedded WebUI without those resources.
func recoverApplication(cause error) error {
	var conflict *desktop.InstanceConflictError
	if errors.As(cause, &conflict) {
		return cause
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	identity, err := desktop.NewIdentity(flagValue("config"))
	if err != nil {
		return err
	}
	instance, err := desktop.AcquireOrActivate(ctx, identity, true)
	if err != nil {
		return err
	}
	if instance == nil {
		// Another matching owner became ready while startup was unwinding.
		return nil
	}
	defer instance.Close()
	preferred := flagValue("listen")
	if port, err := strconv.Atoi(flagValue("restart-port")); err == nil && port > 0 && port <= 65535 {
		preferred = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	return serveFailureUI(ctx, cause, identity, preferred, instance.Publish, openWebUI, desktop.Run)
}

// Recovery requires desktop ownership and a working tray. Its caller presents
// failures through the native dialog after all recovery resources are released.
func serveFailureUI(parent context.Context, cause error, identity desktop.Identity, preferred string, publish func(string) error, open func(string) error, runTray func(context.Context, desktop.Options) error) error {
	host, _, err := net.SplitHostPort(preferred)
	if err != nil || host != "127.0.0.1" {
		preferred = desktop.Address
	}
	listener, err := net.Listen("tcp4", preferred)
	if err != nil && preferred != desktop.Address {
		listener, err = net.Listen("tcp4", desktop.Address)
	}
	if err != nil {
		return fmt.Errorf("无法启动 WebUI：%w", err)
	}
	defer listener.Close()
	view, err := console.NewRecovery(listener.Addr().String(), identity, cause.Error())
	if err != nil {
		return err
	}
	server := &http.Server{Handler: view, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var once sync.Once
	var shutdownErr error
	shutdown := func() {
		once.Do(func() {
			view.BeginShutdown()
			grace, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := server.Shutdown(grace); err != nil {
				_ = server.Close()
			}
			shutdownErr = view.Close(grace)
		})
	}
	defer shutdown()
	if err := publish(listener.Addr().String()); err != nil {
		return fmt.Errorf("登记恢复实例：%w", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener); cancel() }()
	go func() {
		select {
		case <-view.ShutdownRequested():
			cancel()
		case <-ctx.Done():
		}
	}()
	center := faults.New()
	openErr := make(chan error, 1)
	err = runTray(ctx, desktop.Options{
		URL:         "http://" + listener.Addr().String(),
		State:       view.DesktopState,
		ReportFault: center.Set, RequestExit: view.RequestExit, Shutdown: shutdown,
		Ready: func() {
			center.Set("startup", cause)
			go watchFaultOpenings(ctx, center, "http://"+listener.Addr().String(), view.WebUIConnected, open, func(err error) {
				openErr <- err
				cancel()
			})
			go view.ExitWhenUnattended(ctx, time.Minute)
		},
	})
	if err == nil && ctx.Err() == nil && view.ShutdownAction() == "running" {
		err = errors.New("托盘已意外关闭")
	}
	cancel()
	shutdown()
	serverErr := <-serveErr
	select {
	case failure := <-openErr:
		return failure
	default:
	}
	if err != nil {
		return errors.Join(fmt.Errorf("托盘不可用：%w", err), shutdownErr)
	}
	if !errors.Is(serverErr, http.ErrServerClosed) {
		return serverErr
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	if view.ShutdownAction() == console.ShutdownRestart {
		args := []string{"-config", identity.Config, "-no-open"}
		explicit := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "listen" {
				explicit = true
			}
		})
		if explicit {
			args = append(args, "-listen", listener.Addr().String())
		} else {
			args = append(args, "-restart-port", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
		}
		if flagValue("no-tray") == "true" {
			args = append(args, "-no-tray")
		}
		return &restartRequest{args: args}
	}
	return nil
}
