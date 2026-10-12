package mihomo

import (
	"encoding/json"
	"net/netip"
	"strings"
)

// Deliberately narrow audited surface for the first production adapter. This
// is not a protocol translation schema: values go to upstream ParseProxy.
// New transports/options require an explicit lifecycle and routing review.
func checkedParameters(input map[string]any) (map[string]any, string, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, "", ErrConfig
	}
	var p map[string]any
	if json.Unmarshal(data, &p) != nil {
		return nil, "", ErrConfig
	}
	kind, _ := p["type"].(string)
	fields := " name type server port "
	switch kind {
	case "ss":
		fields += "cipher password "
	case "vless":
		fields += "uuid tls servername fingerprint client-fingerprint skip-cert-verify alpn "
	case "anytls":
		fields += "password sni fingerprint skip-cert-verify alpn "
	case "hysteria2":
		fields += "password sni fingerprint skip-cert-verify alpn "
	default:
		return nil, "", ErrConfig
	}
	for key := range p {
		if !strings.Contains(fields, " "+key+" ") {
			return nil, "", ErrConfig
		}
	}
	host, _ := p["server"].(string)
	port, _ := p["port"].(float64)
	if host == "" || strings.ContainsAny(host, " /\\\t\r\n\x00") || port < 1 || port > 65535 || port != float64(int(port)) {
		return nil, "", ErrConfig
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return nil, "", ErrConfig
		}
	} else if strings.Contains(host, ":") {
		return nil, "", ErrConfig
	}
	// Never pass an untrusted display name into upstream logging.
	p["name"] = "fixed-node"
	if kind == "anytls" {
		// Upstream writes the destination on a reused session without observing
		// this dial's context. Until that API is cancellable, use one protocol
		// session per stream; HTTP transports can still reuse their connections.
		// This keeps every handshake socket inside its owning dialAttempt.
		p["disable-reuse"] = true
	}
	if kind == "hysteria2" {
		// A positive value creates a background handshake detached from the
		// foreground context. Keep DNS and transport ownership caller-bound.
		p["handshake-timeout"] = 0
	}
	return p, host, nil
}
