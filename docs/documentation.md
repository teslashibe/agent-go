# Documentation site

The site uses Docusaurus with the classic documentation theme. There is no blog, documentation versioning, external search service, or separate copy of the guides. The plugin reads `docs/` plus the root contribution, security, and support files. The internal publication-readiness report is excluded from the site.

## Local development

Use Node.js 20 or newer; the publishing workflow uses Node.js 24. From the repository root:

```sh
cd website
npm ci
npm start
```

Preview a production build before publishing:

```sh
npm run build
npm run serve
```

Open `http://localhost:3000/agent-go/`. The site has a `/agent-go/` base path and emits directory indexes for GitHub Pages. Broken routes, Markdown links, and heading anchors fail the build. Review desktop and mobile navigation as well as the command examples.

Add or edit the original Markdown file, then update `website/sidebars.js` when a page should appear in navigation. Keep `docs/verified.md` tied to recorded evidence. Do not promote fake-backend, skipped, or failed checks into live capability claims.

## GitHub Pages

The `Documentation` workflow builds on pull requests and pushes affecting the documentation. Only pushes to `main` or a manual run on `main` can deploy the built artifact to the `github-pages` environment. The build job has read-only repository access; only the deployment job gets Pages and identity-token permissions.

Configure the repository's Pages source as **GitHub Actions**. The intended site URL is `https://teslashibe.github.io/agent-go/`. A successful build is not a successful publication: check the deployment result and open that URL before announcing it as live.

GitHub account/plan access and enabled Actions are required. A Pages site may be public even when its source repository is private. Publish only reviewed documentation and static assets; never add operational logs, chat exports, credentials, signing files, or private context to the content include list or `website/static/`.

The site uses public npm dependencies and does not need private Go modules, Codex authentication, Apple permissions, or access to the running agent. Publishing documentation does not install or restart the application.
