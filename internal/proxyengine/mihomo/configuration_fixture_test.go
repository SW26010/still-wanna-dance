package mihomo

import (
	"encoding/json"
	"fmt"
	"maps"
)

// Existing protocol fixtures describe endpoints. Turn them into an ordinary
// named native configuration; production accepts only configuration documents.
type Config struct {
	ID         string
	Parameters map[string]any
}

func fixtureConfiguration(configs []Config) []byte {
	nodes := make([]map[string]any, 0, len(configs))
	for i, cfg := range configs {
		p := maps.Clone(cfg.Parameters)
		if p["name"] == nil || p["name"] == "" {
			p["name"] = fmt.Sprintf("节点 %d", i+1)
		}
		nodes = append(nodes, p)
	}
	dns := []string{"https://223.5.5.5/dns-query", "https://1.1.1.1/dns-query"}
	data, _ := json.Marshal(map[string]any{"proxies": nodes, "dns": map[string]any{"enable": true, "nameserver": dns, "default-nameserver": dns, "proxy-server-nameserver": dns}, "rules": []string{"MATCH,REJECT"}})
	return data
}

func newFixtureRuntime(configs []Config, options RuntimeOptions) (*Runtime, error) {
	return NewRuntime(fixtureConfiguration(configs), options)
}
