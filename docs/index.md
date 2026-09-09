---
slug: /
title: agent-go
description: A personal iMessage agent for macOS, with durable conversations, Apple Notes, and a Go runtime.
---

**An iMessage agent that runs on your Mac.**

Talk to Codex through authorized private and group chats. Work with Apple Notes, keep conversations across restarts, and develop in approved repositories from an owner coding chat. A small Go runtime coordinates identity, tools, durable work, and recovery.

[Install on macOS](install-macos.md) · [See what is verified](verified.md) · [Contribute](../CONTRIBUTING.md)

## Built around your conversations

| Capability | What it does |
| --- | --- |
| Private and group chats | Persistent conversations, exact chat identities, and authenticated senders. |
| Apple Notes | Optional note creation, reading, text and checklist edits, sharing, and confirmed recoverable deletion. |
| Tapbacks | Standard reactions to exact messages through the optional native backend. |
| Group reminders | Sender-owned, one-shot reminders with configured timezones. |
| Documents and Google | Optional document text extraction and scoped, read-only Gmail, Calendar, and Drive access. |
| Repository work | An explicitly configured owner coding DM can scope changes, review code, and prepare pull requests. |

These features have different verification levels. [Verified capabilities](verified.md) records live Mini results, model fixtures, and work that remains.

## A small runtime with durable state

`imsg` receives a message. The runtime authenticates its chat and sender, records a job in SQLite, and continues that chat's Codex conversation. Per-turn MCP tools expose the configured integrations. Replies and action outcomes are recorded for recovery.

The model interprets requests. Application code enforces identity, authorization, confirmation, and exact targets. Unknown external outcomes are preserved for review; persistent state does not promise exactly-once sends or edits.

## Start with one trusted chat

Use a dedicated macOS account with Messages signed in, an active graphical session, authenticated Codex, and the required privacy permissions. Follow the [installation guide](install-macos.md) to build and sign your own installation, verify a foreground reply, then run it at login.

**The default execution policy is `yolo`.** The agent can use the files, tools, credentials, and macOS permissions available to its account. A chat allowlist or workspace is not an operating-system sandbox. Prompts and tool results may be sent to the configured model provider.

## Current status

This is experimental software with a verified reference Mac mini installation. Private/group messaging, native Notes workflows, and standard tapbacks have passed live fixtures. The source and pinned Go dependencies are public. An owner-requested upgrade through the verified PR-to-install pipeline has also completed. Building the code does not grant native app permissions or authenticate the model.

The public repository starts from a reviewed source snapshot. Pre-publication operational history and evidence are retained privately. Contributors use their own accounts, configuration, signing identities, and macOS permissions.

MIT licensed. Independent community project; dependencies and installed tools retain their own licenses.
