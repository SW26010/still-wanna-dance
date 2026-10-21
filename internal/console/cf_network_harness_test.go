//go:build windows && cf_block_experiment

package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"golang.getoutline.org/sdk/transport"
	"golang.org/x/sys/windows"
)

func TestCFNetworkConfig(t *testing.T) {
	const guid = "{12345678-1234-1234-1234-123456789ABC}"
	for _, body := range []string{"{}", "null", `{"interfaceGUID":""}`, `{"interfaceGUID":"Wi-Fi"}`, `{"interfaceGUID":"{00000000-0000-0000-0000-000000000000}"}`, `{"interfaceGUID":"` + guid + `","fallback":true}`, `{"interfaceGUID":"` + guid + `"} {}`, strings.Repeat(" ", 5000)} {
		p := filepath.Join(t.TempDir(), "profile.json")
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := cfReadNetworkConfig(p); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
	for _, p := range []string{"", filepath.Join(t.TempDir(), "missing.json")} {
		if _, err := cfReadNetworkConfig(p); err == nil {
			t.Fatal("missing profile accepted")
		}
	}
	p := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(p, []byte(`{"interfaceGUID":"`+strings.ToLower(guid)+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfReadNetworkConfig(p)
	if err != nil || !strings.EqualFold(cfg.InterfaceGUID, guid) {
		t.Fatal(cfg, err)
	}
}

func cfFixtureNetwork() (*cfNetwork, *bytes.Buffer) {
	a := cfAdapter{GUID: "fixture", Index4: 1, Index6: 1, Up: true, Addresses: []string{"127.0.0.1", "::1"}}
	ctx, cancel := context.WithCancel(context.Background())
	log := new(bytes.Buffer)
	return &cfNetwork{ctx: ctx, initial: a, readAdapter: func(string) (cfAdapter, error) { return a, nil }, bindSocket: cfBindSocket, cancel: cancel, log: log}, log
}

func TestCFNetworkFailClosed(t *testing.T) {
	for _, fault := range []string{"missing", "down", "v4-only", "v6-only", "index-change", "identity-change"} {
		t.Run(fault, func(t *testing.T) {
			n, log := cfFixtureNetwork()
			defer n.cancel()
			a := n.initial
			switch fault {
			case "down":
				a.Up = false
			case "v4-only":
				a.Addresses = []string{"127.0.0.1"}
			case "v6-only":
				a.Addresses = []string{"::1"}
			case "index-change":
				a.Index4++
			case "identity-change":
				a.GUID = "replacement"
			}
			n.readAdapter = func(string) (cfAdapter, error) {
				if fault == "missing" {
					return cfAdapter{}, errors.New("not found")
				}
				return a, nil
			}
			n.bindSocket = func(syscall.RawConn, string, uint32) error {
				t.Error("socket reached after invalid adapter")
				return errors.New("unexpected socket")
			}
			c, err := n.DialContext(context.Background(), "tcp", "192.0.2.1:443")
			if c != nil || err == nil || n.err() == nil || n.ctx.Err() == nil {
				t.Fatal("invalid adapter did not stop batch", c, err)
			}
			// Reappearance does not resume this batch or select a replacement.
			n.readAdapter = func(string) (cfAdapter, error) { t.Error("retried after fatal binding error"); return n.initial, nil }
			if c, err := n.DialContext(context.Background(), "tcp", "192.0.2.1:443"); c != nil || err == nil {
				t.Fatal("resumed failed batch")
			}
			var row cfConnection
			if err := json.NewDecoder(log).Decode(&row); err != nil || row.Error == "" {
				t.Fatal("failure evidence missing", row, err)
			}
		})
	}
}

func TestCFNetworkBindFailure(t *testing.T) {
	for _, target := range []string{"192.0.2.1:443", "[2001:db8::1]:443"} {
		n, _ := cfFixtureNetwork()
		bindErr := errors.New("injected socket option failure")
		calls := 0
		n.bindSocket = func(syscall.RawConn, string, uint32) error { calls++; return bindErr }
		conn, err := n.DialContext(context.Background(), "tcp", target)
		n.cancel()
		if conn != nil || !errors.Is(err, bindErr) || calls != 1 || !errors.Is(n.err(), bindErr) {
			t.Fatal("binding failure must precede connect and never retry unbound", calls, err)
		}
	}
}

func TestCFNetworkRejectsImplicitDNS(t *testing.T) {
	for _, target := range []string{"example.com:443", "127.0.0.1:443", "[::1]:443", "0.0.0.0:443"} {
		n, _ := cfFixtureNetwork()
		n.readAdapter = func(string) (cfAdapter, error) {
			t.Error("invalid destination reached adapter lookup")
			return n.initial, nil
		}
		c, err := n.DialContext(context.Background(), "tcp", target)
		n.cancel()
		if c != nil || err == nil {
			t.Fatal("accepted non-experiment destination", target)
		}
	}
}

func TestCFNetworkWatchStopsIdleBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, _ := cfFixtureNetwork()
		defer n.cancel()
		n.readAdapter = func(string) (cfAdapter, error) { a := n.initial; a.Up = false; return a, nil }
		go n.watch(n.ctx)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if n.ctx.Err() == nil || n.err() == nil {
			t.Fatal("idle disconnected batch was not canceled")
		}
	})
}

type cfRawSocket windows.Handle

func (s cfRawSocket) Control(f func(uintptr)) error  { f(uintptr(s)); return nil }
func (s cfRawSocket) Read(func(uintptr) bool) error  { return errors.New("unused") }
func (s cfRawSocket) Write(func(uintptr) bool) error { return errors.New("unused") }

func TestCFNetworkSocketOptions(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var index int
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			index = iface.Index
			break
		}
	}
	if index == 0 {
		t.Fatal("Windows loopback interface missing")
	}
	var data windows.WSAData
	if err := windows.WSAStartup(0x202, &data); err != nil {
		t.Fatal(err)
	}
	defer windows.WSACleanup()
	for _, family := range []int{windows.AF_INET, windows.AF_INET6} {
		s, err := windows.Socket(family, windows.SOCK_STREAM, windows.IPPROTO_TCP)
		if err != nil {
			t.Fatal(err)
		}
		network := "tcp4"
		if family == windows.AF_INET6 {
			network = "tcp6"
		}
		err = cfBindSocket(cfRawSocket(s), network, uint32(index))
		windows.Closesocket(s)
		if err != nil {
			t.Fatal(network, err)
		}
	}
}

func TestCFNetworkDNSInjection(t *testing.T) {
	var mu sync.Mutex
	var addresses []string
	denied := errors.New("fixture refuses outbound connections")
	d := &directDNS{dialer: transport.FuncStreamDialer(func(ctx context.Context, address string) (transport.StreamConn, error) {
		mu.Lock()
		addresses = append(addresses, address)
		mu.Unlock()
		host, _, err := net.SplitHostPort(address)
		if err != nil || net.ParseIP(host) == nil {
			t.Errorf("dialer received unresolved address %q", address)
		}
		return nil, denied
	})}
	defer d.close()
	ctx := context.Background()
	if c, err := d.DialContext(ctx, "tcp", "192.0.2.1:443"); c != nil || err == nil {
		t.Fatal("ordinary dial bypassed injected dependency")
	}
	if _, _, err := d.queryProvider(ctx, dohProviders[0], "example.com", dnsTypeA); err == nil {
		t.Fatal("DoH bypassed injected dependency")
	}
	if _, err := d.bootstrapProvider(ctx, "doh.pub"); err == nil {
		t.Fatal("bootstrap bypassed injected dependency")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(addresses) < 6 || addresses[0] != "192.0.2.1:443" {
		t.Fatal("missing ordinary/DoH/bootstrap attempts", addresses)
	}
}

type cfBrokenWriter struct{}

func (cfBrokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCFNetworkEvidenceFailureStopsBatch(t *testing.T) {
	n, _ := cfFixtureNetwork()
	defer n.cancel()
	n.log = cfBrokenWriter{}
	if err := n.record(cfConnection{}); !errors.Is(err, io.ErrClosedPipe) || n.ctx.Err() == nil {
		t.Fatal("lost evidence did not stop batch", err)
	}
}
