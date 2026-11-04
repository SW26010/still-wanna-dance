package mihomo

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestOriginalGroupSelectionUsesDedicatedNodeDNS(t *testing.T) {
	if !runtimeProcess(t) {
		return
	}
	var enabled atomic.Bool
	enabled.Store(true)
	a, b := groupExit(t, "A", "", &enabled), groupExit(t, "B", "", &enabled)
	a["server"], b["server"] = "node.fixture.invalid", "node.fixture.invalid"
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var queries atomic.Int32
	server := &dns.Server{PacketConn: packet, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		queries.Add(1)
		answer := new(dns.Msg)
		answer.SetReply(r)
		for _, q := range r.Question {
			if q.Name == "node.fixture.invalid." && q.Qtype == dns.TypeA {
				answer.Answer = append(answer.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(127, 0, 0, 1)})
			}
		}
		_ = w.WriteMsg(answer)
	})}
	go server.ActivateAndServe()
	t.Cleanup(func() { _ = server.Shutdown() })
	data, _ := json.Marshal(map[string]any{
		"proxies":      []map[string]any{a, b},
		"proxy-groups": []map[string]any{{"name": "SWD", "type": "select", "proxies": []string{"A", "B"}}},
		"dns":          map[string]any{"enable": true, "nameserver": []string{"127.0.0.1:1"}, "proxy-server-nameserver": []string{packet.LocalAddr().String()}},
		"rules":        []string{"MATCH,REJECT"},
	})
	r, err := NewRuntime(data, RuntimeOptions{Home: t.TempDir(), DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	entry := httptest.NewRecorder()
	r.ServeHTTP(entry, httptest.NewRequest("GET", "/proxies/"+url.PathEscape("SWD (1)"), nil))
	if entry.Code != http.StatusOK {
		t.Fatal("managed entry did not preserve the original group name")
	}
	tr := &http.Transport{DialContext: r.DialContext}
	defer tr.CloseIdleConnections()
	read := func(want string) {
		t.Helper()
		response, err := (&http.Client{Transport: tr, Timeout: 3 * time.Second}).Get("http://business.invalid/")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != want {
			t.Fatal("wrong original-group exit", err)
		}
	}
	read("A")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/proxies/SWD", strings.NewReader(`{"name":"B"}`)))
	if w.Code != 204 {
		t.Fatal("original selector not writable", w.Code)
	}
	tr.CloseIdleConnections()
	read("B")
	if queries.Load() == 0 {
		t.Fatal("dedicated node resolver was bypassed")
	}
	if _, err := Validate(t.Context(), data, t.TempDir()); err != nil {
		t.Fatal("checking with a live core failed", err)
	}
	tr.CloseIdleConnections()
	read("B")
}
