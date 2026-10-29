package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	winio "github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
)

const hostsPipePrefix = `\\.\pipe\SWD-hosts-`

func launchHostsSession(ctx context.Context) (hostsConnection, error) {
	return connectHostsSession(ctx, elevateHostsHelper)
}

// The listener belongs to the ordinary application. The elevated client uses
// anonymous impersonation level (go-winio's default), so the server cannot
// impersonate it. Both ends verify the kernel-reported peer PID.
func connectHostsSession(ctx context.Context, launch func(context.Context, string) (uint32, error)) (hostsConnection, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	pipe := hostsPipePrefix + hex.EncodeToString(nonce[:])
	listener, err := winio.ListenPipe(pipe, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;BA)(A;;GA;;;" + user.User.Sid.String() + ")",
	})
	if err != nil {
		return nil, err
	}
	defer listener.Close()
	// UAC is allowed only here, from an explicit operation after preflight.
	pid, err := launch(ctx, pipe)
	if err != nil {
		return nil, err
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return nil, err
	}
	wait := func() error {
		defer windows.CloseHandle(process)
		status, err := windows.WaitForSingleObject(process, uint32(hostsReplyTimeout/time.Millisecond))
		if err != nil {
			return err
		}
		if status != windows.WAIT_OBJECT_0 {
			return errors.New("hosts 助手尚未结束，请检查游戏连接")
		}
		return nil
	}
	type accepted struct {
		conn net.Conn
		err  error
	}
	ready := make(chan accepted)
	acceptCtx, cancel := context.WithTimeout(ctx, hostsReplyTimeout)
	defer cancel()
	go func() {
		for {
			conn, err := listener.Accept()
			if err == nil {
				err = verifyHostsPeer(conn, pid, false)
				if err != nil {
					conn.Close()
					continue
				}
			}
			select {
			case ready <- accepted{conn, err}:
			case <-acceptCtx.Done():
				if conn != nil {
					conn.Close()
				}
			}
			return
		}
	}()
	select {
	case r := <-ready:
		if r.err == nil {
			return newHostsClient(r.conn, wait), nil
		}
		err = r.err
	case <-acceptCtx.Done():
		err = acceptCtx.Err()
	}
	listener.Close()
	_ = wait()
	return nil, fmt.Errorf("无法连接 hosts 助手：%w", err)
}

func elevateHostsHelper(ctx context.Context, pipe string) (uint32, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	script := hostsElevationScript(exe, pipe, os.Getpid())
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return hostsElevationResult(out, err)
}

func hostsElevationScript(exe, pipe string, parent int) string {
	// Pipe names contain only a fixed prefix and a generated hexadecimal nonce.
	// Windows PowerShell's Start-Process discards the native exception when
	// shell execution fails. Process.Start retains it for exact cancellation
	// classification; other localized failures must remain visible.
	return "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); $ErrorActionPreference = 'Stop'; try { $startInfo = [System.Diagnostics.ProcessStartInfo]::new(); $startInfo.FileName = '" + strings.ReplaceAll(exe, "'", "''") +
		"'; $startInfo.Arguments = '-hosts-session " + pipe + " -hosts-parent " + strconv.Itoa(parent) +
		"'; $startInfo.UseShellExecute = $true; $startInfo.Verb = 'runas'; $startInfo.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden; $p = [System.Diagnostics.Process]::Start($startInfo); $p.Id } catch { $e = $_.Exception; while ($null -ne $e) { if ($e -is [System.ComponentModel.Win32Exception] -and $e.NativeErrorCode -eq " + strconv.Itoa(int(windows.ERROR_CANCELLED)) +
		") { exit " + strconv.Itoa(int(windows.ERROR_CANCELLED)) + " }; $e = $e.InnerException }; [Console]::Error.WriteLine($_.Exception.Message); exit 1 }"
}

func hostsElevationResult(out []byte, err error) (uint32, error) {
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == int(windows.ERROR_CANCELLED) {
			return 0, errHostsAuthorizationCanceled
		}
		return 0, fmt.Errorf("hosts 授权未完成：%s %w", strings.TrimSpace(string(out)), err)
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 32)
	if err != nil || pid == 0 {
		return 0, errors.New("无法确认 hosts 助手进程")
	}
	return uint32(pid), nil
}

func verifyHostsPeer(conn net.Conn, expected uint32, server bool) error {
	file, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("无法验证 hosts 助手连接")
	}
	var pid uint32
	var err error
	if server {
		err = windows.GetNamedPipeServerProcessId(windows.Handle(file.Fd()), &pid)
	} else {
		err = windows.GetNamedPipeClientProcessId(windows.Handle(file.Fd()), &pid)
	}
	if err != nil {
		return err
	}
	if pid != expected {
		return errors.New("hosts 助手连接的进程身份不匹配")
	}
	return nil
}

func dialHostsParent(pipe string, parent uint32) (net.Conn, error) {
	if parent == 0 || !strings.HasPrefix(pipe, hostsPipePrefix) {
		return nil, errors.New("无效的 hosts 助手参数")
	}
	nonce, err := hex.DecodeString(strings.TrimPrefix(pipe, hostsPipePrefix))
	if err != nil || len(nonce) != 16 {
		return nil, errors.New("无效的 hosts 会话")
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostsReplyTimeout)
	defer cancel()
	conn, err := winio.DialPipeContext(ctx, pipe)
	if err != nil {
		return nil, err
	}
	if err = verifyHostsPeer(conn, parent, true); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// RunHostsSession runs before configuration, logging, tray and service startup.
// No caller-supplied path or arbitrary command crosses this elevated boundary.
func RunHostsSession(pipe string, parent uint32) error {
	conn, err := dialHostsParent(pipe, parent)
	if err != nil {
		return err
	}
	return serveHostsSession(conn, hostsPath(), FlushDNS)
}
