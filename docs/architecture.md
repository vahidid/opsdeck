# Architecture

opsdeck is an **agent harness** in the same family as Claude Code and opencode —
a terminal-first client where a model works through tools in a loop — but its world is
**infrastructure**: servers, networks/VPNs, containers, clusters, logs. Its "project" is
a **workspace**, and its secrets live in a local **vault**.

## Goals

1. **Harness first.** Agent loop, tools, permissions, sessions, multi-model, MCP,
   skills, subagents — the same mechanics as coding harnesses.
2. **Infra-native.** The default target is remote (hosts, clusters, networks), not the
   local filesystem. Tools, safety rules and context are designed for that.
3. **Usable without AI.** Every tool the agent has is also a command the human can run
   (slash commands, `!` shell, CLI subcommands). No model configured = still a complete
   workbench.
4. **Local-first and secret-safe.** Vault is local; secrets never enter the model
   context; every action is audited.
5. **Project isolation.** Workspaces never share credentials, networks, history or memory.

## Layers

```
 ┌────────────────────────── Clients ───────────────────────────┐
 │  TUI (main)   headless (`opsdeck run -p`)   desktop UI (later) │
 │  IDE/other agents via MCP (opsdeck as MCP server)             │
 └──────────────────────────────┬───────────────────────────────┘
                                │ local API (HTTP+SSE over Unix socket)
 ┌──────────────────────────────▼───────────────────────────────┐
 │                       Server (opsdeckd)                       │
 │                                                               │
 │  Harness                                                      │
 │   • Agent loop      model ↔ tools, streaming, interrupts     │
 │   • Model gateway   Anthropic, OpenAI, Gemini, Ollama, …     │
 │   • Tool registry   built-in tools + MCP client tools        │
 │   • Permissions     risk classes × environment × mode        │
 │   • Sessions        transcript, resume, compaction, fork     │
 │   • Context         workspace docs, memory, skills, agents   │
 │   • Redaction       scrub secrets from every model input     │
 │                                                               │
 │  Platform (also used directly by the human)                   │
 │   • Vault           use vs reveal, per-workspace scopes       │
 │   • Workspaces      resources, docs, environments             │
 │   • Resolver        "how do I reach X?" (VPN → bastion → X)   │
 │   • Sessions/conn   live SSH, tunnels, port-forwards          │
 │   • Audit journal   append-only, hash-chained                 │
 │                                                               │
 │  Providers (one module per domain, added over time)           │
 │   ssh · wireguard · openvpn · docker · kubernetes · logs ·    │
 │   metrics · git (gitlab/github) · http/panels · dns/net       │
 └───────────────────────────────────────────────────────────────┘
```

Client/server split (as in opencode): the TUI is just one client. The same server
serves headless runs, a future desktop UI, and external agents through MCP. It also owns
everything that must outlive one window: unlocked vault, tunnels, SSH connections,
background tasks.

### Providers expose tools

A provider owns resource kinds in the workspace model **and** publishes tools. Each tool
declares its schema, its **risk class** and what it needs (secrets, reachability).
The same tool is callable by the agent and by the human (as `/docker ps app-1`), so
"without AI" and "with AI" share one implementation.

See [harness.md](harness.md) for the agent side and [vault.md](vault.md),
[workspace.md](workspace.md) for the platform side.

## Data locations

| What | Where | Format |
|---|---|---|
| Vault | `$DATA/vault.db` | SQLite, every item encrypted |
| Workspace definitions | `$DATA/workspaces/<name>/` or any registered dir | YAML + Markdown, git-friendly, **no secrets** |
| Sessions (transcripts) | `$STATE/workspaces/<id>/sessions/` | JSON lines |
| Memory | `$STATE/workspaces/<id>/memory/` | Markdown |
| known_hosts, caches | `$STATE/workspaces/<id>/` | |
| Audit journal | `$STATE/audit/` | JSON lines, hash-chained |
| Config (models, keys refs, defaults) | `$CONFIG/config.yaml` | YAML |

## Code layout

```
cmd/opsdeck          CLI entry point (serve, url, paths; socks-connect for git over SSH through a VPN)
internal/vault       encrypted store, SSH keys, TOTP, password generator
internal/workspace   workspace/resource model, YAML files, docs, skills, validation
internal/vpn         VPN drivers: WireGuard, Tailscale (tsnet), NetBird (embed), OpenVPN, OpenConnect, openfortivpn; userspace/system modes, local SOCKS5
internal/connect     networks (VPN profiles, SOCKS), SSH pool + jump hosts, exec/PTY/SFTP, port forwards
internal/gitx        Git working copies (system git, isolated config, token header / in-memory ssh-agent / SOCKS via), GitLab and GitHub APIs
internal/dnsx        DNS zones and records of the workspaces' domains through Cloudflare and ArvanCloud connectors
internal/deployx     deployment platforms of a workspace (Portainer, Argo CD, Komodo): targets, health, history, logs, diffs, actions
internal/acmex       TLS certificates from ACME CAs (dns-01 through dnsx), kept in the vault, deployed to hosts, renewed
internal/topology    the topology map: model, catalog of component and connection types, rendering for the agent
internal/skills      built-in skill library (embedded)
internal/docker      docker CLI over SSH
internal/kube        Kubernetes client (kubeconfig from the vault, dialed through via)
internal/ansible     Ansible projects: files, Ansible Vault (native), runs of ansible-playbook with live logs, generated workspace inventory
internal/llm         model gateway: Anthropic SDK + OpenAI-compatible streaming
internal/mcpx        workspace MCP servers (stdio) exposed as agent tools
internal/agent       agent loop, tools, risk classifier, permissions, redaction, sessions
internal/audit       hash-chained audit journal
internal/server      HTTP API, SSE, WebSocket terminal, embedded UI
web/                 React UI (built into internal/server/dist)
tools/demo           local SSH servers + scripted model for trying the UI
```

## Status

| Area | State |
|---|---|
| Vault, workspaces, validation, topology | done |
| SSH (keys, passwords, jump hosts, TOFU known_hosts), terminal, SFTP, stats | done |
| VPNs: WireGuard, Tailscale/Headscale, NetBird, OpenVPN, OpenConnect (AnyConnect, ocserv, FortiGate, GlobalProtect, Pulse, F5), openfortivpn, SOCKS5 — userspace and system modes | done |
| Git repositories: working copies, GitLab/GitHub merge requests, docs from repositories, agent tools | done |
| Domains: DNS zones and records at Cloudflare and ArvanCloud (connectors), the Domains page, the agent's dns tool | done |
| Certificates: ACME (Let's Encrypt…) with dns-01, the vault as the store, deployment to hosts, automatic renewal, the agent's certificate tool | done |
| Deployments: Portainer, Argo CD and Komodo as workspace resources, the Deployments page, the agent's deploy tool | done |
| Docker (CLI over SSH), Kubernetes (client-go) | done |
| Ansible: projects from folders or repositories, Ansible Vault, runs with live logs, agent tools with the password on the approval card | done |
| Logs streaming (journald, files, containers, pod logs) | done |
| Agent: loop, tools, modes, approvals, redaction, sessions, compaction, memory, skills | done |
| MCP client (workspace servers) | done |
| MCP server (expose opsdeck to other harnesses), subagents, hooks, per-workspace ssh-agent socket, CLI subcommands | next |
| Desktop wrapper (Wails/Tauri), TUI client | later |
