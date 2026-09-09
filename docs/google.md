# Google integration

The optional `google` service config uses `github.com/teslashibe/google-go`
v0.3.1. It exposes Gmail search/read/threads/labels, Calendar list/events/event
read/free-busy, and Drive list/read through the existing private per-turn
`harness` MCP endpoint, including in DMs where Apple Notes is disabled.
Upstream arbitrary authentication, sending mail, draft creation, label changes,
calendar edits/RSVP, and all Drive mutations are disabled. The optional private-DM
phone connection flow below can fill operator-provisioned account slots.

Add an explicit account-to-agent allowlist to the service config:

```json
{
  "google": {
    "config_path": "/absolute/operator-managed/google/config.json",
    "account_chats": {
      "personal": ["coding-dm"],
      "family": ["coding-dm", "family-group"],
      "work": []
    }
  }
}
```

Each grant names an existing `agents[].name`; its exact chat ID, GUID, kind, and
allowed senders remain authenticated by the bridge. There are no automatic owner,
DM, group, default-account, or wildcard grants. Omitted config, omitted accounts,
and empty chat lists deny access. Aliases must be exact lowercase ASCII letters,
digits, underscores or hyphens (1–64 characters, starting with a letter/digit),
not email addresses. The credential config must already exist; a missing path
fails startup rather than creating an empty configuration. All-empty grants skip
credential initialization. No credential files or live config are changed during
setup of this feature; when enabled, the upstream client may maintain its config
layout and persist OAuth token refreshes.

## Connect Google accounts from a phone (opt-in)

Links use the existing private iMessage reply transport; this does not add carrier-SMS delivery.

Provision a separate slot for each person/account. Each OAuth-enabled slot must be granted to
**only its one private DM**, not another DM or a group. No dynamic grants are
created. In the service config, for example:

```json
{
  "google": {
    "config_path": "/Users/operator/.google-mcp/config.json",
    "account_chats": {"user_personal": ["user-dm"], "user_work": ["user-dm"]},
    "oauth": {
      "public_base_url": "https://YOUR-DOMAIN.ngrok-free.app",
      "listen_addr": "127.0.0.1:8766",
      "chats": ["user-dm"]
    }
  }
}
```

Pre-create the upstream `config.json` (0600) under a private directory (0700):

```json
{"accounts":{"user_personal":{"email":"user@example.com"},"user_work":{"email":"user@work.example"}}}
```

Use the exact primary Gmail/Workspace email, not a forwarding alias. Leave
`token_path` absent (or `accounts/ALIAS/token.json`); custom/shared token paths
are rejected when phone OAuth is enabled. Slots must exist before startup.

1. In Google Cloud, enable Gmail API, Google Calendar API, and Google Drive API.
   Configure the OAuth consent screen; for an External app in Testing, add both
   people's exact Google accounts as test users. Request only `gmail.readonly`,
   `calendar.readonly`, and `drive.readonly` (full Google API scope URLs).
2. Create an OAuth client with application type **Web application**, not Desktop.
   Add the exact authorized redirect URI
   `https://YOUR-DOMAIN.ngrok-free.app/oauth2callback`. Download its JSON to
   `oauth-keys.json` beside `config.json`, mode 0600. No JavaScript origin is needed.
3. On the Mac mini, run an already-installed ngrok explicitly:

```sh
ngrok http http://127.0.0.1:8766 --url=https://YOUR-DOMAIN.ngrok-free.app --inspect=false
```

   Forward **only this dedicated OAuth port**, never MCP/admin. Keep the tunnel
   running during connection; if its public URL changes, update both the service
   config and Google's redirect URI, then restart through the approved deployment
   procedure. This feature neither installs nor starts a tunnel or edits live config.
4. Ask in the opted-in private DM: “Connect my user_personal Google account.”
   `google_connect_account` returns a Google consent URL through the normal chat
   reply. Open it on the phone within ten minutes and sign in as the slot's email.
   Browser completion reports success only after identity verification, persistence,
   and reload; Google reads become available without restarting.

Links use random, single-use in-memory state and PKCE; preview scanners opening
Google's URL do not consume state. Do not forward links: they are bearer invitations
bound to a fixed slot, not proof of who holds the phone. Wrong accounts are rejected
using Gmail's authenticated profile. Existing token files are never overwritten;
for reconnect/expired credentials, an operator must stop the service, revoke/remove
that slot's token, and restart. Tokens live at `accounts/ALIAS/token.json` (0600)
under private directories; no account email or ACL is changed by the callback.
Pending links expire after ten minutes or a transport/service restart. Failed or
denied callbacks consume the link: request a new one.

Google External apps in **Testing** typically issue refresh tokens expiring after
seven days for these scopes; production may require Google's sensitive/restricted
scope verification. Workspace policy can also block consent. Use a dedicated OAuth
client so previously granted broader scopes are not mixed into this read-only flow.
Disable tunnel request inspection/retention and avoid proxy access logs: callback
query strings contain authorization codes/state. Treat chat/model history containing
connection URLs as sensitive. The service does not log callback codes or tokens.

Account listing returns only aliases granted to the current chat. Account-specific
calls require an exact granted alias. Unified inbox/search/Drive search/agenda
are implemented as separate read calls over only granted accounts, returning
separate pages with account provenance and per-account limits (not a globally
sorted or globally limited merge). Unified agenda covers the primary calendar
only; supply explicit RFC3339 `time_min` and `time_max` bounds. Use Calendar list
and events with an explicit calendar ID for other calendars. A failed account
makes the unified call fail instead of silently presenting incomplete results.
Oversized responses fail explicitly; narrow the query or reduce limits.

Credentials stay in the parent service and are not passed in tool arguments or
MCP subprocess environment. Tool schemas and errors do not disclose unapproved
accounts. The server rejects unknown fields, unapproved aliases, default/email
alias fallbacks, disabled tool names, and calls after turn closure/cancellation.
Do not configure an unrestricted Google MCP server globally alongside this
scoped endpoint.

Grants are loaded from service configuration at startup; credentials are loaded
on connection startup. To revoke future access, remove the grant and restart the
service so active turns end and the policy reloads. A transport reconnect alone
does not reload edited grants. Revocation does not erase results already sent to a chat or retained
in its durable history/model session. Account-level access includes calendars,
mail, and Drive content shared to that approved account; this is not a separate
per-document or per-calendar ACL.

These allowlists protect the MCP dispatch boundary, not the operating-system
account. In particular, a coding DM with unrestricted shell execution may access
same-user credential files or make network calls outside MCP. Prompt instructions
are not an OS sandbox. Protect credentials with a separate service identity or
process sandbox if untrusted shell-capable agents must be isolated from them.
This integration does not provide that stronger isolation.
