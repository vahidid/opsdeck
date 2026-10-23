package workspace

// FieldSchema describes one spec field of a resource kind (drives UI forms and validation).
type FieldSchema struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // string | number | bool | text | secretRef | list | select | resourceRef
	Required    bool     `json:"required,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Options     []string `json:"options,omitempty"`
	RefKinds    []string `json:"refKinds,omitempty"`
	ItemTypes   []string `json:"itemTypes,omitempty"`
	Default     string   `json:"default,omitempty"`
	ShowIf      string   `json:"showIf,omitempty"`   // only shown when this (bool) field is on
	Advanced    bool     `json:"advanced,omitempty"` // folded under "Advanced" in forms
}

// KindSchema describes a resource kind.
type KindSchema struct {
	Kind     string        `json:"kind"`
	Label    string        `json:"label"`
	Category string        `json:"category"` // compute | network | service | cluster
	Icon     string        `json:"icon"`
	CanVia   bool          `json:"canVia"`
	Fields   []FieldSchema `json:"fields"`
}

// NetworkKinds can be referenced as "network/<name>" in via. All but
// SocksProxy are VPN profiles (see internal/vpn).
var NetworkKinds = []string{"WireGuard", "Tailscale", "NetBird", "OpenVPN", "OpenConnect", "OpenFortiVPN", "SocksProxy"}

// VPN profile fields shared by several kinds.
var (
	fieldMode = FieldSchema{Key: "mode", Label: "Connection mode", Type: "select", Options: []string{"userspace", "system"}, Default: "userspace",
		Help: "userspace: the network stack runs inside opsdeck — no root, nothing changes on this machine, many VPNs side by side. system: a TUN interface with system routes/DNS for every app — needs root (sudo/pkexec) and may conflict with other system VPNs"}
	fieldProxyPort = FieldSchema{Key: "proxyPort", Label: "Local SOCKS5 port", Type: "number", Placeholder: "1080",
		Help: "Userspace mode: also serve the VPN to other apps on this machine as a SOCKS5 proxy on 127.0.0.1:<port> (browser, kubectl, curl --socks5-hostname)"}
	fieldHostname = FieldSchema{Key: "hostname", Label: "Device name", Type: "string", Placeholder: "opsdeck-<profile>", Help: "This machine's name in the network"}
	fieldSSO      = FieldSchema{Key: "sso", Label: "Single sign-on (SAML)", Type: "bool",
		Help: "Log in through your identity provider (Azure AD/Entra, Okta, Google, ADFS…). opsdeck opens the login page and takes the VPN session from it — nothing to copy by hand. AnyConnect/ocserv, GlobalProtect and FortiGate"}
	fieldSSOBrowser = FieldSchema{Key: "ssoBrowser", Label: "SSO login in", Type: "select", Options: []string{"auto", "window", "system"}, Default: "auto", ShowIf: "sso",
		Help: "window: a login window opsdeck drives (Chrome/Edge/Chromium/Brave; remembers your IdP login per profile, so later logins are usually automatic). system: your default browser (AnyConnect external-browser and FortiGate only). auto: window when such a browser is installed"}
)

// Kinds is the registry of resource kinds.
var Kinds = []KindSchema{
	{Kind: "Host", Label: "Host", Category: "compute", Icon: "server", CanVia: true, Fields: []FieldSchema{
		{Key: "address", Label: "Address", Type: "string", Required: true, Placeholder: "10.20.1.21 or app-1.example.com"},
		{Key: "port", Label: "SSH port", Type: "number", Placeholder: "22"},
		{Key: "user", Label: "User", Type: "string", Placeholder: "deploy", Help: "Defaults to the workspace default user"},
		{Key: "auth", Label: "Credential", Type: "secretRef", ItemTypes: []string{"ssh-key", "login"}, Help: "SSH key or login (password). Defaults to the workspace default credential"},
		{Key: "roles", Label: "Roles", Type: "list", Help: "e.g. app, db, docker, k8s-node, bastion"},
	}},
	{Kind: "HostGroup", Label: "Host group", Category: "compute", Icon: "layers", Fields: []FieldSchema{
		{Key: "hosts", Label: "Hosts", Type: "resourceRef", RefKinds: []string{"Host"}, Help: "Explicit members"},
		{Key: "roles", Label: "Match roles", Type: "list", Help: "Hosts having any of these roles"},
		{Key: "tags", Label: "Match tags", Type: "list", Help: "Hosts having any of these tags"},
	}},
	{Kind: "WireGuard", Label: "WireGuard", Category: "network", Icon: "shield", Fields: []FieldSchema{
		{Key: "config", Label: "WireGuard config", Type: "secretRef", Required: true, ItemTypes: []string{"vpn"}, Help: "A vault item of type VPN config holding the wg-quick .conf"},
		fieldMode, fieldProxyPort,
	}},
	{Kind: "Tailscale", Label: "Tailscale / Headscale", Category: "network", Icon: "shield", Fields: []FieldSchema{
		{Key: "controlUrl", Label: "Control server", Type: "string", Placeholder: "https://headscale.example.com", Help: "Empty = Tailscale. Set your Headscale URL for a self-hosted control server"},
		{Key: "authKey", Label: "Auth key", Type: "secretRef", ItemTypes: []string{"token"}, Help: "Pre-auth key (vault token). Empty = log in once through a link shown on the VPN page"},
		fieldHostname,
		{Key: "acceptRoutes", Label: "Use subnet routes", Type: "bool", Default: "true", Help: "Reach subnets advertised by subnet routers"},
		{Key: "ephemeral", Label: "Ephemeral node", Type: "bool", Help: "Remove this device from the tailnet when it goes offline"},
		fieldProxyPort,
	}},
	{Kind: "NetBird", Label: "NetBird", Category: "network", Icon: "shield", Fields: []FieldSchema{
		{Key: "managementUrl", Label: "Management URL", Type: "string", Placeholder: "https://netbird.example.com", Help: "Empty = NetBird cloud"},
		{Key: "setupKey", Label: "Setup key", Type: "secretRef", ItemTypes: []string{"token"}, Help: "Needed the first time; afterwards the peer key in local state is used"},
		fieldHostname, fieldMode, fieldProxyPort,
	}},
	{Kind: "OpenVPN", Label: "OpenVPN", Category: "network", Icon: "shield", Fields: []FieldSchema{
		{Key: "config", Label: "Config (.ovpn)", Type: "secretRef", Required: true, ItemTypes: []string{"vpn", "file"}, Help: "A vault item holding the .ovpn with inline certificates. Always system mode (TUN; needs root via sudo/pkexec)"},
		{Key: "auth", Label: "Login", Type: "secretRef", ItemTypes: []string{"login"}, Help: "Username/password if the server asks; a TOTP secret answers static-challenge OTP"},
		{Key: "allowScripts", Label: "Allow config scripts", Type: "bool", Help: "Let up/down scripts in the config run (as root). Off by default"},
	}},
	{Kind: "OpenConnect", Label: "OpenConnect (AnyConnect · ocserv · FortiGate · GlobalProtect · Pulse · F5)", Category: "network", Icon: "shield", Fields: []FieldSchema{
		{Key: "protocol", Label: "Protocol", Type: "select", Options: []string{"anyconnect", "fortinet", "gp", "pulse", "nc", "f5", "array"}, Default: "anyconnect",
			Help: "anyconnect = Cisco ASA/AnyConnect and ocserv; fortinet = FortiGate; gp = Palo Alto GlobalProtect; pulse = Pulse/Ivanti; nc = Juniper"},
		{Key: "server", Label: "Server", Type: "string", Required: true, Placeholder: "vpn.example.com or https://vpn.example.com/group"},
		{Key: "auth", Label: "Login", Type: "secretRef", ItemTypes: []string{"login"}, Help: "Username/password; a TOTP secret on the item answers OTP prompts"},
		{Key: "authGroup", Label: "Group / realm", Type: "string"},
		{Key: "serverCert", Label: "Server certificate pin", Type: "string", Placeholder: "pin-sha256:…", Help: "Only for self-signed gateways; the exact value is shown in the log after the first attempt"},
		fieldSSO, fieldSSOBrowser,
		{Key: "cookie", Label: "Session cookie", Type: "secretRef", ItemTypes: []string{"token"}, Help: "Rarely needed: a session cookie from `openconnect --authenticate`, used instead of a login"},
		fieldMode, fieldProxyPort,
	}},
	{Kind: "OpenFortiVPN", Label: "FortiGate (openfortivpn)", Category: "network", Icon: "shield", Fields: []FieldSchema{
		{Key: "server", Label: "Gateway", Type: "string", Required: true, Placeholder: "vpn.example.com:443"},
		{Key: "auth", Label: "Login", Type: "secretRef", ItemTypes: []string{"login"}},
		fieldSSO, fieldSSOBrowser,
		{Key: "authGroup", Label: "Realm", Type: "string"},
		{Key: "serverCert", Label: "Trusted certificate (sha256)", Type: "string", Help: "Only for self-signed gateways; the value is shown in the log after the first attempt"},
	}},
	{Kind: "SocksProxy", Label: "SOCKS5 proxy", Category: "network", Icon: "network", CanVia: true, Fields: []FieldSchema{
		{Key: "address", Label: "Address", Type: "string", Required: true, Placeholder: "127.0.0.1:1080"},
		{Key: "auth", Label: "Credential", Type: "secretRef", ItemTypes: []string{"login"}},
	}},
	{Kind: "Panel", Label: "Web panel", Category: "service", Icon: "app-window", CanVia: true, Fields: []FieldSchema{
		{Key: "url", Label: "URL", Type: "string", Required: true, Placeholder: "https://grafana.internal"},
		{Key: "login", Label: "Login", Type: "secretRef", ItemTypes: []string{"login"}},
	}},
	{Kind: "GitRemote", Label: "Git repository", Category: "service", Icon: "git-branch", CanVia: true, Fields: []FieldSchema{
		{Key: "connection", Label: "Connector", Type: "gitConnection",
			Help: "The GitHub or GitLab account (Connectors) used for this repository. Empty: the connector whose server matches the URL, if any"},
		{Key: "url", Label: "URL", Type: "string", Required: true, Placeholder: "https://gitlab.example.com/group/project"},
		{Key: "branch", Label: "Branch", Type: "string", Placeholder: "default branch"},
		{Key: "docs", Label: "Use as documentation", Type: "bool", Help: "The agent and the Docs page read this repository's Markdown files"},
		{Key: "docsPaths", Label: "Documentation paths", Type: "list", Placeholder: "README.md, docs/", ShowIf: "docs",
			Help: "Files or folders with Markdown documentation (default: README.md and docs/)"},
		{Key: "protectedBranches", Label: "Protected branches", Type: "list", Placeholder: "main, release/*", Advanced: true,
			Help: "Pushing here always needs approval, like the default branch and branches protected on the server"},
		{Key: "token", Label: "Access token", Type: "secretRef", ItemTypes: []string{"token", "login"}, Advanced: true,
			Help: "Only without a connector, or to use a different token (e.g. a project deploy token). GitLab: api + write_repository; GitHub: Contents and Pull requests read/write"},
		{Key: "deployKey", Label: "Deploy key", Type: "secretRef", ItemTypes: []string{"ssh-key"}, Advanced: true, Help: "For ssh:// or git@host: URLs"},
		{Key: "provider", Label: "Hosting", Type: "select", Options: []string{"auto", "gitlab", "github"}, Default: "auto", Advanced: true,
			Help: "auto: from the connector, github.com, hosts named gitlab, else asks the server"},
		{Key: "apiUrl", Label: "API URL", Type: "string", Placeholder: "https://gitlab.example.com/api/v4", Advanced: true, Help: "Only when the API is not at the usual place"},
		{Key: "caCert", Label: "CA certificate (PEM)", Type: "text", Advanced: true, Help: "For a server with a private CA (the connector's is used otherwise)"},
	}},
	{Kind: "AnsibleProject", Label: "Ansible project", Category: "service", Icon: "workflow", CanVia: true, Fields: []FieldSchema{
		{Key: "source", Label: "Source", Type: "select", Options: []string{"folder", "repo"}, Default: "folder",
			Help: "folder: a folder of the workspace or anywhere on this computer; repo: a folder in one of the workspace's Git repositories"},
		{Key: "repo", Label: "Repository", Type: "resourceRef", RefKinds: []string{"GitRemote"}, Help: "Source repo: the repository that holds the project"},
		{Key: "path", Label: "Folder", Type: "string", Placeholder: "ansible/<name>",
			Help: "repo: the project's folder inside the repository (empty = its root). folder: relative to the workspace (default ansible/<name>), or an absolute path / ~/… on this computer"},
		{Key: "inventory", Label: "Default inventory", Type: "string", Placeholder: "inventories/prod",
			Help: "A file or folder in the project, or 'workspace' for an inventory generated from the workspace's hosts. Empty: ansible.cfg's"},
		{Key: "vaultPassword", Label: "Vault password", Type: "secretRef", ItemTypes: []string{"login", "token", "note"},
			Help: "Optional: the Ansible Vault password saved in the vault. Without it, opsdeck asks for the password when it is needed and keeps it in memory"},
		{Key: "vaultPasswords", Label: "More vault ids", Type: "list", Placeholder: "prod=vault://acme/ansible-prod", Advanced: true,
			Help: "Passwords of other vault ids, as <vault id>=vault://<scope>/<item>"},
		{Key: "sshKey", Label: "SSH key", Type: "secretRef", ItemTypes: []string{"ssh-key"}, Advanced: true,
			Help: "Key ansible logs in with (served by an in-memory ssh-agent). Workspace hosts use their own credentials"},
		{Key: "bin", Label: "Ansible folder", Type: "string", Placeholder: "~/.venvs/ansible/bin", Advanced: true,
			Help: "Folder holding ansible-playbook (a virtualenv). Empty: from PATH"},
	}},
	{Kind: "Database", Label: "Database", Category: "service", Icon: "database", CanVia: true, Fields: []FieldSchema{
		{Key: "engine", Label: "Engine", Type: "select", Options: []string{"postgres", "mysql", "mariadb", "mongodb", "redis", "elasticsearch", "other"}},
		{Key: "address", Label: "Address", Type: "string", Required: true, Placeholder: "10.20.2.5:5432",
			Help: "host:port as opsdeck reaches it, or as the host below sees it (e.g. 127.0.0.1:5432)"},
		{Key: "host", Label: "Runs on host", Type: "resourceRef", RefKinds: []string{"Host"},
			Help: "Without via, opsdeck connects through an SSH tunnel to this host"},
		{Key: "database", Label: "Database name", Type: "string", Help: "Redis: the database number (0, 1, …)"},
		{Key: "login", Label: "Login", Type: "secretRef", ItemTypes: []string{"login"}, Help: "Username and password of the database user (Redis: an empty username is the default user)"},
		{Key: "tls", Label: "TLS", Type: "select", Options: []string{"prefer", "require", "verify-full", "disable"}, Advanced: true,
			Help: "prefer: TLS when the server offers it. verify-full also checks the certificate"},
		{Key: "authSource", Label: "Auth database", Type: "string", Placeholder: "admin", Advanced: true,
			Help: "MongoDB: the database the user is defined in"},
	}},
	{Kind: "KubernetesCluster", Label: "Kubernetes cluster", Category: "cluster", Icon: "boxes", CanVia: true, Fields: []FieldSchema{
		{Key: "kubeconfig", Label: "Kubeconfig", Type: "secretRef", Required: true, ItemTypes: []string{"kubeconfig"}},
		{Key: "context", Label: "Context", Type: "string", Help: "Defaults to the kubeconfig's current context"},
		{Key: "namespace", Label: "Default namespace", Type: "string", Placeholder: "default"},
	}},
}

// KindByName returns the schema of a kind.
func KindByName(kind string) (KindSchema, bool) {
	for _, k := range Kinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return KindSchema{}, false
}

// IsNetworkKind reports whether a kind can be used as via: network/<name>.
func IsNetworkKind(kind string) bool {
	for _, k := range NetworkKinds {
		if k == kind {
			return true
		}
	}
	return false
}
