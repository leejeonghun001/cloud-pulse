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

## Agent-side OCI collection (instance principal)

If the hub can't run the `oci` CLI (or shouldn't hold an OCI API key),
an agent running on any OCI instance in the tenancy can query the Usage
API itself, authenticating as the instance through an **instance
principal**. No API key file is stored anywhere. Settings → Billing has
a step-by-step guide that fills in the commands below from your OCIDs.

1. **Dynamic group** (Identity & Security → Domains → Default → Dynamic
   groups) with the matching rule `instance.id = '<instance OCID>'`.
2. **Policy** in the root compartment:

   ```text
   Allow dynamic-group CostReader to read usage-reports in tenancy
   Allow dynamic-group CostReader to read metrics in tenancy
   ```

   The second statement is only needed for the instance's monthly VNIC
   traffic (below); costs work without it.

   For a dynamic group in a non-Default identity domain, write
   `'<domain>'/'CostReader'`.
3. **Install the CLI system-wide.** The agent runs as the `cloud-pulse`
   system user under `ProtectHome=read-only`, so a CLI under a personal
   home directory is unusable:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/oracle/oci-cli/master/scripts/install/install.sh -o /tmp/oci-install.sh
   sudo bash /tmp/oci-install.sh --accept-all-defaults \
     --install-dir /opt/oci-cli --exec-dir /usr/local/bin --script-dir /opt/oci-cli/scripts
   ```

4. **Test as the agent's user** — `NotAuthorizedOrNotFound` means the
   dynamic group or policy is wrong or hasn't propagated yet:

   ```bash
   sudo -u cloud-pulse env HOME=/tmp oci usage-api usage-summary request-summarized-usages \
     --auth instance_principal --tenant-id "<tenancy OCID>" \
     --time-usage-started "$(date -u +%Y-%m-01T00:00:00Z)" \
     --time-usage-ended "$(date -u -d tomorrow +%Y-%m-%dT00:00:00Z)" \
     --granularity DAILY --query-type COST
   ```

5. **Enable it in `/etc/cloud-pulse/agent.env`** and restart the agent:

   ```bash
   CP_OCI_BILLING=instance_principal
   CP_OCI_TENANCY_ID=<tenancy OCID>
   # CP_OCI_CLI=/path/to/oci         only if not in /usr/local/bin:/usr/bin:/bin
   # CP_OCI_BILLING_INTERVAL=1h      default 1h, minimum 15m
   ```

The agent queries once at startup and then every interval, on its own
goroutine so a slow CLI call never delays metric samples. It runs the
CLI with the same fixed argument array and 60-second timeout as the
hub, a minimal `PATH`, and `HOME` set to a private directory under the
service's private `/tmp`. The resulting snapshot rides on the next
agent report; the hub validates it (OCI only, known status, finite
amounts, bounded size), records the reporting host as `source_host`, and
merges it exactly like a hub-collected snapshot, so last-success values
survive a failed query. A hub with `CP_BILLING=off` ignores it.

Each collection also reads the instance's own month-to-date traffic
from OCI Monitoring (`oci_vcn` namespace: `VnicFromNetworkBytes` in,
`VnicToNetworkBytes` out, summed per day from the start of the UTC
month), finding its compartment and VNICs through instance metadata.
The host detail page shows it as "Measured by OCI (VNIC)" beside the
agent's own interface counters. It counts every byte through the VNIC,
internal VCN traffic included, so it is a cross-check rather than the
billed internet egress. Without the `read metrics` policy it reports
`permission_denied` and the page says which statement to add; costs
are unaffected. Monitoring does not refuse such a query — it hides the
metrics the instance may not read, so the CLI exits 0 with empty
output; cloud-pulse reports that as the likely missing policy. To see
what the instance can read, list its metrics with
`oci monitoring metric list --compartment-id <compartment> --compartment-id-in-subtree true --all --auth instance_principal`;
VNIC traffic lives in the `oci_vcn` namespace.

On the hub, keep `CP_BILLING` on and leave `CP_OCI_TENANCY_ID` unset:
while the stored OCI snapshot is agent-reported, the hub's own poll
skips OCI instead of overwriting it with `not_configured`. Enable this
on **one** agent per tenancy — every agent shares the hub's single OCI
snapshot, so two reporters (or an agent plus a hub-side tenancy) simply
overwrite each other. Any holder of the agent token can submit this
snapshot, the same trust level as the metrics it already reports.

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
combined cost. `POST /api/v1/billing/refresh` (admin) re-runs the
hub's own CLI collection, at most once per 10 minutes (AWS bills each
call), and asks every agent that collects a provider itself to query
again on its next report (at most once per 2 minutes per agent; the
OCI APIs are free). It answers `429` only when neither could run; the
response lists the agents asked in `agent_refresh_requested`. The dashboard's
**Costs** page shows a provider card per cloud (noting the agent for an
agent-collected snapshot) and a per-host cost table; Settings → Billing
exposes status/staleness, the interval selector, the AWS resource-level
toggle, a refresh button, the display-currency setting, and the
agent-side OCI setup guide.

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
