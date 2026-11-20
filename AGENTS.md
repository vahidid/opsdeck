# AGENTS.md

Guidance for AI coding agents working on opsdeck. Humans: see [CONTRIBUTING.md](CONTRIBUTING.md).

## Project map

- `cmd/opsdeck`, `cmd/opsdeck-desktop`: CLI/server and desktop entry points (Go).
- `internal/`: all Go packages (harness in `internal/agent`, `internal/llm`, `internal/mcpx`;
  infra in `internal/docker`, `internal/kube`, `internal/vpn`, `internal/vault`, ...).
- `web/`: UI, React 18 + TypeScript + Vite + Tailwind.
- `e2e/`: Playwright browser tests against a real opsdeck.
- `docs/`: user and design docs. Start with `docs/architecture.md` and `docs/harness.md`.
  Architecture decisions go in `docs/decisions.md`.
- `site/`: the public website (GitHub Pages).

## Commands

```bash
make build                 # web UI + bin/opsdeck
make test                  # Go tests + TypeScript typecheck
go vet ./...
cd e2e && npx playwright test
```

## Workflow: GitHub issues are the source of truth

- Every piece of work starts from a GitHub issue with clear acceptance criteria.
  No issue yet: create one first (`gh issue create`), then start.
- Branch name: `issue-<number>-<short-slug>`, e.g. `issue-42-ssh-timeout`.
- Commits and the PR reference the issue; the PR body contains `Closes #<number>`.
- Keep status visible on the issue: assignee/labels when work starts, a short progress
  note when it is blocked or handed off, closed only through the merged PR.
- Labels: one type (`bug`, `enhancement`, `documentation`, ...), one `area:*`
  and one `priority:*` where it applies. Group release scope with milestones.

## Rules

- One change per branch and per pull request. Never commit directly to `main`.
- Work in the main checkout, one issue at a time. Before switching branches, the working
  tree must be clean: commit the work in progress, never stash or discard someone else's
  changes. Use a separate `git worktree` only when two changes must truly run in
  parallel (e.g. an urgent fix while a feature is open), and remove it right after.
- Run `gofmt`, `go vet ./...` and `make test` before handing work off.
- Add or update tests for changed behaviour; update `docs/` for user-visible behaviour
  and add a line under *Unreleased* in `CHANGELOG.md`.
- Match the style of the surrounding code. No new dependency without a stated reason.
- New agent tools go through the existing risk classification, approvals, redaction and
  audit (`docs/harness.md`). Secrets never reach model context, logs, tests or fixtures.
- Workspaces never share credentials, networks, history or memory; keep that isolation.
- Public text (issues, PRs, commits) contains no hostnames, addresses or vault data.
  Security problems follow `SECURITY.md`, never a public issue.
- Releases, tags and signing (`docs/releasing.md`) only on explicit maintainer request.
