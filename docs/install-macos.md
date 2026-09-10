# Install on macOS

Start with one authorized private chat. Add optional integrations after a new message receives a verified reply.

## Trust and permissions

**The main Codex runner defaults to `codex.ExecutionYOLO`.** The default execution path is not an approval-gated sandbox. Treat it as able to act with the files, credentials, tools, and macOS permissions available to the service account. Set `execution_policy` to `read-only` or `workspace-write` to select a narrower command sandbox; omitting it preserves the YOLO default. MCP servers still run outside the command sandbox.

Chat allowlists and exact chat identities are enforced by the application. Reminder and Notes operations have additional application checks. However, conversational instructions such as “do not use shell workarounds” are **not OS isolation**. Neither the scratch workspace nor a coding chat's workspace override is a security boundary.

Use a dedicated macOS account, trusted participants, minimal credentials, and backups. Do not expose a personal account full of unrelated data to an experimental agent. Prompts, selected conversation history, and tool results may be sent to the configured model provider. Local state, Codex transcripts, logs, and backups should all be treated as private.

An optional reviewed interactive execution path exists, but it requires a deployment-specific configuration contract. It is not enabled in the setup below and should not be presented as a general safety toggle.

## Prerequisites

For the local Mac mini setup:

- **macOS with an active graphical login.** Sign into Messages as the account that will run the service, and verify that you can send and receive messages manually. Local tapbacks drive the Messages UI; this is not a headless, root-owned system daemon.
- **Go 1.25.13 or newer** and Git. The CI matrix is configured for Go 1.25.13 and Go 1.26.6; see the workflow runs for actual results. Xcode Command Line Tools provide the macOS development and signing tools (`xcode-select --install` if needed).
- **Public access to the pinned Go dependencies** in [go.mod](https://github.com/teslashibe/agent-go/blob/main/go.mod).
- **A compatible `imsg` executable**, installed separately from the public MIT-licensed [openclaw/imsg upstream](https://github.com/openclaw/imsg). Its [installation guide](https://github.com/openclaw/imsg/blob/main/docs/install.md) documents `brew install steipete/tap/imsg` (macOS 14+). The application launches `imsg rpc` and requires history, chat subscriptions, sending, and exact chat identity metadata. Verify those features with the pinned client; this is not a guarantee that every upstream version is compatible. Do not enable its optional injected IMCore helper or disable SIP for this setup.
- **Python 3** for the first-install script, and **your own existing valid code-signing certificate and private key** in an accessible keychain. The installer does not create identities, unlock keychains, or use the maintainers' certificate. List available identities with `security find-identity -v -p codesigning`; if none is available, stop and provision a signing identity through your normal Apple development process before continuing.
- **An installed, authenticated Codex CLI** compatible with the pinned Go client. Authenticate using that CLI's own instructions, under the same account and `CODEX_HOME` used by the service. The main model can be configured; the tapback chooser currently uses `gpt-5.1-codex-mini` independently.
- **macOS privacy approvals** for the actual binaries and launch context, described below.

The Go executable also builds on Linux, and `transport: "ssh"` is supported, but Messages and native Notes still require a Mac. This guide covers the simpler `local` deployment.

For standard tapbacks with SIP enabled, the Mini workflow uses the optional
[pinned native backend](https://github.com/teslashibe/imessage/blob/v0.3.5/native/README.md)
from `teslashibe/imessage`. Build and verify it on the Mini, retain the vendor
installation for rollback, and explicitly point `imsg_path` to the signed new
executable. It conservatively rejects ambiguous duplicate text and unsupported
message parts; an unpatched upstream executable does not supply this capability.

## Dependency access

The current direct `teslashibe` dependencies are:

- [`teslashibe/codex`](https://github.com/teslashibe/codex) v0.7.3
- [`teslashibe/google-go`](https://github.com/teslashibe/google-go) v0.3.1
- [`teslashibe/imessage`](https://github.com/teslashibe/imessage) v0.3.5
- [`teslashibe/mcptool`](https://github.com/teslashibe/mcptool) v0.1.2
- [`teslashibe/notes`](https://github.com/teslashibe/notes) v0.2.18

[go.mod](https://github.com/teslashibe/agent-go/blob/main/go.mod) is the source of truth for direct and transitive versions. All
pinned modules must be publicly resolvable before this repository's clean public
CI and baseline installation path can succeed.

## Set up a Mac mini

### 1. Get the source and build

Clone the repository, then build from its root:

```sh
git clone https://github.com/teslashibe/agent-go.git
cd agent-go

GOWORK=off go mod download
GOWORK=off go build -o agent-go ./cmd/agent-go
./agent-go -h
```

`GOWORK=off` uses the versions in this repository's `go.mod`, rather than a surrounding workspace. If module downloads fail, verify that every pinned dependency is public and available through normal Go module resolution.

This builds a development executable. **Do not copy it over an installed, authorized service binary.** Existing installations must preserve their signing identity and designated requirement; see [signing and updates](operations.md#signing-and-updates).

### 2. Create private runtime directories

Keep runtime data outside the checkout and outside every agent workspace:

```sh
mkdir -p "$HOME/.local/share/agent-go" "$HOME/.local/share/agent-go-scratch"
chmod 700 "$HOME/.local/share/agent-go" "$HOME/.local/share/agent-go-scratch"
```

The first directory holds `config.json` and `state.db`. The second is the service's scratch workspace. The application requires an existing workspace directory and does not create the state file's parent directory for you.

### 3. Identify one authorized chat

Using your installed `imsg` CLI's help and chat/history commands, inspect the intended conversation locally. Record:

- Its positive numeric **chat ID**.
- Its exact **chat GUID**, including the service prefix.
- Its exact inbound **sender identity** as reported by the transport.

Do not substitute a contact's display name or guess an identity from a phone number. A DM permits exactly one allowed sender. Direct-chat GUIDs begin with `iMessage;-;` or `any;-;`; group GUIDs begin with `any;+;`. The application checks the configured identity against message history before subscribing.

Have at least one message in the intended chat before setup. Do not paste chat exports or identifiers into public issues.

### 4. Write a minimal configuration

Copy the sanitized [config.example.json](https://github.com/teslashibe/agent-go/blob/main/config.example.json), then **replace
every uppercase placeholder and the zero `chat_id`** before use:

```sh
cp config.example.json "$HOME/.local/share/agent-go/config.json"
```

The checked-in example is valid JSON but deliberately fails runtime validation
until `chat_id` is a positive exact chat ID. Use the actual absolute paths
created in step 2. JSON does **not** expand `$HOME` or `~`.

```sh
chmod 600 "$HOME/.local/share/agent-go/config.json"
```

Configuration rules worth knowing:

- `state_path` must be outside the global workspace and every per-agent workspace.
- `source` identifies this particular Mac/account/Messages-database generation. Preserve it across normal restarts. After restoring or replacing `chat.db`, use a new source and new state database rather than silently reusing the old cursor.
- Agent names, chat IDs, and chat GUIDs must be unique. Names allow ASCII letters, digits, underscores, and hyphens, starting with a letter or digit, up to 64 characters.
- With `agents`, do not also supply top-level chat fields such as `owner`, `allowed_senders`, `group`, `chat_id`, `chat_guid`, or `profiles`.
- Unknown JSON fields are rejected. `model`, `reasoning_effort`, `service_tier`, and `mcp_servers` are optional; omitted model selection is delegated to the Codex client/CLI.
- `execution_policy` accepts `yolo`, `read-only`, or `workspace-write`. Omitted preserves the `yolo` default.
- `document_attachments: true` enables bounded document text extraction for every configured DM and group. It requires local transport.
- For local transport, `imsg_path` must be absolute. Use an absolute `codex_path` too, especially under launchd.

### 5. Prepare a signed first installation

Only on a new installation, after writing the configuration, run:

```sh
python3 scripts/install-first.py --identity YOUR_40_DIGIT_CERTIFICATE_SHA1 \
  --codex-home /ABSOLUTE/PATH/TO/AUTHENTICATED/CODEX_HOME
```

This builds from the pinned modules, signs with your supplied existing identity, verifies the certificate and identifier, and installs `$HOME/.local/bin/agent-go` plus `$HOME/Library/LaunchAgents/ai.teslashibe.agent.plist` without overwriting either. It refuses an existing binary (including symlinks), plist, state database, signing keychain, deploy-secret verifier, legacy PIN file, or loaded service. It does **not** start the service, install helpers, authenticate Codex, change privacy permissions, or configure updater credentials. The plist records the supplied `CODEX_HOME` and your current `PATH`; review both before use. A prepared installation is not an integration test.

Preserve this signing identity/private key and the printed designated requirement for future updates. Do not delete existing artifacts to force this first-install path through an update. Installations at custom paths also require manual review: this script only checks the standard paths and label.

### 6. Grant macOS permissions and run in the foreground

Open **System Settings → Privacy & Security** and review permissions for the executable or responsible application macOS identifies:

- **Full Disk Access:** needed to read the Messages database, including the agent's direct tapback verification queries.
- **Automation:** allow the relevant process to control Messages and System Events when prompted.
- **Accessibility:** needed for local tapbacks, which use Messages keyboard shortcuts. They can activate Messages and change UI focus.

A Terminal approval is not proof that a launchd service or newly signed replacement has the same approval. Test in the intended launch context. Do not reset all privacy permissions or change a signing identity to work around a denial.

Start one foreground instance using the signed, installed binary (not the development build):

```sh
CODEX_HOME=/ABSOLUTE/PATH/TO/AUTHENTICATED/CODEX_HOME \
  "$HOME/.local/bin/agent-go" -config "$HOME/.local/share/agent-go/config.json"
```

Send a **new** message from the allowed sender in the configured chat. Initial history establishes a baseline; it is not replayed as new work. Verify a reply and inspect any errors before adding more chats. Stop with `Ctrl-C` before restarting or using maintenance commands.

This is a live integration: it can send messages and invoke Codex tools. There is no dry-run or config-only validation flag.

### 7. Run at login with launchd

Only move to background operation once foreground messaging and permissions work. Review the generated `$HOME/Library/LaunchAgents/ai.teslashibe.agent.plist`: it uses the installed binary and configuration, private runtime log paths, `RunAtLoad`/`KeepAlive`, and the `CODEX_HOME` and `PATH` captured at installation. launchd does not source your interactive shell configuration. Logs may contain private data; keep the runtime directory mode `700`.

The first-install script deliberately does not load the plist. After stopping the foreground instance, validate and load it from the logged-in account:

```sh
plutil -lint "$HOME/Library/LaunchAgents/ai.teslashibe.agent.plist"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/ai.teslashibe.agent.plist"
launchctl print "gui/$(id -u)/ai.teslashibe.agent"
```

To stop a loaded service before maintenance:

```sh
launchctl bootout "gui/$(id -u)/ai.teslashibe.agent"
```

Do not run the foreground instance alongside it. Keep the Mac awake when you expect replies or reminders; a LaunchAgent does not make sleeping hardware available, and UI automation requires a usable graphical session. A running PID is only a process check, not proof that Messages, Codex, and optional Notes all work.
