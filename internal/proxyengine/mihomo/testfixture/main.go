// fixture is a separate server process. Its inbound imports and initialization
// must never be linked into the embedding probe executable.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/listener/anytls"
	LC "github.com/metacubex/mihomo/listener/config"
	"github.com/metacubex/mihomo/listener/sing_hysteria2"
	"github.com/metacubex/mihomo/listener/sing_shadowsocks"
	"github.com/metacubex/mihomo/listener/sing_vless"
	"github.com/metacubex/mihomo/listener/sing_vmess"
)

type fixtureTunnel struct {
	mu      sync.RWMutex
	targets map[string]string
}

func (f *fixtureTunnel) HandleTCPConn(conn net.Conn, metadata *C.Metadata) {
	defer conn.Close()
	f.mu.RLock()
	destination := f.targets[metadata.RemoteAddress()]
	f.mu.RUnlock()
	if destination == "" {
		return
	} // This fixture is never a general relay.
	upstream, err := net.DialTimeout("tcp", destination, time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{})
	go func() {
		io.Copy(upstream, conn)
		if c, ok := upstream.(*net.TCPConn); ok {
			c.CloseWrite()
		}
		close(done)
	}()
	io.Copy(conn, upstream)
	conn.Close()
	upstream.Close()
	<-done
}
func (*fixtureTunnel) HandleUDPPacket(p C.UDPPacket, _ *C.Metadata) { p.Drop() }
func (*fixtureTunnel) NatTable() C.NatTable                         { return nil }

func certificate() (string, string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "proxy.invalid"}, DNSNames: []string{"proxy.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", "", err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})), hex.EncodeToString(sum[:]), nil
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: fixture manifest.json")
	}
	counts := map[string]*countingListen{}
	for _, name := range []string{"ss", "vless", "anytls", "hysteria2", "vmess"} {
		counts[name] = &countingListen{udpPeers: map[string]bool{}}
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stats" {
			values := map[string]int64{}
			for k, v := range counts {
				values[k] = v.count()
			}
			json.NewEncoder(w).Encode(values)
			return
		}
		if r.Host != "business.invalid" {
			http.Error(w, "wrong destination", 400)
			return
		}
		if r.URL.Path == "/stall" {
			<-r.Context().Done()
			return
		}
		// Deterministic development benchmark payload; never used by product code.
		if r.URL.Path == "/resource" {
			w.Header().Set("Content-Length", strconv.Itoa(16<<20))
			block := make([]byte, 64<<10)
			for i := 0; i < 256; i++ {
				if _, err := w.Write(block); err != nil {
					return
				}
			}
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "mihomo-embedded-business-response\n")
	}))
	defer target.Close()
	tunnel := &fixtureTunnel{targets: map[string]string{"business.invalid:80": target.Listener.Addr().String()}}
	cert, key, fingerprint, err := certificate()
	if err != nil {
		return err
	}
	const password = "local-experiment-only"
	const uuid = "d342d11e-d424-4583-b36e-524ab1f0afa4"
	ss, err := sing_shadowsocks.New(LC.ShadowsocksServer{Listen: "127.0.0.1:0", Cipher: "aes-128-gcm", Password: password}, counts["ss"], tunnel)
	if err != nil {
		return err
	}
	defer ss.Close()
	vl, err := sing_vless.New(LC.VlessServer{Listen: "127.0.0.1:0", Users: []LC.VlessUser{{Username: "test", UUID: uuid}}, Certificate: cert, PrivateKey: key}, counts["vless"], tunnel)
	if err != nil {
		return err
	}
	defer vl.Close()
	at, err := anytls.New(LC.AnyTLSServer{Listen: "127.0.0.1:0", Users: map[string]string{"test": password}, Certificate: cert, PrivateKey: key}, counts["anytls"], tunnel)
	if err != nil {
		return err
	}
	defer at.Close()
	hy, err := sing_hysteria2.New(LC.Hysteria2Server{Listen: "127.0.0.1:0", Users: map[string]string{"test": password}, Certificate: cert, PrivateKey: key}, counts["hysteria2"], tunnel)
	if err != nil {
		return err
	}
	defer hy.Close()
	vm, err := sing_vmess.New(LC.VmessServer{Listen: "127.0.0.1:0", Users: []LC.VmessUser{{Username: "test", UUID: uuid}}, WsPath: "/stream"}, counts["vmess"], tunnel)
	if err != nil {
		return err
	}
	defer vm.Close()
	var nodes []map[string]any
	for _, item := range []struct {
		protocol string
		listener C.MultiAddrListener
	}{{"ss", ss}, {"vless", vl}, {"anytls", at}, {"hysteria2", hy}, {"vmess", vm}} {
		address := item.listener.AddrList()[0].String()
		_, portText, _ := net.SplitHostPort(address)
		port, _ := strconv.Atoi(portText)
		node := map[string]any{"name": item.protocol, "type": item.protocol, "server": "proxy.invalid", "port": port}
		if item.protocol == "ss" {
			node["cipher"] = "aes-128-gcm"
			node["password"] = password
		} else if item.protocol == "vmess" {
			node["uuid"] = uuid
			node["cipher"] = "auto"
			node["alterId"] = 0
			node["network"] = "ws"
			node["ws-opts"] = map[string]any{"path": "/stream"}
		} else {
			node["fingerprint"] = fingerprint
			if item.protocol == "vless" {
				node["uuid"] = uuid
				node["tls"] = true
				node["servername"] = "proxy.invalid"
			} else {
				node["password"] = password
				node["sni"] = "proxy.invalid"
			}
		}
		nodes = append(nodes, node)
		tunnel.mu.Lock()
		tunnel.targets[net.JoinHostPort("proxy.invalid", portText)] = address
		tunnel.mu.Unlock()
	}
	data, err := json.Marshal(map[string]any{"nodes": nodes, "pid": os.Getpid(), "statsURL": target.URL + "/stats"})
	if err != nil {
		return err
	}
	if err := os.WriteFile(os.Args[1], data, 0600); err != nil {
		return err
	}
	// Bounded even if the experiment runner crashes. Only loopback is exposed.
	time.Sleep(5 * time.Minute)
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
