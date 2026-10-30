# Managed Mihomo endpoint

The root module pins official Mihomo v1.19.32, with no_easytier and no fork.
Console owns node import, protected configuration and process lifecycle. Runtime
exposes one dialer through the native SWD selector, defaulting to a native
fallback group containing all imported nodes. zashboard operates the selector,
health checks and connections. SWD never ranks or selects individual nodes.

The [canonical design](../../../docs/mihomo-dashboard.md) defines configuration,
consent, control API authority, selection persistence and shutdown. Import stays
bounded to 2 MiB / 2048 entries and validates native stream capabilities without
executing imported groups, rules, listeners, files, plugins or system networking.
Node names stay in protected data; public import diagnostics never contain them.

Runtime installs controlled ParseWithBytes → ApplyConfig once per process.
Core-owned certificate-validated DoH does not read system hosts. SWD's direct DNS
and standard library resolver/transport remain independent. No plaintext core
configuration file is created. A private native controller is authenticated and
accessed through SWD's limited same-origin reverse proxy. Raw core logs are not
exposed; native logrus output is permanently discarded before import parsing.

The pinned native selector and statistics memory cache contain data races.
selector.go synchronizes the native selector; the automatic fallback never
exposes manual pinning. memory.go streams the native process memory query without
mutating the shared cache. These compatibility guards retain native selection,
fallback and tracking; reassess them when upgrading Mihomo.

Mihomo saves and restores selections by their displayed names. Unnamed nodes get
a generic ordinal rather than an endpoint; imported names may themselves contain
addresses. A transient identity turns off selection record reads/writes to
preserve selections in a valid cache; native cache initialization still runs.
The known cache.db is disposable and may be rebuilt by Mihomo. If it cannot be
opened, the proxy and manual selection remain usable in memory; Console reports
that selections cannot be saved. No plaintext native connection configuration
is created. The canonical design defines the exact cache ownership boundary.

The endpoint owns 160 nonqueued admission slots and a ten-second dial deadline.
Slots cover native calls, delivered connections and actual Close completion.
Retirement rejects new work, cancels pending calls and uses one three-second
budget for connections, adapters, providers, controller and cache closure. A
failed or retired core requires application restart. Close its HTTP pools first.

Tests initialize complete cores in separate processes. Five real local protocol
servers cover protocol support, concurrency, cancellation and wrong credentials.
Distinct local exits cover manual selection, preserving pooled connections,
native fallback, traffic tracking and persisted selection after process restart.
Run go test -tags=no_easytier on this package; SWD_B2_SOAK=1 retains the optional
60-second workload. These tests do not claim public proxy performance or VRC
playback acceptance.
