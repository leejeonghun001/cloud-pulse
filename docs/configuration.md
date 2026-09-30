# Configuration reference

## Hub environment variables (`CP_*`)

| Variable | Default | Notes |
|---|---|---|
| `CP_LISTEN` | `:8090` | HTTP listen address; overridden by a hub-side Network settings override once one is confirmed from the dashboard (see [security.md](security.md#network-settings)) |
| `CP_DATA_DIR` | `./data` | Directory holding `cloud-pulse.db` |
| `CP_AGENT_TOKEN` | *(required)* | >= 16 chars; authenticates agent report ingestion |
| `CP_UI_TOKEN` | *(unset)* | >= 8 chars if set; optional static bearer token for scripts (curl/CI) — full read/admin access, never subject to the must-change-password gate. The dashboard itself always requires signing in regardless of this setting. See [Settings UI token](#settings-ui-token) below |
| `CP_ALLOWED_CIDRS` | `100.64.0.0/10,fd7a:115c:a1e0::/48,127.0.0.0/8,::1/128` | Comma-separated CIDRs/IPs allowed to reach the hub; `*` disables the allowlist; overridden by a hub-side Network settings override once confirmed |
| `CP_OFFLINE_AFTER` | `60s` | Host reported "down" after this long since last-seen |
| `CP_CLOUD_INTERVAL` | `15m` (min `1m`) | Interval between S3/R2 collections |
| `CP_ALERT_WEBHOOK_URL` | *(unset)* | Slack- or Discord-compatible webhook for egress alerts; also seeds a `"Default webhook"` notify channel on the v0.5.0 upgrade migration |
| `CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS` | `0` | `1` relaxes the SSRF guard on Discord/Telegram/WhatsApp notify channels — **tests/fakes only**, never production |
| `CP_UPDATE_CHECK` | `true` | `false` disables all outbound checks against GitHub for a newer release |
| `CP_UPDATE_LATEST_URL` | *(unset)* | Override the "latest release" URL the hub polls |
| `CP_RELEASE_BASE_URL` | *(unset)* | Override the base URL `update` downloads the binary + `checksums.txt` from |
| `CP_LOG_LEVEL` | `info` | `debug\|info\|warn\|error` |
| `CP_LOG_FORMAT` | `text` | `text\|json` |
| `CP_S3_BUCKETS` | *(unset)* | `name[:region],...` (see [billing.md](billing.md)) |
| `CP_S3_REGION` / `AWS_REGION` | `us-east-1` | Default region for bucket entries without `:region` |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` | *(unset)* | AWS credentials for CloudWatch calls |
| `CP_S3_FILTER_ID` | `EntireBucket` | S3 request-metrics filter ID |
| `CP_R2_ACCOUNT_ID` / `CP_R2_API_TOKEN` | *(unset)* | Cloudflare R2 credentials |
| `CP_R2_BUCKETS` | *(unset, = all)* | Comma-separated R2 bucket names |
| `CP_BILLING` | `auto` | `auto\|off` — periodic AWS/OCI CLI cost polling |
| `CP_BILLING_INTERVAL` | `24h` | `6h\|12h\|24h`; a Settings → Billing override takes precedence at runtime once saved |
| `CP_BILLING_AWS_RESOURCES` | `false` | `true` enables per-resource AWS Cost Explorer queries (extra paid calls) |
| `CP_OCI_CONFIG_FILE` / `CP_OCI_PROFILE` / `CP_OCI_TENANCY_ID` | *(unset)* | Override the OCI CLI's config file path/profile/tenancy OCID |
| `CP_BILLING_PATH` | *(unset)* | Overrides the minimal PATH passed to `aws`/`oci` child processes — **test/smoke harnesses only** |
| `CP_STORAGE_INTERVAL` | `1h` | `15m\|1h\|6h\|24h` — Google Drive/Dropbox polling interval; a Settings → Storage override takes precedence once saved (see [storage.md](storage.md)) |
| `CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS` | `0` | `1` relaxes the SSRF guard on storage-provider OAuth/API endpoints, redirecting to `CP_STORAGE_FAKE_BASE_URL` — **tests/fakes only**, mirrors `CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS` |
| `CP_STORAGE_FAKE_BASE_URL` | *(unset)* | The fake server base URL requests are redirected to when the above is set — **tests/fakes only** |

S3 collection is enabled only when `CP_S3_BUCKETS` is non-empty **and**
both AWS key env vars are set. R2 collection is enabled only when both
`CP_R2_ACCOUNT_ID` and `CP_R2_API_TOKEN` are set.

### Settings UI token

`CP_UI_TOKEN` is entirely optional — most installs never set it. It
exists for scripts/CI that need to call the REST API without signing
in interactively (there is no separate "API-only" account; this token
authenticates as full admin). The hub installer's `--ui-token TOKEN`
sets it explicitly; `--generate-ui-token` creates a random one during
install and prints it once; `--rotate-ui-token` replaces an existing
one (invalidating the old value immediately). Unset (the default),
only session-based sign-in works.

## Hub CLI flags (`cloud-pulse-hub`)

| Flag | Purpose |
|---|---|
| `-listen ADDR` | Override `CP_LISTEN` |
| `-data-dir DIR` | Override `CP_DATA_DIR` |
| `-check-config` | Load + validate config, print a secret-redacted summary, exit |
| `-gen-token` | Print a random 32-byte hex token (for `CP_AGENT_TOKEN`/`CP_UI_TOKEN`) and exit |
| `-version` | Print version info and exit |

## Hub subcommands

| Command | Purpose |
|---|---|
| `cloud-pulse-hub update [--check] [--version vX.Y.Z] [--no-restart]` | Self-update; see [upgrade.md](upgrade.md) |
| `cloud-pulse-hub systemd-unit print --bin-path P --env-file E [--user U --group G] [--read-write-path D]` | Render a systemd unit to stdout |
| `cloud-pulse-hub systemd-unit apply [--unit-path PATH] [--no-reload]` | Re-render an installed unit in place if drifted |
| `cloud-pulse-hub reset-password [--data-dir DIR] [--password-stdin]` | Recover from a lost admin password; see [security.md](security.md#recovery) |
| `cloud-pulse-hub reset-network [--data-dir DIR]` | Recover from a Network-settings lockout; see [security.md](security.md#network-settings) |
| `cloud-pulse-hub notify verify --credentials-file FILE [--platform discord\|telegram\|whatsapp\|all] [--cleanup] [--timeout DURATION] [--json]` | Real-account notification test; see [notifications.md](notifications.md) |
| `cloud-pulse-hub storage verify --provider googledrive\|dropbox [--name NAME] [--data-dir DIR] [--json]` | Real-account storage-quota test; see [storage.md](storage.md) |

## Agent environment variables (`CP_*`)

| Variable | Default | Notes |
|---|---|---|
| `CP_HUB_URL` | *(required)* | Hub base URL, `http://` or `https://` |
| `CP_AGENT_TOKEN` | *(required)* | >= 16 chars; must match the hub's `CP_AGENT_TOKEN` |
| `CP_HOST_ID` | sanitized hostname | 1–128 chars of `[A-Za-z0-9._-]` if set explicitly |
| `CP_INTERVAL` | `15s` (min `5s`) | Collect/report interval |
| `CP_PROVIDER` | `auto` | `auto\|aws\|oci\|other`; `auto` detects via Linux DMI |
| `CP_EGRESS_LIMIT_GB` | *(unset = provider default)* | `0` = unlimited; GiB units, fractional allowed |
| `CP_NET_EXCLUDE` | `lo,lo0,docker*,veth*,br-*,virbr*,tailscale*,utun*,cni*,flannel*,cali*,kube*,vxlan*,tun*,wg*,zt*` | Comma-separated interface-name globs excluded from egress/network accounting |
| `CP_TIME_SYNC` | `hub` | `hub\|local` — see [architecture.md](architecture.md#time-synchronization) |
| `CP_SEND_JITTER` | `0s` | Max random delay between collecting and sending a sample; must be `<=` half of `CP_INTERVAL` |
| `CP_DOCKER` | `auto` | `auto\|off\|<socket path/URL>` — see [storage.md](storage.md#docker--podman-inventory-privilege-tradeoff) |
| `CP_LOG_LEVEL` | `info` | `debug\|info\|warn\|error` |
| `CP_LOG_FORMAT` | `text` | `text\|json` |
| `CP_REMOTE_UPDATE` | `off` | `off\|on` — opts this agent in to hub-triggered remote updates; the hub can never turn this on remotely |
| `CP_CLOUD_METADATA` | `auto` | `auto\|off` — detects `HostInfo.CloudInstanceID` via DMI/AWS IMDSv2/OCI instance metadata |

## Agent CLI flags (`cloud-pulse-agent`)

| Flag | Purpose |
|---|---|
| `-once` | Collect two samples 1s apart, print the second as JSON, exit (no hub config needed) |
| `-print-host` | Print detected `HostInfo` as JSON and exit (requires hub config) |
| `-env-file FILE` | Read configuration from a `KEY=VALUE` file instead of the process environment (used by the macOS launchd plist and Windows service wrapper; plain `KEY=VALUE`, no shell interpretation, CRLF-tolerant) |
| `-version` | Print version info and exit |

## Agent subcommands

| Command | Purpose | Platforms |
|---|---|---|
| `cloud-pulse-agent update [--check] [--version vX.Y.Z] [--no-restart] [--from-request FILE --result-dir DIR]` | Self-update, or apply a hub-delivered remote-update request | all |
| `cloud-pulse-agent systemd-unit print\|apply ...` | Render/apply the systemd unit | Linux |
| `cloud-pulse-agent plist print\|apply ...` (alias `launchd`) | Render/apply the launchd plist | macOS |
| `cloud-pulse-agent service install\|uninstall\|start\|stop\|status [--env-file] [--bin-path]` | Windows service control | Windows |

Same `update` exit codes as the hub: `0` success/current, `10`
(`--check` only) update available, `1` error.
