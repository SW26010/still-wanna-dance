// This executable probes library embedding; it does not run Mihomo's main,
// config loader, tunnel, inbound listeners, or external controller.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/component/resolver"
	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/upstreamrequest"
)

func main() {
	_ = upstreamrequest.NewChannel() // Compile alongside the application graph.
	p, err := adapter.ParseProxy(map[string]any{"name": "probe", "type": "ss", "server": "127.0.0.1", "port": 1, "cipher": "aes-128-gcm", "password": "local-experiment-only"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer p.Close()
	json.NewEncoder(os.Stdout).Encode(map[string]any{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "parse": true, "systemResolverInitialized": resolver.SystemResolver != nil, "proxyResolverInitialized": resolver.ProxyServerHostResolver != nil, "defaultResolverInitialized": resolver.DefaultResolver != nil, "consoleLinked": fmt.Sprintf("%T", console.New)})
}
