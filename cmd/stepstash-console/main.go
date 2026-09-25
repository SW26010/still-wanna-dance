package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

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

func run() error {
	address := flag.String("listen", desktop.Address, "local console address (tray mode uses 127.0.0.1:18081)")
	config := flag.String("config", "stepstash-console.json", "settings file")
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
	if !*noTray && *address != desktop.Address {
		return fmt.Errorf("托盘模式固定使用 %s；自定义端口请使用 -no-tray", desktop.Address)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !*noTray {
		lease, err := desktop.AcquireOrActivate(ctx, *address, !*noOpen)
		if err != nil {
			return err
		}
		if lease == nil {
			return nil
		}
		defer lease.Close()
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
	l, err := net.Listen("tcp4", *address)
	if err != nil {
		return desktop.PortError(*address, err)
	}
	defer l.Close()
	c, err := console.New(p, l.Addr().String())
	if err != nil {
		return err
	}
	h := &http.Server{Handler: c, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	var closeOnce sync.Once
	shutdown := func() { closeOnce.Do(func() { _ = h.Close(); _ = c.Close() }) }
	defer shutdown()
	done := make(chan error, 1)
	go func() { done <- h.Serve(l); stop() }()
	url := "http://" + l.Addr().String()
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
