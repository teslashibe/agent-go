# Development: lean, model-driven harness

- Stay within the requested issue. Make the smallest clear change that works; do not bundle adjacent fixes, speculative abstractions, new frameworks, or unnecessary dependencies.
- Prefer deletion and existing package APIs over new code. Keep one implementation of each tool contract and domain operation. Extract only genuine shared behavior; do not move files merely to add layers.
- Let the model use conversation context and MCP discovery/read tools to understand requests, choose targets and actions, and ask for clarification. The harness supplies those capabilities and executes them reliably; Go must not substitute its own interpretation.
- Do not hardcode business decisions in Go or prompts: note/list names, named recipients, grocery aliases, keyword intent gates, guessed defaults, or message-text routing overrides. Obtain values from tool results, authenticated context, or explicit configuration. Never silently replace the model's selected target.
- Use MCP as the model-facing action boundary, not as a wrapper around every internal function. Reuse the owning package's implementation and avoid parallel final-response action dispatchers when migrating a capability to MCP.
- Keep prompts short and non-duplicative: shared guidance once, operation semantics in tool descriptions, and factual current state in context. Do not add prompt examples to patch execution bugs or claim an action succeeded without evidence.
- Keep identity, access control, argument validation, exact-target checks, confirmation, idempotency, durable state, and outcome verification in application code. Protocol constants, schema enums, and justified validation limits are appropriate; this rule is not a ban on Go constants or variables.
- Preserve uncertain outcomes and recovery evidence; do not blindly retry effects, reset sessions, weaken validation, or bypass deployment safeguards to make a change pass.

# Development and release sequence

1. Read the issue, current repository rules, and relevant recent chat/runbook evidence. Inspect current main and active work before starting; do not duplicate another agent's work.
2. Use a fresh branch in an isolated worktree under `.worktrees/<short-name>` at this repository root. Keep `.worktrees` gitignored; do not use sibling directories, `/tmp`, or `.cursor/worktrees` for development checkouts. Preserve unrelated edits and paused drafts. Complete one scoped slice before merging/deploying the next; independent review may run alongside verification.
3. Recheck upstream releases before changing dependency pins. Review API compatibility and use immutable revisions or published versions, never a committed local `replace`. Do not drop validation or reset sessions to make an upgrade compile.
4. Keep one exact candidate revision for review and verification. After corrections, review the changed revision again. Compare the merged tree with the tested tree; rerun affected verification remotely if they differ. Record when identical-tree evidence is reused.
5. Follow the user's approved rollout through review, remote verification, merge, deployment and health checks. Do not stop at opening a PR or request repeated confirmation already granted. Retain explicit deployment approval and the script confirmation flag. Approval already given for the same scope remains valid after merging. Deployment has no separate PIN or secret gate.
6. Checkpoints track progress; they do not replace work. Report completed milestones or concrete blockers, not repeated empty status messages. Never claim a blocked or partially verified rollout is complete.

# Verification environment

For this Mac Mini workflow, all Go commands, formatting, builds and tests run on the remote Mini, not the developer's computer. Local source edits and Git operations are fine.

Use `scripts/verify-mini` for revision-scoped deterministic verification; see
`docs/mini-development.md` for its invocation and the separate model/native gates.
Pass the established private signing runner explicitly. Keep failed and successful
evidence directories separate, and record skips rather than treating them as passes.
The same verifier supports dependent Go repositories through `--repo`.

- Stage the exact source commit into a separate temporary directory on the Mini. Keep verification sources and evidence separate from live checkouts and state; do not overwrite an earlier revision's evidence directory.
- Prepare pinned dependencies remotely, then use isolated test `HOME`, `CODEX_HOME`, caches and database paths with `GOWORK=off` and readonly modules. Disable module downloads during offline verification. Use a short isolated `TMPDIR`: macOS Unix-socket path limits can otherwise break fixtures.
- Reuse the established signed test runner. Unlock the existing keychain privately for the SSH session as needed; scope the real signing home to the signing subprocess only. Test processes retain their temporary home/state. Verify each candidate's designated requirement against the installed artifact. Do not log passphrases or change identities/trust to fix a signing error.
- Run normal tests, race tests, vet, builds and applicable script tests on the Mini. Distinguish compilation from execution. Preserve failure logs and record the exact final revision, commands, results and skipped opt-in tests.
- Distinguish deterministic fake-backend tests, authenticated model tests with fake backends, and live Notes/message tests. A temporary database does not make an external effect fake. Do not use real family Notes or send replacement messages as test shortcuts.
- Before model/native tests, recover the existing successful signed-Mini procedure from recent evidence and inspect its current fixture and effects. Do not assume an old selftest command still exists. Reuse the proven mechanism; do not invent Docker, a VM, or a new sandbox project as an automatic prerequisite. Conversely, a fake backend or temporary home is not proof of OS isolation: describe actual permissions and resolve concrete risks without overstating safety.
- A test must cover the requested behavior. Fake-runner assertions do not prove model understanding; idle process health does not prove a real turn succeeds. Report missing coverage explicitly rather than silently waiving it.

# Deployment and recovery checks

- Build and sign on the Mini for this workflow. `scripts/deploy-mini` builds on the invoking host, so do not invoke it from the developer's computer when local builds are forbidden. Use the existing remote installation path with the signed staged artifact; preserve its approval, backup and rollback protections.
- Compare the candidate's actual tool namespace, relay command, arguments and dynamic environment keys with live reviewed bindings before restarting. A renamed MCP server needs a reviewed binding migration, not a disabled validation check. Back up configuration, assert the expected old value, and verify only the intended fields changed; preserve protected source/home hashes and sessions.
- Check the configured SQLite database with WAL-aware read-only transactions before and after stopping the daemon, across all sources and effect tables. Do not use `immutable=1` as evidence of live idle state. Count unresolved tool operations using both state and resolution; historically abandoned operations retain unknown evidence but are not permission to replay. Use actual Notes action states, not invented state names.
- If closing the last writer prevents a fresh read-only open, the established alternative is a retained read-only connection: end the precheck transaction before shutdown, verify the database device/inode is unchanged, start a fresh post-stop transaction, and close it before recovery writes. Never reuse a stale snapshot or omit the post-stop check.
- Do not deploy over queued/running/uncertain work or discard it merely to pass an idle gate. Use supported, audited recovery only after reviewing exact error/effect evidence. Preserve original request/session/acknowledgement identity. Never rewrite errors to trick recovery guards or replay an abandoned historical operation.
- After replacement, compare installed and candidate hashes/signing requirements; observe a stable process for at least 70 seconds, both source states, unresolved effect counts and new errors. Verify helper/configuration unchanged except an explicitly reviewed migration. Keep backup and rollback available. This stability check is not end-to-end model verification.

# Secret-safe diagnostics

Never print credential-bearing Git remote URLs, complete environments, authentication files, or full live configuration. Inspect only needed fields and redact before output. Keep credentials out of commands recorded in logs, commits, issues and test fixtures. Record exposures without repeating their values and coordinate replacement/revocation without losing recovery access.

# Deployment signing

Sign macOS release binaries and helpers with the same established identity as the currently installed, authorized artifact. Before replacing anything, inspect the live signer, identifier, and designated requirement, then verify the replacement matches. Never ad-hoc sign (`codesign --sign -`), generate a new signer, or change the signing identifier as a shortcut: that can invalidate macOS privacy approvals. If the established identity or its private key is unavailable, stop and report the blocker.

Read the live identity from the installed binary; do not hardcode another machine's certificate.

```bash
codesign -dv --verbose=2 ~/.local/bin/agent-go
codesign -d -r- ~/.local/bin/agent-go
```

Keep that identifier and `certificate root = H"<sha-1>"` on every replacement. Store the private key in a local signing keychain (default `~/.local/share/agent-go/signing.keychain-db`). Never commit the keychain, its passphrase file, or a deploy-secret verifier, and never print the private key.

# Install and restart

Use `scripts/ship-self --confirm` on the installed Mac after an explicit yes, or `scripts/deploy-mini` from a signing Mac. No deployment PIN, secret prompt or secret descriptor is required. The local account, SSH access, and explicit owner approval authorize deployment; signer, idle-state, backup and rollback checks remain mandatory. Legacy PIN/verifier files are unused and must remain private. Set `AGENT_DEPLOY_HOST` or copy `scripts/deploy-mini.env.example` to the gitignored `scripts/deploy-mini.env`.

Inside a running owner coding turn, use `scripts/queue-self-upgrade --confirm
--source MAIN_CHECKOUT --verification EVIDENCE_DIRECTORY` after merging the exact
verified tree. The independent worker waits for the requesting job to complete
before invoking the guarded installer. Report queued as pending, inspect the
saved result, and never retry a failed/uncertain handoff blindly. See
`docs/mini-development.md` for prerequisites and cleanup.

Both paths: designated-requirement check, idle check, `launchctl bootout`, backup, atomic replace onto a new inode, `launchctl bootstrap`, health wait, and rollback on failure. `ship-self` builds and signs locally when the established identity is in the signing keychain.

```bash
./scripts/deploy-mini
./scripts/deploy-mini --restart-only
./scripts/ship-self --confirm
./scripts/ship-self --confirm --staged /tmp/agent-go-VERSION --version VERSION
./scripts/ship-self --confirm --restart-only
```

Do not `scp` over the live binary, `cp` onto a running or codesign-killed inode, or `launchctl kickstart`. Those leave `~/.local/bin/agent-go` rejected by AMFI even when `codesign --verify` passes; the same bytes at a new path or inode will run. The installer refuses a drifted signer, refuses a non-idle durable store, execs the staged binary before replace, and restores the backup if launchd exits `OS_REASON_CODESIGNING` or the process does not stay up.

# Public repository hygiene

Use reserved example domains and synthetic chat, sender, and Notes identifiers in
fixtures. Never copy identities, endpoints, configuration fingerprints, or logs
from a live installation into new examples. Keep operational evidence, exported
editor conversations, signing material, and historical backup bundles outside
this repository. Review staged content and commit metadata before publishing;
a credential scanner alone does not find private conversation data.

This repository starts from a reviewed source snapshot. Do not merge or push
pre-publication history, tags, PR refs, or release assets into it. Dependency
versions are immutable: publish a new version when module contents change.
