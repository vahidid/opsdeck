# Contributing to opsdeck

Thanks for helping. Bug reports, ideas and pull requests are all welcome.

## Reporting a problem

Open an [issue](https://github.com/vahidid/opsdeck/issues/new/choose) with your opsdeck
version (*App settings → Updates*), your operating system and the steps that show the
problem. Leave out hostnames, addresses and anything from your vault.

Security problems are different: please follow [SECURITY.md](SECURITY.md) instead of
opening a public issue.

## Working on the code

Requirements: Go 1.24+ (the toolchain downloads the version in `go.mod`) and Node 20+.

```bash
make build                                     # web UI + bin/opsdeck
make test                                      # Go tests + TypeScript typecheck
cd web && npm run dev                          # UI dev server on :5173
bin/opsdeck --dev-origin http://localhost:5173 # backend for the dev server
```

The Go tests start their own SSH servers, WireGuard peers and scripted models, so they
need no infrastructure. [docs/architecture.md](docs/architecture.md) explains how the
code is organised, and [docs/harness.md](docs/harness.md) how the agent and its tools work.

## Pull requests

- Keep a pull request to one change, and explain what it changes and why.
- Run `make test` before you push; CI runs `go vet`, the Go tests and the web build.
- Match the style of the code around your change. Run `gofmt` on Go files.
- Add or update tests for behaviour you change, and the docs in `docs/` when the
  behaviour is user-visible. Add a line to the *Unreleased* section of
  [CHANGELOG.md](CHANGELOG.md) for changes users will notice.
- New agent tools must go through the same risk classification, approvals, redaction
  and audit as the existing ones (see [docs/harness.md](docs/harness.md)).

By contributing you agree that your contribution is released under the
[MIT License](LICENSE).
