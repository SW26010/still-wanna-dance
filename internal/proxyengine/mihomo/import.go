package mihomo

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"

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
			p, _ := nativeURI(line)
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
