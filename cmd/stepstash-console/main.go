package main

import (
	"context"
	"crypto/rand"
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

	"stepstash/internal/applog"
	"stepstash/internal/console"
	"stepstash/internal/desktop"
)

var visibleErrors = runtime.GOOS == "windows"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if visibleErrors {
			desktop.ShowError(err)
		}
		os.Exit(1)
	}
}

func run() (runErr error) {
	address := flag.String("listen", desktop.Address, "local console address (port 0 selects an available port)")
	defaultConfig := "stepstash-console.json"
	if runtime.GOOS == "windows" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		defaultConfig = filepath.Join(filepath.Dir(exe), defaultConfig)
	}
	config := flag.String("config", defaultConfig, "settings file (relative directories are based on this file)")
	action := flag.String("hosts-action", "", "internal elevated helper: enable or disable")
	noTray := flag.Bool("no-tray", runtime.GOOS != "windows", "run without the Windows tray")
	noOpen := flag.Bool("no-open", false, "do not open the browser on launch")
	flag.Parse()
	visibleErrors = runtime.GOOS == "windows" && !*noTray && *action == ""
	// The short-lived elevated helper bypasses desktop election.
	if *action != "" {
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
	var instance *desktop.Instance
	if !*noTray {
		lease, err := desktop.AcquireOrActivate(ctx, !*noOpen)
		if err != nil {
			return err
		}
		if lease == nil {
			return nil
		}
		defer lease.Close()
		instance = lease
	}
	p, err := filepath.Abs(*config)
	if err != nil {
		return err
	}
	configLease, err := desktop.LockConfig(p)
	if err != nil {
		return err
	}
	defer configLease.Close()
	logPath := filepath.Join(filepath.Dir(p), "logs", filepath.Base(p)+".log")
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(writer, nil)).With("pid", os.Getpid(), "session_id", rand.Text()))
	defer func() {
		if crash := recover(); crash != nil {
			slog.Error("application_panic", "panic", fmt.Sprint(crash), "stack", string(debug.Stack()))
			runErr = fmt.Errorf("程序发生异常，详情见 %s：%v", logPath, crash)
		}
		if runErr != nil {
			slog.Error("application_failed", "error", runErr)
		} else {
			slog.Info("application_stopped")
		}
		slog.SetDefault(previousLogger)
		if err := writer.Close(); err != nil && runErr == nil {
			runErr = fmt.Errorf("关闭日志失败：%w", err)
		}
	}()
	slog.Info("application_starting", "config", p, "listen", *address, "tray", !*noTray, "go", runtime.Version())
	if build, ok := debug.ReadBuildInfo(); ok {
		fields := []any{"version", build.Main.Version}
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" || setting.Key == "vcs.modified" || setting.Key == "vcs.time" {
				fields = append(fields, setting.Key, setting.Value)
			}
		}
		slog.Info("application_build", fields...)
	}
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
	shutdown := func() {
		closeOnce.Do(func() {
			slog.Info("application_stopping")
			_ = h.Close()
			if err := c.Close(); err != nil {
				slog.Error("shutdown_failed", "error", err)
			}
		})
	}
	defer shutdown()
	if instance != nil {
		if err := instance.Publish(l.Addr().String()); err != nil {
			return fmt.Errorf("无法发布控制台地址：%w", err)
		}
	}
	done := make(chan error, 1)
	c.AutoStart()
	go func() { done <- h.Serve(l); stop() }()
	url := "http://" + l.Addr().String()
	slog.Info("console_ready", "url", url)
	fmt.Printf("StepStash 控制台：%s\n关闭浏览器不会退出程序。退出不会恢复 hosts。\n", url)
	if !*noTray {
		err = desktop.Run(ctx, desktop.Options{URL: url, Open: !*noOpen, State: c.DesktopState, Command: c.DesktopCommand, Shutdown: shutdown})
	} else {
		<-ctx.Done()
	}
	shutdown()
	serverErr := <-done
	if !errors.Is(serverErr, http.ErrServerClosed) {
		return serverErr
	}
	return err
}
