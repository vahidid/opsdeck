# Harness

What we keep from coding harnesses (Claude Code, opencode, Crush) and what changes
because the target is infrastructure.

## Same mechanics as coding harnesses

| Mechanic | In opsdeck |
|---|---|
| Agent loop | model → tool calls → results → model, streaming, interruptible (Esc) |
| Multi-model | provider-agnostic gateway; per-agent model choice; local models (Ollama) for sensitive infra |
| Project instructions (`CLAUDE.md` / `AGENTS.md`) | the **workspace**: `OPSDECK.md` + `docs/` + resource model are the agent's context |
| Tools + MCP | built-in infra tools; external MCP servers attachable per workspace |
| Permissions & modes | allow / ask / deny rules; modes: **plan**, **ask** (default), **auto** |
| Sessions | persisted transcripts, resume, fork, context compaction |
| Slash commands | `/ssh app-1`, `/vault`, `/ws use acme`, `/logs`, user-defined commands |
| `!` shell | run a command yourself; output goes into context |
| Subagents | e.g. `investigator` (read-only, cheap model), `deployer` (write, needs approval) |
| Skills | packaged procedures: "deploy docker app", "rotate certificate", "add WireGuard peer" |
| Hooks | before/after tool call (e.g. post to Slack, block commands, extra logging) |
| Memory | per-workspace notes the agent learns ("app-1 uses podman, not docker") |
| Headless | `opsdeck run -p "..." --ws acme` for CI/cron, JSON output |
| Background tasks | long deploys, log tails, watchers that notify the session |

## What is different for infrastructure

### 1. The target is remote
Coding harnesses assume a local working directory. Here the tools take a **target**:
a workspace resource (`host/app-1`, `cluster/prod`, `group/app-servers`). The resolver
brings up whatever path is needed (VPN, bastion) before the tool runs.

Built-in tools:

| Tool | Risk class |
|---|---|
| `inventory` — resources, environments, paths, network status | read |
| `read_doc`, `load_skill`, `memory` | read |
| `write_doc` — record runbooks/notes in the workspace | change |
| `exec(target, command)` — host, group or list; parallel fan-out | classified per command |
| `read_file`, `list_dir` (SFTP) | read |
| `write_file`, `edit_file` — diff preview, atomic write, automatic backup | change |
| `net_check` — tcp / http (status, TLS expiry) / dns / ping, from here or from a host | read |
| `docker` — ps, images, stats, inspect, logs / start, stop, restart / rm | read / change / destructive |
| `kubernetes` — get, yaml, logs, rollout_status / restart, scale / delete_pod | read / change / destructive |
| `git_sync` — clone or update a GitRemote on a host with its vault token | change |
| `mcp__<server>__<tool>` — workspace MCP servers | read if the tool is annotated read-only, else change |

### 2. No undo
Code has git; production does not. So safety comes before execution, not after:

- **Risk classes** on every call: `read` → `change` → `destructive`.
  `exec` commands are classified by a parser (`systemctl status` = read,
  `systemctl restart` = change, `rm -rf`, `DROP`, `kubectl delete` = destructive).
  Unknown = treated as change.
- **Environment-aware policy**: the same call can be auto-allowed on `dev` and require
  approval on `prod` (environments come from the workspace).
- **Plan mode is first-class**: the agent investigates read-only, writes a change plan
  (steps, commands, rollback), the human approves, then it executes step by step.
- **Diffs before writes** for config files; automatic backup of a remote file before
  it is edited.
- **Fan-out guard**: actions over a host group ask once with the full list, and can run
  as canary (1 host → confirm → rest).

### 3. Secrets never reach the model
- The agent references secrets by name; the vault **uses** them (SSH auth, login,
  tokens) but never returns them to the agent.
- Every tool output passes through **redaction** before entering the context: known vault
  values, private keys, tokens and password patterns are masked.
- Policy can force a local model for a workspace (`model.allowed: [ollama/*]`) so nothing
  leaves the machine.

### 4. Everything is journaled
Separate from the chat transcript, an **audit journal** records every executed action:
time, workspace, target, exact command, who approved, exit code, output digest.
A session can be exported as a report or as a reusable runbook/skill.

### 5. Long-running and live
Deploys, rollouts and log tails take minutes. Tools can return a background handle;
the session gets notified on completion or on a matching log line.

## Permission rule examples

```yaml
# in workspace.yaml (or user config)
permissions:
  default: ask
  rules:
    - allow: { risk: read }
    - allow: { risk: change, environment: [dev] }
    - ask:   { risk: change, environment: [staging, prod] }
    - deny:  { risk: destructive, environment: [prod] }   # human must run it via `!`
    - deny:  { tool: exec, match: "iptables -F*" }
```

## Language choice and reference projects

- **opencode** (TypeScript, MIT): client/server design, TUI as a client — we copy the shape.
- **Crush** (Go, Charm): proves a polished Go TUI harness (Bubble Tea) with multi-provider + MCP.
- Go also has the infra libraries natively (`x/crypto/ssh`, `wireguard-go` netstack,
  Docker SDK, `client-go`, MCP Go SDK).

Why not just a plugin/MCP pack on top of opencode or Claude Code: the core guarantees
(redaction of every model input, use-vs-reveal, per-environment risk policy, audit
journal) must live **inside the loop**. A plugin can't enforce them. We can still ship an
MCP server so those harnesses can use opsdeck workspaces too.
