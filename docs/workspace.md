# Workspace

A workspace represents one project/customer/environment set. It is the unit of
isolation: it has its own vault scope, SSH agent, known_hosts, networks, history and
documentation.

## Layout on disk

A workspace is one folder that holds everything about the project — what you
write and what the agent produces:

```
acme/
├── workspace.yaml        # identity, environments, defaults, permissions, MCP servers, hooks
├── resources/            # inventory: hosts, networks, panels, clusters… (YAML)
├── OPSDECK.md            # instructions for the agent (like CLAUDE.md / AGENTS.md)
├── docs/                 # documentation and runbooks (Markdown) — yours and the agent's (write_doc)
├── topology.yaml         # the infrastructure map (Topology page, topology tool)
├── skills/               # reusable procedures the agent can load (name.md or name/SKILL.md + files)
├── scripts/              # scripts run on hosts as one unit (name/script.yaml + script.sh), see scripts.md
├── agents/               # the workspace's own subagents (name.md: front matter + instructions), see harness.md
├── commands/             # slash commands: prompt templates (name.md, $ARGUMENTS), see harness.md
├── ansible/              # Ansible projects kept in the workspace (ansible/<name>/), see ansible.md
├── reports/              # analyses, audits, incident write-ups, plans saved by the agent
│   └── 2026/10/2026-10-21-0930-disk-and-memory-audit-of-prod.md
├── artifacts/            # evidence files (save_artifact): config snapshots, full command output, generated files
│   ├── INDEX.md          #   what was saved, when, from where, by which session, and why
│   └── 2026-10-21/app-1/etc/nginx/nginx.conf
├── MEMORY.md             # facts the agent learned about this project (memory tool)
├── repos/                # working copies of the Git repositories (GitRemote), local-only
│   └── deploy/
├── .gitignore            # ignores .opsdeck/ and repos/
└── .opsdeck/             # local-only, not shared: agent sessions (transcripts), pinned host keys
    ├── sessions/         #   with <id>.outputs/: full outputs of long tool results
    ├── ansible/          # Ansible run history and logs
    └── known_hosts
```

Everything except `.opsdeck/` and `repos/` can be committed to git and shared with the team.
**No secrets are ever stored in the folder** — only `vault://` references; secrets stay
in each person's local vault. Command output in sessions and reports is redacted.

Artifacts are text only (max 5 MB each), redacted before they are written, and never
overwritten (a second snapshot of the same file on the same day gets a time suffix).

Reports carry front matter (`title`, `created`, `session`, `model`, `tags`) so you can
trace a result back to the agent session that produced it.

Still outside the folder: the vault (one per machine, with a separate scope per
workspace) and the global audit journal.

## Export and import

*Workspace settings → Export workspace* writes one file (`<name>-<date>.opsdeck`) with the
workspace's files and the items of its vault scope, so a workspace can be moved to another
machine or handed to a teammate without a shared git repository:

| included | left out |
|---|---|
| `workspace.yaml`, `resources/`, `topology.yaml` | `.opsdeck/` (agent sessions, known_hosts, run history) |
| `OPSDECK.md`, `MEMORY.md`, `docs/`, `skills/`, `agents/`, `commands/` | `reports/`, `artifacts/` |
| `scripts/`, `ansible/` | `repos/` (Git working copies), `.git/` |
| the workspace's vault scope (current revisions) | the global vault scope, item history |

The content is gzip-compressed JSON encrypted with XChaCha20-Poly1305 under a key derived
from the password you type at export time (Argon2id, the vault's parameters); only the
format, version, date and KDF parameters are readable without it. Choose a password other
than your master password when the file goes to someone else.

*New / import workspace → Import export file* asks for that password, decrypts the file
once, creates the workspace under its original name in the workspaces folder and adds the
secrets to the local vault, where they are encrypted with that vault's own keys (as if
they had been entered by hand). The import fails if a workspace with that name already
exists; items already in a leftover vault scope of the same name are kept. MCP servers
from an imported workspace need to be trusted again before they run. References to the
global scope (`vault://global/…`) show up in validation until the receiving user adds them.

## workspace.yaml

```yaml
apiVersion: opsdeck/v1
kind: Workspace
metadata:
  name: acme
  title: ACME Corp
  description: E-commerce platform, managed since 2025
  color: "#e4572e"
  tags: [customer, ecommerce]

environments:          # used for grouping and, later, for agent policies
  - name: dev
  - name: staging
  - name: prod
    protected: true    # UI warns; agent needs approval for changes

settings:
  defaultUser: deploy
  defaultKey: vault://acme/deploy-key
  web: search              # agent web access here: off | search | fetch ("" = the app default)
  webVia: network/office-wg # send the agent's web requests through a VPN or proxy of the workspace
  models: [ollama/*]       # the models sessions may use (patterns; empty = any)

hooks:                     # commands around the agent's tool calls (harness.md#hooks);
  - name: audit            # ones that came with the folder run only after you trust them
    event: after_tool
    command: logger -t opsdeck-agent
```

## Resources

Every resource has the same envelope, so providers can be added without changing the
format:

```yaml
kind: <Kind>          # owned by a provider
name: <unique-in-workspace>
environment: prod     # optional
tags: [...]
via: <kind>/<name>    # optional: how it is reached (network or jump host)
docs: docs/...md      # optional: linked documentation
spec: { ... }         # provider-specific
```

### Examples (first kinds)

```yaml
# networks.yaml — VPN profiles (see vpn.md)
- kind: WireGuard
  name: office-wg
  spec:
    config: vault://acme/office-wg          # the .conf lives in the vault
    mode: userspace                         # or system (TUN, needs root)
    proxyPort: 1081                         # optional SOCKS5 for other apps

- kind: OpenConnect                         # Cisco AnyConnect / ocserv / FortiGate / GlobalProtect…
  name: hq-anyconnect
  spec:
    protocol: anyconnect
    server: vpn.example.com
    auth: vault://acme/vpn-login            # username, password, optional TOTP
    mode: userspace

- kind: Tailscale
  name: tailnet
  spec:
    controlUrl: https://headscale.example.com   # empty = Tailscale
    authKey: vault://acme/ts-authkey

# hosts.yaml
- kind: Host
  name: bastion
  environment: prod
  via: network/office-wg
  spec:
    address: 10.20.0.10
    port: 22
    user: deploy
    auth: vault://acme/deploy-key

- kind: Host
  name: app-1
  environment: prod
  via: host/bastion                          # resolver: VPN → bastion → app-1
  spec:
    address: 10.20.1.21
    auth: vault://acme/deploy-key
    roles: [app, docker]

- kind: HostGroup
  name: app-servers
  spec:
    selector: { roles: [app] }

# panels.yaml
- kind: Panel
  name: grafana
  via: network/office-wg
  spec:
    url: https://grafana.acme.internal
    login: vault://acme/grafana-admin       # or token: vault://… (+ authHeader); the agent's http_request uses it

# observability.yaml — for the agent's metrics_query and logs_query
- kind: Prometheus                         # or VictoriaMetrics, Thanos, Mimir
  name: prom
  environment: prod
  via: network/office-wg
  spec:
    url: http://prometheus.acme.internal:9090

- kind: Loki
  name: loki
  via: network/office-wg
  spec:
    url: http://loki.acme.internal:3100
    tenant: acme                            # X-Scope-OrgID, optional

- kind: Elasticsearch                       # or OpenSearch
  name: logs
  spec:
    url: https://es.acme.internal:9200
    login: vault://acme/es-reader
    index: logs-*

- kind: GitRemote
  name: deploy
  environment: prod
  via: network/office-wg                     # optional: server only reachable over the VPN
  spec:
    url: https://gitlab.acme.internal/ops/deploy.git
    provider: gitlab                         # auto | gitlab | github
    token: vault://acme/gitlab-deploy-token  # or deployKey: vault://acme/deploy-key for ssh:// URLs
    caCert: |                                # private CA, optional
      -----BEGIN CERTIFICATE-----
      …
    protectedBranches: [main, release/*]
    docs: true                               # its README.md and docs/ are documentation
    docsPaths: [README.md, docs]
```

Later kinds (not designed yet, same envelope): `OpenVPN`, `DockerEngine`,
`KubernetesCluster`, `LogSource`, `Database`, `Service`.

## Topology

Topology is derived, not drawn by hand: `via` edges + networks + hosts give the graph
the UI renders and the resolver uses. Free-form notes go in `docs/`.

## Isolation guarantees

| item | per workspace |
|---|---|
| vault scope (keys, passwords) | yes |
| SSH agent socket | yes |
| known_hosts (host key pinning) | yes |
| VPN tunnels | yes (userspace, can run in parallel with other workspaces) |
| session/command history | yes |
| audit log | tagged by workspace |
| AI memory (later) | yes |

## CLI sketch

```
opsdeck ws create acme
opsdeck ws list
opsdeck ws use acme                 # sets current workspace for the shell
opsdeck ws validate                 # schema + dangling vault refs + via cycles
opsdeck ws show app-1               # resolved info, path, linked docs
opsdeck ssh app-1                   # VPN up if needed → bastion → app-1
opsdeck shell acme                  # subshell with SSH_AUTH_SOCK etc. for acme
```
