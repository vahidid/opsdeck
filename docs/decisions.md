# Decisions

## Made

| # | Decision | Why |
|---|---|---|
| 1 | Local-first; no cloud dependency | Requirement |
| 2 | Core runs as a daemon; CLI/UI/MCP/AI are clients | Shared unlocked vault, tunnels, sessions, one audit trail |
| 3 | Workspace files contain no secrets, only `vault://` references | Git-friendly, shareable |
| 4 | Key hierarchy: password → KEK → Vault Key → per-scope keys | Password change, multiple unlock methods, per-project isolation |
| 5 | Argon2id + XChaCha20-Poly1305 | Modern, well-reviewed defaults |
| 6 | "Use vs reveal" split; agents can never reveal | Lets AI operate without seeing secrets |
| 7 | Per-workspace known_hosts (TOFU) and credentials; per-workspace `ssh-agent` socket for external tools is designed but **not built yet** | Isolation, works with existing tools |
| 8 | Resource envelope (`kind/name/via/spec`) + providers | Add VPN/Docker/K8s/logs later without changing the core |
| 9 | Product is an **agent harness** (like Claude Code / opencode) focused on infra; the web UI is the first client (a TUI can follow on the same API) | Requirement |
| 10 | Everything the agent can do, the human can do from the UI (terminal, exec, files, docker, k8s, logs); CLI subcommands per tool are **not built yet** | "Usable without AI" |
| 11 | Safety before execution: risk classes × environment policy, plan mode, diffs + backups, one approval listing every host for fan-out (canary rollout **not built yet**) | Infra has no undo |
| 12 | Redaction of every model input + optional local-only models per workspace | Secrets never reach the model |
| 13 | Own harness, not a plugin on opencode/Claude Code; ship an MCP server for interop | Guarantees must live inside the loop |
| 14 | Go backend; React UI embedded in the single binary and served on 127.0.0.1 | One binary per OS, infra libraries are Go-native; UI works everywhere and can be wrapped as a desktop app later |
| 15 | Anthropic via the official Go SDK (streaming, adaptive thinking, effort, server-side refusal fallback); everything else via the OpenAI-compatible protocol | One code path for Claude features, broad coverage for local/other models |
| 16 | Workspace MCP servers require local trust | A cloned workspace must not be able to run commands on this machine |
| 17 | Git credentials reach `git` through stdin/env, never the command line or `.git/config` | The agent can deploy from private repos without seeing tokens |

## Open

| # | Question | Notes |
|---|---|---|
| C | Product name | `opsdeck` is a placeholder. |
| E | OpenVPN support | Needs the `openvpn` binary and privileges; or a userspace implementation. |
| F | Team sharing | Workspace folders are git-friendly already; sharing vault scopes (encrypted export to a teammate's key) is designed for but not built. |
