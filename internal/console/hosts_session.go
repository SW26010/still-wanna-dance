package console

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/legal"
)

const hostsReplyTimeout = 10 * time.Second

var (
	errHostsRestorePermission     = errors.New("尚未取得恢复权限，请点击游戏连接状态中的「修复」后重试")
	errHostsAuthorizationCanceled = errors.New("管理员授权已取消")
)

// The protocol has no paths, file contents or executable commands. Both modes
// use the same session; only the cleanup policy differs.
type hostsRequest struct {
	Action        string `json:"action"`
	RestoreOnExit bool   `json:"restoreOnExit"`
	TermsVersion  string `json:"termsVersion,omitempty"`
}

type hostsReply struct {
	Error string `json:"error,omitempty"`
}

type hostsConnection interface {
	request(hostsRequest) error
	close() error
}

// Serialized by Console.lifecycleMu, independently of ordinary status reads.
type hostsController struct {
	path             string
	launch           func(context.Context) (hostsConnection, error)
	session          hostsConnection
	ctx              context.Context
	cancel           context.CancelFunc
	closed           bool
	err              error
	prepared         atomic.Bool            // helper acknowledged recovery capability; not an exit policy
	authorizationID  atomic.Pointer[string] // identity of the latest successful helper authorization
	rollbackReady    bool                   // only the most recent dispatched enable may be rolled back
	previousPrepared bool
}

func newHostsController(path string, launch func(context.Context) (hostsConnection, error)) *hostsController {
	ctx, cancel := context.WithCancel(context.Background())
	h := &hostsController{path: path, ctx: ctx, cancel: cancel}
	h.launch = func(ctx context.Context) (hostsConnection, error) {
		session, err := launch(ctx)
		if err == nil {
			id := rand.Text()
			h.authorizationID.Store(&id)
		}
		return session, err
	}
	return h
}

func (h *hostsController) change(action string, restore bool) error {
	if h.closed || action != "rollback" && h.ctx.Err() != nil {
		return errors.New("程序正在退出")
	}
	if action == "rollback" {
		if !h.rollbackReady {
			return nil
		}
		err := h.session.request(hostsRequest{Action: "rollback"})
		if err == nil {
			h.rollbackReady = false
			h.prepared.Store(h.previousPrepared)
		}
		return err
	}
	h.rollbackReady = false
	data, err := os.ReadFile(h.path)
	if err != nil {
		return err
	}
	next, err := transformHosts(string(data), action)
	if err != nil {
		return err
	}
	// Reusing a ready mapping does not require restoration permission. The
	// user prepares it explicitly; starting acceleration must not retry UAC.
	if next == string(data) {
		return nil
	}
	if h.session == nil {
		if action == "disable" {
			return errHostsRestorePermission
		}
		h.session, err = h.launch(h.ctx)
		if err != nil {
			return err
		}
	}
	h.previousPrepared = h.prepared.Load()
	h.rollbackReady = action == "enable"
	err = h.session.request(hostsRequest{Action: action, RestoreOnExit: restore, TermsVersion: legal.Version})
	h.prepared.Store(err == nil && action == "enable" && restore)
	return err
}

// Prepare capability without changing hosts or the helper's crash cleanup.
// Normal exit receives the committed policy from Console.Close after saving.
func (h *hostsController) prepareRestore() error {
	if h.closed || h.ctx.Err() != nil {
		return errors.New("程序正在退出")
	}
	data, err := os.ReadFile(h.path)
	if err != nil {
		return err
	}
	if !hasManagedHosts(string(data)) {
		return nil
	}
	if _, err = transformHosts(string(data), "disable"); err != nil {
		return err
	}
	if h.session == nil {
		h.session, err = h.launch(h.ctx)
		if err != nil {
			return err
		}
	}
	err = h.session.request(hostsRequest{Action: "prepare", TermsVersion: legal.Version})
	h.prepared.Store(err == nil)
	return err
}

func (h *hostsController) close(restore bool) error {
	if h.closed {
		return h.err
	}
	h.closed = true
	h.cancel()
	if h.session != nil {
		// Never launch, reconnect, or retry elevation from this path.
		h.err = errors.Join(h.session.request(hostsRequest{Action: "close", RestoreOnExit: restore}), h.session.close())
	}
	return h.err
}

type hostsClient struct {
	conn   net.Conn
	scan   *bufio.Scanner
	wait   func() error
	once   sync.Once
	err    error
	failed error // an uncertain exchange permanently retires this stream
}

func newHostsClient(conn net.Conn, wait func() error) *hostsClient {
	return &hostsClient{conn: conn, scan: hostsScanner(conn), wait: wait}
}

func hostsScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 4096)
	return s
}

func (c *hostsClient) request(request hostsRequest) error {
	if c.failed != nil {
		return c.failed
	}
	fail := func(err error) error {
		c.failed = err
		_ = c.conn.Close() // Also releases the helper's crash-cleanup path.
		return err
	}
	_ = c.conn.SetDeadline(time.Now().Add(hostsReplyTimeout))
	if err := json.NewEncoder(c.conn).Encode(request); err != nil {
		return fail(fmt.Errorf("hosts 助手连接已中断，请重新打开应用后修复游戏连接：%w", err))
	}
	if !c.scan.Scan() {
		return fail(fmt.Errorf("hosts 助手未确认操作结果，请重新打开应用后检查游戏连接：%w", errors.Join(io.ErrUnexpectedEOF, c.scan.Err())))
	}
	var reply hostsReply
	if err := json.Unmarshal(c.scan.Bytes(), &reply); err != nil {
		return fail(err)
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	return nil
}

func (c *hostsClient) close() error {
	c.once.Do(func() {
		_ = c.conn.Close()
		if c.wait != nil {
			c.err = c.wait()
		}
	})
	return c.err
}

// Only the internal helper entry point supplies the real system path. Tests
// exercise this same loop with private files and unprivileged child processes.
// EOF also runs cleanup, covering a main-process crash while the helper lives.
func serveHostsSession(conn net.Conn, path string, flush func()) (result error) {
	defer conn.Close()
	restore := false
	var previous, applied []byte
	previousRestore := false
	apply := func(action string) error {
		err := applyHosts(path, action)
		if err == nil {
			flush()
		}
		return err
	}
	defer func() {
		if restore {
			result = errors.Join(result, apply("disable"))
		}
	}()
	scan := hostsScanner(conn)
	for scan.Scan() {
		var request hostsRequest
		if err := json.Unmarshal(scan.Bytes(), &request); err != nil {
			return err
		}
		var err error
		switch request.Action {
		case "rollback":
			if previous != nil {
				var current []byte
				current, err = os.ReadFile(path)
				if err == nil && string(current) != string(previous) {
					if string(current) != string(applied) {
						err = errors.New("hosts 在接入后被修改，无法安全撤销本次接入，请检查游戏接入状态")
					} else {
						err = writeHostsChange(path, current, previous)
						if err == nil {
							flush()
						}
					}
				}
				if err == nil {
					restore = previousRestore
					previous, applied = nil, nil
				}
			}
		case "prepare":
			if err = legal.CheckAcceptance(request.TermsVersion); err == nil {
				var data []byte
				data, err = os.ReadFile(path)
				if err == nil {
					_, err = transformHosts(string(data), "disable")
				}
				if err == nil {
					var file *os.File
					file, err = stageHostsFile(path, data)
					if err == nil {
						err = errors.Join(file.Close(), os.Remove(file.Name()))
					}
				}
			}
		case "enable":
			previous, applied = nil, nil
			if err = legal.CheckAcceptance(request.TermsVersion); err == nil {
				var data []byte
				data, err = os.ReadFile(path)
				if err == nil {
					var next string
					next, err = transformHosts(string(data), "enable")
					if err == nil {
						previous, applied, previousRestore = data, []byte(next), restore
						if next != string(data) {
							err = writeHostsChange(path, data, applied)
							if err == nil {
								flush()
							}
						}
						if err == nil {
							restore = request.RestoreOnExit
						}
					}
				}
			}
		case "disable":
			err = apply("disable")
			if err == nil {
				restore = false
			}
		case "close":
			if request.RestoreOnExit {
				err = apply("disable")
			}
			restore = false // Report this result; do not silently retry after ack.
		default:
			err = errors.New("不支持的 hosts 助手操作")
		}
		reply := hostsReply{}
		if err != nil {
			reply.Error = err.Error()
		}
		_ = conn.SetWriteDeadline(time.Now().Add(hostsReplyTimeout))
		if writeErr := json.NewEncoder(conn).Encode(reply); writeErr != nil {
			return errors.Join(err, writeErr)
		}
		if request.Action == "close" {
			return err
		}
	}
	return scan.Err()
}
