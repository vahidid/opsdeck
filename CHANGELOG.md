# Changelog

All notable changes to opsdeck. Versions follow [semantic versioning](https://semver.org);
each release's section becomes its notes on GitHub.

## [Unreleased]

### Other agents use opsdeck

- **opsdeck is an MCP server for the agent you already use.** Claude Code, Codex, Cursor,
  Gemini CLI, VS Code or any MCP client gets a workspace's tools at
  `http://127.0.0.1:7788/mcp/ext` (or over stdio with `opsdeck mcp`, which finds the
  running app). *App settings › Agent access* makes a key per agent and workspace
  (read-only, ask or auto) and shows the setup for that agent. Every call goes through
  the same risk checks, permission rules, approvals, redaction and audit as the built-in
  agent's, in a session of its own on the Agent page; a change waits there for your
  approval. Keys are stored hashed and can be revoked at once. See
  [docs/agents.md](docs/agents.md).
- **The opsdeck skill** teaches such an agent to work through opsdeck: look first, prefer
  the specific tools, expect approvals, leave a report. For Claude Code it comes as a
  plugin (`/plugin marketplace add vahidid/opsdeck`, `/plugin install opsdeck@opsdeck`)
  that also connects the MCP server; `opsdeck skill install claude-code|codex|gemini|cursor`
  writes it for other agents.

### Stability and security

- **Stable file formats.** Workspace folders now say `apiVersion: opsdeck/v1`; an opsdeck
  refuses a folder or vault from a newer version instead of misreading it, and upgrades
  older ones on save. [docs/schema.md](docs/schema.md) documents every resource kind,
  [docs/compatibility.md](docs/compatibility.md) what stays compatible and how anything is
  deprecated.
- **Signed releases.** `SHA256SUMS.txt` is signed with the opsdeck release key
  (minisign). The updater installs nothing that is not signed by that key for that
  version; `install.sh` checks the signature too when `minisign` is installed. With
  certificates configured, the macOS app is notarized and the Windows `.exe` files are
  Authenticode-signed. See [docs/releasing.md](docs/releasing.md#signatures).
- A wrong master password now says so instead of sending you to the "open the link from
  your terminal" screen.
- Dialogs and drawers are announced as dialogs to screen readers.

### Testing

- **Browser tests** (Playwright, `e2e/`) drive the real UI against a real opsdeck with
  SSH hosts and a scripted model, in CI: unlocking, adding a resource, search,
  customizing, the phone layout, an agent change that waits for approval, connecting an
  outside agent. The README screenshots are made the same way (`e2e/screenshots`).

### Interface

- **New layout** — a rail of icons for the five groups (Workspace, Infrastructure, Deploy,
  Library, Security) opens their sections in a panel beside it, so the menu shows a handful
  of entries instead of every section at once; sections can be pinned to the top. The top
  bar holds the tabs, search (Ctrl/⌘+K, now with actions such as *Add resource*) and the
  agent (Ctrl/⌘+J). Each pane scrolls on its own and the window never does.
- **The agent has its own colour** — violet marks everything that hands work to the agent:
  the rail and top-bar buttons, *Ask the agent* on the overview (with a draft box), the
  composer and *Ask the agent* / *Explain errors* on Docker, Logs, VPN, Kubernetes,
  Databases, Domains, Certificates, Deployments, Ansible and the resource editor.
- **Add resource by product** — the picker lists the products you know (Cisco AnyConnect,
  GlobalProtect, FortiGate, PostgreSQL, Redis, Grafana, GitLab, Argo CD…) with their logos
  and colours, searchable and by category, and opens the form already set up for that
  product (an OpenConnect resource with `protocol: gp` for GlobalProtect). The editor is a
  wider dialog with a live YAML preview; resources show their product's logo in the list.
- **Customize** — theme (dark, light, system), accent (the workspace colour or a fixed one),
  density, interface size and corners, the menu layout (icons + panel or icons only), the
  pinned sections and which sections the menu shows; saved in the browser.
- A modern Overview (health, the non-empty resource kinds, topology, networks, activity),
  Inter and JetBrains Mono bundled, and narrower windows stack list/detail pages.

### Workbench

- **Deployments** — a workspace's **Portainer**, **Argo CD** and **Komodo** become resources
  of the workspace (`DeployPlatform`: URL, a token in the workspace's vault, reached through
  its VPN or jump host like any resource; `targets` limits a shared platform to the
  workspace's own stacks or applications). A Deployments page lists what each deploys —
  Portainer stacks, Argo CD applications, Komodo stacks and deployments — with health, sync
  status, services, history, logs and what a deploy would change (Argo CD's live vs Git,
  Komodo's deployed vs latest files), and runs deploy / sync, stack file updates, rollback,
  refresh, restart, start, stop and pull, each confirmed (the name typed on a protected
  environment or with prune) and recorded in the activity journal.

### Agent

- **`deploy` tool** — reads the workspace's deployment platforms (platforms, targets, status,
  logs, diff, file) and deploys, rolls back, restarts or updates a stack file after approval,
  with the target's state and what changes on the card; a sync with prune is destructive.

## [0.3.0] — 2026-10-25

The agent works like a coding harness — web access, subagents, hooks, background tasks,
canary rollouts and headless runs — and the workbench manages domains and DNS at
Cloudflare and ArvanCloud and TLS certificates from Let's Encrypt; FortiGate and AnyConnect
VPNs that ask for one-time codes or a group now connect.

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
- **`dns` tool** — the agent reads and changes the DNS records of the workspace's domains
  through the connected Cloudflare or ArvanCloud account (it never sees the token): `zones`,
  `records`, `set` (create or change one record; the approval card shows the zone-file line
  before and after, values left out are kept) and `delete` (destructive; needs an unambiguous
  record). Offered only when the workspace has a `Domain`; results come with a records table.
- **`certificate` tool** — the agent lists the workspace's certificates (issued, expiry,
  due), shows one, issues or renews it (the ACME flow with its log as the result) and deploys
  chain and key to a host from the vault without seeing the key, then reloads with `exec`.
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

- **Domains and DNS** — **Connectors → DNS & CDN** connects a **Cloudflare** or **ArvanCloud**
  account with an API token (checked against the provider, stored in the vault; an API URL
  and a proxy under *Advanced* for networks where the provider is not reachable directly).
  A new **Domains** page per workspace: add a zone by picking it from the account (a `Domain`
  resource that holds only the zone and, optionally, the connector), see its status, plan,
  name servers (and whether the registrar still points elsewhere), and its records in one
  shape whatever the provider — type, name relative to the zone, content, TTL, priority,
  proxied through the CDN, comment — with filters, an editor with hints per type, and
  deletion that asks (the full name must be typed on a protected environment). ArvanCloud's
  balanced A records and lowercase types, Cloudflare's SRV/CAA data and automatic TTL are
  handled. Every record change is in the activity journal — see [docs/dns.md](docs/dns.md).
- **TLS certificates** — a new **Certificates** page and `Certificate` resource: domains
  (wildcards included), the CA (Let's Encrypt, its staging, Buypass, or any ACME directory),
  contact email, key type, when to renew. *Issue* runs the dns-01 challenge through the DNS
  connectors (TXT records created, checked on the zone's name servers, removed afterwards)
  with a live log, and stores the chain and key in the vault (`vault://<ws>/cert-<name>`,
  with readable expiry and fingerprint fields). The page shows what is valid, due or expired
  and why; *Deploy* writes the files to a host over SFTP (key 0600; nginx, Apache and HAProxy
  layouts) and can reload the service; *Renew automatically* renews due certificates while
  opsdeck runs unlocked. All audited — see [docs/certificates.md](docs/certificates.md).
- **One-time VPN codes by SMS or email** — FortiGate VPNs (OpenConnect or openfortivpn)
  that send a code for every login now connect: opsdeck logs in itself and, when the gateway
  asks for the code, shows a prompt on any page (and on the VPN page) with the gateway's
  message; Persian digits are fine. A TOTP secret on the login answers it by itself, and so
  can your phone: forward the gateway's SMS to an ntfy topic (App settings → One-time VPN
  codes, with a test) or let a local command print the code. A tunnel that drops connects
  again with the same session, without a new code, while the gateway keeps it. The agent
  asks you for the code instead of failing. Untrusted FortiGate certificates now show the
  pin to set before anything else runs.
- **AnyConnect groups and second factors** — Cisco ASA/FTD gateways (and the other
  openconnect gateways) that ask for a GROUP or a code after the password now connect,
  also those with the old `/+webvpn+/` login: opsdeck answers openconnect's questions itself.
  The group comes from the profile, or you pick it from the gateway's list in the same prompt
  as codes; a second password or challenge (*Answer:*, *Response:*) gets the login's TOTP
  code, or the code you enter or forward from your phone, and is asked for again when wrong.
  The TOTP secret is no longer on openconnect's command line (nor in the VPN log).

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
