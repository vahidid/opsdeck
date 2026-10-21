# Workspace

A workspace represents one project/customer/environment set. It is the unit of
isolation: it has its own vault scope, SSH agent, known_hosts, networks, history and
documentation.

## Layout on disk

A workspace is one folder that holds everything about the project — what you
write and what the agent produces:

```
acme/
├── workspace.yaml        # identity, environments, defaults, permissions, MCP servers
├── resources/            # inventory: hosts, networks, panels, clusters… (YAML)
├── OPSDECK.md            # instructions for the agent (like CLAUDE.md / AGENTS.md)
├── docs/                 # documentation and runbooks (Markdown) — yours and the agent's (write_doc)
├── skills/               # reusable procedures the agent can load
├── reports/              # analyses, audits, incident write-ups, plans saved by the agent
│   └── 2026/10/2026-10-21-0930-disk-and-memory-audit-of-prod.md
├── artifacts/            # evidence files (save_artifact): config snapshots, full command output, generated files
│   ├── INDEX.md          #   what was saved, when, from where, by which session, and why
│   └── 2026-10-21/app-1/etc/nginx/nginx.conf
├── MEMORY.md             # facts the agent learned about this project (memory tool)
├── .gitignore            # ignores .opsdeck/
└── .opsdeck/             # local-only, not shared: agent sessions (transcripts), pinned host keys
    ├── sessions/
    └── known_hosts
```

Everything except `.opsdeck/` can be committed to git and shared with the team.
**No secrets are ever stored in the folder** — only `vault://` references; secrets stay
in each person's local vault. Command output in sessions and reports is redacted.

Artifacts are text only (max 5 MB each), redacted before they are written, and never
overwritten (a second snapshot of the same file on the same day gets a time suffix).

Reports carry front matter (`title`, `created`, `session`, `model`, `tags`) so you can
trace a result back to the agent session that produced it.

Still outside the folder: the vault (one per machine, with a separate scope per
workspace) and the global audit journal.

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
    login: vault://acme/grafana-admin

- kind: GitRemote
  name: gitlab
  spec:
    url: https://gitlab.acme.com/shop
    token: vault://global/gitlab-token
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
