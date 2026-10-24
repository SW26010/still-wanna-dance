package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/buildinfo"
	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/legal"
)

var visibleErrors = runtime.GOOS == "windows"

func main() {
	err := run()
	var restart *restartRequest
	if errors.As(err, &restart) {
		err = restartApplication(restart.args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if visibleErrors {
			desktop.ShowError(err)
		}
		os.Exit(1)
	}
}

func run() (runErr error) {
	address := flag.String("listen", desktop.Address, "local console address (port 0 selects an available port)")
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = desktop.CanonicalPath(exe)
	if err != nil {
		return err
	}
	defaultConfig, err := desktop.CanonicalPath(apppaths.DefaultConfig(exe))
	if err != nil {
		return err
	}
	config := flag.String("config", defaultConfig, "settings file (relative directories are based on this file)")
	action := flag.String("hosts-action", "", "internal elevated helper: enable or disable")
	hostsSession := flag.String("hosts-session", "", "internal hosts helper pipe")
	hostsParent := flag.Uint("hosts-parent", 0, "internal hosts helper parent PID")
	noTray := flag.Bool("no-tray", runtime.GOOS != "windows", "run without the Windows tray")
	noOpen := flag.Bool("no-open", false, "do not open the browser on launch")
	showTerms := flag.Bool("show-terms", false, "print usage terms and exit")
	showBuild := flag.Bool("build-info", false, "print executable build identity as JSON and exit")
	acceptTerms := flag.String("accept-terms", "", "terms version for the internal hosts helper")
	flag.Parse()
	if *showBuild {
		visibleErrors = false
		return json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
	}
	if *showTerms {
		fmt.Print(legal.Text)
		return nil
	}
	visibleErrors = runtime.GOOS == "windows" && !*noTray && *action == "" && *hostsSession == ""
	if *hostsSession != "" {
		return console.RunHostsSession(*hostsSession, uint32(*hostsParent))
	}
	// The short-lived elevated helper bypasses desktop election.
	if *action != "" {
		if *action == "enable" {
			if err := legal.CheckAcceptance(*acceptTerms); err != nil {
				return err
			}
		}
		if err := console.ApplyHosts(*action); err != nil {
			return err
		}
		console.FlushDNS()
		return nil
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("不支持的位置参数")
	}
	host, _, err := net.SplitHostPort(*address)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("控制台必须绑定 127.0.0.1")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	identity, err := desktop.NewIdentity(*config)
	if err != nil {
		return err
	}
	p := identity.Config
	var instance *desktop.Instance
	if !*noTray {
		lease, err := desktop.AcquireOrActivate(ctx, identity, !*noOpen)
		if err != nil {
			return err
		}
		if lease == nil {
			return nil
		}
		defer lease.Close()
		instance = lease
	}
	if p == defaultConfig {
		if err := apppaths.CheckLegacyInstall(filepath.Dir(exe)); err != nil {
			return err
		}
	}
	configLease, err := desktop.LockConfig(p)
	if err != nil {
		return err
	}
	defer configLease.Close()
	logPath := apppaths.ForConfig(p).Log
	writer, err := applog.Open(logPath, applog.MaxBytes, applog.Backups, func(err error) {
		message := fmt.Errorf("程序日志写入失败（%s）：%w", logPath, err)
		fmt.Fprintln(os.Stderr, message)
		if visibleErrors {
			go desktop.ShowError(message)
		}
	})
	if err != nil {
		return fmt.Errorf("无法创建程序日志（%s）：%w", logPath, err)
	}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Value.Kind() == slog.KindTime {
				a.Value = slog.TimeValue(a.Value.Time().UTC())
			}
			return a
		},
	})).With("pid", os.Getpid(), "session_id", rand.Text()))
	defer func() {
		if crash := recover(); crash != nil {
			slog.Error("application_panic", "panic", fmt.Sprint(crash), "stack", string(debug.Stack()))
			runErr = fmt.Errorf("程序发生异常，详情见 %s：%v", logPath, crash)
		}
		var restart *restartRequest
		if runErr != nil && !errors.As(runErr, &restart) {
			slog.Error("application_failed", "error", runErr)
		} else {
			slog.Info("application_stopped")
		}
		slog.SetDefault(previousLogger)
		if err := writer.Close(); err != nil && (runErr == nil || restart != nil) {
			runErr = fmt.Errorf("关闭日志失败：%w", err)
		}
	}()
	slog.Info("application_starting", "config", p, "listen", *address, "tray", !*noTray, "go", runtime.Version())
	if err := configLease.CleanWorkFiles(); err != nil {
		return err
	}
	build := buildinfo.Current()
	slog.Info("application_build", "kind", build.Kind, "version", build.Version, "vcs.revision", build.Revision,
		"vcs.modified", build.Dirty, "builtAtUtc", build.BuiltAtUTC, "toolchain", build.Toolchain, "target", build.Target)
	l, err := net.Listen("tcp4", *address)
	if err != nil {
		return desktop.PortError(*address, err)
	}
	defer l.Close()
	c, err := console.New(p, l.Addr().String())
	if err != nil {
		return err
	}
	h := &http.Server{Handler: c, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)}
	var closeOnce sync.Once
	var startup sync.WaitGroup
	var shutdownErr error
	shutdownContext, endSession := context.WithCancel(context.Background())
	defer endSession()
	shutdown := func() {
		closeOnce.Do(func() {
			slog.Info("application_stopping")
			c.BeginShutdown()
			startup.Wait()
			// Let the exit acknowledgement finish before closing connections.
			// Windows session end cancels the grace period, even during an exit.
			grace, cancel := context.WithTimeout(shutdownContext, 2*time.Second)
			defer cancel()
			if err := h.Shutdown(grace); err != nil {
				_ = h.Close()
			}
			if err := c.Close(); err != nil {
				shutdownErr = err
				slog.Error("shutdown_failed", "error", err)
			}
		})
	}
	defer shutdown()
	go func() {
		select {
		case <-c.ExitRequested():
			stop()
		case <-c.RestartRequested():
			stop()
		case <-ctx.Done():
		}
	}()
	if instance != nil {
		if err := instance.Publish(l.Addr().String()); err != nil {
			return fmt.Errorf("无法发布控制台地址：%w", err)
		}
	}
	done := make(chan error, 1)
	if err := c.StartUpstreamMonitor(); err != nil {
		return err
	}
	go func() { done <- h.Serve(l); stop() }()
	// Keep the console and tray usable while automatic acceleration waits for UAC.
	startup.Add(1)
	go func() { defer startup.Done(); c.AutoStart() }()
	url := "http://" + l.Addr().String()
	slog.Info("console_ready", "url", url)
	fmt.Printf("Still Wanna Dance 控制台：%s\n同意条款后关闭浏览器不会退出程序；未同意时关闭条款页会尝试在 60 秒后退出。退出按当前设置处理游戏连接，不再请求管理员权限。\n", url)
	if !*noTray {
		err = desktop.Run(ctx, desktop.Options{URL: url, Open: !*noOpen, State: c.DesktopState, Command: c.DesktopCommand, Shutdown: shutdown, EndSession: endSession})
	} else {
		<-ctx.Done()
	}
	shutdown()
	serverErr := <-done
	if !errors.Is(serverErr, http.ErrServerClosed) {
		return serverErr
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	if err == nil {
		select {
		case <-c.RestartRequested():
			// Return through every defer before main launches the replacement.
			// Pin even an ephemeral listener so the existing browser can reconnect.
			return &restartRequest{args: []string{"-config", p, "-listen", l.Addr().String(), fmt.Sprintf("-no-tray=%t", *noTray), "-no-open"}}
		default:
		}
	}
	return err
}
