# Changelog

All notable changes to opsdeck. Versions follow [semantic versioning](https://semver.org);
each release's section becomes its notes on GitHub.

## [Unreleased]

### Agent

- **Web access** — `web_search` (DuckDuckGo without a key, or SearXNG, Brave, Tavily),
  `web_fetch` (pages as Markdown, in parts; GitHub files as raw) and `software_lookup`
  (end-of-life dates from endoflife.date, known vulnerabilities from OSV.dev, GitHub releases).
  Only public addresses are reached — every redirect and resolved IP is checked, so a page
  cannot point the agent at opsdeck, this computer, the local network or cloud metadata.
  Sites outside the allowed list (documentation sites built in, plus your own) ask once;
  pages are marked untrusted and the next change after web content asks, in every mode; a
  query that names a workspace address asks first. App settings → Web access sets the
  default, the search backend and a proxy; a workspace can lower it or route the web through
  one of its VPNs, and a workspace that allows only local models has it off.
- **Workspace APIs, metrics and logs** — `http_request` calls the API of a panel (Grafana,
  Alertmanager, Harbor, Proxmox, an admin API…) through its VPN or jump host with its vault
  login or token; writes ask like other changes. New resource kinds **Prometheus**, **Loki**
  and **Elasticsearch/OpenSearch** with `metrics_query` (PromQL, ranges drawn as a chart,
  alerts, targets) and `logs_query` (LogQL or a query string, lines coloured by level).
- **Subagents** — `delegate` hands work to an `investigator` (read-only; several run in
  parallel), a `deployer` (changes with the usual approvals) or the workspace's own
  `agents/*.md`, each with its own context and optionally a cheaper model. Their steps,
  calls and approvals show inside the call.
- **Hooks** — commands before and after tool calls and at the end of a turn, with the event
  as JSON on stdin: block a call (exit 2), make it ask, keep an audit log, post to a chat.
  In App settings and per workspace; workspace hooks run only after you trust them here.
- **Background tasks** — `exec` and `local_exec` can run a command in the background; the
  session is told when it ends or prints a matching line, and an idle session picks it up by
  itself. `tasks` lists, reads, waits for and stops them; so does the session header.
- **Canary rollouts** — a change over several hosts can run on one host first; a second card
  then asks to continue on the rest or stop.
- **Waiting, asking, planning** — `wait_for` (port, URL, command output, Kubernetes rollout)
  instead of sleep loops; `ask_user` asks a multiple-choice question in the chat; `todo` keeps
  a live checklist above the message box.
- **Sessions** — fork a session (or from one of your messages, to edit and resend it), export
  it as a report, runbook or skill, `!command` (or `!@host command`) to run something
  yourself, and slash commands: the built-in ones and the workspace's own prompt templates in
  `commands/*.md`.
- **Headless runs** — `opsdeck run -p "…" --ws acme` for cron and CI, with text, JSON or
  streamed JSON output; anything that would ask is denied.
- **Model policy** — a workspace can limit its sessions to some models (e.g. `ollama/*`).
- **Faster, leaner loop** — the reads of one step run in parallel; a long output is kept in
  full and read in parts with `read_output` instead of being cut; tools are offered only
  when the workspace has what they work on.
- **The chat** — tool results come with views: charts, log lines, search results, the page
  read, HTTP status, end-of-life tables and vulnerabilities, with the text the agent got one
  click away.

### Workbench

- **One-time VPN codes by SMS or email** — FortiGate VPNs (OpenConnect or openfortivpn)
  that send a code for every login now connect: opsdeck logs in itself and, when the gateway
  asks for the code, shows a prompt on any page (and on the VPN page) with the gateway's
  message; Persian digits are fine. A TOTP secret on the login answers it by itself, and so
  can your phone: forward the gateway's SMS to an ntfy topic (App settings → One-time VPN
  codes, with a test) or let a local command print the code. A tunnel that drops connects
  again with the same session, without a new code, while the gateway keeps it. The agent
  asks you for the code instead of failing. Untrusted FortiGate certificates now show the
  pin to set before anything else runs.

## [0.2.0] — 2026-10-24

Databases in the workbench and for the agent, workspaces you can move to another machine
as one encrypted file, and far fewer approval prompts for read-only commands.

### Workbench

- **Databases** — a new page for the PostgreSQL, MySQL, MariaDB, MongoDB and Redis databases
  of a workspace, reached through its VPNs and jump hosts or an SSH tunnel to the database's
  host, with the login from the vault: the databases on the server, tables and views,
  collections or keys with estimated sizes, their structure (columns and indexes, fields
  inferred from sampled documents, a key's type, TTL and memory), and an editor for SQL,
  mongosh-style queries or redis-cli commands (highlighting, completion, Ctrl/⌘-Enter,
  history). Reads cannot change data (a read-only transaction for SQL; only read operations
  and commands for MongoDB and Redis); a script that is not a read is shown with its risk for
  confirmation (destructive ones on a protected environment ask for the database's name),
  runs as one transaction where the engine has one, and is recorded in the activity journal.
- **Results grid** — virtualized rows, resizable and sortable columns, a row filter, wrapped
  or single-line cells and a JSON view. Double-click a cell (or press Enter) for the whole
  value — up to 64K characters, JSON indented, with search — or open the whole row; copy as
  tab-separated, CSV or JSON, or download a CSV or JSON file.
- **Export and import a workspace** — workspace settings export the workspace (hosts and
  resources, docs, skills, scripts, Ansible projects) and the items of its vault scope as one
  file encrypted with a password you choose; *New / import workspace → Import export file*
  opens it on another machine and stores the secrets in that vault under its own master
  password. Agent sessions, reports, artifacts and Git working copies stay behind.

### Agent

- **`database` tool** — the agent queries workspace databases with the vault login instead
  of running psql, mysql, mongosh or redis-cli with passwords on a host: databases, tables
  (collections, keys), describe, query. Reads need no approval; changes go to the approval
  card with their script.
- **Fewer approvals for reads on servers.** The command classifier knows many more read-only
  commands and forms (shell builtins such as `set -e` and `read`, `awk` conditions, `curl -o
  /dev/null`, `dpkg -l`, storage, firewall and certificate status tools, cloud CLIs' `list` and
  `describe`), classifies `docker exec`, `docker compose exec`, `kubectl exec` and `find -exec`
  by the command they run, and evaluates loop variables with their literal values. Unknown
  values are tried with hostile ones, so a read stays a read only if it is one whatever they
  are. In a set of 80 everyday read commands, 47 used to ask for approval; none do now.
- **Permission rules match each command of a script.** An `allow` rule for `exec` covers a
  script only when every command in it that is not a read matches, so `systemctl status*` no
  longer allows `systemctl status x; rm -rf /`. `ask` and `deny` rules apply when any command
  matches.
- **Model lists from the CLI** — Settings asks each local agent which models your login can
  use (Claude Code, Codex, Gemini CLI, opencode and other ACP agents) and shows them with
  their names and descriptions to tick; API providers list their models from the API
  (Anthropic, OpenAI, OpenRouter, Ollama, LM Studio…). The model picker shows these names,
  grouped by provider.
- ACP agents (opencode, Qwen Code, Copilot CLI…) now run with the chosen model; before,
  only Gemini CLI honoured it.

## [0.1.0] — 2026-10-23

The first release: a local workbench and AI agent harness for infrastructure, as a
desktop app for Windows, macOS and Linux and as a CLI that serves the same UI to your
browser.

### Workbench

- **Workspaces** per project — hosts, groups, VPN profiles, jump hosts, web panels, Git
  repositories, databases, Kubernetes clusters, docs and runbooks as plain YAML and
  Markdown, with no secrets in them.
- **Vault** — local and encrypted (Argon2id → XChaCha20-Poly1305): SSH keys, logins with
  TOTP, API tokens, kubeconfigs, VPN configs, certificates.
- **VPNs** — WireGuard, Tailscale/Headscale, NetBird, OpenVPN, AnyConnect/ocserv,
  FortiGate, GlobalProtect and more, in userspace or system mode, with single sign-on.
- **SSH** terminals, remote files, host stats, live logs and port forwards — through VPNs
  and jump hosts automatically.
- **Docker** (containers, Compose, Swarm, volumes), **Kubernetes** (every resource type,
  logs, exec, apply with diff, rollouts, Helm) and **Ansible** (editor with visual views,
  Ansible Vault, runs with live logs).
- **Git repositories** with GitHub/GitLab connectors and merge requests, a **topology**
  map, a **script** library and 20 built-in **skills**.

### Agent

- Works with Anthropic, OpenAI-compatible APIs, OpenRouter, Ollama and LM Studio, or a
  local agent CLI you are signed in to (Claude Code, Codex, Gemini CLI, ACP agents).
- Every tool call classified and approved by mode (Plan / Ask / Auto / Full), secrets
  redacted from everything the model sees, a hash-chained audit journal.

### App

- Desktop app with installers for each system; the sidebar groups sections and opens
  each in its own tab.
- Updates from inside the app: it checks GitHub releases, verifies the download against
  the release checksums, installs it and restarts.
