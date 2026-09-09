# Development and verification on the Mac mini

Build and test on the Mini. Keep the installed LaunchAgent, its configuration and
SQLite state separate from development checkouts and verification evidence.
Use a short-lived branch/worktree for each change; preserve uncommitted work
before removing an old worktree. Never run tests against the service database.

## Connect

Use ordinary SSH with a verified host key and a dedicated identity. Keep connection
details in the laptop's private SSH configuration, not in this repository. If SSH
hangs after selecting public-key authentication, an unresponsive authentication
agent can be the cause: `IdentityAgent none` selects the configured key directly.
It does not disable host verification or change the server's authentication policy.

For access outside the home network, put ordinary SSH behind a private VPN such as
Tailscale; do not expose the Mini's SSH port through the router. Test the route from
another network. Screen lock, sleep, reboot and preboot recovery are separate
checks: a working SSH connection does not prove native app automation is ready.

Use the ordinary macOS Remote Login service over the VPN, retaining the verified
SSH host key and dedicated login key. Tailscale's macOS GUI app does not provide
the separate Tailscale SSH server. Enroll both devices in the owner's private
network and restrict access to the intended user/devices. Keep router port
forwarding disabled. Enrollment requires the owner's account authentication and
macOS network-extension approval; installing software alone is not verification.
See [Apple Remote Login](https://support.apple.com/en-au/guide/mac-help/mchlp1066/mac),
[SSH over Tailscale](https://tailscale.com/docs/reference/ssh-over-tailscale), and
[Tailscale on macOS](https://tailscale.com/docs/install/mac).

Acceptance: connect from a different network, verify the same host key, run a
read-only status check, and confirm the GUI account is ready for native fixtures.
Separately record what happens after sleep, screen lock, reboot and FileVault
preboot. Do not weaken screen-lock or disk-encryption settings to make those
checks appear successful.

## Tailscale setup for remote maintenance

1. Install the [standalone macOS app](https://tailscale.com/docs/install/mac) on
   the Mini and the maintenance laptop. Sign both into the intended private
   Tailscale network. Approve the Mini's network extension in System Settings;
   follow the [current extension instructions](https://tailscale.com/docs/concepts/macos-sysext).
2. Keep macOS Remote Login limited to the maintenance account. Retain the existing
   SSH key and host-key verification. Use ordinary SSH over the Mini's Tailscale
   address or MagicDNS name; the GUI app does not host Tailscale SSH.
3. Restrict network access to the maintenance identities/devices and the Mini's
   SSH port. Review existing broad access rules as well as any new rule. No router
   port forwarding, exit node or public application tunnel is needed for SSH.
4. Add a private SSH alias for the Tailscale endpoint, verifying its host key
   against the already trusted home-network connection. Do not accept an unknown
   key automatically or copy private connection details into this repository.
5. Test from a genuinely different network, such as a phone hotspot: SSH login,
   read-only agent status, file transfer and a second connection after disconnect.
   Keep the home-network route available until this passes.

For unattended availability, review sleep settings and device-key expiry with the
owner. Test screen lock and reconnect separately. The GUI Tailscale variants do
not run before user login; see the [macOS variant comparison](https://tailscale.com/docs/concepts/macos-variants).
The open-source daemon can run before login but is a different installation to
review, and it does not make Notes/Messages available before their GUI session.
Do not promise recovery from reboot or FileVault preboot until it has been tested.
Keep encryption and existing privacy protections in place.

## Deterministic verification

On the Mini, run the committed verifier against the exact candidate commit:

```sh
python3 scripts/verify-mini \
  --repo /absolute/path/to/checkout \
  --revision HEAD \
  --output /absolute/path/to/new-evidence-directory \
  --go /absolute/path/to/go \
  --test-exec /absolute/path/to/established-sign-and-run
```

The output directory must be new and outside the checkout. The script archives
the committed revision, so uncommitted edits are not included. It records the
commit, tree, archive hash, actual Go version and signing-runner hash. It prepares
pinned modules, then checks formatting, normal/race tests, vet, builds and applicable
Python tests. For agent-go it also builds and signs a candidate executable. Failed
checks stop the sequence and preserve their evidence. Retries need a new directory.

The same entry point accepts a dependent Go repository through `--repo`. Use a
supported patched Go toolchain explicitly for every repository. The established
private signing runner must sign and verify test binaries against the installed
artifact's designated requirement, then execute them with the isolated test
environment. Signing credentials remain local to that runner and never belong in
command arguments, logs, or Git.

Each run has separate HOME, CODEX_HOME, build/module caches, and a private short
TMPDIR. The module cache is copied using APFS copy-on-write. Inherited model/native
test opt-ins and credentials are not forwarded. Module downloads are disabled after
preparation; this is not a claim of OS-level network or tool isolation.

`summary.json` lists executed checks and skipped tests. Preserve it with the logs.
An interrupted run with status `running` is incomplete, never a pass. The verifier
does not install, restart, recover production state, or run authenticated/native
effect tests.

The verifier checks for at least 4 GiB free before starting and before each stage.
It removes its regenerable build/module caches after success or failure, retaining
source snapshots, summaries, logs and signed artifacts. Use `--keep-caches` only
when a following fixture needs them, and remove those caches afterward. Failed
empty logs during low disk space are incomplete verification, not test evidence.

## Model and native verification

After deterministic checks, verify the real configured model with fake backend
effects and an explicitly reviewed tool inventory. A prompt telling the model to
use only fake tools is not isolation. Then verify the actual macOS Notes/message
integration using dedicated fixture notes and explicitly authorized test chats.
Exercise single-person and group conversations, exact targeting, edits/deletion,
duplicate requests, cancellation, and restart recovery. Record locked/unlocked
desktop behavior and the identity/permissions of the actual running process.

Do not use personal Notes or historical family messages as disposable fixtures.
Live messages and tapbacks need explicit destination authorization. Capture
operational evidence without copying message bodies, credentials or personal
configuration into GitHub issues or public logs.

## Release

Review and merge the tested revision. If the merged tree differs, rerun affected
checks. Match the signed artifact's hash and designated requirement to the evidence.
Use the existing installation path with its confirmation,
idle check, backup, atomic replacement and rollback protections. Preserve sessions
and unresolved effect evidence; do not clear a pause just to make deployment pass.
Observe the existing 70-second health window and the authorized end-to-end fixture.
Process survival is not proof of Notes or message success.

## Authenticated Notes model fixture

After deterministic verification, use its signed agent executable as the MCP
relay in `TestRealCodexNotesSmoke`. Set `AGENT_CODEX_SMOKE_BINARY` to that absolute
path, `AGENT_CODEX_SMOKE_CODEX` to the reviewed absolute Codex CLI path,
`AGENT_CODEX_SMOKE_MODEL=gpt-6-astra`, and `CODEX_HOME` to the existing authenticated
home. Run the test on the Mini with the established signing runner and a short
private temporary directory. Keep the exact command, CLI version, source revision,
artifact hash, exit status and test output in a separate evidence directory.

The fixture checks note reading, three ordered additions, and clarification
before ambiguous changes. Notes and messaging backends are fake. The adapter
ignores user configuration and rules; the fixture disables native shell, desktop,
app, browser, image and delegation tools, while retaining the code-mode MCP
dispatcher. It uses new fixture sessions without copying or resetting credentials
or existing sessions. This is not OS isolation. Inspect the current CLI feature
surface and inherited instructions before running after a CLI upgrade.

A passing fixture does not establish live chat delivery, tapbacks, sharing,
owner-only shipping, or release/rollback behavior. Those remain separate gates.

## Upgrading from an owner coding turn

An agent turn cannot synchronously restart its own service: its running job must
remain protected by the installer's idle check. After explicit owner approval,
review and complete Mini verification, merge the candidate and check out clean
`main` with the identical verified tree. From the running coding turn, use:

```sh
scripts/queue-self-upgrade --confirm --source /path/to/agent-go \
  --verification /private/path/to/passed-mini-evidence
```

The queue requires exactly one running authenticated coding DM job. It snapshots
the signed, verified artifact and installer into a private per-job directory and
starts a one-shot launchd worker independently of the agent. The worker waits for
that same job to complete and for all sources/effects to become idle. Failure,
identity changes, configuration/database drift or a 15-minute expiry refuse the
upgrade. The original installer repeats its checks across shutdown and retains
signing, backup, 70-second health and rollback checks.

Queued means pending, not deployed. Inspect `request.json` and `deployment.log`
in the returned evidence directory. A repeated queue for the same job or worker
invocation is rejected; an interrupted or failed attempt needs inspection rather
than replay. The one-shot plist is outside `Library/LaunchAgents`, so reboot does
not silently retry it. After completion, unload its `ai.teslashibe.agent-upgrade-JOB`
launchd service while retaining the evidence directory. Continue using `ship-self`
for an operator installation outside an agent turn.

These checks protect the workflow under the existing trusted local-account model;
they do not isolate unrestricted same-user shell access or cryptographically prove
the owner's approval. Native and authenticated-model acceptance remain separate
from the deterministic verification record.

## Reviewed Notes recovery

For a paused native Notes attempt, first inspect its private operation arguments,
result, item progress, original request and current external effect. A fingerprint
only detects changed evidence; it does not prove that an effect succeeded.
Do not replay creation or sharing to discover what happened.

With the daemon stopped and its database backed up consistently, fingerprint the
exact job and operation using the staged, verified executable:

```sh
agent-go -config /private/config.json -agent CHAT \
  -review-note-job JOB_ID -note-operation OPERATION_ID
```

If the reviewed decision is to leave the request outstanding and abandon only
that attempt, repeat with `-resolve-reviewed-note`, `-review-sha256` and
`-recovery-reason`. Keep the reason factual and free of credentials or personal
note contents. Review completed sibling operations as well: their effects remain.
If the acknowledgement is unknown, resolution additionally requires
`-abandon-reviewed-unknown-ack`. That explicitly abandons further acknowledgement
attempts while preserving its unknown state, original reaction and observed outcome
(if recorded); it does not establish delivery. An actively dispatching acknowledgement
always rejects recovery. The flag is rejected when there is no unknown acknowledgement.
A changed fingerprint or other unresolved work rejects recovery.

This records the review, marks that attempt failed and its operation abandoned,
and clears only the reviewed source's ordinary pause. Original errors, requests,
sessions, acknowledgement identity, note collaboration state and effect/progress
evidence remain intact. It does not fulfill the request, authorize an unverified
shared note, delete replies, reset sessions, or retry effects. Recheck all sources
and unresolved operations before following the normal release sequence.

If the retained creation is already shared externally, a fresh group request
using its exact returned note ID can recheck it after reviewed abandonment. The
harness requires an active, unprotected shared note and freshly verifies the
configured participants before authorizing that recovered scope. The original
failure, sharing attempt and creation progress remain unchanged; a new audit entry
records verification. Missing membership, unresolved work or changed job identity
refuse access. This path does not create notes or resend invitations.

### Keep the dedicated session available

Native Notes editing needs an unlocked, logged-in graphical session. A sleeping
or locked Mini can still accept SSH or messages while its keyboard automation is
unavailable. On the dedicated agent account, after reviewing this operating mode:

```sh
python3 scripts/install-awake-session --confirm
```

This installs a per-user Apple `caffeinate` service and sets that account's
current-host screen-saver idle time to zero. It saves the previous idle setting
in the private `awake-session-before.json` beside the runtime configuration.
Read back the launch service and power assertions to verify setup. The script
is idempotent and refuses to overwrite a different existing awake service.

A manual screen lock or restart still requires normal Mac login. Keep remote
Screen Sharing available through the private network for that recovery. To undo
the awake setup, unload/remove `ai.teslashibe.agent-awake.plist` and restore the
saved screen-saver idle setting (delete the current-host `idleTime` preference
when the saved value is null).

Authenticated model fixtures can cause the CLI to persist temporary workspace trust entries outside the supplied authentication directory. Compare all reviewed configuration sources before and after a fixture. Archive only entries proven to have been created by that fixture; never replace a user configuration or accept a new source merely to pass validation. An exact `unexpected configuration source` rejection occurs before the pinned adapter starts app-server. After restoring the reviewed environment, the existing operator binding-recovery command can requeue that exact failure only if its durable checks prove no tool operation or reply was dispatched. The original request, session, acknowledgement and audit evidence are preserved.
