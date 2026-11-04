# Managed Mihomo endpoint

The root module pins official Mihomo v1.19.32, with no_easytier and no fork.
Console owns original native configuration import, protected storage and lifecycle.
The runtime adds a SWD selector and native automatic fallback before existing
groups. Both reference original groups; without groups, native include-all
covers inline nodes and providers. SWD business dials bypass routing rules.
Original DNS, hosts, names, policies and references remain native.

The [canonical design](../../../docs/mihomo-dashboard.md) defines the single
managed override policy. Files, decoded Base64 documents and one-shot URL
responses share the same bounded native configuration path. Original bytes are
saved with DPAPI; acquisition URLs are discarded. Legacy node snapshots are
ordinary invalid configurations. Native cache/resource files may be plaintext;
SWD does not encrypt or rewrite them.

Full native checking runs in a separate process via a private stdin entry point
and never calls ApplyConfig. It may load/download native resources but cannot
change the running core's globals. Startup uses the same override policy and
ParseRawConfig → ApplyConfig once, after consent. Imported listeners, controller,
TUN, other network interception and Geo periodic updates are disabled. Core DNS
retains the imported policy but never reads system hosts. SWD's direct resolver
and standard library networking remain independent. Raw native output is discarded.

selector.go synchronizes native selectors, including original groups. Automatic
fallback/url-test groups do not expose native manual pinning, whose state is
unsynchronized in this version. memory.go avoids the native shared statistics
cache race. Reassess these compatibility guards when upgrading Mihomo.

Mihomo persists selection by original name, without SWD identity suffixes or
channel.key. Native cache failure remains nonfatal: proxy/manual selection work
in memory, with a fixed warning. Native shutdown persists its configured state
before the cache closes. Rule and proxy providers are closed within retirement.

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
