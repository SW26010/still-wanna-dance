package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"stepstash/internal/console"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	address := flag.String("listen", "127.0.0.1:18081", "local console address")
	config := flag.String("config", "stepstash-console.json", "settings file")
	action := flag.String("hosts-action", "", "internal elevated helper: enable or disable")
	flag.Parse()
	if *action != "" {
		if err := console.ApplyHosts(*action); err != nil {
			return err
		}
		console.FlushDNS()
		return nil
	}
	host, _, err := net.SplitHostPort(*address)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("控制台必须绑定 127.0.0.1")
	}
	p, err := filepath.Abs(*config)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp4", *address)
	if err != nil {
		return fmt.Errorf("控制台端口无法监听：%w", err)
	}
	defer l.Close()
	c, err := console.New(p, l.Addr().String())
	if err != nil {
		return err
	}
	defer c.Close()
	h := &http.Server{Handler: c, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- h.Serve(l) }()
	fmt.Printf("StepStash 控制台：http://%s\n关闭 CDN 不会恢复 hosts；请使用控制台的恢复按钮。\n", l.Addr())
	select {
	case <-ctx.Done():
		_ = h.Close()
		return nil
	case err := <-done:
		return err
	}
}
