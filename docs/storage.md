# Storage usage (Google Drive / Dropbox)

Since v0.7.0 the hub can periodically poll **Google Drive** and/or
**Dropbox** for a connected account's quota/usage, alongside the
existing S3/R2 object-storage monitoring. Like [billing](billing.md),
this is a **quiet-skip polling framework** (`internal/poller`, shared
with the AWS/OCI billing collector) — a missing connection, an expired
token, or a rate limit is never a hub error, just a status.

Authentication is **OAuth only** — the hub never accepts or stores a
Google/Dropbox account password.

## Google Drive: app registration (device flow)

Google Drive uses OAuth 2.0's **device flow**, which needs an OAuth
client of type "TV and Limited Input devices" — this type is required
because the device flow's polling-based grant only works for that
client category.

1. Go to [Google Cloud Console](https://console.cloud.google.com/) →
   create or select a project.
2. **APIs & Services → Library** → enable the **Google Drive API**.
3. **APIs & Services → OAuth consent screen** → configure it (External
   is fine for personal use; you don't need Google's app-verification
   review for the scope used here since it's not a sensitive/restricted
   scope, but you may see an "unverified app" warning during the
   device-flow authorization page the first few times — this is
   expected until/unless you complete Google's verification process).
4. **APIs & Services → Credentials → Create Credentials → OAuth client
   ID** → Application type **"TVs and Limited Input devices"**. Copy
   the generated **Client ID** and **Client secret**.
5. In cloud-pulse's dashboard, **Settings → Storage → Connect Google
   Drive**, paste the Client ID and Client secret, and click Connect.
   The dashboard shows a **user code** and a URL
   (`google.com/device`) — open that URL on any device, sign in, and
   enter the code. The dashboard polls automatically and shows
   "Connected" once you complete that step.

**Scope**: `https://www.googleapis.com/auth/drive.file` — this is the
minimal scope the device flow's grant type supports and is enough for
`drive/v3/about` to report quota; it does **not** grant cloud-pulse
access to files created by other apps, since `drive.file` only covers
files the app itself creates or that the user explicitly opens with it
(cloud-pulse only ever calls `about.get`, never creates or lists
files). Re-verify Google's own current documentation for this scope's
exact semantics before relying on this description in a security review
— Google's scope definitions are outside this project's control and
can change.

**Privacy**: cloud-pulse only calls `GET
https://www.googleapis.com/drive/v3/about?fields=storageQuota,user(emailAddress,displayName)`.
It never lists, reads, or modifies file contents. The account's email
and display name are stored so the dashboard can label the connected
account; the refresh token is stored the same way a notify channel's
secret is (see [security.md](security.md)).

## Dropbox: app registration (PKCE flow)

Dropbox uses OAuth 2.0's **PKCE code flow with no redirect** — the
dashboard shows an authorize link, you approve it and get a code back
from Dropbox's own page, then paste that code into the dashboard. PKCE
means only an **App key** is needed, not a secret.

1. Go to the [Dropbox App Console](https://www.dropbox.com/developers/apps)
   → **Create app**.
2. Choose **Scoped access**, API access type of your choice (either
   works for this integration since only account-info is used), and
   give the app a name.
3. Under the app's **Permissions** tab, enable the
   **`account_info.read`** scope, then click **Submit** to save
   permissions.
4. Under the app's **Settings** tab, copy the **App key** (the App
   secret is not needed — PKCE never uses it).
5. In cloud-pulse's dashboard, **Settings → Storage → Connect
   Dropbox**, paste the App key and click Connect. The dashboard opens
   an authorize URL
   (`www.dropbox.com/oauth2/authorize?...&token_access_type=offline`
   with an S256 PKCE challenge, no `redirect_uri`) — approve it on
   Dropbox's page, copy the code it displays, and paste it back into
   the dashboard to complete the connection.

**Scope**: `account_info.read` — read-only access to the account's own
profile and space-usage figures; no file access is requested or used.

**Privacy**: cloud-pulse only calls `POST
https://api.dropboxapi.com/2/users/get_space_usage` and `POST
.../2/users/get_current_account`. It never lists, reads, or modifies
file contents.

## Disconnecting an account

Settings → Storage → Disconnect calls the provider's own revoke
endpoint (Google's token-revocation endpoint, Dropbox's
`auth/token/revoke`) before deleting the account row — the OAuth grant
itself is invalidated at the provider, not just forgotten locally.

## Polling and alerting

Default interval is 1 hour; choose 15 minutes, 1 hour, 6 hours, or 24
hours from Settings → Storage (`CP_STORAGE_INTERVAL`, or `PUT
/api/v1/settings/storage/interval` once changed from the dashboard). A
manual "refresh now" is rate-limited to once per minute.

Alert rules support a `storage_usage_pct` metric — account-scoped
(reusing the rule model's `host_id` field as a repurposed account-ID
string, since a storage account has no natural host to attach to),
sustained-duration semantics are not used for this metric, and the
default re-notification cadence is once per day while it stays above
threshold. A preset "Google Drive above 90%" is offered in the rule
editor.

## Quiet-skip status model

| Status | Meaning |
|---|---|
| `not_configured` | No account connected for this provider |
| `pending_oauth` | Account created but the OAuth flow hasn't completed yet |
| `auth_failed` | The stored token was rejected (e.g. revoked externally) |
| `permission_denied` | Authenticated, but the granted scope is insufficient |
| `error` | Reached the API but got something else unexpected (including a timeout) |
| `ok` | Collected successfully |

The same freshness/staleness tracking billing uses applies here: the
last successful snapshot is preserved across a failed poll, and
`Stale` is set once the time since the last success exceeds twice the
current polling interval.

## API

`GET /api/v1/storage/accounts` (any signed-in user, secrets redacted).
Admin-only: `POST/DELETE /api/v1/storage/accounts[/{id}]`, `POST
.../{id}/oauth/start`, `POST .../{id}/oauth/complete`, `POST
/api/v1/storage/refresh`, `PUT /api/v1/settings/storage/interval`.

## Real-account verification

`cloud-pulse-hub storage verify --provider googledrive|dropbox [--name
NAME] [--json]` loads an already-connected account from the hub's own
database and exercises the exact same quota-fetch code path the
background poller uses — this proves the connection actually works
beyond what the dashboard's "Connected" status alone shows. `--name` is
required only if more than one account uses the same `--provider`.

## SSRF guard

Each provider validates its OAuth/API endpoint against a fixed
allowlist of official hosts (`oauth2.googleapis.com`,
`www.googleapis.com`, `api.dropboxapi.com`, `www.dropbox.com`) and
requires `https://`. `CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS=1` +
`CP_STORAGE_FAKE_BASE_URL` relax this — **tests/CI fakes only**, never
production; see [security.md](security.md#security-model).

## Docker / Podman inventory (privilege tradeoff)

Unrelated to Google Drive/Dropbox, the agent's `--docker` install flag
(and `CP_DOCKER` env var) control a *different* kind of inventory —
listing running Docker/Podman containers on the monitored host itself,
not cloud object storage. It's documented here only because both are
sometimes grouped loosely under "storage-adjacent" features:

- `CP_DOCKER=auto` (default) probes `/var/run/docker.sock` (or
  `DOCKER_HOST`'s `unix://` path); `off` disables it; an explicit path
  or `unix://` URL points at an alternate socket, e.g. a **rootless
  Podman** socket (`unix:///run/user/<uid>/podman/podman.sock`), which
  avoids the privilege tradeoff below entirely.
- `install-agent.sh --docker` adds the agent's system user to the
  host's `docker` group. **This is root-equivalent on the host** — the
  Docker Engine API can mount arbitrary host paths into a container, so
  anything able to talk to the socket can read/write any file on the
  host as root. The installer's help text and interactive prompt both
  say this explicitly before an operator opts in.
