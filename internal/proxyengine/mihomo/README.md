# Fixed Mihomo adapter

This package is the only production boundary allowed to import/use Mihomo. It
creates fixed outbound nodes; it does not load subscriptions or full runtime
configs, start listeners/controllers, or change system networking. The desktop
does not enable this backend yet. No user-visible setting or behavior changes.

```go
node, err := mihomo.New(mihomo.Config{
    ID: keyedConfigurationID,
    Parameters: nodeParameters,
}, mihomo.Options{
    LookupIP: proxyServerLookup, // context-aware, hosts-independent DNS
    DialTimeout: 10 * time.Second,
})
// Handle err before publishing this node.
defer node.Close()
pool, err := upstreamrequest.NewMultiPool("socks5", nil,
    []upstreamrequest.Proxy{node.Proxy()})
// Handle err before publishing this pool.
defer pool.Close()
```

`Proxy()` supplies the existing `DialFunc` without exposing Mihomo types.
`Node.Close()` owns node resources; `Pool.Close()` owns HTTP transports. On
replacement publish the new generation, close the old pool, then close its
nodes. Never reuse a node ID for a different connection configuration; keyed
fingerprints and source/generation management belong to the caller. This package
does not implement subscription refresh or node identity generation.

## Lifecycle contract

- Config is copied at construction. One node may be dialed concurrently and
  repeatedly. It never selects another node or the direct business path.
- Protocol establishment is serialized per node using a context-aware queue;
  established streams remain concurrent. This prevents canceled callers from
  waiting on Hysteria2's non-context-aware cold-offer mutex. The dial timeout
  includes time in this queue. Nodes never share the queue.
- Dial accepts TCP business destinations, preserving domain metadata for the
  proxy server. UDP is internal to QUIC, not a public UDP API.
- Dial timeout covers DNS/establishment (default ten seconds). Pre-cancellation
  is checked, error wrapping is normalized, and a connection returned after
  cancellation/retirement is closed. Parser and dial error details are redacted.
- A failed/canceled dial closes the physical sockets it created. Successful
  dials transfer physical socket ownership to the node. Returned business
  connections belong to the caller until Close; cancellation **after Dial has
  returned** does not close a successful stream (standard net.Dialer semantics).
  HTTP owns its request cancellation; raw callers must close/set deadlines.
- Retirement is terminal and immediate: block new dials, cancel pending work,
  close physical TCP/QUIC sockets and business streams, wait for pending dials,
  then close the upstream Proxy exactly once. Concurrent Close callers wait for
  the same completion. There is no grace period, hidden redial or resurrection.
- `LookupIP` must be thread safe and honor context cancellation. Close waits for
  pending callbacks; arbitrary non-cooperative callbacks cannot be forcibly
  stopped safely. The adapter does not hide them in detached goroutines.

## DNS and accepted configurations

The package initializes Mihomo resolver/hosts globals once, before parsing any
node. Proxy DNS is dispatched via dial context to that node's injected resolver,
and can only resolve that node's server hostname. Default/system/direct DNS and
ECH exchange fail closed. System hosts are disabled. Globals are never swapped
per node and never restored on Close. No other production code may mutate them
or independently initialize Mihomo; application direct DNS remains separate.

The initial audited surface is SS (cipher/password), VLESS TCP (uuid and optional
TLS), AnyTLS and Hysteria2. Common fields: name/type/server/port. TLS nodes accept
fingerprint/skip-cert-verify/alpn plus servername (VLESS) or sni (AnyTLS/Hysteria2).
Values are parsed by upstream, not translated through a homegrown protocol
schema. Unknown fields are rejected rather than ignored: full runtime config,
dynamic groups, dialer-proxy chains, interface/routing-mark, plugins, smux,
certificate files and advanced transports need separate review and tests.

Two internal policies are enforced regardless of user input:

- Hysteria2 `handshake-timeout=0`: use the caller-bound handshake, avoiding the
  upstream independent background offer context. QUIC session reuse remains.
- AnyTLS `disable-reuse=true`: upstream's reused-session destination write does
  not observe the new dial context. A fresh protocol session per stream keeps
  canceled handshakes inside their socket ownership boundary. HTTP keepalive
  still works. Re-enabling AnyTLS protocol reuse requires a cancellable upstream
  API and a stalled warm-session regression; the original experiment's reuse
  success alone is insufficient.

## Dependency and integration checks

The **root** go.mod/go.sum pin official Mihomo v1.19.32. No fork or protobuf
replace is introduced; the selected protobuf remains v1.34.2 for these paths.
Desktop builds and CI use upstream's `-tags=no_easytier` to exclude the unused
EasyTier WASM. SS, VLESS, AnyTLS and Hysteria2 remain available. Source collection
and independent rebuilds use the same tag; release verification rejects binaries
without it or with EasyTier module metadata. This does not change the adapter's
existing protocol allowlist or require a Mihomo fork.
Go 1.25.0 / Windows amd64 / CGO=0 builds and runs the adapter. The independent
experiment's Go 1.27.1 result is not used as evidence for this baseline. CI stays
on Go 1.25.x with `GOTOOLCHAIN=local`, explicitly builds with CGO=0, and retains
the Linux race check.

Tests cover real local SS/VLESS/AnyTLS/Hysteria2 HTTP via NewMultiPool, concurrent
independent nodes, retired-node refusal with a healthy sibling, active-stream
retirement, cancel then reuse, cold DNS cancellation, stalled TLS and QUIC
handshake cleanup, DNS context isolation, late-success cancellation and
concurrent/idempotent Close. The shared `testfixture` executable contains server
imports; the adapter test process does not import inbound packages. The fixture
permits only fixed loopback destinations and is killed after each test run.

Run `go test -tags=no_easytier ./internal/proxyengine/mihomo` using Go 1.25. For full CI parity use
a clean source tree (`go test -tags=no_easytier ./...`, `go vet -tags=no_easytier ./...`, `CGO_ENABLED=0 go build -tags=no_easytier
./...`); ignored local artifacts with .go files can otherwise pollute `./...`.
This is not public-server interoperability, VRChat or long-duration soak coverage.

Local validation on 2026-10-11: Go 1.25.0/CGO=0 clean-source full tests, vet,
Windows build and Linux amd64 cross-build passed; existing desktop build and
package-notices check passed. Local `-race` could not build because this Windows
host has no gcc. The retained Linux CI race check still needs to run; these
results do not claim race-detector coverage or a Linux runtime test.

Mihomo v1.19.32's GPLv3 text is preserved in `LICENSE.mihomo`. SWD's current
combined distribution uses GPLv3; new original contributions use GPL-3.0-only,
with historical MIT grants and third-party notices preserved. See
[the audit](../../../docs/issue-30-license-audit.md). The pinned go-lzo license
option is a maintainer-accepted non-blocking risk, not a confirmed compatibility
finding. Its presence alone does not reject a release. Required corresponding
source, notices and exact dependency checks still apply. The current desktop
entry does not yet link this adapter; retain actual build evidence after #14
integration. No fork or engine replacement is introduced for this risk.
