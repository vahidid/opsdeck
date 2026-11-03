# Resource schema

<!-- Generated from internal/workspace/kinds.go by internal/workspace/schemadoc.go.
     Do not edit by hand: OPSDECK_UPDATE_DOCS=1 go test ./internal/workspace -->

The inventory of a workspace is a list of resources in `resources/*.yaml` (see
[workspace.md](workspace.md)), each with these common keys:

| Key | Meaning |
|---|---|
| `kind` | one of the kinds below |
| `name` | lower-case letters, digits and dashes; unique per kind |
| `environment` | one of the workspace's environments (`prod`, `staging`…) |
| `tags` | free labels |
| `via` | how it is reached: `network/<name>` (a VPN or proxy) or `host/<name>` (a jump host); kinds marked *via* accept it |
| `description`, `docs` | text for people and the agent; a path to a runbook |
| `spec` | the kind's own fields, below |

Secret fields (*vault reference*) hold `vault://<scope>/<item>[#field]` only, never the
secret itself. The format is `opsdeck/v1`; [compatibility.md](compatibility.md) says
what may change between versions.

| Kind | Category | via |
|---|---|---|
| [`Host`](#host) | compute | yes |
| [`HostGroup`](#hostgroup) | compute |  |
| [`WireGuard`](#wireguard) | network |  |
| [`Tailscale`](#tailscale) | network |  |
| [`NetBird`](#netbird) | network |  |
| [`OpenVPN`](#openvpn) | network |  |
| [`OpenConnect`](#openconnect) | network |  |
| [`OpenFortiVPN`](#openfortivpn) | network |  |
| [`SocksProxy`](#socksproxy) | network | yes |
| [`Panel`](#panel) | service | yes |
| [`GitRemote`](#gitremote) | service | yes |
| [`AnsibleProject`](#ansibleproject) | service | yes |
| [`Database`](#database) | service | yes |
| [`Prometheus`](#prometheus) | service | yes |
| [`Loki`](#loki) | service | yes |
| [`Elasticsearch`](#elasticsearch) | service | yes |
| [`Domain`](#domain) | service |  |
| [`Certificate`](#certificate) | service |  |
| [`DeployPlatform`](#deployplatform) | service | yes |
| [`KubernetesCluster`](#kubernetescluster) | cluster | yes |

## Host

Host. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `address` | string | required | Address (e.g. `10.20.1.21 or app-1.example.com`) |
| `port` | number |  | SSH port (e.g. `22`) |
| `user` | string |  | User: Defaults to the workspace default user (e.g. `deploy`) |
| `auth` | vault reference (ssh-key, login) |  | Credential: SSH key or login (password). Defaults to the workspace default credential |
| `roles` | list |  | Roles: e.g. app, db, docker, k8s-node, bastion |

## HostGroup

Host group.

| Field | Type | | Description |
|---|---|---|---|
| `hosts` | name of a Host |  | Hosts: Explicit members |
| `roles` | list |  | Match roles: Hosts having any of these roles |
| `tags` | list |  | Match tags: Hosts having any of these tags |

## WireGuard

WireGuard.

| Field | Type | | Description |
|---|---|---|---|
| `config` | vault reference (vpn) | required | WireGuard config: A vault item of type VPN config holding the wg-quick .conf |
| `mode` | one of `userspace`, `system` | default `userspace` | Connection mode: userspace: the network stack runs inside opsdeck — no root, nothing changes on this machine, many VPNs side by side. system: a TUN interface with system routes/DNS for every app — needs root (sudo/pkexec) and may conflict with other system VPNs |
| `proxyPort` | number |  | Local SOCKS5 port: Userspace mode: also serve the VPN to other apps on this machine as a SOCKS5 proxy on 127.0.0.1:<port> (browser, kubectl, curl --socks5-hostname) (e.g. `1080`) |

## Tailscale

Tailscale / Headscale.

| Field | Type | | Description |
|---|---|---|---|
| `controlUrl` | string |  | Control server: Empty = Tailscale. Set your Headscale URL for a self-hosted control server (e.g. `https://headscale.example.com`) |
| `authKey` | vault reference (token) |  | Auth key: Pre-auth key (vault token). Empty = log in once through a link shown on the VPN page |
| `hostname` | string |  | Device name: This machine's name in the network (e.g. `opsdeck-<profile>`) |
| `acceptRoutes` | bool | default `true` | Use subnet routes: Reach subnets advertised by subnet routers |
| `ephemeral` | bool |  | Ephemeral node: Remove this device from the tailnet when it goes offline |
| `proxyPort` | number |  | Local SOCKS5 port: Userspace mode: also serve the VPN to other apps on this machine as a SOCKS5 proxy on 127.0.0.1:<port> (browser, kubectl, curl --socks5-hostname) (e.g. `1080`) |

## NetBird

NetBird.

| Field | Type | | Description |
|---|---|---|---|
| `managementUrl` | string |  | Management URL: Empty = NetBird cloud (e.g. `https://netbird.example.com`) |
| `setupKey` | vault reference (token) |  | Setup key: Needed the first time; afterwards the peer key in local state is used |
| `hostname` | string |  | Device name: This machine's name in the network (e.g. `opsdeck-<profile>`) |
| `mode` | one of `userspace`, `system` | default `userspace` | Connection mode: userspace: the network stack runs inside opsdeck — no root, nothing changes on this machine, many VPNs side by side. system: a TUN interface with system routes/DNS for every app — needs root (sudo/pkexec) and may conflict with other system VPNs |
| `proxyPort` | number |  | Local SOCKS5 port: Userspace mode: also serve the VPN to other apps on this machine as a SOCKS5 proxy on 127.0.0.1:<port> (browser, kubectl, curl --socks5-hostname) (e.g. `1080`) |

## OpenVPN

OpenVPN.

| Field | Type | | Description |
|---|---|---|---|
| `config` | vault reference (vpn, file) | required | Config (.ovpn): A vault item holding the .ovpn with inline certificates. Always system mode (TUN; needs root via sudo/pkexec) |
| `auth` | vault reference (login) |  | Login: Username/password if the server asks; a TOTP secret answers static-challenge OTP |
| `allowScripts` | bool |  | Allow config scripts: Let up/down scripts in the config run (as root). Off by default |

## OpenConnect

OpenConnect (AnyConnect · ocserv · FortiGate · GlobalProtect · Pulse · F5).

| Field | Type | | Description |
|---|---|---|---|
| `protocol` | one of `anyconnect`, `fortinet`, `gp`, `pulse`, `nc`, `f5`, `array` | default `anyconnect` | Protocol: anyconnect = Cisco ASA/AnyConnect and ocserv; fortinet = FortiGate; gp = Palo Alto GlobalProtect; pulse = Pulse/Ivanti; nc = Juniper |
| `server` | string | required | Server (e.g. `vpn.example.com or https://vpn.example.com/group`) |
| `auth` | vault reference (login) |  | Login: Username/password; a TOTP secret on the item answers one-time code requests. Other codes (SMS, email, a token) are asked for in opsdeck (or come from your phone, see App settings) |
| `authGroup` | string |  | Group / realm: AnyConnect: the GROUP to log in to (empty = opsdeck shows the gateway's groups to choose from); FortiGate: the realm; GlobalProtect: the gateway (e.g. `e.g. IT-Staff`) |
| `serverCert` | string |  | Server certificate pin: Only for self-signed gateways; the exact value is shown in the log after the first attempt (e.g. `pin-sha256:…`) |
| `sso` | bool |  | Single sign-on (SAML): Log in through your identity provider (Azure AD/Entra, Okta, Google, ADFS…). opsdeck opens the login page and takes the VPN session from it — nothing to copy by hand. AnyConnect/ocserv, GlobalProtect and FortiGate |
| `ssoBrowser` | one of `auto`, `window`, `system` | default `auto`, with `sso` | SSO login in: window: a login window opsdeck drives (Chrome/Edge/Chromium/Brave; remembers your IdP login per profile, so later logins are usually automatic). system: your default browser (AnyConnect external-browser and FortiGate only). auto: window when such a browser is installed |
| `cookie` | vault reference (token) |  | Session cookie: Rarely needed: a session cookie from `openconnect --authenticate`, used instead of a login |
| `mode` | one of `userspace`, `system` | default `userspace` | Connection mode: userspace: the network stack runs inside opsdeck — no root, nothing changes on this machine, many VPNs side by side. system: a TUN interface with system routes/DNS for every app — needs root (sudo/pkexec) and may conflict with other system VPNs |
| `proxyPort` | number |  | Local SOCKS5 port: Userspace mode: also serve the VPN to other apps on this machine as a SOCKS5 proxy on 127.0.0.1:<port> (browser, kubectl, curl --socks5-hostname) (e.g. `1080`) |

## OpenFortiVPN

FortiGate (openfortivpn).

| Field | Type | | Description |
|---|---|---|---|
| `server` | string | required | Gateway (e.g. `vpn.example.com:443`) |
| `auth` | vault reference (login) |  | Login: Username/password; a TOTP secret on the item answers the code request. Codes sent by SMS or email are asked for in opsdeck (or come from your phone, see App settings) |
| `sso` | bool |  | Single sign-on (SAML): Log in through your identity provider (Azure AD/Entra, Okta, Google, ADFS…). opsdeck opens the login page and takes the VPN session from it — nothing to copy by hand. AnyConnect/ocserv, GlobalProtect and FortiGate |
| `ssoBrowser` | one of `auto`, `window`, `system` | default `auto`, with `sso` | SSO login in: window: a login window opsdeck drives (Chrome/Edge/Chromium/Brave; remembers your IdP login per profile, so later logins are usually automatic). system: your default browser (AnyConnect external-browser and FortiGate only). auto: window when such a browser is installed |
| `authGroup` | string |  | Realm |
| `serverCert` | string |  | Trusted certificate (sha256): Only for self-signed gateways; the value is shown in the log after the first attempt |

## SocksProxy

SOCKS5 proxy. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `address` | string | required | Address (e.g. `127.0.0.1:1080`) |
| `auth` | vault reference (login) |  | Credential |

## Panel

Web panel / API. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `url` | string | required | URL (e.g. `https://grafana.internal`) |
| `login` | vault reference (login) |  | Login: Username and password: sent as HTTP Basic auth when the agent calls the API (http_request) |
| `token` | vault reference (token) |  | API token: Sent instead of the login, as Authorization: Bearer <token> unless a header is set below (Elasticsearch: ApiKey) |
| `authHeader` | string |  | Token header: Header that carries the token, e.g. X-API-Key or PRIVATE-TOKEN (the token is sent as it is) (e.g. `Authorization`) |
| `caCert` | text |  | CA certificate (PEM): For a server with a private CA |
| `insecure` | bool |  | Skip TLS verification: Only for self-signed test servers |

## GitRemote

Git repository. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `connection` | connector id |  | Connector: The GitHub or GitLab account (Connectors) used for this repository. Empty: the connector whose server matches the URL, if any |
| `url` | string | required | URL (e.g. `https://gitlab.example.com/group/project`) |
| `branch` | string |  | Branch (e.g. `default branch`) |
| `path` | string |  | Working copy folder: An existing clone elsewhere on this computer (absolute path or ~/…), or an empty folder to clone into. Empty: repos/<name> in the workspace. opsdeck never deletes this folder (e.g. `~/projects/deploy`) |
| `docs` | bool |  | Use as documentation: The agent and the Docs page read this repository's Markdown files |
| `docsPaths` | list | with `docs` | Documentation paths: Files or folders with Markdown documentation (default: README.md and docs/) (e.g. `README.md, docs/`) |
| `protectedBranches` | list |  | Protected branches: Pushing here always needs approval, like the default branch and branches protected on the server (e.g. `main, release/*`) |
| `token` | vault reference (token, login) |  | Access token: Only without a connector, or to use a different token (e.g. a project deploy token). GitLab: api + write_repository; GitHub: Contents and Pull requests read/write |
| `deployKey` | vault reference (ssh-key) |  | Deploy key: For ssh:// or git@host: URLs |
| `provider` | one of `auto`, `gitlab`, `github` | default `auto` | Hosting: auto: from the connector, github.com, hosts named gitlab, else asks the server |
| `apiUrl` | string |  | API URL: Only when the API is not at the usual place (e.g. `https://gitlab.example.com/api/v4`) |
| `caCert` | text |  | CA certificate (PEM): For a server with a private CA (the connector's is used otherwise) |

## AnsibleProject

Ansible project. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `source` | one of `folder`, `repo` | default `folder` | Source: folder: a folder of the workspace or anywhere on this computer; repo: a folder in one of the workspace's Git repositories |
| `repo` | name of a GitRemote |  | Repository: Source repo: the repository that holds the project |
| `path` | string |  | Folder: repo: the project's folder inside the repository (empty = its root). folder: relative to the workspace (default ansible/<name>), or an absolute path / ~/… on this computer (e.g. `ansible/<name>`) |
| `inventory` | string |  | Default inventory: A file or folder in the project, or 'workspace' for an inventory generated from the workspace's hosts. Empty: ansible.cfg's (e.g. `inventories/prod`) |
| `vaultPassword` | vault reference (login, token, note) |  | Vault password: Optional: the Ansible Vault password saved in the vault. Without it, opsdeck asks for the password when it is needed and keeps it in memory |
| `vaultPasswords` | list |  | More vault ids: Passwords of other vault ids, as <vault id>=vault://<scope>/<item> (e.g. `prod=vault://acme/ansible-prod`) |
| `sshKey` | vault reference (ssh-key) |  | SSH key: Key ansible logs in with (served by an in-memory ssh-agent). Workspace hosts use their own credentials |
| `bin` | string |  | Ansible folder: Folder holding ansible-playbook (a virtualenv). Empty: from PATH (e.g. `~/.venvs/ansible/bin`) |

## Database

Database. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `engine` | one of `postgres`, `mysql`, `mariadb`, `mongodb`, `redis`, `elasticsearch`, `other` |  | Engine |
| `address` | string | required | Address: host:port as opsdeck reaches it, or as the host below sees it (e.g. 127.0.0.1:5432) (e.g. `10.20.2.5:5432`) |
| `host` | name of a Host |  | Runs on host: Without via, opsdeck connects through an SSH tunnel to this host |
| `database` | string |  | Database name: Redis: the database number (0, 1, …) |
| `login` | vault reference (login) |  | Login: Username and password of the database user (Redis: an empty username is the default user) |
| `tls` | one of `prefer`, `require`, `verify-full`, `disable` |  | TLS: prefer: TLS when the server offers it. verify-full also checks the certificate |
| `authSource` | string |  | Auth database: MongoDB: the database the user is defined in (e.g. `admin`) |

## Prometheus

Prometheus. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `url` | string | required | URL: Prometheus or a compatible API (VictoriaMetrics, Thanos, Mimir: the URL that serves /api/v1/query) (e.g. `http://prometheus.internal:9090`) |
| `login` | vault reference (login) |  | Login: HTTP Basic auth, if any |
| `token` | vault reference (token) |  | API token: Sent instead of the login, as Authorization: Bearer <token> unless a header is set below (Elasticsearch: ApiKey) |
| `authHeader` | string |  | Token header: Header that carries the token, e.g. X-API-Key or PRIVATE-TOKEN (the token is sent as it is) (e.g. `Authorization`) |
| `caCert` | text |  | CA certificate (PEM): For a server with a private CA |
| `insecure` | bool |  | Skip TLS verification: Only for self-signed test servers |

## Loki

Loki. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `url` | string | required | URL (e.g. `http://loki.internal:3100`) |
| `login` | vault reference (login) |  | Login: HTTP Basic auth, if any |
| `tenant` | string |  | Tenant: X-Scope-OrgID of a multi-tenant Loki |
| `token` | vault reference (token) |  | API token: Sent instead of the login, as Authorization: Bearer <token> unless a header is set below (Elasticsearch: ApiKey) |
| `authHeader` | string |  | Token header: Header that carries the token, e.g. X-API-Key or PRIVATE-TOKEN (the token is sent as it is) (e.g. `Authorization`) |
| `caCert` | text |  | CA certificate (PEM): For a server with a private CA |
| `insecure` | bool |  | Skip TLS verification: Only for self-signed test servers |

## Elasticsearch

Elasticsearch / OpenSearch. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `url` | string | required | URL (e.g. `https://es.internal:9200`) |
| `login` | vault reference (login) |  | Login: HTTP Basic auth |
| `index` | string |  | Log indices: Index pattern the agent searches for logs (default: all) (e.g. `logs-*`) |
| `timeField` | string |  | Time field (e.g. `@timestamp`) |
| `token` | vault reference (token) |  | API token: Sent instead of the login, as Authorization: Bearer <token> unless a header is set below (Elasticsearch: ApiKey) |
| `authHeader` | string |  | Token header: Header that carries the token, e.g. X-API-Key or PRIVATE-TOKEN (the token is sent as it is) (e.g. `Authorization`) |
| `caCert` | text |  | CA certificate (PEM): For a server with a private CA |
| `insecure` | bool |  | Skip TLS verification: Only for self-signed test servers |

## Domain

Domain (DNS zone).

| Field | Type | | Description |
|---|---|---|---|
| `zone` | string | required | Domain: The zone as the provider lists it (without a trailing dot) (e.g. `example.com`) |
| `connection` | connector id |  | Connector: The Cloudflare or ArvanCloud account (Connectors) that manages this domain. Empty: the connected account that has the zone |
| `notes` | text |  | What points here: Which services and hosts the records of this zone belong to (the agent reads it) |

## Certificate

TLS certificate (ACME).

| Field | Type | | Description |
|---|---|---|---|
| `domains` | list | required | Domains: Names the certificate covers (the first is its common name). Their zones must be reachable through a DNS connector: a Domain of this workspace, or a zone of a connected account (e.g. `example.com, *.example.com`) |
| `ca` | one of `letsencrypt`, `letsencrypt-staging`, `buypass`, `buypass-test`, `custom` | default `letsencrypt` | Certificate authority: letsencrypt: 90-day certificates. letsencrypt-staging / buypass-test: for trying things out, not trusted by browsers. custom: another ACME directory (below) |
| `email` | string |  | Contact email: For the ACME account: expiry warnings and problems with the certificate (e.g. `ops@example.com`) |
| `keyType` | one of `ec256`, `ec384`, `rsa2048`, `rsa4096` | default `ec256` | Key type |
| `renewDays` | number |  | Renew before expiry (days): The certificate is due for renewal this many days before it expires (e.g. `30`) |
| `autoRenew` | bool |  | Renew automatically: While opsdeck runs with the vault unlocked, a due certificate is renewed by itself (into the vault; deploying it to servers stays a separate step) |
| `directoryUrl` | string |  | ACME directory URL: Only for ca: custom (step-ca, an internal CA). External account binding (ZeroSSL, Google) is not supported yet (e.g. `https://ca.example.internal/acme/acme/directory`) |
| `caCert` | text |  | CA's TLS certificate (PEM): For a custom ACME CA served with a private certificate |

## DeployPlatform

Deployment platform (Portainer · Argo CD · Komodo). Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `platform` | one of `portainer`, `argocd`, `komodo` | required, default `portainer` | Platform: portainer: Docker / Swarm stacks; argocd: Argo CD applications (GitOps on Kubernetes); komodo: Komodo stacks and deployments |
| `url` | string | required | URL: The platform's address as opsdeck reaches it (self-hosted or behind a VPN / jump host: set via) (e.g. `https://portainer.internal:9443`) |
| `token` | vault reference (token, login) | required | Credential: Portainer: an access token (My account → Access tokens). Argo CD: an account token. Komodo: a login item with the API key as username and the secret as password |
| `targets` | list |  | Targets of this workspace: Names (glob patterns) of the stacks or applications that belong to this workspace, when the platform also serves others. Empty: all it lists (e.g. `shop-*, billing`) |
| `notes` | text |  | Notes for the agent: What runs here and how it is deployed (the agent reads it) |
| `caCert` | text |  | CA certificate (PEM): For a server with a private CA |
| `insecure` | bool |  | Skip TLS verification: Only for self-signed test servers (Portainer's default certificate) |

## KubernetesCluster

Kubernetes cluster. Accepts `via`.

| Field | Type | | Description |
|---|---|---|---|
| `kubeconfig` | vault reference (kubeconfig) | required | Kubeconfig |
| `context` | string |  | Context: Defaults to the kubeconfig's current context |
| `namespace` | string |  | Default namespace (e.g. `default`) |
