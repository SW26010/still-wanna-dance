//go:build windows && cf_block_experiment

package console

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.getoutline.org/sdk/transport"
	"golang.org/x/sys/windows"
	"still-wanna-dance/internal/upstreamrequest"
)

var cfexpNetworkConfig = flag.String("cfexp-network", "", "private JSON network profile; required for live CF experiments")

type cfNetworkConfig struct {
	InterfaceGUID string `json:"interfaceGUID"`
}

func cfReadNetworkConfig(path string) (cfNetworkConfig, error) {
	var cfg cfNetworkConfig
	if path == "" {
		return cfg, errors.New("explicit -cfexp-network required; no default-interface fallback")
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 4096 {
		return cfg, errors.New("network profile is unreadable or larger than 4 KiB")
	}
	d := json.NewDecoder(io.LimitReader(f, 4097))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("network profile must contain one JSON object")
	}
	guid, err := windows.GUIDFromString(cfg.InterfaceGUID)
	if err != nil || guid == (windows.GUID{}) {
		return cfg, errors.New("network profile requires a nonzero interfaceGUID")
	}
	cfg.InterfaceGUID = guid.String()
	return cfg, nil
}

type cfAdapter struct {
	GUID, Name     string
	Index4, Index6 uint32
	Up             bool
	Addresses      []string
}

// Use the persistent adapter GUID, not an interface index saved on another boot.
// Only preferred, usable unicast addresses can become explicitly bound sources.
func cfReadAdapter(guid string) (cfAdapter, error) {
	size := uint32(15000)
	for {
		buf := make([]byte, size)
		head := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER, 0, head, &size)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) && size > uint32(len(buf)) {
			continue
		}
		if err != nil {
			return cfAdapter{}, err
		}
		for a := head; a != nil; a = a.Next {
			if !strings.EqualFold(windows.BytePtrToString(a.AdapterName), guid) {
				continue
			}
			out := cfAdapter{GUID: guid, Name: windows.UTF16PtrToString(a.FriendlyName), Index4: a.IfIndex, Index6: a.Ipv6IfIndex, Up: a.OperStatus == windows.IfOperStatusUp}
			for u := a.FirstUnicastAddress; u != nil; u = u.Next {
				ip := u.Address.IP()
				// IPv6 ULA is not an Internet source for these experiments.
				// Private IPv4 is normal.
				if u.DadState == windows.IpDadStatePreferred && u.PreferredLifetime > 0 && ip.IsGlobalUnicast() && (ip.To4() != nil || !ip.IsPrivate()) {
					out.Addresses = append(out.Addresses, ip.String())
				}
			}
			return out, nil
		}
		return cfAdapter{}, errors.New("selected network adapter not found")
	}
}

func (a cfAdapter) source(v4 bool) net.IP {
	for _, text := range a.Addresses {
		if ip := net.ParseIP(text); ip != nil && (ip.To4() != nil) == v4 {
			return ip
		}
	}
	return nil
}

func (a cfAdapter) validate(initial cfAdapter) error {
	if a.GUID != initial.GUID || a.Index4 != initial.Index4 || a.Index6 != initial.Index6 {
		return errors.New("selected adapter identity/index changed; start a new batch")
	}
	if !a.Up || a.Index4 == 0 || a.Index6 == 0 || a.source(true) == nil || a.source(false) == nil {
		return errors.New("selected adapter requires usable IPv4 and IPv6; no fallback")
	}
	return nil
}

// Windows expects IPv4's index in network byte order, IPv6's in host order.
// Both get operations return a host-order index. This touches only this socket.
func cfBindSocket(c syscall.RawConn, network string, index uint32) error {
	level := windows.IPPROTO_IPV6
	value := int(index)
	if network == "tcp4" {
		level = windows.IPPROTO_IP
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], index)
		value = int(binary.NativeEndian.Uint32(b[:]))
	} else if network != "tcp6" {
		return net.UnknownNetworkError(network)
	}
	if index == 0 {
		return errors.New("zero interface index would allow default routing")
	}
	var socketErr error
	err := c.Control(func(fd uintptr) {
		// IP_UNICAST_IF and IPV6_UNICAST_IF both have option number 31.
		socketErr = windows.SetsockoptInt(windows.Handle(fd), level, 31, value)
		if socketErr == nil {
			var got int
			got, socketErr = windows.GetsockoptInt(windows.Handle(fd), level, 31)
			if socketErr == nil && uint32(got) != index {
				socketErr = errors.New("interface socket option readback mismatch")
			}
		}
	})
	return errors.Join(err, socketErr)
}

type cfConnection struct {
	Started, Finished                     time.Time
	Network, Target, Local, Remote, Error string
	InterfaceIndex                        uint32
}

type cfNetwork struct {
	ctx         context.Context
	initial     cfAdapter
	readAdapter func(string) (cfAdapter, error)
	bindSocket  func(syscall.RawConn, string, uint32) error
	cancel      context.CancelFunc
	mu          sync.Mutex
	failure     error
	log         io.Writer
	closed      bool
	active      sync.WaitGroup
}

func (n *cfNetwork) fail(err error) error {
	n.mu.Lock()
	if n.failure == nil {
		n.failure = err
	}
	n.mu.Unlock()
	n.cancel()
	return err
}

func (n *cfNetwork) err() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.failure
}

func (n *cfNetwork) adapter() (cfAdapter, error) {
	if err := n.err(); err != nil {
		return cfAdapter{}, err
	}
	a, err := n.readAdapter(n.initial.GUID)
	if err == nil {
		err = a.validate(n.initial)
	}
	if err != nil {
		return a, n.fail(err)
	}
	return a, nil
}

func (n *cfNetwork) record(row cfConnection) error {
	n.mu.Lock()
	err := json.NewEncoder(n.log).Encode(row)
	n.mu.Unlock()
	if err != nil {
		return n.fail(fmt.Errorf("save connection evidence: %w", err))
	}
	return nil
}

func (n *cfNetwork) DialContext(ctx context.Context, network, address string) (conn net.Conn, err error) {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil, net.ErrClosed
	}
	n.active.Add(1)
	n.mu.Unlock()
	defer n.active.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(n.ctx, cancel)
	defer func() { stop(); cancel() }()
	row := cfConnection{Started: time.Now(), Network: network, Target: address}
	defer func() {
		row.Finished = time.Now()
		if err != nil {
			row.Error = err.Error() // Only socket/IP errors, never signed URLs.
		}
		if logErr := n.record(row); logErr != nil {
			if conn != nil {
				conn.Close()
				conn = nil
			}
			err = errors.Join(err, logErr)
		}
	}()
	if err := n.ctx.Err(); err != nil {
		return nil, err
	}
	host, _, parseErr := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if parseErr != nil || ip == nil || !ip.IsGlobalUnicast() || (network != "tcp" && network != "tcp4" && network != "tcp6") {
		return nil, n.fail(errors.New("bound experiment dial requires a unicast IP:port over TCP"))
	}
	v4 := ip.To4() != nil
	if network == "tcp4" && !v4 || network == "tcp6" && v4 {
		return nil, n.fail(errors.New("destination address family mismatch"))
	}
	a, err := n.adapter()
	if err != nil {
		return nil, err
	}
	network, row.InterfaceIndex = "tcp6", a.Index6
	if v4 {
		network, row.InterfaceIndex = "tcp4", a.Index4
	}
	row.Network = network
	d := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second, LocalAddr: &net.TCPAddr{IP: a.source(v4)},
		Control: func(network, _ string, c syscall.RawConn) error {
			if err := n.bindSocket(c, network, row.InterfaceIndex); err != nil {
				return n.fail(fmt.Errorf("bind network interface: %w", err))
			}
			return nil
		},
	}
	conn, err = d.DialContext(ctx, network, address)
	if err != nil {
		return nil, err // Remote endpoint failures retain normal experiment policy.
	}
	row.Local, row.Remote = conn.LocalAddr().String(), conn.RemoteAddr().String()
	current, checkErr := n.adapter()
	local, _, _ := net.SplitHostPort(row.Local)
	if checkErr == nil && !slices.Contains(current.Addresses, local) {
		checkErr = n.fail(errors.New("connection source is not on the selected adapter"))
	}
	if checkErr != nil {
		conn.Close()
		return nil, checkErr
	}
	return conn, nil
}

func (n *cfNetwork) DialStream(ctx context.Context, address string) (transport.StreamConn, error) {
	c, err := n.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	return c.(*net.TCPConn), nil
}

func (n *cfNetwork) pool(dns upstreamrequest.DNSPool) (*upstreamrequest.Pool, error) {
	return upstreamrequest.NewDirectPool(dns, n.DialContext)
}

func (n *cfNetwork) watch(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := n.adapter(); err != nil {
				return
			}
		}
	}
}

func cfexpStartNetwork(t *testing.T, ctx context.Context, cancel context.CancelFunc, dir string) *cfNetwork {
	t.Helper()
	cfg, err := cfReadNetworkConfig(*cfexpNetworkConfig)
	if err != nil {
		t.Fatal(err)
	}
	a, err := cfReadAdapter(cfg.InterfaceGUID)
	if err == nil {
		err = a.validate(a)
	}
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "connections-private.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	n := &cfNetwork{ctx: ctx, initial: a, readAdapter: cfReadAdapter, bindSocket: cfBindSocket, cancel: cancel, log: f}
	watchCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); n.watch(watchCtx) }()
	save := func() {
		failure := ""
		if err := n.err(); err != nil {
			failure = err.Error()
		}
		if err := cfexpSave(filepath.Join(dir, "network-private.json"), struct {
			Config  cfNetworkConfig
			Adapter cfAdapter
			Failure string
		}{cfg, a, failure}); err != nil {
			t.Error(err)
			cancel()
		}
	}
	t.Cleanup(func() {
		stop()
		<-done
		n.mu.Lock()
		n.closed = true
		n.mu.Unlock()
		cancel()
		n.active.Wait()
		if err := f.Close(); err != nil {
			t.Error(err)
		}
		save()
		if err := n.err(); err != nil {
			t.Error(err)
		}
	})
	save()
	if t.Failed() {
		t.FailNow()
	}
	return n
}
