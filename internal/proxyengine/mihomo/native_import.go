package mihomo

import (
	"encoding/json"
	"io"
	"net/netip"
	"strconv"
	"strings"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	"github.com/sirupsen/logrus"
)

// Mihomo conversion errors can include complete private URIs. Its logrus sink
// is discarded before ANY conversion/constructor, for the lifetime of SWD.
// SWD uses slog and publishes only fixed diagnostics. Never subscribe to the
// core's unredacted event stream or temporarily enable its sink for debugging.
func init() { logrus.SetOutput(io.Discard) }

// ImportNative is staged separately until the complete runtime is connected.
// Preview never applies a config or acquires the process's runtime ownership.
func ImportNative(data []byte) (ImportResult, error) {
	return importNodes(data, nativeURI, NormalizeNative)
}

func nativeURI(line string) (map[string]any, error) {
	nodes, err := convert.ConvertsV2Ray([]byte(line))
	if err != nil || len(nodes) != 1 {
		return nil, ErrConfig
	}
	return nodes[0], nil
}

// NormalizeNative preserves native connection semantics, including credentials
// and otherwise unknown options. It deliberately does not equate every encoding
// or protocol default. A conservative new identity is safer than wrong reuse.
func NormalizeNative(input map[string]any) (map[string]any, error) {
	nativeConfigMu.Lock()
	defer nativeConfigMu.Unlock()
	p, err := nativeParameters(input)
	if err != nil {
		return nil, err
	}
	// Audited v1.19.32 constructors are lazy with respect to sockets/files after
	// the capability gate. AnyTLS creates an idle maintenance worker; Close owns
	// it. No custom dialer, resolver or ApplyConfig is installed by validation.
	p["name"] = "fixed-node"
	proxy, err := adapter.ParseProxy(p)
	if err != nil {
		return nil, ErrConfig
	}
	if proxy.Close() != nil {
		return nil, ErrConfig
	}
	delete(p, "name")
	return p, nil
}

func nativeParameters(input map[string]any) (map[string]any, error) {
	data, err := json.Marshal(input)
	if err != nil || len(data) > MaxImportBytes {
		return nil, ErrConfig
	}
	var p map[string]any
	if json.Unmarshal(data, &p) != nil || p == nil || gateNativeValue(p, 0) != nil {
		return nil, ErrConfig
	}
	// Canonicalize top-level option keys only; nested headers/values belong to
	// the native parser and must not be rewritten as protocol configuration.
	for key, value := range p {
		canonical := nativeKey(key)
		if key != canonical {
			delete(p, key)
			p[canonical] = value
		}
	}
	delete(p, "name")
	kind, _ := p["type"].(string)
	switch strings.ToLower(kind) {
	case "", "direct", "reject", "dns", "pass", "compatible", "rematch", "select", "url-test", "fallback", "load-balance", "relay":
		return nil, ErrConfig
	case "tailscale", "zerotier", "easytier", "wireguard", "openvpn", "masque":
		// Overlay/network-stack constructors can own interfaces, files or eager
		// background networking; they are not fixed stream-only capabilities.
		return nil, ErrConfig
	}
	host, _ := p["server"].(string)
	if host == "" || strings.ContainsAny(host, " /\\\t\r\n\x00") {
		return nil, ErrConfig
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return nil, ErrConfig
		}
		p["server"] = ip.Unmap().String()
	} else if strings.Contains(host, ":") {
		return nil, ErrConfig
	} else {
		p["server"] = strings.ToLower(strings.TrimSuffix(host, "."))
	}
	if port, ok := p["port"].(string); ok {
		n, err := strconv.Atoi(port)
		if err != nil {
			return nil, ErrConfig
		}
		p["port"] = float64(n)
	}
	port, ok := p["port"].(float64)
	if !ok || port < 1 || port > 65535 || port != float64(int(port)) {
		return nil, ErrConfig
	}
	return p, nil
}

func nativeKey(key string) string { return strings.ToLower(strings.ReplaceAll(key, "_", "-")) }

// This is a cross-protocol capability gate, not a protocol field whitelist.
// Re-audit constructors and file/network capabilities on a core version bump.
func gateNativeValue(value any, depth int) error {
	if depth > 16 {
		return ErrConfig
	}
	switch v := value.(type) {
	case map[string]any:
		seen := make(map[string]bool, len(v))
		for key, child := range v {
			key = nativeKey(key)
			if seen[key] {
				return ErrConfig
			}
			seen[key] = true
			switch key {
			case "dialer-proxy", "interface-name", "routing-mark", "ip-version",
				"certificate", "private-key", "ca", "ca-str", "client-certificate", "client-key",
				"plugin", "plugin-opts", "realm-opts", "state-dir", "planet", "script", "command",
				"proxies", "use", "include-all", "provider", "url", "ip-stack", "-",
				"smux":
				// The native parser constructs its base before validating smux;
				// failures can orphan a session worker. Reject this optional
				// multi-adapter capability until upstream owns failure cleanup.
				return ErrConfig
			}
			if strings.HasSuffix(key, "-file") || strings.HasSuffix(key, "-path") {
				return ErrConfig
			}
			if err := gateNativeValue(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := gateNativeValue(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
