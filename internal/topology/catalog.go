package topology

// The catalog drives the editor's palette and tells the agent which types
// exist. Unknown types are kept (shown as generic nodes).

// NodeType is a kind of component on the diagram.
type NodeType struct {
	Type     string   `json:"type"`
	Label    string   `json:"label"`
	Category string   `json:"category"`
	Icon     string   `json:"icon"`               // lucide icon name (UI)
	Variants []string `json:"variants,omitempty"` // e.g. router: edge, core, BGP border…
	Tech     []string `json:"tech,omitempty"`     // well-known products, for suggestions
	Hint     string   `json:"hint,omitempty"`
}

// LinkKind is a kind of connection between two nodes.
type LinkKind struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Category string `json:"category"` // network | tunnel | routing | app | data | mgmt
	Port     string `json:"port,omitempty"`
}

// Option is a simple id/label pair.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var NodeTypes = []NodeType{
	// Edge & access
	{Type: "internet", Label: "Internet", Category: "Edge & access", Icon: "globe"},
	{Type: "isp", Label: "ISP / uplink", Category: "Edge & access", Icon: "cable", Variants: []string{"fiber", "DSL", "LTE/5G", "satellite", "leased line", "MPLS"}},
	{Type: "users", Label: "Users / clients", Category: "Edge & access", Icon: "users", Variants: []string{"public users", "employees", "mobile apps", "partners", "IoT devices"}},
	{Type: "office", Label: "Office / branch", Category: "Edge & access", Icon: "building-2"},
	{Type: "dns", Label: "DNS", Category: "Edge & access", Icon: "book-open-text", Variants: []string{"authoritative", "recursive resolver", "internal", "GeoDNS"},
		Tech: []string{"Cloudflare DNS", "Route 53", "PowerDNS", "BIND", "CoreDNS", "Unbound", "Pi-hole", "ArvanCloud DNS"}},
	{Type: "cdn", Label: "CDN", Category: "Edge & access", Icon: "cloud", Tech: []string{"Cloudflare", "Akamai", "Fastly", "CloudFront", "Bunny CDN", "ArvanCloud CDN", "Varnish"}},
	{Type: "waf", Label: "WAF", Category: "Edge & access", Icon: "shield-check", Variants: []string{"cloud WAF", "appliance", "reverse-proxy module"},
		Tech: []string{"Cloudflare WAF", "AWS WAF", "ModSecurity", "Coraza", "F5 Advanced WAF", "FortiWeb", "Imperva", "ArvanCloud WAF"}},
	{Type: "ddos", Label: "DDoS protection", Category: "Edge & access", Icon: "shield-alert", Tech: []string{"Cloudflare", "AWS Shield", "Akamai Prolexic", "ArvanCloud", "Arbor"}},
	{Type: "load-balancer", Label: "Load balancer", Category: "Edge & access", Icon: "split", Variants: []string{"L4 (TCP/UDP)", "L7 (HTTP)", "global / GSLB", "cloud LB", "keepalived VIP"},
		Tech: []string{"HAProxy", "nginx", "F5 BIG-IP", "AWS ALB", "AWS NLB", "ArvanCloud Load Balancer", "MetalLB", "Keepalived", "Envoy", "Traefik", "Citrix ADC"}},
	{Type: "reverse-proxy", Label: "Reverse proxy", Category: "Edge & access", Icon: "arrow-left-right", Variants: []string{"edge", "ingress controller", "sidecar"},
		Tech: []string{"nginx", "Traefik", "Caddy", "Envoy", "HAProxy", "Apache httpd", "ingress-nginx"}},
	{Type: "api-gateway", Label: "API gateway", Category: "Edge & access", Icon: "waypoints", Tech: []string{"Kong", "Apache APISIX", "Tyk", "KrakenD", "AWS API Gateway", "Envoy Gateway"}},

	// Network
	{Type: "router", Label: "Router", Category: "Network", Icon: "router",
		Variants: []string{"edge", "core", "distribution", "border (BGP)", "branch", "wireless", "virtual / cloud", "LTE / 5G"},
		Tech:     []string{"MikroTik RouterOS", "Cisco IOS-XE", "Cisco IOS-XR", "Juniper Junos", "VyOS", "pfSense", "OPNsense", "Ubiquiti EdgeRouter", "Huawei VRP", "FRRouting", "BIRD"}},
	{Type: "switch", Label: "Switch", Category: "Network", Icon: "network", Variants: []string{"L2 access", "L3", "core", "distribution", "top-of-rack", "virtual switch"},
		Tech: []string{"Cisco Catalyst", "Cisco Nexus", "Arista EOS", "Juniper EX", "MikroTik CRS", "Ubiquiti UniFi", "HPE Aruba", "Open vSwitch"}},
	{Type: "firewall", Label: "Firewall", Category: "Network", Icon: "brick-wall", Variants: []string{"perimeter", "next-generation (NGFW)", "internal segmentation", "host firewall", "cloud security group"},
		Tech: []string{"FortiGate", "Palo Alto", "Check Point", "Cisco ASA / FTD", "pfSense", "OPNsense", "Sophos", "nftables", "iptables", "firewalld", "AWS Security Group"}},
	{Type: "vpn-gateway", Label: "VPN gateway", Category: "Network", Icon: "lock-keyhole", Variants: []string{"site-to-site", "remote access", "mesh / overlay"},
		Tech: []string{"WireGuard", "OpenVPN", "IPsec (strongSwan)", "Cisco AnyConnect", "FortiGate SSL VPN", "ocserv", "Tailscale", "Headscale", "NetBird", "GlobalProtect"}},
	{Type: "nat-gateway", Label: "NAT gateway", Category: "Network", Icon: "arrow-right-left", Tech: []string{"AWS NAT Gateway", "iptables MASQUERADE", "router NAT"}},
	{Type: "wifi", Label: "Wi-Fi", Category: "Network", Icon: "wifi", Variants: []string{"access point", "controller"}, Tech: []string{"Ubiquiti UniFi", "Cisco Meraki", "Aruba", "MikroTik CAP", "TP-Link Omada"}},
	{Type: "bastion", Label: "Bastion / jump host", Category: "Network", Icon: "door-open", Tech: []string{"OpenSSH", "Teleport", "Boundary", "Guacamole"}},
	{Type: "forward-proxy", Label: "Forward proxy", Category: "Network", Icon: "arrow-up-right", Tech: []string{"Squid", "Zscaler", "tinyproxy"}},

	// Compute
	{Type: "server", Label: "Server", Category: "Compute", Icon: "server", Variants: []string{"bare metal", "dedicated", "VPS"},
		Tech: []string{"Ubuntu", "Debian", "RHEL", "Rocky Linux", "AlmaLinux", "Windows Server"}},
	{Type: "vm", Label: "Virtual machine", Category: "Compute", Icon: "monitor", Tech: []string{"Ubuntu", "Debian", "RHEL", "Rocky Linux", "Windows Server", "ArvanCloud Cloud Server", "EC2", "Compute Engine", "Hetzner Cloud"}},
	{Type: "hypervisor", Label: "Hypervisor", Category: "Compute", Icon: "layers", Tech: []string{"Proxmox VE", "VMware ESXi", "Hyper-V", "KVM / libvirt", "XCP-ng", "OpenStack"}},
	{Type: "container-host", Label: "Container host", Category: "Compute", Icon: "container", Tech: []string{"Docker", "Docker Swarm", "Podman", "containerd", "Nomad", "ArvanCloud Container (PaaS)"}},
	{Type: "k8s-cluster", Label: "Kubernetes cluster", Category: "Compute", Icon: "boxes", Variants: []string{"self-managed", "managed"},
		Tech: []string{"Kubernetes", "k3s", "RKE2", "OpenShift", "Talos", "EKS", "GKE", "AKS", "ArvanCloud Managed Kubernetes", "MicroK8s"}},
	{Type: "serverless", Label: "Serverless function", Category: "Compute", Icon: "zap", Tech: []string{"AWS Lambda", "Cloudflare Workers", "Cloud Functions", "Knative", "OpenFaaS"}},

	// Data
	{Type: "database", Label: "Database", Category: "Data", Icon: "database", Variants: []string{"primary", "replica", "cluster", "managed"},
		Tech: []string{"PostgreSQL", "MySQL", "MariaDB", "MongoDB", "ClickHouse", "SQL Server", "Oracle", "Cassandra", "CockroachDB", "Patroni", "ArvanCloud DBaaS"}},
	{Type: "cache", Label: "Cache", Category: "Data", Icon: "gauge", Tech: []string{"Redis", "Valkey", "Memcached", "KeyDB", "Varnish"}},
	{Type: "queue", Label: "Queue / stream", Category: "Data", Icon: "list-ordered", Tech: []string{"Kafka", "RabbitMQ", "NATS", "Redis Streams", "Pulsar", "ActiveMQ", "SQS"}},
	{Type: "object-storage", Label: "Object storage", Category: "Data", Icon: "package", Tech: []string{"S3", "MinIO", "Ceph RGW", "Google Cloud Storage", "Azure Blob", "ArvanCloud Object Storage"}},
	{Type: "file-storage", Label: "File / block storage", Category: "Data", Icon: "hard-drive", Variants: []string{"NAS", "SAN", "distributed"},
		Tech: []string{"NFS", "SMB / CIFS", "CephFS", "GlusterFS", "TrueNAS", "Longhorn", "iSCSI"}},
	{Type: "search", Label: "Search", Category: "Data", Icon: "search", Tech: []string{"Elasticsearch", "OpenSearch", "Meilisearch", "Solr"}},

	// Platform & DevOps
	{Type: "git", Label: "Git hosting", Category: "Platform & DevOps", Icon: "git-branch", Tech: []string{"GitLab", "GitHub", "Gitea", "Bitbucket"}},
	{Type: "ci-cd", Label: "CI/CD", Category: "Platform & DevOps", Icon: "workflow", Variants: []string{"CI runner", "CD / GitOps"},
		Tech: []string{"GitLab CI", "GitHub Actions", "Jenkins", "Argo CD", "Flux", "Tekton", "Drone", "TeamCity"}},
	{Type: "registry", Label: "Container / artifact registry", Category: "Platform & DevOps", Icon: "archive", Tech: []string{"Harbor", "GitLab Registry", "GHCR", "Docker Hub", "Nexus", "Artifactory", "ECR"}},
	{Type: "monitoring", Label: "Monitoring", Category: "Platform & DevOps", Icon: "activity", Tech: []string{"Prometheus", "Grafana", "VictoriaMetrics", "Zabbix", "Netdata", "Datadog", "Nagios"}},
	{Type: "logging", Label: "Logging", Category: "Platform & DevOps", Icon: "scroll-text", Tech: []string{"Loki", "Elasticsearch / ELK", "OpenSearch", "Graylog", "Fluent Bit", "Vector"}},
	{Type: "tracing", Label: "Tracing", Category: "Platform & DevOps", Icon: "route", Tech: []string{"Jaeger", "Tempo", "Zipkin", "OpenTelemetry Collector"}},
	{Type: "alerting", Label: "Alerting / on-call", Category: "Platform & DevOps", Icon: "bell-ring", Tech: []string{"Alertmanager", "PagerDuty", "Opsgenie", "Grafana OnCall"}},
	{Type: "secrets", Label: "Secrets", Category: "Platform & DevOps", Icon: "key-round", Tech: []string{"HashiCorp Vault", "OpenBao", "AWS Secrets Manager", "SOPS", "Sealed Secrets", "Infisical"}},
	{Type: "identity", Label: "Identity / SSO", Category: "Platform & DevOps", Icon: "fingerprint", Tech: []string{"Keycloak", "Active Directory", "FreeIPA", "Okta", "Entra ID", "Authentik", "OpenLDAP"}},
	{Type: "backup", Label: "Backup", Category: "Platform & DevOps", Icon: "history", Tech: []string{"Restic", "Borg", "Velero", "Veeam", "Proxmox Backup Server", "pgBackRest"}},
	{Type: "mail", Label: "Mail", Category: "Platform & DevOps", Icon: "mail", Tech: []string{"Postfix", "Exim", "Mailcow", "Amazon SES", "Mailgun"}},

	// External
	{Type: "cloud-provider", Label: "Cloud provider", Category: "External", Icon: "cloud", Variants: []string{"IaaS", "PaaS", "managed services"},
		Tech: []string{"ArvanCloud", "AWS", "Google Cloud", "Azure", "Hetzner", "DigitalOcean", "OVHcloud", "Oracle Cloud", "Alibaba Cloud", "Iran Server", "ParsPack", "Asiatech"}},
	{Type: "external", Label: "External service", Category: "External", Icon: "plug", Hint: "SaaS, partner API, payment provider…"},
	{Type: "generic", Label: "Other component", Category: "External", Icon: "box"},
}

var LinkKinds = []LinkKind{
	{Kind: "lan", Label: "LAN / Ethernet", Category: "network"},
	{Kind: "vlan-trunk", Label: "VLAN trunk", Category: "network"},
	{Kind: "wan", Label: "WAN", Category: "network"},
	{Kind: "internet", Label: "Internet", Category: "network"},
	{Kind: "fiber", Label: "Fiber", Category: "network"},
	{Kind: "mpls", Label: "MPLS", Category: "network"},
	{Kind: "wireless", Label: "Wireless", Category: "network"},
	{Kind: "direct-connect", Label: "Dedicated cloud link", Category: "network"},
	{Kind: "peering", Label: "Peering", Category: "network"},
	{Kind: "ipsec", Label: "IPsec tunnel", Category: "tunnel"},
	{Kind: "wireguard", Label: "WireGuard tunnel", Category: "tunnel", Port: "51820"},
	{Kind: "openvpn", Label: "OpenVPN tunnel", Category: "tunnel", Port: "1194"},
	{Kind: "ssl-vpn", Label: "SSL VPN", Category: "tunnel", Port: "443"},
	{Kind: "gre", Label: "GRE", Category: "tunnel"},
	{Kind: "vxlan", Label: "VXLAN", Category: "tunnel", Port: "4789"},
	{Kind: "bgp", Label: "BGP session", Category: "routing", Port: "179"},
	{Kind: "ospf", Label: "OSPF", Category: "routing"},
	{Kind: "static-route", Label: "Static route", Category: "routing"},
	{Kind: "http", Label: "HTTP", Category: "app", Port: "80"},
	{Kind: "https", Label: "HTTPS", Category: "app", Port: "443"},
	{Kind: "grpc", Label: "gRPC", Category: "app"},
	{Kind: "websocket", Label: "WebSocket", Category: "app"},
	{Kind: "tcp", Label: "TCP", Category: "app"},
	{Kind: "udp", Label: "UDP", Category: "app"},
	{Kind: "dns", Label: "DNS", Category: "app", Port: "53"},
	{Kind: "smtp", Label: "SMTP", Category: "app", Port: "25"},
	{Kind: "sql", Label: "SQL", Category: "data"},
	{Kind: "redis", Label: "Redis protocol", Category: "data", Port: "6379"},
	{Kind: "amqp", Label: "AMQP", Category: "data", Port: "5672"},
	{Kind: "kafka", Label: "Kafka", Category: "data", Port: "9092"},
	{Kind: "nfs", Label: "NFS / SMB", Category: "data"},
	{Kind: "s3", Label: "S3 API", Category: "data"},
	{Kind: "replication", Label: "Replication", Category: "data"},
	{Kind: "backup", Label: "Backup", Category: "data"},
	{Kind: "ssh", Label: "SSH", Category: "mgmt", Port: "22"},
	{Kind: "rdp", Label: "RDP", Category: "mgmt", Port: "3389"},
	{Kind: "snmp", Label: "SNMP", Category: "mgmt", Port: "161"},
	{Kind: "syslog", Label: "Syslog / log shipping", Category: "mgmt"},
	{Kind: "metrics", Label: "Metrics scrape", Category: "mgmt"},
	{Kind: "deploy", Label: "Deploys to", Category: "mgmt"},
	{Kind: "other", Label: "Other", Category: "app"},
}

var ZoneKinds = []Option{
	{ID: "region", Label: "Region"}, {ID: "datacenter", Label: "Data center"}, {ID: "cloud", Label: "Cloud account"},
	{ID: "vpc", Label: "VPC / VNet"}, {ID: "subnet", Label: "Subnet"}, {ID: "vlan", Label: "VLAN"}, {ID: "dmz", Label: "DMZ"},
	{ID: "office", Label: "Office / site"}, {ID: "cluster", Label: "Cluster"}, {ID: "namespace", Label: "Namespace"},
	{ID: "environment", Label: "Environment"}, {ID: "zone", Label: "Other zone"},
}

// Providers are well-known clouds and hosting companies (zone provider).
var Providers = []Option{
	{ID: "ArvanCloud", Label: "ArvanCloud (ابرآروان)"}, {ID: "AWS", Label: "Amazon Web Services"}, {ID: "Google Cloud", Label: "Google Cloud"},
	{ID: "Azure", Label: "Microsoft Azure"}, {ID: "Hetzner", Label: "Hetzner"}, {ID: "DigitalOcean", Label: "DigitalOcean"},
	{ID: "OVHcloud", Label: "OVHcloud"}, {ID: "Oracle Cloud", Label: "Oracle Cloud"}, {ID: "Alibaba Cloud", Label: "Alibaba Cloud"},
	{ID: "Linode / Akamai", Label: "Linode (Akamai)"}, {ID: "Vultr", Label: "Vultr"}, {ID: "Scaleway", Label: "Scaleway"},
	{ID: "Cloudflare", Label: "Cloudflare"}, {ID: "Iran Server", Label: "Iran Server"}, {ID: "Pars Pack", Label: "ParsPack"},
	{ID: "Asiatech", Label: "Asiatech"}, {ID: "Afranet", Label: "Afranet"}, {ID: "on-prem", Label: "On-premises"},
}

var DeploymentKinds = []Option{
	{ID: "docker-compose", Label: "Docker Compose"}, {ID: "docker", Label: "Docker container"}, {ID: "kubernetes", Label: "Kubernetes workload"},
	{ID: "helm", Label: "Helm release"}, {ID: "systemd", Label: "systemd service"}, {ID: "package", Label: "OS package (apt/rpm)"},
	{ID: "binary", Label: "Binary / tarball"}, {ID: "static", Label: "Static site"}, {ID: "vm", Label: "VM image"},
	{ID: "serverless", Label: "Serverless"}, {ID: "cron", Label: "Cron job"}, {ID: "other", Label: "Other"},
}

// TypeOf returns the catalog entry of a node type.
func TypeOf(t string) (NodeType, bool) {
	for _, n := range NodeTypes {
		if n.Type == t {
			return n, true
		}
	}
	return NodeType{Type: t, Label: t, Category: "External", Icon: "box"}, false
}

func linkKind(k string) (LinkKind, bool) {
	for _, l := range LinkKinds {
		if l.Kind == k {
			return l, true
		}
	}
	return LinkKind{Kind: k, Label: k, Category: "app"}, false
}

func optionLabel(list []Option, id string) string {
	for _, o := range list {
		if o.ID == id {
			return o.Label
		}
	}
	return id
}
