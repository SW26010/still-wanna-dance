// Package desktop owns the optional desktop shell, not the CDN or download engine.
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

const Address = "127.0.0.1:0"
const MutexName = `Local\StepStash.Desktop.v1`

type Identity struct {
	App      string `json:"app"`
	Protocol int    `json:"protocol"`
}

var AppIdentity = Identity{App: "stepstash-console", Protocol: 1}

type Lease interface{ Close() error }

// elect waits for a starting owner, but can take over if that owner dies before
// its HTTP server becomes ready. It never treats an arbitrary occupied port as ours.
func elect(ctx context.Context, acquire func() (Lease, error), ready func(context.Context) bool, open func() error) (Lease, error) {
	for {
		lease, err := acquire()
		if err != nil {
			return nil, err
		}
		if lease != nil {
			return lease, nil
		}
		if ready(ctx) {
			if open != nil {
				if err = open(); err != nil {
					return nil, fmt.Errorf("已有控制台运行，但打开浏览器失败：%w", err)
				}
			}
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("已有 StepStash 正在启动或退出，暂时无法打开；请稍后重试")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func AcquireOrActivate(ctx context.Context, openBrowser bool) (*Instance, error) {
	path, err := instancePath()
	if err != nil {
		return nil, err
	}
	return acquireOrActivate(ctx, path, func() (Lease, error) { return acquire(MutexName) }, openBrowser, OpenBrowser)
}

func acquireOrActivate(ctx context.Context, path string, acquireLease func() (Lease, error), openBrowser bool, browser func(string) error) (*Instance, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var address string
	var open func() error
	if openBrowser {
		open = func() error { return browser("http://" + address) }
	}
	lease, err := elect(ctx, acquireLease, func(ctx context.Context) bool {
		address = readInstanceAddress(path)
		return address != "" && probe(ctx, address)
	}, open)
	if err != nil || lease == nil {
		return nil, err
	}
	instance := &Instance{lease: lease, path: path}
	// Clear crash leftovers before the new owner starts serving.
	if err := instance.clear(); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return instance, nil
}

func probe(ctx context.Context, address string) bool {
	ctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/api/identity", nil)
	if err != nil {
		return false
	}
	t := &http.Transport{Proxy: nil}
	defer t.CloseIdleConnections()
	client := &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var identity Identity
	return json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&identity) == nil && identity == AppIdentity
}

type Owner struct {
	PID  uint32 `json:"pid"`
	Name string `json:"name"`
}

func PortOwner(address string) *Owner {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return nil
	}
	return portOwner(host, uint16(p))
}

func PortError(address string, cause error) error {
	if owner := PortOwner(address); owner != nil {
		return fmt.Errorf("%s 被 %s（PID %d）占用，请先关闭该服务：%w", address, owner.Name, owner.PID, cause)
	}
	return fmt.Errorf("%s 无法监听（端口被占用或系统限制）：%w", address, cause)
}

const (
	OpenConsole = 1001 + iota
	ToggleCDN
	CancelBatch
	Exit
)

type State struct{ CDN, Batch bool }
type Item struct {
	ID       int
	Label    string
	Disabled bool
}

func Menu(s State, busy bool) []Item {
	label := "启动本地 CDN"
	if s.CDN {
		label = "关闭本地 CDN"
	}
	return []Item{{OpenConsole, "打开控制台", false}, {0, "", false}, {ToggleCDN, label, busy}, {CancelBatch, "停止批量任务", busy || !s.Batch}, {0, "", false}, {Exit, "退出 StepStash", false}}
}

type Options struct {
	URL     string
	Open    bool
	State   func() State
	Command func(int) error
	// Shutdown must close both the HTTP server and cache engine, and be idempotent.
	Shutdown func()
}
