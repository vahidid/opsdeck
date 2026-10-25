import type { ReactNode } from "react";
import { AppWindow, Database, Globe, Layers } from "lucide-react";
import {
  siAnsible,
  siArgo,
  siCisco,
  siElasticsearch,
  siF5,
  siFortinet,
  siGit,
  siGithub,
  siGitlab,
  siGrafana,
  siKubernetes,
  siLetsencrypt,
  siLinux,
  siMariadb,
  siMongodb,
  siMysql,
  siOpensearch,
  siOpenvpn,
  siPaloaltonetworks,
  siPortainer,
  siPostgresql,
  siPrometheus,
  siRedis,
  siTailscale,
  siWireguard,
} from "simple-icons";
import type { SimpleIcon } from "simple-icons";
import type { Resource } from "../api/types";

// The add-resource catalog: products people know, each mapped onto one of the
// workspace kinds with the spec values that product needs (OpenConnect with
// protocol "gp" is GlobalProtect, Database with engine "redis" is Redis).
// Logos are the brands' marks from Simple Icons (CC0); products without one
// get a monogram in their colour.

export type Category = "servers" | "vpn" | "db" | "obs" | "code" | "k8s" | "web";

export const CATEGORIES: { id: Category; label: string }[] = [
  { id: "servers", label: "Servers" },
  { id: "vpn", label: "VPN & networks" },
  { id: "db", label: "Databases" },
  { id: "obs", label: "Observability" },
  { id: "code", label: "Code & deploy" },
  { id: "k8s", label: "Kubernetes" },
  { id: "web", label: "Web & DNS" },
];

export type Mark = { si: SimpleIcon } | { text: string } | { icon: ReactNode };

export type Product = {
  id: string;
  name: string;
  kind: string;
  category: Category;
  color: string;
  mark: Mark;
  caption: string;
  keywords?: string;
  spec?: Record<string, unknown>;
  defaultName?: string;
};

export const PRODUCTS: Product[] = [
  { id: "host", name: "Linux host", kind: "Host", category: "servers", color: "#FCC624", mark: { si: siLinux }, caption: "Any server over SSH", keywords: "ssh server vm ubuntu debian centos", defaultName: "app-1" },
  { id: "hostgroup", name: "Host group", kind: "HostGroup", category: "servers", color: "#3F4A5A", mark: { icon: <Layers /> }, caption: "Hosts by role or tag", keywords: "group", defaultName: "web" },

  { id: "wireguard", name: "WireGuard", kind: "WireGuard", category: "vpn", color: "#88171A", mark: { si: siWireguard }, caption: "wg-quick config", keywords: "wg vpn" },
  { id: "tailscale", name: "Tailscale", kind: "Tailscale", category: "vpn", color: "#242424", mark: { si: siTailscale }, caption: "Tailnet", keywords: "vpn mesh" },
  { id: "headscale", name: "Headscale", kind: "Tailscale", category: "vpn", color: "#3E4A59", mark: { text: "HS" }, caption: "Self-hosted Tailscale", keywords: "vpn tailscale self-hosted" },
  { id: "netbird", name: "NetBird", kind: "NetBird", category: "vpn", color: "#F68330", mark: { text: "NB" }, caption: "Mesh VPN", keywords: "vpn wiretrustee" },
  { id: "openvpn", name: "OpenVPN", kind: "OpenVPN", category: "vpn", color: "#EA7E20", mark: { si: siOpenvpn }, caption: ".ovpn profile", keywords: "vpn ovpn" },
  { id: "anyconnect", name: "Cisco AnyConnect", kind: "OpenConnect", category: "vpn", color: "#1BA0D7", mark: { si: siCisco }, caption: "Cisco Secure Client", keywords: "cisco vpn openconnect secure client asa", spec: { protocol: "anyconnect" } },
  { id: "fortigate", name: "FortiGate", kind: "OpenFortiVPN", category: "vpn", color: "#EE3124", mark: { si: siFortinet }, caption: "Fortinet SSL VPN", keywords: "fortinet forticlient vpn openfortivpn" },
  { id: "globalprotect", name: "GlobalProtect", kind: "OpenConnect", category: "vpn", color: "#F04E23", mark: { si: siPaloaltonetworks }, caption: "Palo Alto Networks", keywords: "palo alto vpn gp openconnect", spec: { protocol: "gp" } },
  { id: "pulse", name: "Ivanti Secure", kind: "OpenConnect", category: "vpn", color: "#C8102E", mark: { text: "IV" }, caption: "formerly Pulse Secure", keywords: "pulse juniper ivanti vpn openconnect", spec: { protocol: "pulse" } },
  { id: "f5", name: "F5 BIG-IP", kind: "OpenConnect", category: "vpn", color: "#E4002B", mark: { si: siF5 }, caption: "Edge Client · APM", keywords: "vpn openconnect bigip", spec: { protocol: "f5" } },
  { id: "ocserv", name: "ocserv", kind: "OpenConnect", category: "vpn", color: "#475569", mark: { text: "oc" }, caption: "OpenConnect server", keywords: "vpn openconnect anyconnect", spec: { protocol: "anyconnect" } },
  { id: "socks", name: "SOCKS5 proxy", kind: "SocksProxy", category: "vpn", color: "#52525B", mark: { text: "S5" }, caption: "Local or remote proxy", keywords: "proxy socks ssh -D" },

  { id: "postgres", name: "PostgreSQL", kind: "Database", category: "db", color: "#4169E1", mark: { si: siPostgresql }, caption: "Relational database", keywords: "postgres sql psql", spec: { engine: "postgres" }, defaultName: "shop-db" },
  { id: "mysql", name: "MySQL", kind: "Database", category: "db", color: "#4479A1", mark: { si: siMysql }, caption: "Relational database", keywords: "sql", spec: { engine: "mysql" } },
  { id: "mariadb", name: "MariaDB", kind: "Database", category: "db", color: "#003545", mark: { si: siMariadb }, caption: "Relational database", keywords: "sql mysql", spec: { engine: "mariadb" } },
  { id: "mongodb", name: "MongoDB", kind: "Database", category: "db", color: "#001E2B", mark: { si: siMongodb }, caption: "Document database", keywords: "mongo nosql", spec: { engine: "mongodb" } },
  { id: "redis", name: "Redis", kind: "Database", category: "db", color: "#FF4438", mark: { si: siRedis }, caption: "Key-value store", keywords: "cache valkey keydb", spec: { engine: "redis" } },
  { id: "otherdb", name: "Other database", kind: "Database", category: "db", color: "#3F4A5A", mark: { icon: <Database /> }, caption: "Any TCP database", keywords: "sql", spec: { engine: "other" } },

  { id: "prometheus", name: "Prometheus", kind: "Prometheus", category: "obs", color: "#E6522C", mark: { si: siPrometheus }, caption: "Metrics · PromQL", keywords: "metrics victoria thanos mimir" },
  { id: "loki", name: "Loki", kind: "Loki", category: "obs", color: "#F2CC0C", mark: { text: "Lk" }, caption: "Logs · LogQL", keywords: "logs grafana" },
  { id: "elastic", name: "Elasticsearch", kind: "Elasticsearch", category: "obs", color: "#005571", mark: { si: siElasticsearch }, caption: "Logs · search", keywords: "elk kibana logs" },
  { id: "opensearch", name: "OpenSearch", kind: "Elasticsearch", category: "obs", color: "#005EB8", mark: { si: siOpensearch }, caption: "Logs · search", keywords: "logs elastic" },
  { id: "grafana", name: "Grafana", kind: "Panel", category: "obs", color: "#F46800", mark: { si: siGrafana }, caption: "Dashboards · web panel", keywords: "dashboards panel", defaultName: "grafana" },

  { id: "git", name: "Git repository", kind: "GitRemote", category: "code", color: "#F05032", mark: { si: siGit }, caption: "Any Git URL", keywords: "repo gitea forgejo bitbucket" },
  { id: "gitlab", name: "GitLab", kind: "GitRemote", category: "code", color: "#FC6D26", mark: { si: siGitlab }, caption: "Project or group", keywords: "repo git", spec: { provider: "gitlab" } },
  { id: "github", name: "GitHub", kind: "GitRemote", category: "code", color: "#181717", mark: { si: siGithub }, caption: "Repository", keywords: "repo git", spec: { provider: "github" } },
  { id: "ansible", name: "Ansible project", kind: "AnsibleProject", category: "code", color: "#EE0000", mark: { si: siAnsible }, caption: "Playbooks & inventories", keywords: "playbook" },
  { id: "portainer", name: "Portainer", kind: "DeployPlatform", category: "code", color: "#13BEF9", mark: { si: siPortainer }, caption: "Stacks & containers", keywords: "deploy docker", spec: { platform: "portainer" } },
  { id: "argocd", name: "Argo CD", kind: "DeployPlatform", category: "code", color: "#EF7B4D", mark: { si: siArgo }, caption: "GitOps for Kubernetes", keywords: "deploy gitops k8s argo", spec: { platform: "argocd" } },
  { id: "komodo", name: "Komodo", kind: "DeployPlatform", category: "code", color: "#1E293B", mark: { text: "Ko" }, caption: "Build & deploy", keywords: "deploy", spec: { platform: "komodo" } },

  { id: "k8s", name: "Kubernetes cluster", kind: "KubernetesCluster", category: "k8s", color: "#326CE5", mark: { si: siKubernetes }, caption: "kubeconfig · any distro", keywords: "k8s k3s eks gke aks openshift rancher cluster", defaultName: "prod-cluster" },

  { id: "panel", name: "Web panel / API", kind: "Panel", category: "web", color: "#0E7490", mark: { icon: <AppWindow /> }, caption: "Any URL the agent may call", keywords: "http api dashboard url" },
  { id: "domain", name: "Domain", kind: "Domain", category: "web", color: "#2563EB", mark: { icon: <Globe /> }, caption: "DNS zone", keywords: "dns zone cloudflare route53 arvancloud" },
  { id: "cert", name: "TLS certificate", kind: "Certificate", category: "web", color: "#003A70", mark: { si: siLetsencrypt }, caption: "Let's Encrypt · ACME", keywords: "ssl acme letsencrypt https" },
];

export const SUGGESTED = ["host", "postgres", "k8s", "grafana", "gitlab", "anyconnect"];

const byId = new Map(PRODUCTS.map((p) => [p.id, p]));
export const productById = (id: string) => byId.get(id);

// productFor finds the catalog product a saved resource came from.
export function productFor(r: Pick<Resource, "kind" | "name" | "spec">): Product | undefined {
  const s = r.spec ?? {};
  const v = (k: string) => String(s[k] ?? "").toLowerCase();
  switch (r.kind) {
    case "Database":
      return byId.get({ postgres: "postgres", mysql: "mysql", mariadb: "mariadb", mongodb: "mongodb", redis: "redis" }[v("engine")] ?? "otherdb");
    case "OpenConnect": {
      const p = v("protocol");
      return byId.get(p === "gp" ? "globalprotect" : p === "pulse" || p === "nc" ? "pulse" : p === "f5" ? "f5" : p === "fortinet" ? "fortigate" : "anyconnect");
    }
    case "DeployPlatform":
      return byId.get(v("platform") || "portainer");
    case "GitRemote": {
      const p = v("provider");
      const url = v("url");
      if (p === "github" || url.includes("github.com")) return byId.get("github");
      if (p === "gitlab" || url.includes("gitlab")) return byId.get("gitlab");
      return byId.get("git");
    }
    case "Tailscale":
      return byId.get(v("controlUrl") ? "headscale" : "tailscale");
    case "Elasticsearch":
      return byId.get(v("url").includes("opensearch") || r.name.toLowerCase().includes("opensearch") ? "opensearch" : "elastic");
    case "Panel":
      return byId.get(v("url").includes("grafana") || r.name.toLowerCase().includes("grafana") ? "grafana" : "panel");
    default:
      return PRODUCTS.find((p) => p.kind === r.kind);
  }
}
