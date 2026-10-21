# Workspace

A workspace represents one project/customer/environment set. It is the unit of
isolation: it has its own vault scope, SSH agent, known_hosts, networks, history and
documentation.

## Layout on disk

```
acme/
├── workspace.yaml        # identity, environments, settings, permissions
├── OPSDECK.md            # instructions for the agent (like CLAUDE.md / AGENTS.md)
├── resources/            # one or more YAML files, any split you like
│   ├── networks.yaml
│   ├── hosts.yaml
│   └── panels.yaml
├── docs/                 # free Markdown: runbooks, topology notes, contacts
│   ├── README.md
│   └── runbooks/deploy.md
├── skills/               # workspace-specific agent skills (optional)
├── mcp.yaml              # external MCP servers for this workspace (optional)
└── .opsdeck/             # ignored by git: local-only overrides
```

No secrets anywhere in this directory. It can live inside the app's data folder or in
any directory the user registers (e.g. a git repo shared by the team).

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
# networks.yaml
- kind: WireGuard
  name: office-wg
  spec:
    config: vault://acme/office-wg          # the .conf lives in the vault
    routes: [10.20.0.0/16]

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
