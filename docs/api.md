# REST API

All responses are JSON. Read endpoints (`GET`, except `/healthz` and
static assets) require signing in (session bearer token) or the
optional `CP_UI_TOKEN` bearer token — see
[security.md](security.md#sign-in-and-accounts). **Admin** endpoints
require the same; there is no "admin_disabled" mode.

| Method & path | Purpose | Auth |
|---|---|---|
| `POST /api/v1/agent/report` | Ingest a batch of samples; response includes `server_time_ms` | Agent token |
| `GET /api/v1/agent/time` | Hub wall clock, for agent NTP-style offset estimation | Agent token |
| `POST /api/v1/auth/login` | Sign in with `admin`/password; issues a session token | None (rate-limited) |
| `POST /api/v1/auth/logout` | Delete the caller's current session | Session or API token |
| `GET /api/v1/auth/me` | Identify the caller | Session or API token |
| `POST /api/v1/auth/password` | Change the admin password; issues a fresh session, revokes all others | Session or API token |
| `GET /api/v1/auth/sessions` | List active sessions | Session or API token |
| `POST /api/v1/auth/sessions/revoke-others` | Sign out every session except the caller's own | Session or API token |
| `GET /api/v1/hosts` | List all hosts with status, latest sample, egress usage | Session or API token |
| `GET /api/v1/hosts/{id}` | One host's summary | Session or API token |
| `GET /api/v1/hosts/{id}/metrics?range=1h\|6h\|24h\|7d\|30d` | Time series for charts | Session or API token |
| `GET /api/v1/egress?month=YYYY-MM` | Per-host outbound + inbound egress usage for a month | Session or API token |
| `GET /api/v1/buckets` | Latest S3/R2 stats, 24h history, collector status | Session or API token |
| `GET /api/v1/version` | Build version/commit/date + self-update check status | Session or API token |
| `GET /api/v1/settings` | Hub config summary + per-host limits | Admin |
| `GET /api/v1/settings/agent-token` | Reveal the agent token + ready-to-run install commands (per OS) | Admin |
| `PUT /api/v1/hosts/{id}/limits` | Set/clear a host's outbound/inbound limit overrides | Admin |
| `PUT /api/v1/settings/alerts` | Set/clear the hub-side alert webhook URL override | Admin |
| `POST /api/v1/settings/alerts/test` | Send a test notification to the effective webhook URL | Admin |
| `GET /api/v1/settings/network` | Adapters, listen config, allowlist, listener status, pending change | Admin |
| `PUT /api/v1/settings/network` | Set the listen config + allowlist (validated; may return `pending`) | Admin |
| `POST /api/v1/settings/network/confirm` | Persist a pending network change | Admin |
| `POST /api/v1/settings/network/revert` | Revert a pending network change immediately | Admin |
| `DELETE /api/v1/settings/network` | Drop the hub-side network override | Admin |
| `GET /api/v1/alerts/channels` | List notify channels, secrets redacted | Admin |
| `POST /api/v1/alerts/channels` | Create a notify channel | Admin |
| `PUT /api/v1/alerts/channels/{id}` | Update a notify channel | Admin |
| `DELETE /api/v1/alerts/channels/{id}` | Delete a notify channel | Admin |
| `POST /api/v1/alerts/channels/{id}/test` | Send a sample notification to a saved channel | Admin |
| `POST /api/v1/alerts/channels/test` | Send a sample notification using an unsaved (draft) channel config | Admin |
| `GET /api/v1/alerts/rules` | List alert rules | Admin |
| `POST /api/v1/alerts/rules` | Create an alert rule | Admin |
| `PUT /api/v1/alerts/rules/{id}` | Update an alert rule | Admin |
| `DELETE /api/v1/alerts/rules/{id}` | Delete an alert rule and its persisted state | Admin |
| `POST /api/v1/alerts/rules/{id}/preview` | Report whether a rule is currently satisfied, without altering state | Admin |
| `GET /api/v1/alerts/events?state=&host=&limit=&before=` | Paginated alert event history | Session or API token |
| `GET /api/v1/alerts/active` | Every currently-firing event | Session or API token |
| `GET /api/v1/hosts/{id}/inventory` | A host's most recently reported listening ports + Docker containers | Session or API token |
| `GET /api/v1/billing` | Provider cloud-cost snapshots + per-host cost view | Session or API token |
| `POST /api/v1/billing/refresh` | Trigger an immediate AWS/OCI CLI poll; rate-limited to once per 10 minutes | Admin |
| `PUT /api/v1/settings/billing/interval` | Set the hub-side billing polling interval override | Admin |
| `GET /api/v1/billing/plans` | List network-cost pricing plans (builtin + custom) | Session or API token |
| `POST /api/v1/billing/plans` | Create a custom pricing plan | Admin |
| `PUT /api/v1/billing/plans/{id}` | Update a custom pricing plan | Admin |
| `DELETE /api/v1/billing/plans/{id}` | Delete a custom pricing plan | Admin |
| `PUT /api/v1/hosts/{id}/pricing` | Assign a pricing plan to a host | Admin |
| `GET /api/v1/billing/network?month=YYYY-MM` | Estimated network egress cost per host for a month | Session or API token |
| `GET /api/v1/settings/billing/currency` | Current display-currency settings | Admin |
| `PUT /api/v1/settings/billing/currency` | Set the display currency | Admin |
| `GET /api/v1/storage/accounts` | List connected storage accounts, secrets redacted | Session or API token |
| `POST /api/v1/storage/accounts` | Create a storage account (provider + non-secret config) | Admin |
| `DELETE /api/v1/storage/accounts/{id}` | Revoke and delete a storage account | Admin |
| `POST /api/v1/storage/accounts/{id}/oauth/start` | Start the OAuth device/PKCE flow for an account | Admin |
| `POST /api/v1/storage/accounts/{id}/oauth/complete` | Complete the OAuth flow (poll result or pasted code) | Admin |
| `POST /api/v1/storage/refresh` | Trigger an immediate storage-usage poll; rate-limited to once per minute | Admin |
| `PUT /api/v1/settings/storage/interval` | Set the hub-side storage polling interval override | Admin |
| `GET /api/v1/agents/updates?batch=` | List remote-update jobs, optionally filtered to one batch | Admin |
| `POST /api/v1/agents/updates` | Create a remote-update batch | Admin |
| `POST /api/v1/agents/updates/{job_id}/retry` | Re-queue a failed job as a new job | Admin |
| `POST /api/v1/agents/updates/{batch_id}/cancel` | Cancel every still-`queued` job in a batch | Admin |
| `GET /api/v1/audit?entity_type=&limit=&before=` | Paginated audit log | Admin |
| `GET /healthz` | Liveness check | None |
| `GET /` and static assets | Embedded dashboard | None |

A validation failure on any `POST`/`PUT` endpoint above responds `400`
with `APIError.details` — a `field -> message` map — so the Settings
UI can highlight the offending form field directly. See
[alerting.md](alerting.md), [billing.md](billing.md), and
[storage.md](storage.md) for the response shapes and semantics behind
each feature area's routes.
