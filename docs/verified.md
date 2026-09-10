---
title: Verified capabilities
description: What has actually worked on the reference Mac mini, what the tests prove, and what remains unfinished.
---

# Verified capabilities

This is the evidence summary for the reference Mac mini as of **9 September 2026**. Live results apply to that account, configured participants, signed helpers, and available GUI session. They do not guarantee every macOS version or an unattended first installation.

## Passed with real Messages and Notes

| Area | Verified behavior |
| --- | --- |
| Private messaging | Authorized DM intake, model reply, and independently observed receipt in the intended conversation. |
| Group messaging | Shared conversation with authenticated senders; replies independently received in the exact group. |
| Standard tapbacks | Exact-message reactions independently observed in private and group chats using the optional patched native backend with SIP enabled. |
| Batch deletion | A native fixture created three disposable private notes, reviewed and deleted exactly two, verified the third remained active, then cleaned it up through a separate confirmation. This fixture used real Apple Notes with a temporary test chat/store. |
| Private Notes | Create, read, multiline text edits, add real checklist items, change checked state, and confirmed recoverable deletion. |
| Group Notes | Create a fresh shared note, verify configured participants and a sharing link, read content, add/check/rename checklist items, and confirm recoverable deletion. |
| First-creation invitations | A fresh authenticated group request created a shared two-item checklist and included its invitation link in the first reply. The recipient laptop received the reply and observed the exact acknowledgement tapback. |
| Longer shared checklists | A disposable note was created with 22 items, then extended to 30 while retaining the checked item. The recipient laptop displayed all 30 and changed a checkbox; the change was independently read on the Mini. An unselected control stayed unchanged. |
| Existing invitation links | The two requested existing shared-note links were retrieved and delivered to the original group without recreating notes or inviting participants again. |
| Reviewed Notes recovery | A fresh request verified membership before using a note from an abandoned sharing attempt. Original evidence was retained; creation and invitations were not replayed. |
| Owner coding chat | Scope tickets, create issues and PRs, and perform code review in approved repositories. |

Deletion moved the disposable test note to **Recently Deleted**, with independent readback. It was not permanent erasure. The checklist collaboration test verified one recipient account. Other recipients and each new invitation still require their own opening/acceptance evidence; owner-side sharing metadata does not establish it. See the [shared-note quickstart](shared-notes-quickstart.md). Group discovery still requires care: a note being shared does not prove its collaborators match a particular chat. See [Notes access rules](capabilities.md#apple-notes-optional).

Tapbacks require English Messages labels and a usable GUI. Attachments, unsupported message parts, duplicate text, and ambiguous UI targets are deliberately excluded. Selecting a message or submitting a UI action does not establish a successful reaction.

## Passed in the reference runtime

- **Signed deployment:** the installed application passed the nine-stage Mini verifier and a guarded deployment with 70 seconds of stable health observation. The application and native helpers retained their established Keychain signing identities.
- **Owner-requested upgrade:** an authorized coding DM queued the verified revision, the independent worker waited for the requesting job to finish, and the guarded installer completed. Installed bytes matched the verified artifact, the existing signer and configuration were preserved, and the 70-second health check passed with no unresolved work.
- **Public build:** the public source was cloned anonymously on the Mini and its pinned modules resolved from an empty cache without maintainer credentials. All nine deterministic verification stages passed. Hosted CI did not execute because of an account billing lock; the hosted version matrix and Pages deployment still need a successful run.
- **State preservation:** deployment and reviewed recovery retained existing sessions and durable evidence. A database-copy comparison checked existing values across 21 tables for the tested migration path.
- **Keep awake:** the per-user helper holds idle display and system sleep assertions. It cannot unlock a manually locked screen or complete login after logout, reboot, or FileVault preboot.
- **VPN maintenance:** ordinary SSH over Tailscale connects to the Mini using the existing login key and verified host key. A test from a different physical network remains outstanding.

These are observations from recorded checks, not a live service-status dashboard. Process health alone does not verify model or native app behavior.

## Verified with model fixtures and fake services

Authenticated Codex fixtures passed Notes discovery/read, multiple additions to the selected note, and clarification before ambiguous writes. Reminder fixtures passed natural-language intent, saved clarification across turns, requester/time binding, creation receipts, cancellation, and combined Notes reading plus reminder creation.

Batch-deletion model fixtures also verified full-list approval, refusal, and partial approval: only explicit approval of the complete reviewed set caused deletion.

Those tests used temporary stores and fake Notes/messaging backends. They establish model-to-tool behavior for the tested cases, not real reminder delivery or changes to a person's Notes.

The deterministic Mini pipeline separately covers formatting, normal and race tests, vet, builds, Python script tests, and signed executable execution. See [the verification sequence](mini-development.md).

## Implemented, with additional acceptance needed

| Capability | Remaining evidence |
| --- | --- |
| Tapback receipt persistence | The imessage result API is released. Application receipt persistence has passed signed unit/race tests but remains in a draft PR pending final Mini checks and deployment; audio targets remain unsupported. |
| Invitation opening | The disposable shared checklist was read and edited on one recipient Mac. The other participant’s device and the fresh production invitation’s browser-to-Notes handoff have not been verified. |
| Google integration | Per-chat account grants and read-only tools are implemented. This summary does not claim a complete live OAuth and provider acceptance run. |
| Document attachments | Bounded extraction and authorization checks are implemented. This summary does not claim live acceptance across every supported document format. |
| Remote availability | Test from a different physical network and establish behavior after sleep, lock, logout, reboot, and preboot recovery. |
| Fresh installation | Source and pinned dependencies are public. A successful anonymous build still does not verify first-run authentication, macOS permissions, or every native integration on a new Mac. |

Operator-driven deployment and one complete owner-requested PR-to-install handoff have now worked on the reference Mini. The public snapshot has the same runtime and installer code as that deployed revision; its additional changes sanitize fixtures and update community documentation. Failed and incomplete earlier runs remain failures; they are not counted as passes.
