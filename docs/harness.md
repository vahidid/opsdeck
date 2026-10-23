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
| Permissions & modes | allow / ask / deny rules; modes: **plan**, **ask** (default), **auto**, **full** ([below](#modes)) |
| Sessions | persisted transcripts, resume, fork, context compaction |
| Slash commands | `/ssh app-1`, `/vault`, `/ws use acme`, `/logs`, user-defined commands |
| `!` shell | run a command yourself; output goes into context |
| Subagents | e.g. `investigator` (read-only, cheap model), `deployer` (write, needs approval) |
| Skills | packaged procedures: "deploy docker app", "rotate certificate", "add WireGuard peer"; the ones matching a message are attached to it ([knowledge](#knowledge)) |
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
| `read_doc`, `load_skill`, `knowledge` (search skills, docs, reports, memory, scripts) | read |
| `memory` — read / append / replace the workspace's notes | read / change (runs without asking in Auto and Full) |
| `write_doc` — record runbooks/notes in the workspace | change |
| `write_report` — save findings as `reports/<date>-<title>.md` (never overwrites) | read (local only, allowed in plan mode) |
| `save_artifact` — keep a remote file, a command's full output, or generated text under `artifacts/` | read for files/text; commands are classified like `exec` |
| `exec(target, command)` — host, group or list; parallel fan-out | classified per command |
| `read_file`, `list_dir` (SFTP) | read |
| `write_file`, `edit_file` — diff preview, atomic write, automatic backup | change |
| `net_check` — tcp / http (status, TLS expiry) / dns / ping, from here or from a host | read |
| `docker` — containers (ps, inspect, logs, top, stats / start, stop, restart, pause, kill / rm), compose (ls, ps, logs, files / up, pull, restart, validated file writes / down), swarm (services, tasks, logs / scale, redeploy, rollback, image / rm; stacks, nodes), volumes (files / write / delete, rm), images, networks, df, prune — see [docker.md](docker.md) | read / change / destructive |
| `kubernetes` — any resource type incl. CRDs: get, describe, yaml, logs (a pod or a workload's pods), events, top, overview, history, helm / apply (dry-run diff on the approval card), restart, scale, set_image, pause, resume, undo, suspend, trigger, cordon / exec / delete, drain — see [kubernetes.md](kubernetes.md) | read / change / per command / destructive |
| `repo` — Git working copies: list, status, log, diff, show, branches, clone, fetch, pull / checkout, commit, pull --rebase / push / discard, force push | read / change (one approval per repository per session) / change, protected for default and protected branches / destructive |
| `repo_files` — list, read, search a repository (working copy or any ref) | read |
| `repo_write` — write, edit (exact replacement), delete files in a working copy; diff preview | change (shares the repository's approval) |
| `merge_request` — GitLab merge requests / GitHub pull requests: list, get / create, comment / merge | read / change / always asks |
| `git_sync` — clone or update a GitRemote on a host with its vault token | change |
| `topology` — get / catalog / add, update, remove nodes, zones and links, import_inventory, replace, batch | read / change, always asks (one "always" per session) |
| `load_skill(name, file?)` — a workspace or built-in skill, and its supporting files | read |
| `script` — saved scripts: list, read, save ([scripts.md](scripts.md)) | read / change (saved as a draft for review) |
| `run_script(script, target, params)` — run a saved script on hosts as one unit; secrets injected from the vault | change for approved scripts; destructive for drafts (refused on protected environments) |
| `ansible` — Ansible projects: projects, files, read, search, inventory, syntax, list_hosts/tasks/tags, runs, log / write, edit, delete ([ansible.md](ansible.md)) | read / change (one approval per project; a repository project shares the repository's) |
| `ansible_run` — ansible-playbook, ad-hoc modules, galaxy, lint on this computer, with the log on the Ansible page; `inventory=workspace` reaches the workspace's hosts through their VPNs and jump hosts | change; check runs and read-only ad-hoc modules are reads that ask once; protected environments always ask |
| `ansible_vault` — view, edit, set (generate), encrypt, decrypt, encrypt_string | view always asks (decrypted content reaches the model); changes like files; decrypt is destructive. A missing vault password is typed into the approval card |
| `mcp__<server>__<tool>` — workspace MCP servers | read if the tool is annotated read-only, else change |

### 2. No undo
Code has git; production does not. So safety comes before execution, not after:

- **Risk classes** on every call: `read` → `change` → `destructive`.
  `exec` commands are classified by a parser (`systemctl status` = read,
  `systemctl restart` = change, `rm -rf`, `DROP`, `kubectl delete` = destructive).
  Unknown = treated as change. See [How commands are classified](#how-commands-are-classified).
- **Environment-aware policy**: the same call can be auto-allowed on `dev` and require
  approval on `prod` (environments come from the workspace).
- **Modes** decide what runs without asking (see [Modes](#modes)); changes on servers ask
  in every mode unless the user turns that off.
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

### Passwords on the approval card
Some actions need a secret only the user has — an Ansible Vault password. The tool marks
its call with a password prompt: the approval card then has a password field (in every mode,
also Auto), opsdeck checks the password before the card closes, and hands it to that one
call — or keeps it in memory until opsdeck locks when the user picks "for session". The
password never enters the transcript, the audit journal or the model's context; the audit
records that one was entered. See [ansible.md](ansible.md#agent-tools).

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
| `local_write_file`, `local_edit_file` | create/overwrite/edit files, shown as a diff | in Ask mode; "always allow" for a folder. Not in Auto and Full |
| `local_exec` | `sh -c` (`cmd /C` on Windows) in its own process group, empty stdin, timeout | in Ask mode (read-only commands can be set to run without asking); in Auto only destructive commands; never in Full |
| `vault_import_ssh_key` | opsdeck reads a private key file and stores it encrypted; the model only gets the `vault://` ref, type, fingerprint and public key | in every mode but Full |
| `vault_add_item` | create an item; secret fields are generated by opsdeck (passwords, a new ed25519 key pair) or read from a local file — never passed by the model | in every mode but Full |
| `vault_list` | items of the workspace and global scopes, non-secret fields only | no |
| `known_hosts_import` | pin host keys from an OpenSSH `known_hosts` (plain, wildcard, negated and hashed `\|1\|` entries) for workspace hosts; a host whose pinned key differs is reported as a conflict and left unchanged | in every mode but Full |
| `workspace_resource` | get / apply / delete inventory resources (no literal secrets; `vault://` refs must resolve in the workspace's scopes) | like other workspace changes; delete is destructive |

Credential locations ask once per file in Ask and Auto mode (not in Full).

Hard limits, independent of approvals and modes:
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

## Modes

The mode is chosen per session (the picker under the message box, or `/plan`, `/ask`,
`/auto`, `/full`) and can change while the agent works. "This computer" is everything local:
files anywhere the user's account can reach, local commands, the workspace, Git working
copies before a push, the vault, the topology. "Servers" are the hosts, clusters, Docker
hosts, remote repositories (push, merge requests) and MCP servers.

| | Plan | Ask | Auto | Full |
|---|---|---|---|---|
| Reads (hosts, files, docs, logs) | run | run | run | run |
| Reads that need consent: a folder outside the workspace | ask | ask | run | run |
| Credential files, decrypted Ansible Vault content | ask | ask | ask | run |
| Changes on this computer: files anywhere, local commands, workspace, working copies | — | ask | run | run |
| Destructive commands on this computer | — | ask | ask | run |
| Vault items, key and known_hosts import, topology | — | ask | ask | run |
| Changes on servers | — | ask | ask ¹ | ask ¹ |

¹ **App settings → Agent → "Ask before every change on a server, in every mode"** is on by
default. Turned off, Full runs server changes too, and Auto runs them except destructive ones
and changes in protected environments.

In every mode: workspace permission rules come first (a `deny` holds in Full), a credential
marked *confirm on use* asks, a password the agent needs (Ansible Vault) is typed into its
approval card, and opsdeck's own files stay out of reach.

## Knowledge

Skills, runbooks and notes only help when the agent uses them, and a list of names in the
prompt is easy to skip. So opsdeck looks them up itself, before the model starts:

- A local **search index** (BM25 over names, descriptions, headings and text, rebuilt when a
  file changes) covers the workspace's skills, the built-in library (by name, description and
  headings), `docs/**` and repository docs, `reports/`, memory notes and saved scripts. A
  small vocabulary maps Persian operations words (سرور، بکاپ، گواهی، دیپلوی …) to English, so
  a Persian request finds English runbooks.
- **Every user message** is searched. What matches well is attached to the message (for the
  model) as an `<opsdeck-context>` block — each hit with a snippet and how to open it
  (`load_skill`, `read_doc`, `run_script`) — and shown in the chat as a **Knowledge** row under
  the message, each item linking to its page. A workspace skill that clearly fits best is
  included in full (no extra round trip), once per conversation. Short replies ("ok", "بله")
  match nothing. The system prompt itself stays the same, so prompt caching keeps working.
- The **`knowledge` tool** searches the same index on demand (optionally one kind) instead of
  listing and opening docs one by one.
- The system prompt lists workspace skills with their descriptions and the built-in library in
  one line each, asks the agent to follow the team's own skills, runbooks and scripts before its
  general knowledge, and to save durable facts to **memory** as soon as it learns them (one fact
  per note; corrected with `replace` when wrong) — memory changes run without asking in Auto
  and Full mode.

## The chat

Consecutive tool calls are grouped into one line — the number of calls, the tools with their
counts (`local_exec ×3`), approvals waiting, failures and the total time. A group opens by
itself while it runs or waits for an approval; clicking it shows every call with its input,
output and approval as before. **Grouped / Detailed** in the session header switches between
grouped and all calls open (remembered in the browser). Nothing is dropped: the session keeps
every call, output and approval.

## How commands are classified

A shell command from `exec`, `local_exec` or `save_artifact` is parsed (mvdan/sh), not matched
as text. Like Codex's safe-command check and Claude Code's per-subcommand rules, every simple
command in it is classified on its own (pipelines, `&&`, `;`, loops, subshells, `$(…)`,
`bash -c '…'` and here-docs fed to a shell), and the script gets the highest risk. A script runs
without asking only when **every** command in it is a read:

- **Reads are an allowlist.** About 300 commands are known, with their read-only forms: `cat`,
  `grep`, `journalctl`, `ss`, `systemctl status`, `docker ps`/`logs`/`inspect`, `kubectl get`,
  `dpkg -l`, `zpool status`, `pvs`, `smartctl -a`, `firewall-cmd --list-all`, `certbot certificates`,
  `openssl x509 -noout`, `gcloud … list`, `psql -c 'select …'` and so on. Anything not
  recognised is a change.
- **Shell builtins** that only affect the script (`set -euo pipefail`, `read`, `:`, `exit`,
  `local`) are reads. `source` reads only `/etc/os-release` and `/etc/lsb-release`.
- **Commands inside containers** are classified by what they run: `docker exec web cat …`,
  `docker compose exec db pg_isready` and `kubectl exec pod -- env` are reads; `docker exec web
  rm …` is not. `find -exec` and `xargs` likewise.
- **Programs passed as arguments** are inspected: `awk '$3 > 80 {print $1}'` is a read, an awk
  program that prints into a file or a pipe or calls `system()` is a change; `sed` without `-i`,
  `w` and `e`; SQL without writes.
- **Values known only when the script runs.** A variable that the script sets only to literal
  values (`for f in /etc/nginx/*.conf`, `dir=/var/log`) is classified with those values. Any
  other expansion (`$1`, `read x`, `$(…)`) is tried with hostile values (`rm`, `-delete`,
  `-o /tmp/x`, `rm -rf /`, a sed or awk program that writes): the command is a read only if it
  stays one whatever the value. A command *name* that is not known statically is destructive.
- **Environment.** Setting `PATH`, `LD_*`, `GIT_*`, `HOME`, `BASH_ENV` and similar for later
  commands (`export PATH=…`) makes the script a change; a prefix like `LANG=C` or `KUBECONFIG=…`
  on a read stays a read.
- Writes through redirection (`> file`), `tee`, `curl -o file`, `sort -o` are changes;
  `> /dev/null` is not.

This is a safety net for approvals, not a sandbox: the approval card shows the reasons for the
class (e.g. `rm -r/-f deletes files`, `depends on values only known when it runs`).

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

For shell commands, `match` is a pattern (`*` = any text) applied to each command of the script,
with and without wrappers such as `sudo`. An **allow** rule covers a script only when every
command in it that is not a read matches: `allow: { tool: exec, match: "systemctl restart nginx*" }`
allows `sudo systemctl restart nginx && systemctl status nginx`, but not
`systemctl restart nginx; rm -rf /tmp/x`. A write through a redirection (`… > /etc/x`), a
variable such as `PATH` set for later commands and a destructive SQL statement count as commands
of their own, so a command pattern never covers them. **ask** and **deny** rules apply when any
command (or the script as a whole) matches. A script that cannot be parsed is never covered by an
allow rule.

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

**Models.** Settings asks each CLI which models your login can use, without sending a
prompt: Claude Code through the stream-json control protocol (`initialize` lists its
models), Codex through `codex app-server` (`model/list`), Gemini CLI and other ACP agents
from a new ACP session's model choices (the `model` config option, or the older `models`
field). A chosen model goes on the command line (`--model`) for Claude Code, Codex and
Gemini CLI; other ACP agents are switched with `session/set_config_option` (or
`session/set_model`) before the prompt. `default` leaves the agent's own choice.

Agents that only launch stdio MCP servers get `opsdeck mcp-stdio`, a relay to the bridge
(token passed in the environment). The bridge refuses requests with an `Origin` header
(browsers) and tokens are valid only while that session's turn runs.

Logins stay inside each vendor's own CLI; opsdeck never reads or stores them. Using your
own subscription through the official, unmodified CLI on your own machine is what each
vendor's terms allow; don't share one login across other people's requests.
