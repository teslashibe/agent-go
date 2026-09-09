# Operations and recovery

## Persistence and recovery

SQLite stores chat cursors, conversation references, jobs, outgoing replies, reminders, and Notes operation records. A process lock prevents concurrent access by daemon/maintenance instances. Messages are deduplicated, and normal connectivity failures are retried.

The reliability goal is **not to repeat an external effect whose outcome is unknown**, rather than promising exactly-once delivery. A crash during a send or tool action can pause work for operator review. Source/history mismatches fail closed instead of silently accepting a different conversation.

- Back up private runtime state using a consistent SQLite backup, or stop the service and preserve the database with any WAL files. Copying only a live `state.db` can miss committed data.
- `/new` resets the current chat's conversation when idle; it does not cancel reminders. Imported history is untrusted reference, not executable work.
- **Stop the daemon before `-status`.** This command opens state and performs crash recovery; it is not a read-only health endpoint:

```sh
./agent-go -config "$HOME/.local/share/agent-go/config.json" -status -agent assistant
```

With multiple agents, specify `-agent` for status, history import, and recovery. It does not select a subset of agents for normal daemon operation.

Advanced recovery flags are listed by `./agent-go -h`. In particular, `-discard-replies-and-resume` discards **all unresolved replies**, abandons uncertain turns/reminders, and resets private contexts. It does not clear source invalidation. Do not use it as a routine restart command. Review the actual external effects and back up state before any recovery operation.

## Signing and updates

Read [AGENTS.md](https://github.com/teslashibe/agent-go/blob/main/AGENTS.md) before replacing any authorized macOS binary or helper. Inspect the installed artifact first:

```sh
codesign -dv --verbose=2 "$HOME/.local/bin/agent-go"
codesign -d -r- "$HOME/.local/bin/agent-go"
```

Replacements must preserve the established signer, identifier, and designated requirement. Never ad-hoc sign, generate a replacement signer as a workaround, overwrite a running binary's inode, or use `launchctl kickstart`. If the established identity/private key is unavailable, stop.

Updates require operator authorization, with `--confirm` for `ship-self`. There is
no deployment PIN or separate secret. Existing legacy PIN/verifier files are unused;
keep them private. SSH/account access and signing-key access remain required.

The three update scripts below are **existing-deployment tooling**, not first-install commands:

- [`scripts/ship-self`](https://github.com/teslashibe/agent-go/blob/main/scripts/ship-self) requires `--confirm` after explicit owner approval and runs without an interactive credential prompt. It derives the signer, identifier, and designated requirement from the installed binary and refuses drift.
- [`scripts/deploy-mini`](https://github.com/teslashibe/agent-go/blob/main/scripts/deploy-mini) builds for Darwin/arm64, signs, and deploys over SSH. It uses `AGENT_DEPLOY_HOST` plus optional `AGENT_DEPLOY_KEY` and `AGENT_DEPLOY_SIGNER`, or a private `scripts/deploy-mini.env` based on the [example](https://github.com/teslashibe/agent-go/blob/main/scripts/deploy-mini.env.example). It derives the expected identifier and designated requirement from the remote installed binary. Unlike `ship-self`, it does not implement the local confirmation flag.
- [`scripts/deploy-mini-remote.py`](https://github.com/teslashibe/agent-go/blob/main/scripts/deploy-mini-remote.py) expects an existing standard-layout binary, database, configuration, and LaunchAgent. It uses WAL-aware retained idle checks, verifies the database inode across shutdown, backs up the binary and both reviewed configuration files, atomically replaces files, probes configured computer-use MCP servers, and requires a 70-second stable process before success. If the signed OpenAI desktop app moves between `Codex.app` and `ChatGPT.app`, it updates only reviewed runtime paths and both attestation hashes. Automatic rollback stops the failed candidate first and is refused if durable work became uncertain.

These scripts preserve the live installation's signing identity; they do not
create or replace certificates. Do not substitute identities as a shortcut.
Never publish signing keychains, private keys, deploy secrets or verifiers,
deployment environment files, runtime configurations, transcripts, or state
backups.

## Troubleshooting

- **Module download fails:** verify that every version in `go.mod` is publicly resolvable through normal Go module resolution.
- **Configuration rejected:** check absolute paths, existing workspace directories, state outside all workspaces, valid JSON, and exact chat/sender identities. Unknown fields are errors.
- **Works in Terminal, fails under launchd:** check the plist's executable/config paths, `CODEX_HOME`, tool environment, logged-in GUI session, and permissions for the service's actual identity.
- **History or send fails:** verify Messages works manually and that `imsg` can read the intended chat under the same account. Local transport subprocess diagnostics are intentionally suppressed because they may contain private data.
- **Tapback fails or targets do not appear:** check Full Disk Access, Automation, Accessibility, and an available Messages UI. The agent verifies tapbacks against `chat.db`; UI submission alone is not success.
- **Codex fails:** verify CLI authentication, model access (including the separate tapback model), and compatibility with the pinned client. Reviewed interactive failures require re-review, not bypassing contract checks.
- **Notes missing or denied:** confirm a local absolute helper path, a group with at least two senders, helper permissions, and eligible shared/unprotected notes. An empty catalog is not permission to access private notes through another tool.
- **Paused, uncertain, or source invalid:** stop the service, inspect `-status`, and review external effects before recovery. Do not delete state or resend the same request just to clear the error.
- **macOS kills a replacement:** inspect signing and designated requirements. Do not re-sign ad hoc or repeatedly restart a rejected artifact.


## Deferred self-upgrades

The owner coding workflow can prepare repository changes and pull requests. A complete owner-requested handoff has passed on the reference Mini: the independent worker waited for the requesting job to finish, installed the exact verified artifact through the guarded installer, and passed the health check with the original signer and configuration. Queueing an upgrade is still only a pending request; inspect its completed result before claiming deployment. See [Mini verification](mini-development.md) for the required evidence and recovery rules.
