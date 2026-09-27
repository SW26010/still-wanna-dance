# Real cache listing baseline — 2026-09-27

Measured the current `ReadCachePage` implementation against
`<portable-directory>\stepstash-data` using read-only listing.
No engine was started; no video contents were read or modified.

- Windows/amd64, AMD Ryzen 7 9850X3D, Go 1.27.1.
- 10,076 matching MP4 files, approximately 433.85 GiB.
- `stepstash.sqlite`: 12,169,216 bytes.
- Separate PowerShell directory enumeration: 83 ms. This is not a Go phase measurement.
- Six scenarios, three single-operation samples each, sequentially in table order.
- OS caches were not cleared. The first listing is a first observed call, not a controlled cold-cache result.

| Scenario | Sample 1 (s) | Sample 2 (s) | Sample 3 (s) |
| --- | ---: | ---: | ---: |
| Size, offset 0 | 35.561 | 2.465 | 2.445 |
| Size, offset 50 | 2.468 | 2.817 | 2.644 |
| Size, offset 9950 | 2.557 | 2.493 | 2.549 |
| Recent, offset 0 | 2.463 | 2.524 | 2.527 |
| Title, offset 0 | 2.738 | 2.502 | 2.499 |
| Search with no matches | 3.177 | 3.593 | 3.042 |

Unfiltered calls returned a total of 10,076 matches and allocated approximately
64.3 MB and 903,000 objects per operation. The missing-search calls returned zero
matches and allocated approximately 76.5 MB and 1,276,000 objects. These are
cumulative allocation counts, not retained heap size or peak process memory.

Reproduce from the repository root in PowerShell:

```powershell
$env:STEPSTASH_BENCH_CACHE_ROOT = '<portable-directory>\stepstash-data'
go test ./internal/cacheproxy -run '^$' -bench '^BenchmarkReadCachePageExisting$' -benchtime=1x -count=3 -benchmem
```

The benchmark skips unless the environment variable is set. It invokes only the
existing read-only listing function, with a two-minute context timeout per call.
All 18 calls passed. These samples do not establish p95 latency or performance
under concurrent downloads, and do not isolate SQL versus file identity costs.

## Next change to evaluate

Batch metadata needed for filtering and sorting, retain actual directory files
as the source of membership and size, then load display details and generate
identity stamps only for the selected page. Preserve unknown-file visibility,
search across all song associations, deterministic sorting, and live validation
before deletion or opening a file. Re-run the same benchmark after that change.
Consider a short-lived snapshot only if repeated scans remain too slow.

## After batched listing implementation

Re-ran the identical command against the same 10,076 files. OS caches were not
cleared; this is a repeated-access comparison, not a cold-start comparison.
The full Go test suite was also running during this measurement.

| Scenario | Sample 1 (ms) | Sample 2 (ms) | Sample 3 (ms) |
| --- | ---: | ---: | ---: |
| Size, offset 0 | 49.937 | 36.820 | 27.017 |
| Size, offset 50 | 23.923 | 25.128 | 26.010 |
| Size, offset 9950 | 32.550 | 24.739 | 24.046 |
| Recent, offset 0 | 49.968 | 50.694 | 48.148 |
| Title, offset 0 | 105.091 | 105.398 | 98.975 |
| Search with no matches | 88.338 | 90.301 | 92.129 |

Size listings allocated about 5.7 MB per call, recent listings 10 MB, and title
or search listings 11.4–11.6 MB. All calls passed with unchanged match totals.

The implementation enumerates actual directory files, batches only the metadata
needed for the requested filter/order in groups of 256 keys, and loads full
details and file identity stamps for up to 50 displayed entries. Size sorting
without a search does not need database metadata until the page is selected.
Deletion and file opening still use the original live validation path. Missing
files discovered while loading a page are skipped and replaced by the next
candidate. No snapshot, schema migration, or additional database index was added.

Regression coverage compares all returned details and stamps against individual
entry reads across batch boundaries, sorting ties, first/second/final/out-of-range
pages, missing files, unknown files, Unicode search, and HTTP event recency. The
existing tests cover hidden song associations, stale deletion stamps, protection,
and symlink rejection. `go test ./...` passed on Windows; no controlled cold-cache
or Linux runtime measurement has been made.
