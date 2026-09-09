# Optional capabilities

## Group reminders

Add another entry to `agents` with `group: true`, the exact group chat ID/GUID, and its allowed senders. Keep it on the scratch workspace. Configure per-agent `profiles` for reminder users with `id`, `sender`, and `time_zone`; sender identities must match the allowlist, and timezone values are IANA names such as `UTC`. Set them explicitly: an omitted profile timezone defaults to `America/Los_Angeles`, not the sender's location.

Reminders belong to the authenticated requester, not a person named in message text. They are one-shot reminders managed by this application's SQLite state, **not Apple Reminders integration**. Changing a saved timezone does not reschedule existing reminders.

## Coding DM

Set `work_dir` on the individual private agent, pointing to the **parent of recognized checkouts**. Coding instructions activate when that directory contains `agent-go`, `notes`, `imessage`, `codex`, or an `agent-*` directory whose module path matches `github.com/teslashibe/<directory>`.

An empty arbitrary directory does not activate the coding workflow. Do not set a coding workspace on a group. A per-agent workspace is extra access; it does not create a separate Codex home, MCP configuration, tool set, or interactive policy. Coding DMs use the same service-wide reviewed interactive contract as other chats.

GitHub work also requires an authenticated `gh` CLI with access to the explicitly authorized repository. A repository appearing in search results does not authorize changes. Use feature branches and isolated worktrees for approved repositories; repository work does not itself authorize an agent installation.

## Document attachments

Set `document_attachments: true` to let authenticated participants attach
documents directly to either a DM or group message. The service requests
attachment metadata only on live/resumed subscriptions; it does not enumerate
unrelated Messages attachments or import historical files.

Supported text extraction covers PDF, plain text, Markdown, CSV/TSV, JSON,
XML/YAML, HTML, RTF, Word (`.doc`/`.docx`), OpenDocument text, and web archives.
Unsupported document formats produce an explicit extraction status; images and
other non-document media remain outside this feature. Encrypted or image-only
PDFs require separate OCR support and are reported as unreadable.

Files must be regular files under the current account's
`~/Library/Messages/Attachments` tree. Each file is snapshotted into a private
temporary directory before parsing; paths are never placed in model context.
The service accepts at most four documents per message, 25 MiB per file,
128 KiB extracted text per document, and 192 KiB total document text. Temporary
snapshots are deleted after extraction. Extracted text, file name, MIME type,
size, SHA-256, and truncation/error status become part of the durable chat
prompt and must be treated as sensitive runtime state.

## Experimental computer use

`computer_use` is a service-wide, explicit opt-in that requires an
operator-supplied `mcp_servers.cua_repl` command. When a reviewed interactive
configuration is present, the operator must also review the Codex binary,
configuration sources, workspace, MCP bindings, and disabled shadowing plugin.
Every configured chat inherits this integration.

The `cua_repl` bridge relies on OpenAI desktop internals and an external runtime
that this repository does not install. There is no reproducible baseline public
installation for computer use. Treat it as experimental operator integration,
not a sandbox or a safety toggle.

## Apple Notes (optional)

Leave `notes_helper` unset to keep Notes disabled. Enabling it requires a separately installed native helper from the pinned `notes` dependency, the helper's macOS permissions, and an **absolute** `notes_helper` path with local transport. This checkout does not contain a standalone helper installer.

For the Mac account owner's single-person DM, set `owner_notes: true` on that
agent entry. This is an explicit grant to read and change all unprotected Notes
available to the Mac account, including private notes. Do not grant it to another
person's DM. It is rejected for groups and is not inherited by other agents.
The owner gets private `create_note`, exact text/checklist edits and the existing
two-turn recoverable deletion confirmation. Private creation sends no invitations;
uncertain creation or edits retain their operation records and cannot be replayed
automatically. Leave the setting absent to keep ordinary DMs' Notes disabled.

For configured groups with at least two allowed senders, the current discovery filter exposes shared, non-password-protected notes with valid names and IDs. **“Shared” does not prove that a note's collaborators match the configured chat.** Review the account's shared notes before enabling this feature; use an account containing only appropriately scoped data.

The default exec path adds a per-turn MCP server named `harness`, implemented by this executable's `notes-mcp` relay over a private Unix socket. It preserves other configured MCP entries but owns the `harness` name. Do not run the relay manually as the daemon or invent an `AGENT_NOTES_SOCKET` value.

For the optional `interactive_config` path, the reviewed contract must explicitly bind `harness` to the reviewed executable plus `notes-mcp` argument and allow only the dynamic `AGENT_NOTES_SOCKET` environment binding. Binary/version, workspace, Codex home/config, MCP configuration, and other reviewed sources must match the pinned contract. Do not copy fixture hashes, versions, or another machine's contract. See [the adapter](https://github.com/teslashibe/agent-go/blob/main/cmd/agent-go/main.go) and [binding tests](https://github.com/teslashibe/agent-go/blob/main/cmd/agent-go/approvals_test.go).

Notes operations record outcomes durably. Ambiguous checklist matches are not changed, partial results are preserved, and deleting a whole note requires a later confirmation from the same requester. An uncertain mutation is not safe to replay automatically.

For batch deletion, ask for an explicit set of up to 20 notes. The agent presents the exact list before any deletion. The same requester must confirm the complete list in a later message within ten minutes. A renamed, changed, locked, missing, or inaccessible note rejects the batch before deletion starts. Each note is rechecked immediately before its own deletion; a later failure stops the remaining notes and reports each result. Completed deletions are not rolled back, and an uncertain result never permits an automatic retry. Notes move to **Recently Deleted**, not permanent erasure.

Only one pending deletion review is kept per requester per chat. A new batch request replaces the previous review. Single-note deletion remains supported; a single-note confirmation cannot approve part of a batch.


## Google

See [Google integration](google.md) for per-chat, read-only Gmail, Calendar, and Drive access and optional account connection from a phone.
