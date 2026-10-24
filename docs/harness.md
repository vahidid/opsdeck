# Harness

What we keep from coding harnesses (Claude Code, opencode, Crush) and what changes
because the target is infrastructure.

## Same mechanics as coding harnesses

| Mechanic | In opsdeck |
|---|---|
| Agent loop | model → tool calls → results → model, streaming, interruptible (Esc); the reads of one step run in parallel ([below](#tool-calls-outputs-and-the-tool-set)) |
| Multi-model | provider-agnostic gateway; per-agent model choice; local models (Ollama) for sensitive infra; a workspace can limit its models ([model policy](#model-policy)) |
| Project instructions (`CLAUDE.md` / `AGENTS.md`) | the **workspace**: `OPSDECK.md` + `docs/` + resource model are the agent's context |
| Tools + MCP | built-in infra tools; external MCP servers attachable per workspace |
| Permissions & modes | allow / ask / deny rules; modes: **plan**, **ask** (default), **auto**, **full** ([below](#modes)) |
| Sessions | persisted transcripts, resume, fork (from any of your messages), export as report / runbook / skill, context compaction ([sessions](#sessions)) |
| Slash commands | `/plan` `/ask` `/auto` `/full` `/model` `/compact` `/fork` `/export` `/tasks`, plus the workspace's own prompt templates in `commands/*.md` ([sessions](#slash-commands)) |
| `!` shell | `!uptime` runs on this computer, `!@app-1 uptime` on a host; the output goes into context with your next message |
| Subagents | `delegate` to `investigator` (read-only, several in parallel), `deployer` (changes, with the usual approvals) or the workspace's `agents/*.md` ([subagents](#subagents)) |
| Skills | packaged procedures: "deploy docker app", "rotate certificate", "add WireGuard peer"; the ones matching a message are attached to it ([knowledge](#knowledge)) |
| Hooks | commands before / after a tool call and at the end of a turn: block or review calls, audit logs, chat notifications ([hooks](#hooks)) |
| Memory | per-workspace notes the agent learns ("app-1 uses podman, not docker") |
| Headless | `opsdeck run -p "..." --ws acme` for CI/cron, text or JSON output ([headless runs](#headless-runs)) |
| Background tasks | long commands and log tails that keep running after the turn and wake the session when they finish or print a matching line ([background tasks](#background-tasks)) |
| Web access | `web_search`, `web_fetch`, `software_lookup` — docs, release notes, CVEs, end-of-life dates, with SSRF guard, consent per site and taint ([web access](#web-access)) |
| Checklist and questions | `todo` keeps a live checklist above the chat; `ask_user` asks a multiple-choice question and waits |

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
| `database` — PostgreSQL / MySQL / MariaDB / MongoDB / Redis: databases, tables (collections, keys), describe / query (SQL, mongosh-style queries or redis-cli commands; SQL reads in a read-only transaction and a change as one transaction) — see [databases.md](databases.md) | read / per statement: change, destructive |
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
| `http_request` — the HTTP API of a workspace Panel, Prometheus, Loki or Elasticsearch resource (Grafana, Alertmanager, Harbor, Proxmox, an admin API…) through its VPN or jump host, logged in with its vault login or token | GET/HEAD/OPTIONS read; POST/PUT/PATCH change; DELETE destructive |
| `metrics_query` — PromQL on a workspace Prometheus (or VictoriaMetrics, Thanos, Mimir): instant or range (chart in the chat), series, labels, label values, alerts, rules, targets | read |
| `logs_query` — LogQL on Loki or a query string on Elasticsearch/OpenSearch, newest first; Loki labels | read |
| `web_search`, `web_fetch`, `software_lookup` (end-of-life dates, OSV vulnerabilities, GitHub releases) — see [Web access](#web-access) | read (a site outside the allowed list asks once) |
| `wait_for` — until a port opens, a URL answers, a command succeeds or prints a match, or a Kubernetes rollout finishes; progress in the chat | read (a command is classified like `exec`) |
| `ask_user` — a question with 2–6 options (and a free answer) that waits for the user | read |
| `todo` — the session's checklist, shown live above the chat | read |
| `delegate` — hand a task to a subagent ([Subagents](#subagents)) | read for read-only subagents; their calls are classified as usual |
| `tasks` — the session's background tasks: list, output (tail or grep), wait, stop | read; stop is a change |
| `read_output` — another part (offset/limit or grep) of a long tool output that was cut for the model | read |
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
  as canary (1 host → confirm → rest) — see [Canary rollouts](#canary-rollouts).

### 3. Secrets never reach the model
- The agent references secrets by name; the vault **uses** them (SSH auth, login,
  tokens) but never returns them to the agent.
- Every tool output passes through **redaction** before entering the context: known vault
  values, private keys, tokens and password patterns are masked.
- Policy can force a local model for a workspace (Allowed models: `ollama/*`) so nothing
  leaves the machine ([Model policy](#model-policy)).

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
Deploys, rollouts and log tails take minutes. `exec` and `local_exec` can start a command in
the background and return a handle; the session is notified when it finishes or prints a
matching line ([Background tasks](#background-tasks)). `wait_for` waits for a condition with
progress in the chat instead of a sleep loop.

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
counts (`local_exec ×3`), approvals and questions waiting, failures and the total time. A group
opens by itself while it runs or waits for the user; clicking it shows every call with its
input, output and approval. **Grouped / Detailed** in the session header switches between
grouped and all calls open (remembered in the browser). Nothing is dropped: the session keeps
every call, output and approval.

Tools attach a **view** to their result for the chat (never sent to the model): a chart of a
PromQL range with each series' last and max value, log lines coloured by level with a level
filter, search results with their sites, the page read and its size, the HTTP status and
timing of an API call, end-of-life cycles with the installed one marked, vulnerabilities with
severity and fixed version. The text the model got stays one click away ("Text sent to the
agent"). A long output that was cut for the model can be opened in full.

- **Checklist** — the `todo` list is pinned above the message box with its progress and can be
  folded.
- **Questions** — `ask_user` shows a card with the options (one or several), a free answer and
  Skip; the bar above the message box points to it.
- **Subagents** — a `delegate` call shows the subagent's own steps, tool calls and approvals
  inside the call, then its report.
- **Live progress** — long calls (`wait_for`, fan-out over hosts, background output) show what
  they are doing in their line.
- **Background tasks** — the session header shows the running tasks; each can be opened
  (command, state, output) or stopped. A finished task's notification is a message of its own.
- **Canary** — the approval card of a change over several hosts names the hosts and offers to
  run on one first; after it, a card asks to continue on the rest or stop.
- **Composer** — `/` lists the slash commands (built-in and the workspace's, with their
  argument hints); `!` switches to shell mode. The session menu (⋯) forks, compacts and
  exports the session.

## Tool calls, outputs and the tool set

- **Parallel reads.** When the model asks for several calls in one step, the reads that are
  allowed run at the same time (up to 6); changes run one after another, in order, each after
  its own approval. A read that needs consent waits for it like a change.
- **Long outputs.** The model gets up to 60,000 characters of a result. A longer one is kept in
  full with the session (`<session>.outputs/`, up to 5 MB, already redacted); the model reads
  other parts with `read_output` (offset/limit or grep) instead of running the command again,
  and the chat can show the whole output.
- **Only useful tools.** Tools that need a kind of resource are offered only when the workspace
  has one (no `database` tool without databases, no `metrics_query` without Prometheus), the
  web tools only with web access, `delegate` only when subagents are on. The model sees fewer
  tools and spends fewer tokens on them.

## Web access

`web_search` searches the public web (DuckDuckGo without a key; SearXNG, Brave or Tavily with
an instance URL or a key from the vault). `web_fetch` reads a page as Markdown (HTML is
converted, JSON and text are returned as they are; long pages come in parts; GitHub file links
are read as raw files). `software_lookup` asks endoflife.date (support and end-of-life dates),
OSV.dev (known vulnerabilities of a package version, Debian/Ubuntu/Alpine packages and the
language ecosystems) and GitHub releases.

App settings → **Web access** sets the default (off, search only, search and read pages), the
search backend, a proxy (`socks5://` or `http://`, for networks where sites are blocked) and the
sites read without asking. A workspace can choose its own level and send its web requests
through one of its VPNs or proxies (Workspace settings → Agent); a workspace that allows only
local models has web access off unless it says otherwise.

Safeguards, in every mode:

- **Only public addresses.** URLs, every redirect and every resolved IP are checked: loopback,
  private, link-local, CGNAT and cloud metadata addresses (and names like `localhost`,
  `*.internal`) are refused, so a page cannot point the agent at opsdeck itself, this computer,
  the office network or `169.254.169.254` (only the SearXNG instance you configured may be on
  the local network). Workspace panels and APIs are reached with `http_request`, which goes
  through the workspace's own paths and logins.
- **Consent per site.** In Plan and Ask mode, reading a site that is not on the allowed list
  (the built-in documentation sites — kubernetes.io, docs.docker.com, nginx.org, github.com,
  postgresql.org … — plus your own) asks once; "always" covers the site and its subdomains for
  the session.
- **Untrusted content.** Pages reach the model inside `<untrusted-web-content>` with an
  instruction never to follow what they say. After web content is read, the next change asks
  — in every mode — until the user approves something or sends a new message.
- **Nothing leaks out.** Queries and URLs are redacted like everything else; a query that names
  an address or host name of the workspace asks first. Every request is in the audit journal,
  like any other tool call.

## Subagents

`delegate` hands a self-contained task to a subagent with its own context, model and tool set,
and returns its report:

| Subagent | Tools | Use |
|---|---|---|
| `investigator` | the session's tools without the ones that change things; it runs in Plan mode, so a change is refused | diagnostics, logs, audits; several `delegate` calls in one step run **in parallel** (one per host group or symptom) |
| `deployer` | the session's tools; changes ask like in the session | carrying out a planned change step by step |
| `agents/<name>.md` | as the file says (`mode: read-only` works like the investigator) | the workspace's own: front matter `description`, `mode: read-only` or `changes`, optional `model` and `tools`; the body is its instructions |

The subagent does not see the conversation, only the task (and `context`) the main agent gives
it; it cannot delegate further. Its events are part of the session (with `parent` = the
delegate call): the chat shows its steps inside the call, its approvals appear there and in the
bar, and the audit journal records its calls with the subagent's name. App settings → Agent
harness turns subagents off or gives them another model (e.g. a cheaper, faster one for
investigators); local agent CLIs cannot run subagents, so then the first API model is used.

## Hooks

Hooks are commands on this computer that run around the agent's work, with the event as JSON on
stdin (`event`, `workspace`, `session`, `mode`, `model`, `subagent`, `tool`, `callId`, `input`,
`risk`, `target`, `environment`, and for later events `decision`, `output`, `isError`,
`durationMs`, `message`):

| Event | When | Can |
|---|---|---|
| `before_tool` | after the permission decision, before the call runs | exit 2 or print `{"decision":"deny","reason":"…"}` to block the call; `{"decision":"ask"}` makes it ask (in every mode); `{"decision":"allow"}` lets a call that would ask run (App settings hooks only) |
| `after_tool` | after the call, with its result | logging, notifications; does not hold up the agent |
| `turn_end` | the agent finished or stopped a turn, with its last message | e.g. post it to a chat channel |

A hook matches tools by name patterns (`exec, local_*`; empty = any), a risk and environments;
an App settings hook can be limited to one workspace. It runs in the workspace folder with
`OPSDECK_HOOK_EVENT`, `OPSDECK_WORKSPACE`, `OPSDECK_SESSION` and `OPSDECK_TOOL` set, and times
out after 30 seconds (`timeoutSec`); a `before_tool` hook that fails or times out does not
block. What a hook did is shown on the call in the chat and recorded in the audit journal.

```yaml
# workspace.yaml
hooks:
  - name: change freeze
    event: before_tool
    risk: change
    environment: [prod]
    command: test "$(date +%u)" -lt 5 || { echo "change freeze on weekends" >&2; exit 2; }
```

Workspace hooks (in `workspace.yaml`) travel with the workspace, so — like MCP servers — hooks
that came with the folder (a clone, a pull, an edit on disk) run only after the user trusts them
on this machine (Workspace settings → Agent); the ones added in the UI are trusted. A workspace
hook can block or ask, never allow.

## Background tasks

`exec` and `local_exec` with `background: true` start the command, return a task id at once
and keep it running after the turn (a deploy, a migration, a log tail). With `notify_on` (a
regular expression), the session is told when the output prints a matching line; it is always
told when the task ends, with its exit code and the last lines. A notification arrives as a
message of its own: an idle session starts a turn by itself so the agent can react, a busy one
gets it when its turn ends. The `tasks` tool lists the tasks, reads their output (tail or grep),
waits for one or stops it; the session header has the same list. A task keeps the latest
megabyte of its output; tasks stop when opsdeck stops.

## Canary rollouts

A change over several hosts (`exec` or `run_script` on a group or a list) asks once with the
whole list. The approval card can run it on **one host first**; when the agent asked for a
canary (`canary: true`) that is the default button. After the first host, a second card shows
its result and asks to **continue on the rest** or **stop here**; stopping returns what ran to
the agent. Approving the whole list at once still works.

## Sessions

- **Fork** — copy a session into a new one: all of it (⋯ → Fork, `/fork`), or from one of your
  messages (its fork button), which puts that message back into the composer to edit and send
  again. Approvals given in the old session are not copied.
- **Export** — save a session to the workspace as a report (the transcript, `reports/`), a
  runbook (`docs/runbooks/`) or a skill (`skills/<name>/SKILL.md`); runbooks and skills are
  written by the model from the transcript, so they need an API model.
- **Compaction** — `/compact` (or ⋯ → Compact) summarizes the conversation so far to free
  context; it also happens by itself near the model's limit.

### Slash commands

The built-in commands switch the mode (`/plan`, `/ask`, `/auto`, `/full`) or the model
(`/model provider/model`), and run the session actions (`/compact`, `/fork`,
`/export report|runbook|skill`, `/tasks`). A workspace adds its own as prompt templates:

```markdown
<!-- commands/health.md -->
---
description: Health check of a host
argument-hint: <host>
---
Check the health of $ARGUMENTS: load, memory, disk, failed units, and the last errors in the journal.
```

`/health app-1` sends the template with `$ARGUMENTS` (and `$1`…`$9`) replaced; the chat shows
`/health app-1` and the agent gets the prompt.

## Headless runs

```
opsdeck run -p "Check disk on all prod hosts and report anything above 80%" --ws acme \
            [--mode plan|ask|auto|full] [--model provider/model] [--session <id>] \
            [--output text|json|stream-json] [--timeout 30m] [--wait-tasks=true]
```

`opsdeck run` talks to the running opsdeck (`--port`, default 7788) with its login token. It
creates a headless session (or continues `--session`), sends the prompt (`-p -` reads stdin)
and prints the agent's answer — as text, as one JSON object at the end (`session`,
`workspace`, `result`, `toolCalls`, `denied`, `errors`, `usage`, `durationMs`) or as every event
(`stream-json`). Nobody is there to approve, so whatever would ask is **denied** and the agent
is told why; allow what the job needs with workspace permission rules or the mode. With
`--wait-tasks` the run lasts while the session's background tasks run and their notifications
are handled. Exit codes: 0 done, 1 the run ended with an error, 2 bad usage, 3 an action was
denied.

## Model policy

Workspace settings → Agent → **Allowed models** limits the models a workspace's sessions may
use (patterns like `ollama/*` or `anthropic/claude-*`): a new session starts with an allowed
model, other models are refused (choosing one, or sending with one), and subagents follow the
same list. When only local
models are allowed (Ollama, LM Studio, a local OpenAI-compatible server), nothing leaves this
computer, and web access defaults to off.

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
