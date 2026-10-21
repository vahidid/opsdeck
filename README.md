# opsdeck

An AI agent harness for infrastructure — like Claude Code / opencode, but its world is
servers, networks/VPNs, containers, clusters and logs instead of a code repository —
plus a complete local workbench that is just as useful without AI.

- **Workspaces** — one per project: hosts, groups, WireGuard/SOCKS networks, jump hosts,
  web panels, Git repos, databases, Kubernetes clusters, docs and runbooks. Plain YAML +
  Markdown, git-friendly, **never contain secrets**.
- **Vault** — local, encrypted (Argon2id → XChaCha20-Poly1305, a separate key per
  workspace): SSH keys (generate/import), logins with TOTP, API tokens, kubeconfigs,
  VPN configs, certificates, secret files. Secrets are *used* by opsdeck, never shown to
  the agent.
- **Reachability** — `via: network/office-wg` or `via: host/bastion`; opsdeck brings up
  userspace WireGuard tunnels (no root, many at once) and chains SSH jump hosts
  automatically, for the UI, the terminal and the agent alike.
- **Workbench** — SSH terminals in the browser, remote file browser/editor, quick host
  stats, live log streaming (journald, files, containers), Docker and Kubernetes views,
  port-forwarding for panels that are only reachable through a VPN.
- **Agent** — chat with any model (Anthropic, OpenAI, OpenRouter, Ollama, LM Studio…)
  that works through infra tools: `exec` (single host or groups), `read_file`/`edit_file`
  with diffs and backups, `docker`, `kubernetes`, `net_check`, `git_sync`, docs, skills,
  memory, and any MCP server you attach to the workspace.
- **Local agents** — besides model APIs, the agent can be a CLI you already use with
  your own login: **Claude Code**, **Codex** (ChatGPT account), **Gemini CLI** (Google
  account) or any **ACP** agent (opencode, Qwen Code, GitHub Copilot CLI…). Their
  built-in tools are switched off; they work only through opsdeck's tools (served to
  them over MCP), so approvals, redaction and audit still apply.
- **Safety** — every tool call is classified (`read` / `change` / `destructive`; shell
  commands are parsed); Plan / Ask / Auto modes; protected environments; per-workspace
  permission rules; approvals with previews; secrets redacted from everything the model
  sees; a hash-chained audit journal of every action.

## Quick start

Requirements to build: Go 1.24+ (the toolchain auto-downloads 1.26), Node 20+.

```bash
make build          # builds the web UI and bin/opsdeck
bin/opsdeck         # starts on http://127.0.0.1:7788 and opens the browser
```

The first screen creates the vault (master password; there is no recovery). Then:

1. **New workspace** → e.g. `acme`.
2. **Vault → Generate SSH key** (or import yours); copy the public key to your servers.
3. **Resources → Add resource** → hosts with address, port, user, credential, and `via`
   if they sit behind a bastion or VPN. Add a **WireGuard network** by storing the
   `.conf` as a *VPN config* vault item.
4. **App settings → Model providers** → add an API provider (key stored in the vault), or
   under **Local agents** pick an installed CLI (Claude Code, Codex, Gemini CLI…) that
   you have already signed in to.
5. **Agent** → ask: *"Check disk and memory on all prod hosts"*, or *"Clone shop from
   GitLab onto test-1 and run it as a single container"*.

Without any model configured everything except the Agent page works.

### Try it without servers

```bash
make demo
bin/opsdeck-demo -pubkey "<public key from a vault ssh-key>"
```

starts two local SSH servers (a bastion and an app host reachable only through it) and a
scripted model endpoint (OpenAI-compatible), and prints their addresses.

### Commands

```
opsdeck [serve] [--port 7788] [--no-open]   start the server and UI
opsdeck url                                 print the login link
opsdeck paths                               show where data is stored
```

The server only listens on `127.0.0.1`. The login link carries a token (stored in the
state directory); the browser keeps it as an HttpOnly cookie. Requests with a foreign
`Host` or `Origin` are refused.

## Data

| What | Where (Linux; macOS/Windows use their app-data folders) |
|---|---|
| Vault | `~/.local/share/opsdeck/vault.db` |
| Workspaces — inventory, docs, runbooks, agent reports, memory, sessions | `~/.local/share/opsdeck/workspaces/<name>/` (or any imported folder); see [docs/workspace.md](docs/workspace.md) |
| Audit journal | `~/.local/state/opsdeck/audit/` |
| Settings | `~/.config/opsdeck/config.yaml` |

`OPSDECK_HOME=/some/dir` puts everything under one directory.

## Development

```bash
make test                                      # Go tests + TypeScript typecheck
cd web && npm run dev                          # UI dev server on :5173
bin/opsdeck --dev-origin http://localhost:5173 # backend for the dev server
```

The Go test suite runs real SSH servers (exec, PTY, SFTP, jump hosts), userspace
WireGuard peers, a stdio MCP server, a fake Kubernetes API and scripted models — no
external infrastructure needed.

## Status and known limits

- OpenVPN networks are not supported yet (WireGuard and SOCKS5 are).
- Docker is driven through the `docker` CLI on the host over SSH (retries with `sudo -n`).
- PuTTY `.ppk` keys must be converted to OpenSSH format first.
- Kubeconfigs that use `exec` auth plugins run those plugins on this machine.
- MCP servers from a workspace only start after you save them in Workspace settings on
  this machine (protects you from commands in a cloned workspace).
- Tested on Linux; macOS and Windows builds compile but have not been exercised yet.

## Documents

- [docs/architecture.md](docs/architecture.md) — framework, modules, data layout
- [docs/harness.md](docs/harness.md) — agent loop, tools, permissions
- [docs/vault.md](docs/vault.md) — vault design
- [docs/workspace.md](docs/workspace.md) — workspace model and files
- [docs/api.md](docs/api.md) — local HTTP API used by the UI
- [docs/decisions.md](docs/decisions.md) — decisions and open questions
