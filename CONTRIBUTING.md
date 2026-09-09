# Contributing

Keep changes focused and preserve identity checks, authorization, confirmation, durable recovery evidence, and data-loss protection.

1. Open an issue for behavior or architecture changes.
2. Work on a branch and include tests for changed behavior.
3. Run the checks below on the development Mac with public module resolution and no enclosing Go workspace. For the reference deployment, all Go commands run remotely on the Mac mini; follow [the isolated verification sequence](docs/mini-development.md). Never run tests against the live service state.
4. Open a pull request describing the user-visible effect, risks, and verification.

```sh
GOWORK=off go mod tidy
git diff --exit-code -- go.mod go.sum
GOWORK=off go build ./cmd/agent-go
GOWORK=off go vet ./...
GOWORK=off go test -race ./...
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_*.py' -v
```

Do not commit real identities, chat data, credentials, runtime databases, logs, backups, signing material, or deployment configuration. Live external tests must be explicitly opted into and must not use other people's chats or Notes as fixtures.

Documentation-only changes should verify examples, relative links, and the behavior being described. Do not claim native integration coverage from a documentation check. Runtime changes must retain revision-scoped verification evidence; installation and live effects have separate gates.
