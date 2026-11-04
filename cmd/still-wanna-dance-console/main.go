package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"syscall"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/buildinfo"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/faults"
	"still-wanna-dance/internal/legal"
)

var visibleErrors = runtime.GOOS == "windows"
var openWebUI = desktop.OpenBrowser

func main() {
	err := run()
	var restart *restartRequest
	if errors.As(err, &restart) {
		err = restartApplication(restart.args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if visibleErrors {
			if recoveryErr := recoverApplication(err); recoveryErr != nil {
				if errors.Is(recoveryErr, err) {
					desktop.ShowError(recoveryErr)
				} else {
					desktop.ShowError(errors.Join(err, recoveryErr))
				}
			}
		}
		os.Exit(1)
	}
}

func run() (runErr error) {
	address := flag.String("listen", desktop.Address, "local WebUI address (port 0 selects an available port)")
	restartPort := flag.Int("restart-port", 0, "internal preferred WebUI port after restart")
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
	hostsSession := flag.String("hosts-session", "", "internal hosts helper pipe")
	hostsParent := flag.Uint("hosts-parent", 0, "internal hosts helper parent PID")
	noTray := flag.Bool("no-tray", runtime.GOOS != "windows", "run without the Windows tray")
	noOpen := flag.Bool("no-open", false, "do not open the browser on launch")
	showTerms := flag.Bool("show-terms", false, "print usage terms and exit")
	showBuild := flag.Bool("build-info", false, "print executable build identity as JSON and exit")
	flag.Parse()
	listenExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "listen" {
			listenExplicit = true
		}
	})
	if *showBuild {
		visibleErrors = false
		return json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
	}
	if *showTerms {
		fmt.Print(legal.Text)
		return nil
	}
	visibleErrors = runtime.GOOS == "windows" && !*noTray && *hostsSession == ""
	incidentCenter := faults.New()
	if *hostsSession != "" {
		return console.RunHostsSession(*hostsSession, uint32(*hostsParent))
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("不支持的位置参数")
	}
	if *restartPort < 0 || *restartPort > 65535 {
		return fmt.Errorf("无效的重启 WebUI 端口")
	}
	host, _, err := net.SplitHostPort(*address)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("WebUI 必须绑定 127.0.0.1")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	identity, err := desktop.NewIdentity(*config)
	if err != nil {
		return err
	}
	p := identity.Config
	if err := apppaths.ValidateWorkSeparation(exe, p); err != nil {
		return err
	}
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
	configLease, err := desktop.LockConfig(p)
	if err != nil {
		return err
	}
	defer configLease.Close()
	level, configErr := console.ReadLogLevel(p)
	startupSettings, settingsErr := console.ReadStartupSettings(p)
	if configErr == nil {
		configErr = settingsErr
	}
	// Reclaim disposable blocks before any log/configuration failure can abort
	// startup. Cleanup respects the configured level before opening file logs.
	// Retain the work lease until all consumers have stopped.
	workPath := apppaths.WorkForCacheRoot(exe, p, startupSettings.BlockCacheDir)
	if configErr != nil {
		// A malformed configuration cannot authorize cleaning a custom path.
		workPath = apppaths.WorkForConfig(exe, p)
	}
	var work *cacheproxy.WorkDirectory
	var workErr error
	var inspection console.WorkLocationInspection
	if configErr == nil {
		inspection, workErr = console.InspectWorkLocation(exe, p, startupSettings)
	}
	if workErr == nil {
		if inspection.Unresolved != nil {
			work, workErr = cacheproxy.OpenWorkDirectoryDeferred(workPath, applog.New(os.Stderr, level))
		} else {
			work, workErr = cacheproxy.OpenWorkDirectory(workPath, applog.New(os.Stderr, level))
		}
	}
	if workErr != nil {
		workErr = fmt.Errorf("临时视频缓存目录不可用（%s）：%w", workPath, workErr)
	} else {
		defer func() { runErr = errors.Join(runErr, work.Close()) }()
	}
	logDir := apppaths.ForConfig(p).LogDir
	var writer *applog.Writer
	if level != applog.Off {
		writer, err = applog.Open(logDir, applog.MaxBytes, applog.MaxTotalBytes, func(err error) {
			message := fmt.Errorf("程序日志写入失败（%s）：%w", logDir, err)
			fmt.Fprintln(os.Stderr, message)
		})
		if err != nil {
			return fmt.Errorf("无法创建程序日志（%s）：%w", logDir, err)
		}
		if visibleErrors {
			writer.SetFaultObserver(func(err error) {
				if err != nil {
					err = fmt.Errorf("程序日志写入失败（%s）：%w", logDir, err)
				}
				incidentCenter.Set("logging", err)
			})
		}
	}
	previousLogger := slog.Default()
	slog.SetDefault(applog.New(writer, level))
	defer func() {
		if crash := recover(); crash != nil {
			slog.Error("application_panic", "panic", fmt.Sprint(crash), "stack", string(debug.Stack()))
			if level == applog.Off {
				runErr = fmt.Errorf("程序发生异常（程序日志已关闭）：%v", crash)
			} else {
				runErr = fmt.Errorf("程序发生异常，详情见 %s：%v", logDir, crash)
			}
		}
		var restart *restartRequest
		if runErr != nil && !errors.As(runErr, &restart) {
			slog.Error("application_failed", "error", runErr)
		} else {
			slog.Info("application_stopped")
		}
		slog.SetDefault(previousLogger)
		if writer != nil {
			if err := writer.Close(); err != nil && (runErr == nil || restart != nil) {
				runErr = fmt.Errorf("关闭日志失败：%w", err)
			}
		}
	}()
	slog.Info("application_starting", "config", p, "listen", *address, "tray", !*noTray, "go", runtime.Version())
	if configErr != nil {
		return configErr
	}
	if err := configLease.CleanWorkFiles(); err != nil {
		return err
	}
	if workErr != nil {
		slog.Warn("block_cache_unavailable", "error", workErr)
	} else if inspection.Unresolved != nil {
		slog.Warn("block_cache_cleanup_deferred", "error", inspection.Unresolved)
	}
	console.CleanupPendingBlockCaches(exe, p, startupSettings, work, slog.Default())
	build := buildinfo.Current()
	slog.Info("application_build", "kind", build.Kind, "version", build.Version, "vcs.revision", build.Revision,
		"vcs.modified", build.Dirty, "builtAtUtc", build.BuiltAtUTC, "toolchain", build.Toolchain, "target", build.Target)
	l, err := listenConsole(*address, p, !listenExplicit, *restartPort)
	if err != nil {
		return desktop.PortError(*address, err)
	}
	defer l.Close()
	c, err := console.NewWithWorkError(p, l.Addr().String(), work, workErr)
	if err != nil {
		return err
	}
	c.SetFaultCenter(incidentCenter)
	h := &http.Server{Handler: c, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)}
	var closeOnce sync.Once
	var background sync.WaitGroup
	var shutdownErr error
	shutdownContext, endSession := context.WithCancel(context.Background())
	defer endSession()
	shutdown := func() {
		closeOnce.Do(func() {
			slog.Info("application_stopping")
			stop()
			c.BeginShutdown()
			background.Wait()
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
		case <-c.ShutdownRequested():
			stop()
		case <-ctx.Done():
		}
	}()
	if instance != nil {
		if err := instance.Publish(l.Addr().String()); err != nil {
			return fmt.Errorf("无法发布 WebUI 地址：%w", err)
		}
	}
	done := make(chan error, 1)
	if err := c.StartUpstreamServices(); err != nil {
		return err
	}
	go func() { done <- h.Serve(l); stop() }()
	if err := saveConsolePort(p, l.Addr()); err != nil {
		slog.Warn("console_port_save_failed", "error", err)
	}
	// Keep the console and tray usable while automatic acceleration waits for UAC.
	url := "http://" + l.Addr().String()
	if !*noTray {
		if !*noOpen && c.OpenWebUIOnLaunch() {
			incidentCenter.Open()
		}
		background.Add(1)
		go func() {
			defer background.Done()
			watchFaultOpenings(ctx, incidentCenter, url, openWebUI, func(err error) { go desktop.ShowError(err) })
		}()
	}
	background.Add(3)
	go func() { defer background.Done(); c.AutoStart() }()
	go func() { defer background.Done(); c.WatchVRChat() }()
	go func() { defer background.Done(); c.WatchFaults(ctx) }()
	slog.Info("console_ready", "url", url)
	fmt.Printf("Still Wanna Dance WebUI：%s\n同意条款后关闭浏览器不会退出程序；未同意时关闭条款页会尝试在 60 秒后退出。退出按当前设置处理游戏连接，不再请求管理员权限。\n", url)
	if !*noTray {
		err = desktop.Run(ctx, desktop.Options{URL: url, State: c.DesktopState, Command: c.DesktopCommand, CommandReportsErrors: true, ReportFault: incidentCenter.Set, RequestExit: c.RequestExit, Shutdown: shutdown, EndSession: endSession})
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
	if !*noTray {
		// A terminal fault can be published while background tasks drain after
		// the normal listener has begun shutting down. Present it through the
		// independent WebUI rather than silently discarding it with the process.
		if _, pending := incidentCenter.Take(); pending != nil {
			return pending
		}
	}
	if err == nil {
		if c.ShutdownAction() == console.ShutdownRestart {
			// Return through every defer before main launches the replacement.
			args := []string{"-config", p, fmt.Sprintf("-no-tray=%t", *noTray), "-no-open"}
			if listenExplicit {
				// Preserve the existing fixed-address behavior for explicit -listen.
				args = append(args, "-listen", l.Addr().String())
			} else {
				// The replacement can reuse this port even if the hint could not be saved.
				args = append(args, "-restart-port", strconv.Itoa(l.Addr().(*net.TCPAddr).Port))
			}
			return &restartRequest{args: args}
		}
	}
	return err
}
