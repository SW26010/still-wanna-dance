// A separate, loopback-only Hysteria2 peer. Uses the pinned upstream service;
// the gate delays its TCP response, not the client's Go implementation.
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/metacubex/quic-go"
	"github.com/metacubex/sing-quic/hysteria2"
	"github.com/metacubex/sing/common/logger"
	M "github.com/metacubex/sing/common/metadata"
	N "github.com/metacubex/sing/common/network"
	"github.com/metacubex/tls"
)

type peer struct {
	mu                                       sync.Mutex
	gate                                     chan struct{}
	held, peak, entered, completed, canceled int
	handshakes                               int
	peers                                    map[string]bool
}

func (p *peer) NewConnection(_ context.Context, c net.Conn, metadata M.Metadata) error {
	defer c.Close()
	// Never relay to arbitrary destinations or interpret a supplied URL.
	if metadata.Destination.Port != 80 || (metadata.Destination.Fqdn != "business.invalid" && metadata.Destination.Fqdn != "hold.invalid") {
		return fmt.Errorf("fixture destination rejected")
	}
	reader := bufio.NewReader(c)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return err
	}
	req.Body.Close()
	p.mu.Lock()
	p.peers[metadata.Source.String()] = true
	gate := p.gate
	held := metadata.Destination.Fqdn == "hold.invalid"
	if held {
		p.held++
		p.entered++
		p.peak = max(p.peak, p.held)
	}
	p.mu.Unlock()
	if held {
		defer func() { p.mu.Lock(); p.held--; p.mu.Unlock() }()
		// Read observes the client's FIN/reset independently of gate release.
		ended := make(chan struct{})
		go func() { io.Copy(io.Discard, reader); close(ended) }()
		defer func() { c.Close(); <-ended }()
		select {
		case <-gate:
		case <-ended:
			p.mu.Lock()
			p.canceled++
			p.mu.Unlock()
			return nil
		case <-time.After(15 * time.Second):
			return fmt.Errorf("fixture gate safety timeout")
		}
	}
	_, err = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
	if err == nil {
		p.mu.Lock()
		p.completed++
		p.mu.Unlock()
	}
	return err
}
func (*peer) NewPacketConnection(_ context.Context, c N.PacketConn, _ M.Metadata) error {
	return c.Close()
}

func (p *peer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.URL.Path {
	case "/release":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		select {
		case <-p.gate:
		default:
			close(p.gate)
		}
	case "/arm":
		if r.Method != "POST" || p.held != 0 {
			w.WriteHeader(409)
			return
		}
		p.gate = make(chan struct{})
	case "/stats":
	default:
		w.WriteHeader(404)
		return
	}
	json.NewEncoder(w).Encode(map[string]int{"held": p.held, "peak": p.peak, "entered": p.entered, "completed": p.completed, "canceled": p.canceled, "peers": len(p.peers), "handshakes": p.handshakes})
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: hotfixture manifest.json stream-limit")
	}
	limit, err := strconv.Atoi(os.Args[2])
	if err != nil || limit < 2 || limit > 128 {
		return fmt.Errorf("invalid limit")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"proxy.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return err
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}))
	if err != nil {
		return err
	}
	p := &peer{gate: make(chan struct{}), peers: map[string]bool{}}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{pair}, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		p.mu.Lock()
		p.handshakes++
		p.mu.Unlock()
		return nil, nil // Use the unchanged original TLS config.
	}}
	service, err := hysteria2.NewService[string](hysteria2.ServiceOptions{Context: context.Background(), Logger: logger.NOP(), TLSConfig: tlsConfig, QUICConfig: &quic.Config{MaxIncomingStreams: int64(limit)}, Handler: p})
	if err != nil {
		return err
	}
	service.UpdateUsers([]string{"fixture"}, []string{"local-experiment-only"})
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer udp.Close()
	if err = service.Start(udp); err != nil {
		return err
	}
	defer service.Close()
	control, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: p, ReadHeaderTimeout: time.Second}
	defer server.Close()
	go server.Serve(control)
	port := udp.LocalAddr().(*net.UDPAddr).Port
	data, _ := json.Marshal(map[string]any{"control": "http://" + control.Addr().String(), "node": map[string]any{"type": "hysteria2", "server": "127.0.0.1", "port": port, "password": "local-experiment-only", "skip-cert-verify": true, "sni": "proxy.invalid"}})
	if err = os.WriteFile(os.Args[1], data, 0600); err != nil {
		return err
	}
	// Runner closes stdin for graceful fixture cleanup; deadline bounds crashes.
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, os.Stdin); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
