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

// One ordinary desktop owner per Windows session, across all configurations.
const MutexName = `Local\SWD.Desktop.v1`

type Identity struct {
	App        string `json:"app"`
	Protocol   int    `json:"protocol"`
	Executable string `json:"executable"`
	Config     string `json:"config"`
	PID        uint32 `json:"pid"`
	Started    string `json:"started"`
}

var AppIdentity = Identity{App: "swd-console", Protocol: 1}

type Lease interface{ Close() error }

// elect waits for a starting owner, but can take over if that owner dies before
// its HTTP server becomes ready. It never treats an arbitrary occupied port as ours.
func elect(ctx context.Context, acquire func() (Lease, error), ready func(context.Context) (bool, error), open func() error) (Lease, error) {
	for {
		lease, err := acquire()
		if err != nil {
			return nil, err
		}
		if lease != nil {
			return lease, nil
		}
		isReady, err := ready(ctx)
		if err != nil {
			return nil, err
		}
		if isReady {
			if open != nil {
				if err = open(); err != nil {
					return nil, fmt.Errorf("已有 SWD 在运行，但打开浏览器失败：%w", err)
				}
			}
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("已有 Still Wanna Dance 正在启动、退出或实例身份无法验证，暂时无法打开；请稍后重试")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func AcquireOrActivate(ctx context.Context, identity Identity, openBrowser bool) (*Instance, error) {
	path, err := instancePath()
	if err != nil {
		return nil, err
	}
	return acquireOrActivate(ctx, path, identity, func() (Lease, error) { return acquire(MutexName) }, openBrowser, OpenBrowser)
}

func acquireOrActivate(ctx context.Context, path string, identity Identity, acquireLease func() (Lease, error), openBrowser bool, browser func(string) error) (*Instance, error) {
	if !identity.valid() {
		return nil, errors.New("无效的 SWD 实例身份")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var address string
	var open func() error
	if openBrowser {
		open = func() error { return browser("http://" + address) }
	}
	lease, err := elect(ctx, acquireLease, func(ctx context.Context) (bool, error) {
		record := readInstanceRecord(path)
		if record == nil || !probe(ctx, record.Address, record.Identity) {
			return false, nil
		}
		// Revalidate process/port ownership after the HTTP round trip.
		if current := readInstanceRecord(path); current == nil || *current != *record {
			return false, nil
		}
		if !identity.sameInstallation(record.Identity) {
			return false, record.Identity.conflict()
		}
		address = record.Address
		return true, nil
	}, open)
	if err != nil || lease == nil {
		return nil, err
	}
	instance := &Instance{lease: lease, path: path, identity: identity}
	// Clear crash leftovers before the new owner starts serving.
	if err := instance.clear(); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return instance, nil
}

func probe(ctx context.Context, address string, expected Identity) bool {
	if !expected.valid() || !validAddress(address) {
		return false
	}
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
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&identity) == nil && identity == expected
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
	StartAcceleration
	Exit
	StopAcceleration
)

type State struct {
	Theme                string
	CDN, Batch           bool
	Ready, Activating    bool
	TermsAccepted        bool
	NeedsExitPreparation bool
}
type Item struct {
	ID       int
	Label    string
	Disabled bool
}

func Menu(s State, busy bool) []Item {
	busy = busy || s.Activating
	command := StartAcceleration
	label := "开始加速"
	if s.CDN {
		command, label = StopAcceleration, "停止加速"
	}
	if !s.TermsAccepted {
		label = "开始加速（需先同意条款）"
	}
	return []Item{{OpenConsole, "打开 WebUI", false}, {0, "", false}, {command, label, !s.TermsAccepted || busy}, {0, "", false}, {Exit, "退出 Still Wanna Dance", false}}
}

type Options struct {
	URL     string
	Open    bool
	State   func() State
	Command func(int) error
	// RequestExit records an explicit tray/session exit before cleanup starts.
	// Context cancellation alone must not be reported as a planned exit.
	RequestExit func()
	// Shutdown must close both the HTTP server and cache engine, and be idempotent.
	Shutdown func()
	// EndSession optionally cancels graceful waiting before Shutdown on Windows
	// session end, including when Shutdown is already running. It must not block.
	EndSession func()
}
