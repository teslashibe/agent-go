# Public release and privacy review

The application and its five Go dependency repositories use the MIT license.
The public application repository begins with a reviewed source snapshot;
pre-publication history, PRs, operational evidence, and release assets stay in a
separate private repository. Do not import that history into this repository.

## Pinned dependencies

| Module | Required version | Visibility |
| --- | --- | --- |
| github.com/teslashibe/codex | v0.7.3 | Public |
| github.com/teslashibe/notes | v0.2.15 | Public |
| github.com/teslashibe/imessage | v0.3.4 | Public |
| github.com/teslashibe/google-go | v0.3.1 | Public |
| github.com/teslashibe/mcptool | v0.1.2 | Public |

Existing dependency versions retain their original contents and checksums.
The codex v0.7.3 pin is intentional for the interactive maintenance API. Public
builds must resolve the committed versions without a maintainer token, local
replacement, enclosing Go workspace, or pre-populated private module cache.

## Publication audit

On 9 September 2026, the review covered all six repositories: GitHub branches,
tags and retained PR refs; reachable source and commit metadata; issue and PR
text, comments and reviews; and available release assets. The scan used Gitleaks
8.30.1 with redacted reports, plus separate checks for private identities,
Notes identifiers, local paths and endpoints. Scanner findings were reviewed;
example strings and public contributor attribution are not credentials.

Historical private application data was found and excluded by keeping the
original repository private. A real Notes identifier in a current fake-service
test was replaced with a synthetic identifier, and personal fixture names were
replaced with generic examples. The dependency review found no private runtime
identities or confirmed credentials in the material selected for publication.
Public contributor and automation attribution is retained. Previously public
copies of contributor metadata cannot be recalled by changing this repository.

These checks reduce disclosure risk; they do not prove the absence of every
possible secret. Private context, editor-chat exports, database snapshots,
Keychains, passphrases and machine configuration are never release inputs.
Revocation of credentials exposed outside Git is a separate operator task;
a clean source scan does not establish provider revocation.

## Verification and support

Run the deterministic [Mini verification sequence](mini-development.md) against
one exact revision. Keep normal/race tests, vet, builds and script checks
separate from authenticated model fixtures and real Apple app tests. Record
failures and skipped opt-in tests. Anonymous module resolution and CI must pass
before claiming a reproducible public build.

[Verified capabilities](verified.md) lists the live reference results and
remaining acceptance work. Publication is not a stable-release claim. Native
Messages and Notes need a logged-in GUI session and permissions; SSH does not
unlock a Mac. Community installations provide their own signing identity.
The model runner is not an OS sandbox.

Use [private vulnerability reporting](https://github.com/teslashibe/agent-go/security/advisories/new)
for sensitive reports. Do not attach private operational evidence to public
issues. A clean issue list is not proof that outstanding acceptance work passed.
