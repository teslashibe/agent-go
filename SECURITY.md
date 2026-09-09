# Security Policy

## Reporting

Do not open a public issue for a vulnerability or include chat identifiers, credentials, transcripts, attached documents or extracted text, attachment paths, configuration, database contents, signing material, or other private operational data in a report.

Use GitHub's private vulnerability reporting for this repository. If that feature is unavailable, contact the maintainers through a private channel listed on their GitHub profiles and ask for a secure reporting path before sending details.

Include the affected revision, impact, reproduction steps, and any known mitigations. Allow maintainers time to investigate before public disclosure.

## Scope

The default runner uses YOLO execution and is not an OS sandbox. Reports that show a bypass of documented identity, authorization, confirmation, durable-outcome, or reviewed-configuration checks are in scope. A trusted model using permissions deliberately granted to its service account is part of the documented threat model, not by itself a vulnerability.

Document ingestion is authorized by the exact inbound message identity before
opening any attachment. Path confinement, regular-file checks, private
snapshotting, extraction limits, and omission of local paths from model context
are security boundaries.
