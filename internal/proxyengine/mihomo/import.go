package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/metacubex/mihomo/common/convert"
	"go.yaml.in/yaml/v3"
)

// Import limits are independent of the active-node policy. No raw input,
// display names, endpoints, or upstream parser errors belong in diagnostics.
const MaxImportBytes = 2 << 20
const MaxImportNodes = 2048

type ImportItem struct {
	Index    int    `json:"index"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

type ImportResult struct {
	Accepted int              `json:"accepted"`
	Skipped  int              `json:"skipped"`
	Failed   int              `json:"failed"`
	Items    []ImportItem     `json:"items"`
	Nodes    []map[string]any `json:"-"`
}

// Import reads data only. Complete runtime configurations are never executed.
// The returned canonical maps contain secrets and must stay inside protected
// storage; only the result counters and fixed diagnostics are public.
func Import(data []byte) (ImportResult, error) {
	var result ImportResult
	if len(data) == 0 || len(data) > MaxImportBytes {
		return result, errors.New("节点内容为空或超过 2 MiB")
	}
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	var inputs []map[string]any
	var root yaml.Node
	if yaml.Unmarshal(data, &root) != nil {
		return result, errors.New("节点内容格式无效，请检查 YAML 语法或 URI 列表")
	}
	if len(root.Content) > 0 && root.Content[0].Kind == yaml.MappingNode {
		var document struct {
			Proxies yaml.Node `yaml:"proxies"`
		}
		var entries []yaml.Node
		if root.Decode(&document) != nil || document.Proxies.Kind == 0 || document.Proxies.Decode(&entries) != nil {
			return result, errors.New("YAML proxies 必须是节点列表")
		}
		if len(entries) > MaxImportNodes {
			return result, errors.New("节点条目超过 2048 个")
		}
		for _, entry := range entries {
			var p map[string]any
			if entry.Decode(&p) != nil {
				p = nil // One invalid item must not reject its valid siblings.
			}
			inputs = append(inputs, p)
		}
	} else {
		// Upstream decoder handles padded/raw standard and URL-safe Base64.
		decoded := convert.DecodeBase64(data)
		for _, line := range strings.Split(string(decoded), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			p, _ := convertURI(line)
			inputs = append(inputs, p)
			if len(inputs) > MaxImportNodes {
				break
			}
		}
	}
	if len(inputs) == 0 || len(inputs) > MaxImportNodes {
		return result, errors.New("未找到节点，或节点条目超过 2048 个")
	}
	seen := make(map[string]bool)
	for i, input := range inputs {
		item := ImportItem{Index: i + 1, Status: "failed"}
		p, err := Normalize(input)
		if err != nil {
			item.Reason = "不支持的协议、传输或选项，或节点参数无效"
			result.Failed++
		} else {
			item.Protocol, _ = p["type"].(string)
			canonical, _ := json.Marshal(p)
			if seen[string(canonical)] {
				item.Status, item.Reason = "skipped", "重复的有效连接配置"
				result.Skipped++
			} else {
				seen[string(canonical)] = true
				item.Status = "accepted"
				result.Accepted++
				result.Nodes = append(result.Nodes, p)
			}
		}
		result.Items = append(result.Items, item)
	}
	// Stable ordering makes a reordered import the same saved snapshot.
	slices.SortFunc(result.Nodes, func(a, b map[string]any) int {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return bytes.Compare(x, y)
	})
	return result, nil
}

// Reject unsupported query fields BEFORE conversion: upstream intentionally
// ignores some fields, and VLESS error paths can log the entire private URI.
// This audited subset cannot reach handleVShareLink's warning paths.
func convertURI(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil || u.Opaque != "" {
		return nil, ErrConfig
	}
	allowed := ""
	switch u.Scheme {
	case "ss":
		// Only basic SIP002/legacy SS; plugin and UDP tunneling are unsupported.
	case "vless":
		allowed = " security sni alpn pcs type encryption "
		if u.Hostname() == "" || u.Port() == "" || strings.ContainsAny(u.Host, "@/?#") {
			return nil, ErrConfig
		}
	case "anytls":
		allowed = " insecure sni hpkp "
	case "hy2", "hysteria2":
		allowed = " insecure sni pinSHA256 alpn "
	default:
		return nil, ErrConfig
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, ErrConfig
	}
	for key, values := range query {
		if !strings.Contains(allowed, " "+key+" ") || len(values) != 1 {
			return nil, ErrConfig
		}
	}
	if u.Scheme == "vless" {
		if v := query.Get("type"); v != "" && v != "tcp" {
			return nil, ErrConfig
		}
		if v := query.Get("security"); v != "" && v != "none" && v != "tls" {
			return nil, ErrConfig
		}
		if v := query.Get("encryption"); v != "" && v != "none" {
			return nil, ErrConfig
		}
	}
	if v := query.Get("insecure"); v != "" && v != "0" && v != "1" {
		return nil, ErrConfig
	}
	if u.Path != "" && u.Path != "/" {
		return nil, ErrConfig
	}
	proxies, err := convert.ConvertsV2Ray([]byte(line))
	if err != nil || len(proxies) != 1 {
		return nil, ErrConfig
	}
	p := proxies[0]
	// AnyTLS's converter emits a username although the protocol uses only the
	// password. Hysteria2's converter retains URI escaping in userinfo.
	if u.Scheme == "anytls" {
		delete(p, "username")
	}
	if u.Scheme == "hy2" || u.Scheme == "hysteria2" {
		p["password"], err = url.PathUnescape(u.User.String())
		if err != nil {
			return nil, ErrConfig
		}
		for _, key := range []string{"obfs", "obfs-password", "up", "down"} {
			if p[key] == "" {
				delete(p, key)
			}
		}
	}
	return p, nil
}

// Normalize is the single connection-identity boundary. Display names and
// TCP-irrelevant UDP flags do not affect the effective outbound configuration.
// All substantive unsupported options are rejected, never silently dropped.
func Normalize(input map[string]any) (map[string]any, error) {
	b, err := json.Marshal(input)
	if err != nil {
		return nil, ErrConfig
	}
	var p map[string]any
	if json.Unmarshal(b, &p) != nil || p == nil {
		return nil, ErrConfig
	}
	delete(p, "name")
	for _, key := range []string{"udp", "xudp", "packet-addr"} {
		if v, ok := p[key]; ok {
			if _, ok := v.(bool); !ok {
				return nil, ErrConfig
			}
			delete(p, key)
		}
	}
	if port, ok := p["port"].(string); ok {
		n, err := strconv.Atoi(port)
		if err != nil {
			return nil, ErrConfig
		}
		p["port"] = float64(n)
	}
	if host, ok := p["server"].(string); ok {
		if ip, err := netip.ParseAddr(host); err == nil {
			p["server"] = ip.Unmap().String()
		} else {
			p["server"] = strings.ToLower(strings.TrimSuffix(host, "."))
		}
	}
	if p["type"] == "vless" {
		if v, ok := p["network"]; ok && (v == "tcp" || v == "") {
			delete(p, "network")
		}
		if v, ok := p["encryption"]; ok && (v == "none" || v == "") {
			delete(p, "encryption")
		}
		if id, ok := p["uuid"].(string); ok {
			// Use the upstream UUID parser. Custom IDs are case-sensitive input
			// to VLESS's UUID mapping and must retain their original spelling.
			if parsed, err := uuid.FromString(id); err == nil {
				p["uuid"] = parsed.String()
			}
		}
	}
	for _, key := range []string{"tls", "skip-cert-verify"} {
		if v, ok := p[key]; ok {
			if _, ok := v.(bool); !ok {
				return nil, ErrConfig
			}
			if v == false {
				delete(p, key)
			}
		}
	}
	for _, key := range []string{"sni", "servername", "fingerprint", "client-fingerprint"} {
		if v, ok := p[key]; ok {
			s, ok := v.(string)
			if !ok {
				return nil, ErrConfig
			}
			if s == "" {
				delete(p, key)
			}
		}
	}
	// Validate with the same upstream parser used at startup, without a dial.
	n, err := New(Config{ID: "stable-v1-" + strings.Repeat("0", 64), Parameters: p}, Options{LookupIP: func(context.Context, string) ([]netip.Addr, error) { return nil, ErrDNS }})
	if err != nil {
		return nil, ErrConfig
	}
	_ = n.Close()
	if p["type"] == "vless" && p["tls"] != true {
		// TCP VLESS ignores these options without TLS. Validate them above
		// before dropping them so normalization cannot bypass the allowlist.
		for _, key := range []string{"servername", "fingerprint", "client-fingerprint", "skip-cert-verify", "alpn"} {
			delete(p, key)
		}
	}
	return p, nil
}
