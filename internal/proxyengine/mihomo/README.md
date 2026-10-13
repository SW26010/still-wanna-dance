# Fixed exits from the complete Mihomo runtime

This package owns SWD's single embedded B2 runtime. The root module pins official
Mihomo v1.19.32; desktop builds use the no_easytier tag, with no fork or replacement.
The old A adapter, custom DNS/dialer bridges and protocol rewrites have been removed.
Historical experiments and their evidence remain in the repository.

Import accepts native YAML node lists, URIs and Base64 subscriptions, bounded to
2 MiB / 2048 entries. It never fetches a subscription or executes a full imported
configuration. Normalize uses upstream validation after a cross-protocol
capability gate. Groups, chains, file access, plugins, interfaces/routing marks,
overlay stacks and smux wrappers are rejected. Ordinary stream options retain
native semantics; there is no per-protocol field whitelist. Audited constructors
do not dial or alter core globals; temporary AnyTLS workers are closed immediately.
Re-audit these guarantees when upgrading Mihomo.

Names are removed, host/port syntax is normalized, and credentials/defaults are
preserved. Equivalent encodings may conservatively have different identities.
Console owns keyed identities and DPAPI version=2 snapshots; old snapshots require
explicit reimport. Native logrus output is permanently discarded before parsing;
only fixed application diagnostics may be exposed. Never subscribe to the raw
native log event stream.

NewRuntime installs a controlled ParseWithBytes → ApplyConfig configuration once
per process, in an absolute application-owned directory. It disables proxy
listeners, controllers, TUN, sniffing, NTP and automatic geo updates. Core-owned,
certificate-validated literal-IP DoH does not read system hosts; SWD's direct DNS
and standard library resolver/transport remain independent. The original node
data is never written as a core config file.

Runtime.Proxies returns existing upstreamrequest.Proxy values for NewMultiPool.
Each dialer captures a fixed native object through proxydialer.New(proxy, true),
so rebinding a name cannot redirect that exit. Console activates at most five
saved nodes; external SOCKS5 remains available. Changes apply after application
restart, never by reinitializing core globals.

Each node owns 32 nonqueued admission slots. A slot covers native dialing, a
delivered connection and its actual Close completion. Canceled native calls or
blocked cleanup retain their slots, so abandoned work stays bounded and cannot
starve sibling nodes. The default dial deadline is ten seconds. Returned streams
follow net.Dialer ownership: callers close them and control subsequent I/O.

Retirement rejects new work immediately and cancels pending dials. Close initiates
connection/adapter/provider cleanup and waits at most three seconds for the whole
runtime to drain. Timeout is reported; it does not assert that native globals were
unloaded. A failed or retired runtime cannot restart in the same process.
Close the HTTP pool before its Runtime. Local capacity/retirement/caller cancellation
are separate Monitor diagnostics; genuine timeouts still affect node health.

Tests use independent processes for complete-core initialization, with no singleton
reset hook. Five local protocol servers (including VMess/WS) exercise fixed exits, concurrency, warm
cancellation, wrong credentials and retirement. Admission tests cover uncooperative
native calls and blocking Close. Run go test -tags=no_easytier on this package.
Set SWD_B2_SOAK=1 to enable TestRuntimeSoak, a 60-second paced workload.

See [B2 development validation](../../../docs/issue-14-b2-integration.md) for exact
tested revisions and limitations, and [license/source review](../../../docs/licenses/README.md)
for distribution inputs. Loopback checks do not replace real-game acceptance.
