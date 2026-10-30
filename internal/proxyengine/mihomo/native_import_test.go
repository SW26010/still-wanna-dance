package mihomo

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/listener/inner"
)

const basicSS = "ss://YWVzLTEyOC1nY206cHJpdmF0ZS1wYXNz@Example.COM:1234#private-name"

func TestNativeImportFormatsAndPreview(t *testing.T) {
	uri := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"private-label","add":"127.0.0.1","port":"1234","id":"00000000-0000-0000-0000-000000000001","aid":"0","net":"ws","path":"/stream","tls":""}`))
	before, innerBefore := resolver.DefaultResolver, inner.GetTunnel()
	for _, input := range []string{uri, base64.RawStdEncoding.EncodeToString([]byte(uri)), "proxies: [{name: DIRECT, type: vmess, server: 127.0.0.1, port: 1234, uuid: '00000000-0000-0000-0000-000000000001', alterId: 0, cipher: auto, network: ws, ws-opts: {path: /stream}}]\ntun: {enable: true}\nport: 12345\nrules: [MATCH,DIRECT]"} {
		r, err := Import([]byte(input))
		if err != nil || r.Accepted != 1 {
			t.Fatalf("native import: %+v %v", r.Items, err)
		}
		if name, ok := r.Nodes[0]["name"].(string); !ok || name == "" {
			t.Fatal("dashboard label missing from protected node data")
		}
		public, _ := json.Marshal(r)
		if strings.Contains(string(public), "private") || strings.Contains(string(public), "127.0.0.1") {
			t.Fatal("private report")
		}
	}
	if resolver.DefaultResolver != before || inner.GetTunnel() != innerBefore {
		t.Fatal("preview installed core globals")
	}
}

func TestNativeMixedItemsAndIdentity(t *testing.T) {
	r, err := Import([]byte("proxies:\n  - {name: one, type: vless, server: localhost, port: 1234, uuid: UserA}\n  - bad\n  - null\n  - {name: two, type: vless, server: localhost, port: 1234, uuid: UserA}\n  - {name: three, type: vless, server: localhost, port: 1234, uuid: usera}\n"))
	if err != nil || r.Accepted != 2 || r.Skipped != 1 || r.Failed != 2 || len(r.Items) != 5 {
		t.Fatalf("partial result: %+v %v", r.Items, err)
	}
	if r.Nodes[0]["uuid"] == r.Nodes[1]["uuid"] {
		t.Fatal("credential was changed")
	}
	p := map[string]any{"type": "ss", "server": "localhost", "port": 1234, "cipher": "aes-128-gcm", "password": "private"}
	base, err := Normalize(p)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := json.Marshal(base)
	for _, key := range []string{"server", "password"} {
		changed := maps.Clone(p)
		changed[key] = "different"
		v, err := Normalize(changed)
		if err != nil {
			t.Fatal(err)
		}
		y, _ := json.Marshal(v)
		if string(x) == string(y) {
			t.Fatal("different connection shared identity")
		}
	}
}

func TestNativeMieruPortAlternatives(t *testing.T) {
	r, err := Import([]byte("proxies: [{type: mieru, server: 127.0.0.1, port-range: 12000-12002, transport: TCP, username: fixture, password: fixture}]"))
	if err != nil || r.Accepted != 1 || r.Nodes[0]["port-range"] != "12000-12002" {
		t.Fatalf("native port-range import: %+v %v", r.Items, err)
	}
	if _, present := r.Nodes[0]["port"]; present {
		t.Fatal("port-range gained a synthetic port")
	}
	base := maps.Clone(r.Nodes[0])
	delete(base, "port-range")
	for _, tc := range []struct {
		name   string
		fields map[string]any
		valid  bool
	}{
		{"single-port", map[string]any{"port": "1234"}, true},
		{"both", map[string]any{"port": 1234, "port-range": "12000-12002"}, false},
		{"neither", nil, false},
		{"reversed-range", map[string]any{"port-range": "12002-12000"}, false},
		{"invalid-port", map[string]any{"port": 65536}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := maps.Clone(base)
			maps.Copy(p, tc.fields)
			normalized, err := Normalize(p)
			if !tc.valid {
				if err != ErrConfig {
					t.Fatalf("invalid port selection accepted: %v", err)
				}
				return
			}
			if err != nil || normalized["port"] != float64(1234) {
				t.Fatalf("single port normalization: %v %v", normalized, err)
			}
		})
	}
}

func TestNativeCapabilityGate(t *testing.T) {
	base := map[string]any{"type": "ss", "server": "localhost", "port": 1234, "cipher": "aes-128-gcm", "password": "private"}
	for _, key := range []string{"dialer-proxy", "DIALER_PROXY", "interface_name", "private-key", "CA", "foo-file", "state-path", "realm-opts", "smux", "-"} {
		for _, nested := range []bool{false, true} {
			p := maps.Clone(base)
			if nested {
				p["transport-options"] = map[string]any{key: "private"}
			} else {
				p[key] = "private"
			}
			if _, err := Normalize(p); err != ErrConfig {
				t.Fatalf("accepted capability %s nested=%v", key, nested)
			}
		}
	}
	for _, kind := range []string{"direct", "reject", "select", "wireguard", "openvpn", "masque", "tailscale", "easytier", "zerotier"} {
		p := maps.Clone(base)
		p["type"] = kind
		if _, err := Normalize(p); err != ErrConfig {
			t.Fatalf("accepted %s", kind)
		}
	}
	p := maps.Clone(base)
	p["PASSWORD"] = "other"
	if _, err := Normalize(p); err != ErrConfig {
		t.Fatal("ambiguous option alias")
	}
	for _, data := range []string{"", strings.Repeat("x", MaxImportBytes+1), strings.Repeat(basicSS+"\n", MaxImportNodes+1)} {
		if _, err := Import([]byte(data)); err == nil {
			t.Fatal("unbounded native import")
		}
	}
}
