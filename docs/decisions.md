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
| 18 | The agent can work on this computer (files, commands, key import) behind consent: reads outside the workspace ask per folder, credential files per file, writes and commands always; opsdeck's own files are unreachable | Real tasks start from the user's machine (`~/.ssh`, project folders); a harness that cannot see them is not useful |
| 20 | VPNs are drivers with two modes: userspace (netstack in opsdeck or a SOCKS helper; default) and system (TUN). Native Go where a maintained library exists (WireGuard, tsnet, NetBird embed), official binaries otherwise (openvpn + management interface, openconnect, openfortivpn) | Userspace needs no root and lets many VPNs coexist; system mode is still there when every app must use the VPN. No reliable pure-Go OpenVPN/AnyConnect client exists |
| 21 | The agent may connect userspace VPNs without asking; system-mode connects and all disconnects need approval | A userspace tunnel only affects opsdeck (like an SSH connection); a system tunnel changes the machine |
| 22 | VPN SSO is done by opsdeck (aggregate-auth XML with STRAP/HPKE, GlobalProtect prelogin, FortiGate SAML) with a DevTools-driven browser window or the default browser; the VPN client only receives the resulting cookie | The CLIs need a webview for most SAML flows; doing the exchange ourselves works without IPv6 or an HPKE-enabled openconnect and lets a per-profile browser profile remember the IdP login |
| 23 | Workspace tabs keep every open page mounted (hidden) with its own router location; polling pauses in background tabs; the terminal stays a single tab | Switching tabs must not lose an agent stream, a draft or a log view |
| 24 | Text direction is decided per message (RTL when right-to-left script dominates the prose) and per paragraph otherwise; code is always LTR; Vazirmatn is bundled | Persian answers mix Persian prose with commands and paths |
| 25 | Git working copies live in the workspace (`repos/<name>`, git-ignored) and are driven with the system `git` under an isolated config; credentials are an origin-scoped header or an in-memory ssh-agent; `via` goes through a per-command authenticated local SOCKS proxy | Same behaviour as the user's own git (protocols, LFS-free repos, SSH), no secret on disk or in argv, and no user/system git config (helpers, hooks, aliases) can interfere |
| 26 | Local repository edits and commits need one approval per repository per session; pushing asks with the outgoing diff, pushing to default/protected branches and merging always ask | Nothing leaves the machine before a push, so editing should be smooth; the push and the merge are what reach infrastructure |
| 19 | Secrets move from local files into the vault inside opsdeck (`vault_import_ssh_key`, `vault_add_item` with `file`/`generate`), never through the model | Keeps "secrets never reach the model" while letting the agent onboard credentials |

## Open

| # | Question | Notes |
|---|---|---|
| C | Product name | `opsdeck` is a placeholder. |
| F | Team sharing | Workspace folders are git-friendly already; sharing vault scopes (encrypted export to a teammate's key) is designed for but not built. |
