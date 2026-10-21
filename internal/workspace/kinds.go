package workspace

// FieldSchema describes one spec field of a resource kind (drives UI forms and validation).
type FieldSchema struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // string | number | text | secretRef | list | select | resourceRef
	Required    bool     `json:"required,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Options     []string `json:"options,omitempty"`
	RefKinds    []string `json:"refKinds,omitempty"`
	ItemTypes   []string `json:"itemTypes,omitempty"`
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

// NetworkKinds can be referenced as "network/<name>" in via.
var NetworkKinds = []string{"WireGuard", "SocksProxy"}

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
	{Kind: "WireGuard", Label: "WireGuard network", Category: "network", Icon: "shield", Fields: []FieldSchema{
		{Key: "config", Label: "WireGuard config", Type: "secretRef", Required: true, ItemTypes: []string{"vpn"}, Help: "A vault item of type VPN holding the wg-quick .conf"},
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
		{Key: "url", Label: "URL", Type: "string", Required: true, Placeholder: "https://gitlab.example.com/group/project"},
		{Key: "branch", Label: "Default branch", Type: "string", Placeholder: "main"},
		{Key: "token", Label: "Access token", Type: "secretRef", ItemTypes: []string{"token", "login"}},
		{Key: "deployKey", Label: "Deploy key", Type: "secretRef", ItemTypes: []string{"ssh-key"}},
	}},
	{Kind: "Database", Label: "Database", Category: "service", Icon: "database", CanVia: true, Fields: []FieldSchema{
		{Key: "engine", Label: "Engine", Type: "select", Options: []string{"postgres", "mysql", "mariadb", "mongodb", "redis", "elasticsearch", "other"}},
		{Key: "address", Label: "Address", Type: "string", Required: true, Placeholder: "10.20.2.5:5432"},
		{Key: "host", Label: "Runs on host", Type: "resourceRef", RefKinds: []string{"Host"}},
		{Key: "database", Label: "Database name", Type: "string"},
		{Key: "login", Label: "Login", Type: "secretRef", ItemTypes: []string{"login"}},
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

func isNetworkKind(kind string) bool {
	for _, k := range NetworkKinds {
		if k == kind {
			return true
		}
	}
	return false
}
