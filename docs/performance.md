# Local development performance baseline

This is a measured **local development** baseline, not a fleet SLO, production
capacity estimate, release-binary comparison, or installed-agent footprint
claim. The run used source-built `dev (none, unknown)` hub/agent binaries from
the current working tree (HEAD reference `33701617cc782710092086714559aba45ddb441b`);
no product changes were made for performance.

## Reproduce

To collect a new run, use a fresh disposable checkout containing the same
intended source/input state and an empty `.verification/round31/performance`
destination, then run from its repository root:

```sh
python3 scripts/measure-round31-performance.py
```

The standard-library harness builds with `CGO_ENABLED=0`,
`-mod=readonly`, `GOPROXY=off`, `GOSUMDB=off`, `GOENV=off`, `GOWORK=off`, and
`GOTOOLCHAIN=local`. It reuses the existing local module cache with downloads
disabled; `-mod=readonly` does not make that cache filesystem-read-only. Go
build cache, binaries, and fresh hub data live in an automatically removed
`/tmp/opencode/round31-performance-*` scratch directory. It refuses to overwrite
existing evidence. Preserve the archived measurement: do not delete or move its
evidence to make room for a rerun. On success, new command/build metadata,
measurements, raw first-response headers, and selected raw document/asset bodies
are saved privately in `.verification/round31/performance/`.

## HTTP TTFB and body sizes

Measured 2026-10-06 22:38:14–22:39:52 UTC against the separately source-built
development hub on `127.0.0.1:18132`. It used a fresh temporary SQLite data
directory (no seeded hosts/reports), synthetic agent/UI tokens,
`CP_UPDATE_CHECK=false`,
`CP_BILLING=off`, a loopback-only allowlist, and no configured or inherited
cloud credentials. The dashboard API requests used the synthetic static UI
token. All paths returned HTTP 200.

Each path has 20 sequential `curl` requests. TTFB is curl's
`time_starttransfer`, including local connection setup; median/p95/max cover all
20 requests. “First” is the first request for that path after the hub passed a
`/healthz` readiness check. The hub process and database were therefore already
warm; the next column is not a process-cold or OS-cache-cold result. The 19
repeat requests used new curl processes and TCP connections. Percentiles use
linear interpolation at `(n-1)*p` (type 7); p95 from only 20 samples is a small
sample estimate, not a tail guarantee.

| Kind | GET path | First TTFB ms | Median ms | p95 ms | Sample max ms | First body bytes |
|---|---|---:|---:|---:|---:|---:|
| Document | `/` | 2.206 | 0.380 | 0.501 | 2.206 | 1,007 |
| Document | `/login` | 0.369 | 0.370 | 0.409 | 0.432 | 1,007 |
| Document | `/settings/network` | 0.412 | 0.383 | 0.408 | 0.412 | 1,007 |
| Embedded asset | `/assets/app.css` | 0.411 | 0.416 | 0.542 | 0.587 | 148,294 |
| Embedded asset | `/assets/js/main.js` | 0.424 | 0.423 | 0.493 | 0.557 | 16,916 |
| Embedded asset | `/assets/js/theme-init.js` | 0.450 | 0.374 | 0.449 | 0.450 | 1,424 |
| Embedded asset | `/assets/vendor/uplot/uPlot.min.css` | 0.408 | 0.392 | 0.559 | 2.884 | 1,857 |
| API | `/api/v1/hosts` | 0.854 | 0.691 | 0.814 | 0.854 | 39 |
| API | `/api/v1/egress` | 0.714 | 0.712 | 0.771 | 0.784 | 31 |
| API | `/api/v1/buckets` | 0.699 | 0.690 | 0.802 | 0.820 | 31 |
| API | `/api/v1/billing` | 0.736 | 0.635 | 0.738 | 0.782 | 89 |
| API | `/api/v1/version` | 0.482 | 0.373 | 0.520 | 1.241 | 204 |
| API | `/api/v1/settings` | 0.902 | 0.681 | 0.779 | 0.902 | 229 |
| API | `/api/v1/fleet/versions` | 0.606 | 0.559 | 0.626 | 0.628 | 188 |

The document responses were served from the embedded SPA fallback; these direct
path requests are not timings for client-side navigation. A URL fragment such
as `/#/settings/network` is not sent in an HTTP request. Browser rendering,
JavaScript execution, SPA hash-route navigation, browser cache behavior, and
user-perceived load were **not measured** (UX is held).

For the selected documents/assets, the saved first-response headers show no
`Content-Encoding` header (identity body). `Content-Length`, curl
`size_download`, and saved raw body size agree: the body lengths above are
encoded HTTP entity-body bytes, not decoded page size or TCP/IP/wire bytes.
Headers and HTTP framing are excluded. API response bodies were not retained;
their measured body byte counts are recorded in the private JSON evidence.

## Agent development measurements

Both agent measurements used the exact development binary above. `-version`
returned `dev (none, unknown)` successfully. `-once` completed its local
two-sample collection in **1.005 s** (exit 0; 1,039 stdout bytes); only JSON key
names and byte count were retained, not host metric values. Its four
approximately 250 ms `/proc` observations reported CPU mean/median/p95/sample
max of **0/0/0/0% of one core** and VmRSS/VmHWM mean/median/p95/sample max of
**6,672/6,672/6,672/6,672 KiB**. This very short sampled CPU result is
quantized and is not a claim of zero CPU work.

The normal loop ran for **60.000 s** with `CP_INTERVAL=15s` and an owned fake
hub bound only to `127.0.0.1:18134`. Its environment set `CP_TIME_SYNC=local`,
`CP_CLOUD_METADATA=off`, `CP_DOCKER=off`, and `CP_REMOTE_UPDATE=off`, with
provider `other`, synthetic host/token, and ambient credentials/proxies
scrubbed. It sent four `POST /api/v1/agent/report` requests to the fake hub;
there were no time-sync requests. The fake hub drained but did not parse, retain,
log, or forward payloads; it returned no redirects. All owned processes and
listeners were stopped afterward.

Linux `/proc/<pid>/stat` utime+stime deltas divided by `SC_CLK_TCK` and actual
sample intervals give one-core CPU percentages; the reported CPU mean is the
**unweighted arithmetic mean of sampled interval percentages**, not total
process CPU over the whole window. `/proc/<pid>/status` VmRSS and
VmHWM are sampled every nominal 0.25 s. For 240 loop samples over 60.000 s:

| Observation | Mean | Median | p95 | Sample max |
|---|---:|---:|---:|---:|
| CPU, % of one core | 0.0166% | 0.0000% | 0.0000% | 3.9936% |
| VmRSS, KiB | 10,402.4 | 10,736 | 11,056 | 11,056 |
| VmHWM, KiB | 10,402.4 | 10,736 | 11,056 | 11,056 |

The sampled maximum is what these observations saw, not a guaranteed absolute
instantaneous peak. The report request count and aggregate drained byte count
are in `.verification/round31/performance/measurement.json`; no report payload
is retained there.

## Host, binaries, and limitations

- Generic Linux arm64 single-board-computer-class host, 4 logical CPUs.
- Go `go1.27.1 linux/arm64`; Python 3.13.5; curl 8.14.1.
- Load averages (1/5/15 minute): start `0.847/0.829/0.630`, finish
  `1.158/1.209/0.805`. First available thermal-zone reading: start 50.70 °C,
  finish 51.25 °C. These host observations can include unrelated system load.
- Exact source-built development binary SHA-256: hub
  `c1df45b10dad1f8b2b627460d0d1327b186cfe12e4529c4149dbd456e0bd7b11`; agent
  `533d931c06b2afc999b98a429747ce9fd0c43e946a74a0a3419b9d97c0397bdf`.
- The measurements above are the archived run made with the original harness
  SHA-256 `4ee01ac7b17d72a00a4af70f42d39df7b7a0dcc86b4939d86d76c933044833d9`,
  preserved at `.verification/round31/performance/measurement-harness.original.py`.
  The active harness has since received safety-only corrections; it did not
  generate or change these measurements, and no benchmark was rerun for those
  corrections. Active corrected harness SHA-256:
  `0aa945132b367e390957cfd79c648639a13b735e3944b3a2bd1daf65ff477b45`.
- Raw captured headers/bodies, all measured route samples, configuration
  summary, start/finish context, hashes, and build log are under the private
  `.verification/round31/performance/` evidence path. This is a local synthetic
  baseline only; it does not establish remote-network behavior, production
  database scale, installed service resource use, browser rendering, or an SLO.
