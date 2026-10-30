package mihomo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/ca"
)

// The local CONNECT fixtures identify the actual exit, without public proxies.
func groupExit(t *testing.T, label string, health string, enabled *atomic.Bool) map[string]any {
	t.Helper()
	business := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, label)
	}))
	t.Cleanup(business.Close)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || !enabled.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		destination := ""
		switch r.Host {
		case "business.invalid:80":
			destination = business.Listener.Addr().String()
		case "www.gstatic.com:443":
			destination = health
		}
		if destination == "" {
			http.Error(w, "fixture destination only", 403)
			return
		}
		up, err := net.Dial("tcp", destination)
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		defer up.Close()
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		rw.Flush()
		done := make(chan struct{})
		go func() { io.Copy(up, rw); up.Close(); close(done) }()
		io.Copy(conn, up)
		conn.Close()
		<-done
	}))
	t.Cleanup(proxy.Close)
	host, port, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	return map[string]any{"name": label, "type": "http", "server": host, "port": n}
}

func TestStrategyGroupSwitchFallbackAndPersistence(t *testing.T) {
	if !runtimeProcess(t) {
		return
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"www.gstatic.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	health := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Millisecond); w.WriteHeader(204) }))
	health.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	health.StartTLS()
	defer health.Close()
	var a, b atomic.Bool
	a.Store(true)
	b.Store(true)
	configs := []Config{
		{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: groupExit(t, "A", health.Listener.Addr().String(), &a)},
		{ID: "stable-v1-" + strings.Repeat("b", 64), Parameters: groupExit(t, "B", health.Listener.Addr().String(), &b)},
	}
	home := t.TempDir()
	r, err := NewRuntime(configs, RuntimeOptions{Home: home, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// ApplyConfig resets roots. Trust only this fixture after initialization.
	if err := ca.AddCertificate(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))); err != nil {
		t.Fatal(err)
	}
	names := runtimeProxyNames(t, r)
	if len(names) != 3 || names[0] != automaticGroup {
		t.Fatal(names)
	}
	tr := &http.Transport{DialContext: r.DialContext}
	defer tr.CloseIdleConnections()
	read := func(want string) {
		t.Helper()
		resp, err := (&http.Client{Transport: tr, Timeout: 2 * time.Second}).Get("http://business.invalid/")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != want {
			t.Fatalf("exit=%q want=%s err=%v", body, want, err)
		}
	}
	selectRuntimeProxy(t, r, names[1])
	read("A")
	selectRuntimeProxy(t, r, names[2])
	read("A") // Existing pooled stream is preserved.
	tr.CloseIdleConnections()
	read("B")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/connections", nil))
	var connections struct {
		DownloadTotal int64 `json:"downloadTotal"`
		Connections   []any `json:"connections"`
	}
	if json.Unmarshal(w.Body.Bytes(), &connections) != nil || connections.DownloadTotal == 0 || len(connections.Connections) == 0 {
		t.Fatal("missing native traffic/connection tracking", w.Body.String())
	}
	// Native selection, API serialization, health tests and actual dials race in
	// normal dashboard use. Exercise them concurrently, not just sequentially.
	var concurrent sync.WaitGroup
	for worker := range 4 {
		concurrent.Go(func() {
			for i := range 12 {
				switch worker {
				case 0:
					w := httptest.NewRecorder()
					body, _ := json.Marshal(map[string]string{"name": names[i%3]})
					r.ServeHTTP(w, httptest.NewRequest("PUT", "/proxies/SWD", strings.NewReader(string(body))))
					if w.Code != 204 {
						t.Errorf("concurrent selection: %d", w.Code)
					}
				case 1, 2:
					path := "/proxies"
					if worker == 2 {
						path = "/group/" + url.PathEscape(automaticGroup) + "/delay?timeout=3000&url=" + url.QueryEscape("https://www.gstatic.com/generate_204")
					}
					w := httptest.NewRecorder()
					r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
					if w.Code != 200 {
						t.Errorf("concurrent native read/test: %d", w.Code)
					}
				case 3:
					conn, err := r.DialContext(t.Context(), "tcp", "business.invalid:80")
					if err != nil {
						t.Error(err)
					} else {
						conn.Close()
					}
				}
			}
		})
	}
	concurrent.Wait()
	// Exercise the native health observation used by the unmodified fallback.
	a.Store(false)
	for i, name := range names[1:] {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/proxies/"+url.PathEscape(name)+"/delay?timeout=3000&url="+url.QueryEscape("https://www.gstatic.com/generate_204"), nil))
		if (w.Code == 200) != (i == 1) {
			t.Fatal("native health API", name, w.Code, w.Body.String())
		}
	}
	selectRuntimeProxy(t, r, automaticGroup)
	tr.CloseIdleConnections()
	read("B")
	for _, methodPath := range [][2]string{{"PATCH", "/configs"}, {"PUT", "/configs"}, {"POST", "/restart"}, {"POST", "/upgrade"}, {"POST", "/upgrade/ui"}, {"PUT", "/proxies/GLOBAL"}, {"GET", "/logs"}, {"GET", "/cache"}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(methodPath[0], methodPath[1], strings.NewReader(`{}`)))
		if w.Code != 403 {
			t.Fatal("managed boundary", methodPath, w.Code)
		}
	}
	selectRuntimeProxy(t, r, names[2])
	tr.CloseIdleConnections()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	restart := func(expected string, transient bool) {
		t.Helper()
		data, _ := json.Marshal(configs)
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStrategySelectionAfterRestart$")
		cmd.Env = append(os.Environ(), "SWD_SELECTION_HOME="+home, "SWD_SELECTION_CONFIG="+string(data), "SWD_SELECTION_NAME="+expected, fmt.Sprintf("SWD_SELECTION_TRANSIENT=%t", transient))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restart: %v\n%s", err, output)
		}
	}
	restart(names[2], false)
	// A temporary key disables reading and writing the durable selection.
	restart(automaticGroup, true)
	restart(names[2], false)
	// The native cache stores names; a renamed node no longer matches.
	configs[1].Parameters = maps.Clone(configs[1].Parameters)
	configs[1].Parameters["name"] = "B renamed"
	restart(automaticGroup, false)
}

func TestStrategySelectionAfterRestart(t *testing.T) {
	home := os.Getenv("SWD_SELECTION_HOME")
	if home == "" {
		t.Skip("subprocess restart check")
	}
	var configs []Config
	if err := json.Unmarshal([]byte(os.Getenv("SWD_SELECTION_CONFIG")), &configs); err != nil {
		t.Fatal(err)
	}
	transient := os.Getenv("SWD_SELECTION_TRANSIENT") == "true"
	r, err := NewRuntime(configs, RuntimeOptions{Home: home, DisableSelectionPersistence: transient})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/proxies/SWD", nil))
	var result struct {
		Now string `json:"now"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Now != os.Getenv("SWD_SELECTION_NAME") {
		t.Fatal("selection not restored", w.Body.String())
	}
	if transient {
		selectRuntimeProxy(t, r, runtimeProxyNames(t, r)[1])
	}
	fmt.Fprintln(os.Stdout, "native selection restored")
}
