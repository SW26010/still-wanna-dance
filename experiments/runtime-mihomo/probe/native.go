//go:build !external_only

package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/common/yaml"
	"github.com/metacubex/mihomo/log"
)

// This is a pinned-version capability gate for an experiment, not a protocol
// schema or a production import format. Check BEFORE any adapter constructor.
// Conservative rejection is intentional; WS paths and ordinary TLS options pass.
func nativeNodes(data []byte) ([]map[string]any, error) {
	log.SetLevel(log.SILENT)
	if len(data) == 0 || len(data) > 2<<20 {
		return nil, errors.New("input-budget")
	}
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		var err error
		doc.Proxies, err = convert.ConvertsV2Ray(data)
		if err != nil {
			return nil, errors.New("native-conversion")
		}
	}
	if len(doc.Proxies) == 0 || len(doc.Proxies) > 256 {
		return nil, errors.New("empty-or-count-budget")
	}
	for i, node := range doc.Proxies {
		typ, _ := node["type"].(string)
		switch strings.ToLower(typ) {
		case "", "direct", "reject", "dns", "pass", "compatible", "rematch", "select", "url-test", "fallback", "load-balance", "relay":
			return nil, errors.New("non-fixed-outbound")
		case "tailscale", "zerotier", "easytier", "wireguard", "openvpn":
			return nil, errors.New("overlay-or-system-capability")
		}
		if err := gateValue(node, 0); err != nil {
			return nil, err
		}
		// Runtime names never come from subscription display labels (or DIRECT).
		node["name"] = fmt.Sprintf("n%d", i)
	}
	return doc.Proxies, nil
}

func gateValue(value any, depth int) error {
	if depth > 16 {
		return errors.New("nesting-budget")
	}
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			// Mihomo accepts underscore aliases and case-insensitive struct keys.
			key = strings.ToLower(strings.ReplaceAll(key, "_", "-"))
			switch key {
			case "dialer-proxy", "interface-name", "routing-mark", "ip-version",
				"certificate", "private-key", "ca", "ca-str", "client-certificate", "client-key",
				"plugin", "plugin-opts", "realm-opts", "state-dir", "planet", "script", "command",
				"proxies", "use", "include-all", "provider", "url":
				return errors.New("capability-denied")
			}
			if strings.HasSuffix(key, "-file") || strings.HasSuffix(key, "-path") {
				return errors.New("file-capability-denied")
			}
			if err := gateValue(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := gateValue(item, depth+1); err != nil {
				return err
			}
		}
	case map[any]any:
		return errors.New("non-string-key")
	}
	return nil
}
