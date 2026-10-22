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
| `write_report` — save findings as `reports/<date>-<title>.md` (never overwrites) | read (local only, allowed in plan mode) |
| `save_artifact` — keep a remote file, a command's full output, or generated text under `artifacts/` | read for files/text; commands are classified like `exec` |
| `exec(target, command)` — host, group or list; parallel fan-out | classified per command |
| `read_file`, `list_dir` (SFTP) | read |
| `write_file`, `edit_file` — diff preview, atomic write, automatic backup | change |
| `net_check` — tcp / http (status, TLS expiry) / dns / ping, from here or from a host | read |
| `docker` — ps, images, stats, inspect, logs / start, stop, restart / rm | read / change / destructive |
| `kubernetes` — get, yaml, logs, rollout_status / restart, scale / delete_pod | read / change / destructive |
| `repo` — Git working copies: list, status, log, diff, show, branches, clone, fetch, pull / checkout, commit, pull --rebase / push / discard, force push | read / change (one approval per repository per session) / change, protected for default and protected branches / destructive |
| `repo_files` — list, read, search a repository (working copy or any ref) | read |
| `repo_write` — write, edit (exact replacement), delete files in a working copy; diff preview | change (shares the repository's approval) |
| `merge_request` — GitLab merge requests / GitHub pull requests: list, get / create, comment / merge | read / change / always asks |
| `git_sync` — clone or update a GitRemote on a host with its vault token | change |
| `topology` — get / catalog / add, update, remove nodes, zones and links, import_inventory, replace, batch | read / change, always asks (one "always" per session) |
| `load_skill(name, file?)` — a workspace or built-in skill, and its supporting files | read |
| `script` — saved scripts: list, read, save ([scripts.md](scripts.md)) | read / change (saved as a draft for review) |
| `run_script(script, target, params)` — run a saved script on hosts as one unit; secrets injected from the vault | change for approved scripts; destructive for drafts (refused on protected environments) |
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

### 6. This computer is part of the job
opsdeck runs on the engineer's own machine, so — like a coding harness — the agent can
work there too (Settings → **This computer**, on by default):

| Tool | What it does | Asks? |
|---|---|---|
| `local_read_file`, `local_list_dir`, `local_search` | read, list, grep files anywhere the user's account can (`~/.ssh/config`, `~/.ssh/known_hosts`, a project folder) | no inside the workspace folder and the allowed folders; otherwise once per folder ("always allow" covers sub-folders); credential locations (`~/.ssh`, `~/.kube`, `~/.aws`, `.env`, `*.pem`, `id_*` …) once per file |
| `local_write_file`, `local_edit_file` | create/overwrite/edit files, shown as a diff | always (also in Auto mode); "always allow" for a folder |
| `local_exec` | `sh -c` (`cmd /C` on Windows) in its own process group, empty stdin, timeout | always; read-only commands can be set to run without asking |
| `vault_import_ssh_key` | opsdeck reads a private key file and stores it encrypted; the model only gets the `vault://` ref, type, fingerprint and public key | always |
| `vault_add_item` | create an item; secret fields are generated by opsdeck (passwords, a new ed25519 key pair) or read from a local file — never passed by the model | always |
| `vault_list` | items of the workspace and global scopes, non-secret fields only | no |
| `known_hosts_import` | pin host keys from an OpenSSH `known_hosts` (plain, wildcard, negated and hashed `\|1\|` entries) for workspace hosts; a host whose pinned key differs is reported as a conflict and left unchanged | always |
| `workspace_resource` | get / apply / delete inventory resources (no literal secrets; `vault://` refs must resolve in the workspace's scopes) | like other workspace changes; delete is destructive |

Hard limits, independent of approvals:
- opsdeck's own files — `vault.db`, the state folder (login token, audit, sessions), `config.yaml`
  and every workspace's `.opsdeck/` — are refused by the file tools, symlinks included.
- Files holding a private key are never returned by `local_read_file` or `local_search`, and
  are not overwritten or edited; everything returned is redacted as usual.
- `local_exec` gets opsdeck's environment without `OPSDECK_*` variables.

So the request "look at my known_hosts and import my key into the vault" becomes:
`local_read_file ~/.ssh/known_hosts` (consent) → `vault_import_ssh_key ~/.ssh/id_ed25519`
(approval) → `known_hosts_import` (approval) → `workspace_resource apply Host … auth: vault://…`
→ `exec` on the host to verify. Local agent CLIs (Claude Code, Codex, …) get the same tools
through the MCP bridge, since their own file and shell tools stay disabled.

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

## Local agent CLIs as the model

Instead of a model API, a session can use an agent CLI installed on this machine with
the user's own login. opsdeck stays the harness around it:

```
opsdeck session ──► agent CLI (own loop, own login)
      ▲                   │ only tools: MCP server "opsdeck"
      │                   ▼
      └──── /mcp/ bridge (per-turn bearer token) ──► runTool: risk → rules → approval → run → redact → audit
```

| Agent | How it is run | Built-in tools off | Context between turns |
|---|---|---|---|
| Claude Code | `claude -p --output-format stream-json`, MCP via `--mcp-config` (http) | `--tools ""`, `--strict-mcp-config`, `--allowedTools mcp__opsdeck__*`, `--permission-mode dontAsk`, no settings/hooks/CLAUDE.md | `--session-id` / `--resume` |
| Codex (ChatGPT) | `codex exec --json`, MCP via `-c mcp_servers.opsdeck.*` (bearer from env) | `--disable shell_tool,unified_exec,…`, no web search, `--ignore-user-config`, read-only sandbox + `approval_policy=never` (blocks `apply_patch`) | `codex exec resume <thread>` |
| Gemini CLI | `gemini --acp` | private settings (`GEMINI_CLI_SYSTEM_SETTINGS_PATH`): `tools.core` = opsdeck tools only, built-ins excluded, opsdeck server trusted | ACP `session/load` |
| Any ACP agent | its ACP command (e.g. `opencode acp`, `qwen --acp`, `copilot --acp`) | client offers no fs/terminal; permission requests granted only for opsdeck tools | ACP `session/load` |

Agents that only launch stdio MCP servers get `opsdeck mcp-stdio`, a relay to the bridge
(token passed in the environment). The bridge refuses requests with an `Origin` header
(browsers) and tokens are valid only while that session's turn runs.

Logins stay inside each vendor's own CLI; opsdeck never reads or stores them. Using your
own subscription through the official, unmodified CLI on your own machine is what each
vendor's terms allow; don't share one login across other people's requests.
