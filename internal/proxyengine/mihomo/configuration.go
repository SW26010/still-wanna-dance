package mihomo

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"

	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/log"
	"go.yaml.in/yaml/v3"
)

const MaxImportBytes = 2 << 20
const MaxImportNodes = 2048

var ErrConfiguration = errors.New("Mihomo 配置无效或依赖资源不可用")
var yamlLine = regexp.MustCompile(`line ([0-9]+)`)

// Summary contains only public counts. Original configuration and native parser
// errors can contain credentials and must never be returned to the browser.
type Summary struct {
	Nodes  int `json:"nodes"`
	Groups int `json:"groups"`
}

// Inspect reads the native document without constructing adapters, accessing
// resources or changing core globals. Full native validation runs out of process.
func Inspect(data []byte) (Summary, error) {
	raw, err := readConfiguration(data)
	if err != nil {
		return Summary{}, err
	}
	return Summary{Nodes: len(raw.Proxy), Groups: len(raw.ProxyGroup)}, nil
}

func readConfiguration(data []byte) (*config.RawConfig, error) {
	if len(data) == 0 || len(data) > MaxImportBytes {
		return nil, errors.New("配置内容为空或超过 2 MiB")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		if match := yamlLine.FindStringSubmatch(err.Error()); len(match) == 2 {
			return nil, fmt.Errorf("Mihomo 配置 YAML 语法无效（第 %s 行）", match[1])
		}
		return nil, ErrConfiguration
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, ErrConfiguration
	}
	raw, err := config.UnmarshalRawConfig(data)
	if err != nil || len(raw.Proxy) == 0 && len(raw.ProxyProvider) == 0 {
		return nil, ErrConfiguration
	}
	if len(raw.Proxy) > MaxImportNodes || len(raw.ProxyGroup) > MaxImportNodes {
		return nil, errors.New("配置节点或代理组超过 2048 个")
	}
	return raw, nil
}

// managedConfiguration is the single override policy used by checking and
// startup. Each call parses a fresh copy; saved bytes are never rewritten.
func managedConfiguration(data []byte, persist bool) (*config.RawConfig, string, error) {
	raw, err := readConfiguration(data)
	if err != nil {
		return nil, "", err
	}
	used := map[string]bool{"GLOBAL": true, "DIRECT": true, "REJECT": true, "COMPATIBLE": true}
	for _, entries := range [][]map[string]any{raw.Proxy, raw.ProxyGroup} {
		for _, p := range entries {
			if name, ok := p["name"].(string); ok {
				used[name] = true
			}
		}
	}
	unique := func(base string) string {
		name := base
		for i := 1; used[name]; i++ {
			name = fmt.Sprintf("%s (%d)", base, i)
		}
		used[name] = true
		return name
	}
	entry, automatic := unique(ProxyGroup), unique(automaticGroup)
	var candidates []string
	for _, group := range raw.ProxyGroup {
		name, ok := group["name"].(string)
		if !ok || name == "" {
			return nil, "", ErrConfiguration
		}
		candidates = append(candidates, name)
	}
	auto := map[string]any{"name": automatic, "type": "fallback", "url": "https://www.gstatic.com/generate_204", "interval": 300, "lazy": true, "empty-fallback": "REJECT"}
	selector := map[string]any{"name": entry, "type": "select", "proxies": append([]string{automatic}, candidates...), "empty-fallback": "REJECT"}
	if len(candidates) > 0 {
		auto["proxies"] = candidates
	} else {
		// Native include-all supports both inline nodes and dynamic providers.
		auto["include-all"] = true
		selector["include-all"] = true
	}
	raw.ProxyGroup = append([]map[string]any{selector, auto}, raw.ProxyGroup...)

	raw.Port, raw.SocksPort, raw.MixedPort, raw.RedirPort, raw.TProxyPort = 0, 0, 0, 0, 0
	raw.ShadowSocksConfig, raw.VmessConfig = "", ""
	raw.AllowLan, raw.BindAddress = false, "127.0.0.1"
	raw.ExternalController, raw.ExternalControllerTLS, raw.ExternalControllerUnix, raw.ExternalControllerPipe = "", "", "", ""
	raw.ExternalUI, raw.ExternalUIURL, raw.ExternalUIName, raw.ExternalDohServer, raw.Secret = "", "", "", "", ""
	raw.Listeners, raw.Tunnels = nil, nil
	raw.Tun, raw.TuicServer, raw.IPTables, raw.NTP = config.RawTun{}, config.RawTuicServer{}, config.RawIPTables{}, config.RawNTP{}
	raw.Sniffer = config.RawSniffer{}
	raw.Interface, raw.RoutingMark = "", 0
	raw.FindProcessMode, raw.LogLevel, raw.GeoAutoUpdate = process.FindProcessOff, log.SILENT, false
	raw.DNS.Listen, raw.DNS.UseSystemHosts = "", false
	if !persist {
		raw.Profile.StoreSelected = false
	}
	return raw, entry, nil
}
