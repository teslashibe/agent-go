# agent-go

**An iMessage agent that runs on your Mac.** Go, SQLite, Codex, and optional native Apple Notes tools.

[Documentation](https://teslashibe.github.io/agent-go/) · [What works](docs/verified.md) · [Install](docs/install-macos.md) · [Roadmap](https://github.com/users/teslashibe/projects/1) · [Contribute](CONTRIBUTING.md)

## What works

Live Mac mini fixtures have verified private/group replies, exact-message standard tapbacks, private Notes creation and editing, group sharing and checklist edits, and confirmed deletion to Recently Deleted. Signed operator deployments preserve the installation's Keychain identity. VPN SSH works; testing from another physical network remains.

Authenticated model fixtures with fake services also cover Notes tool use and reminder clarification, creation, and cancellation. Google reads and document extraction are implemented; this summary does not claim complete live acceptance. Shared-note invitations and recipient opening are described in the [group Notes quickstart](docs/shared-notes-quickstart.md). See [verified capabilities](docs/verified.md) for the evidence and limits.

## Run it

Follow [Install on macOS](docs/install-macos.md). You need Messages signed in, an active GUI session, authenticated Codex, a compatible `imsg` executable, Go, and your own signing identity and macOS permissions.

**Experimental:** native integrations depend on your Mac’s logged-in session and permissions. The default execution policy is `yolo`; use a dedicated account and trusted participants. Chat allowlists are not an OS sandbox.

## Documentation

The [Docusaurus site](https://teslashibe.github.io/agent-go/) reads the existing Markdown guides directly and publishes through GitHub Pages. With Node.js 20 or newer:

```sh
cd website
npm ci
npm start
```

`npm run build` produces `website/build/` and rejects broken internal links. See [site development and GitHub Pages](docs/documentation.md). Go verification runs on the [Mac mini](docs/mini-development.md), separately from the documentation build.

[MIT](LICENSE) · [Security](SECURITY.md) · [Support](SUPPORT.md)
