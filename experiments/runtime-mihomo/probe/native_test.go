//go:build !external_only

package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestNativeCapabilityBoundary(t *testing.T) {
	base := map[string]any{"name": "DIRECT", "type": "vmess", "server": "proxy.invalid", "port": 443, "uuid": "d342d11e-d424-4583-b36e-524ab1f0afa4", "network": "ws", "ws-opts": map[string]any{"path": "/socket", "headers": map[string]any{"Host": "proxy.invalid"}}}
	for _, key := range []string{"dialer-proxy", "dialer_proxy", "INTERFACE_NAME", "routing-mark", "private-key", "certificate", "realm-opts", "plugin", "state-dir", "client-key-file"} {
		t.Run(key, func(t *testing.T) {
			n := map[string]any{}
			for k, v := range base {
				n[k] = v
			}
			n[key] = "do-not-read-or-dial"
			b, _ := json.Marshal(map[string]any{"proxies": []any{n}})
			if _, err := nativeNodes(b); err == nil {
				t.Fatal("capability accepted")
			}
		})
	}
	b, _ := json.Marshal(map[string]any{"proxies": []any{base}, "tun": map[string]any{"enable": true}, "listeners": []any{map[string]any{"port": 9999}}, "rules": []string{"MATCH,DIRECT"}})
	nodes, err := nativeNodes(b)
	if err != nil || len(nodes) != 1 || nodes[0]["name"] != "n0" || nodes[0]["network"] != "ws" {
		t.Fatal("native node data lost or full config inherited")
	}
	for _, typ := range []string{"direct", "rematch", "select", "tailscale", "zerotier", "easytier", "wireguard", "openvpn"} {
		base["type"] = typ
		b, _ = json.Marshal(map[string]any{"proxies": []any{base}})
		if _, err := nativeNodes(b); err == nil {
			t.Fatalf("accepted %s", typ)
		}
	}
	for _, input := range []string{"", "proxies: []", "proxies: [null]", "proxies: [broken]", "not-a-node"} {
		if _, err := nativeNodes([]byte(input)); err == nil {
			t.Fatal("bad/empty input accepted")
		}
	}
}
func TestNativeFormats(t *testing.T) {
	vmess, _ := json.Marshal(map[string]any{"v": "2", "ps": "fixture", "add": "proxy.invalid", "port": "443", "id": "d342d11e-d424-4583-b36e-524ab1f0afa4", "aid": "0", "scy": "auto", "net": "ws", "type": "none", "host": "proxy.invalid", "path": "/socket", "tls": "tls"})
	uri := "vmess://" + base64.StdEncoding.EncodeToString(vmess)
	for _, s := range []string{uri, base64.StdEncoding.EncodeToString([]byte(uri))} {
		nodes, err := nativeNodes([]byte(s))
		if err != nil || len(nodes) != 1 || nodes[0]["network"] != "ws" {
			t.Fatal("native VMess/WS conversion failed")
		}
	}
}
