package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	identity, identityErr := desktop.NewIdentity(flagValue("config"))
	var instance *desktop.Instance
	if identityErr == nil {
		var err error
		instance, err = desktop.AcquireOrActivate(ctx, identity, true)
		if err == nil && instance == nil {
			// Another matching owner became ready while startup was unwinding.
			return nil
		}
		if err == nil {
			defer instance.Close()
		}
	}
	preferred := flagValue("listen")
	if port, err := strconv.Atoi(flagValue("restart-port")); err == nil && port > 0 && port <= 65535 {
		preferred = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	return serveFailureUI(ctx, cause, identity, preferred, instance, openWebUI, desktop.Run)
}

// serveFailureUI also works without a desktop lease (e.g. a conflicting owner):
// it does not write or take over that owner's configuration or tray.
func serveFailureUI(parent context.Context, cause error, identity desktop.Identity, preferred string, instance *desktop.Instance, open func(string) error, runTray func(context.Context, desktop.Options) error) error {
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
	view, err := console.NewRecovery(listener.Addr().String(), identity)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: view, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			view.BeginShutdown()
			grace, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := server.Shutdown(grace); err != nil {
				_ = server.Close()
			}
			view.Close(grace)
		})
	}
	defer shutdown()
	if instance != nil {
		// Failure to publish does not prevent a local browser from displaying it.
		_ = instance.Publish(listener.Addr().String())
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
	center.Set("startup", cause)
	openErr := make(chan error, 1)
	go watchFaultOpenings(ctx, center, "http://"+listener.Addr().String(), view.WebUIConnected, open, func(err error) {
		openErr <- err
		cancel()
	})
	if instance != nil && runTray != nil {
		err = runTray(ctx, desktop.Options{
			URL:         "http://" + listener.Addr().String(),
			State:       func() desktop.State { return desktop.State{Unavailable: true} },
			ReportFault: center.Set, RequestExit: view.RequestExit, Shutdown: shutdown,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "托盘不可用：%v\n", err)
		}
		// The tray is not required to serve or close the limited WebUI.
		// If it cannot start, keep the page alive until exit or cancellation.
		if ctx.Err() == nil {
			go view.ExitWhenUnattended(ctx, time.Minute)
			<-ctx.Done()
		}
		err = nil
	} else {
		go view.ExitWhenUnattended(ctx, time.Minute)
		<-ctx.Done()
	}
	shutdown()
	serverErr := <-serveErr
	select {
	case failure := <-openErr:
		return failure
	default:
	}
	if err != nil {
		return err
	}
	if !errors.Is(serverErr, http.ErrServerClosed) {
		return serverErr
	}
	return nil
}
