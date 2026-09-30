# Billing

The hub can periodically poll **AWS Cost Explorer** and/or **OCI's
Usage API** for account/tenancy-level cloud cost, and match each host
to its own cloud resource for a per-host cost view. This is **cloud
CLI polling**, not a metrics agent: the hub shells out to the
`aws`/`oci` CLI binaries you already have installed and authenticated
on the hub host — no AWS/OCI SDK dependency, no credentials sent
anywhere but to AWS/OCI's own APIs via their own CLI.

## Requirements

- **AWS**: the `aws` CLI on the hub's `PATH`, authenticated as a
  principal with:

  ```json
  {
    "Version": "2012-10-17",
    "Statement": [
      {
        "Effect": "Allow",
        "Action": ["ce:GetCostAndUsage", "ce:GetCostForecast"],
        "Resource": "*"
      }
    ]
  }
  ```

  Add `ce:GetCostAndUsageWithResources` only if you enable
  `CP_BILLING_AWS_RESOURCES=true` — it requires Cost Explorer's
  resource-level data opt-in and costs more per call.
- **OCI**: the `oci` CLI on the hub's `PATH`, authenticated as a
  principal with a usage-report read policy, e.g.:

  ```text
  Allow group <YourGroup> to read usage-reports in tenancy
  ```

  OCI's Usage API is free to call — no per-request billing.
- Both CLIs are invoked with a fixed argument array (never a shell
  string) and a 60-second timeout per call; see [Exec
  rules](../CODING_CONVENTIONS.md#exec-rules-privileged-child-processes)
  in `CODING_CONVENTIONS.md`.

## Where the hub looks for CLI config

The hub does **not** read your shell's `~/.aws`/`~/.oci` config by
default:

- `HOME` is set to `<CP_DATA_DIR>/cloud-cli` (created `0700` on first
  use).
- `PATH` is a minimal fixed list (`/usr/bin:/bin:/usr/local/bin`, or
  `CP_BILLING_PATH` for a test/smoke harness only).
- Everything else passes through **only if explicitly set** on the hub
  itself: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`,
  `AWS_SESSION_TOKEN`, `AWS_REGION`, `AWS_PROFILE`, `AWS_CONFIG_FILE`,
  `AWS_SHARED_CREDENTIALS_FILE`, `OCI_CLI_CONFIG_FILE` (from
  `CP_OCI_CONFIG_FILE`), `OCI_CLI_PROFILE` (from `CP_OCI_PROFILE`).
  `CP_OCI_TENANCY_ID` overrides the tenancy OCID the CLI's own config
  file would otherwise supply.

## Polling interval and its own API cost

AWS Cost Explorer bills **$0.01 per API call, per page** — this
project makes 2 calls per poll (skipped on the last calendar day of
the month), so the interval you choose directly sets a small recurring
AWS bill:

| Interval | Approx. AWS API cost/month |
|---|---|
| 24h (default) | ~ $0.60 |
| 12h | ~ $1.20 |
| 6h | ~ $2.40 |

Enabling `CP_BILLING_AWS_RESOURCES=true` adds one more call per poll,
scaling the same table proportionally. **OCI's Usage API is free**
regardless of interval. Set the interval via `CP_BILLING_INTERVAL`
(`6h|12h|24h`, default `24h`); once changed from Settings → Billing
(`PUT /api/v1/settings/billing/interval`), the hub-side setting takes
precedence and applies immediately, no restart needed.

## Quiet-skip status model

`CP_BILLING=auto` (default) queries whichever of `aws`/`oci` is
installed and authenticated, silently skipping the other — a missing
CLI, unauthenticated principal, insufficient permissions, or timeout
are **never** treated as hub errors:

| Status | Meaning |
|---|---|
| `not_installed` | The CLI binary isn't on `PATH` |
| `not_configured` | No credentials/config found |
| `auth_failed` | Credentials present but rejected |
| `permission_denied` | Authenticated, but the policy is missing the required action |
| `error` | Reached the CLI/API but got something else unexpected (including a timeout) |
| `ok` | Collected successfully |

`CP_BILLING=off` disables polling entirely. The classified reason is
logged at most once per day per provider — raw stderr and credentials
are never logged.

## Not-connected states and data freshness

A provider showing anything other than `ok` still displays its last
successful snapshot if one exists. `Stale` is set once the time since
the last success exceeds **twice** the current polling interval.

## Currency and tax

All cloud-CLI-reported amounts are shown in whatever currency the CLI
itself reports (normally `USD`), carrying an "excl. tax" label since
Cost Explorer/Usage API figures are pre-tax.

## Host <-> cloud resource matching

Agents report `HostInfo.CloudInstanceID`, detected in this order (each
step 2-second timeout, first non-empty wins): DMI
(`/sys/class/dmi/id/board_asset_tag`), AWS IMDSv2, OCI instance
metadata v2. `CP_CLOUD_METADATA=off` disables detection entirely. A
host that doesn't match shows `matched: false`.

## API and UI

`GET /api/v1/billing` returns every provider's snapshot plus per-host
combined cost. `POST /api/v1/billing/refresh` (admin) triggers an
immediate poll, rate-limited to once per 10 minutes. The dashboard's
**Costs** page shows a provider card per cloud and a per-host cost
table; Settings → Billing exposes status/staleness, the interval
selector, the AWS resource-level toggle, a refresh button, and the
display-currency setting.

## Network cost estimate

Independent of real cloud-CLI billing, cloud-pulse can also
**estimate** what a host's tracked network egress would cost under a
configurable **pricing plan**.

### Builtin plans

Four read-only builtin plans are seeded on every hub, each carrying a
reference-pricing disclaimer and a link to the provider's own pricing
page:

| Plan | Free tier/month | Rate beyond free tier | Pooled? |
|---|---|---|---|
| AWS Free Tier (Internet egress, US) | 100 GB (account-wide) | $0.09/GB up to 10 TB, tiered down to $0.05/GB | Yes |
| OCI Always Free (NA/EU) | 10 TB (tenancy-wide) | $0.0085/GB | Yes |
| OCI Always Free (APAC/Japan/South America) | 10 TB (tenancy-wide) | $0.025/GB | Yes |
| Other / Free | Unlimited | $0/GB | N/A |

"Pooled" means the free allowance is shared across every host assigned
to that plan, matching how AWS/OCI actually bill. A host's plan is
assigned explicitly (`PUT /api/v1/hosts/{id}/pricing`) or falls back to
a provider-based default.

Byte->GB conversion uses the billing-industry decimal GB (10^9 bytes),
not the binary GiB (2^30 bytes) `CP_EGRESS_LIMIT_GB` uses — these are
intentionally separate units for two different purposes.

### Editing plans

Settings → Billing's pricing-plan editor lets you clone/edit a
**custom** plan (free-tier amount, a tiered-rate table, an inbound
price per GB, the pooled-free-tier toggle) with a live preview. Builtin
plans are immutable — `PUT`/`DELETE` on one responds `403
builtin_immutable`.

### Display currency

All storage and calculation is fixed in **USD**. A separate display
setting lets the dashboard render figures in **KRW** via a manually
entered exchange rate — there is no live FX API call, by design.
`GET`/`PUT /api/v1/settings/billing/currency` manage
`display_currency` (`USD|KRW`), `krw_per_usd`, and `rate_updated_at`.
Selecting `KRW` without ever having set a rate is rejected: `400
rate_required`. This setting affects **only** cloud-pulse's own
network-cost estimate display — a cloud CLI's own reported currency is
never converted.

### Audit log

Every pricing-plan create/update/delete, host->plan assignment change,
display-currency/rate change, and billing-interval change is recorded
to the `audit_log` table. See [security.md](security.md#security-model).
