import {
  Activity,
  Boxes,
  Cloud,
  Database,
  FileKey2,
  FileLock2,
  FileText,
  GitBranch,
  Globe,
  KeyRound,
  Layers,
  LayoutDashboard,
  Lock,
  Network,
  ScrollText,
  Search,
  Server,
  Shield,
  ShieldCheck,
  StickyNote,
  Ticket,
  Waypoints,
  Workflow,
} from "lucide-react";
import type { ReactNode } from "react";

const byIcon: Record<string, (c: string) => ReactNode> = {
  server: (c) => <Server className={c} />,
  layers: (c) => <Layers className={c} />,
  group: (c) => <Layers className={c} />,
  shield: (c) => <Shield className={c} />,
  network: (c) => <Network className={c} />,
  waypoints: (c) => <Waypoints className={c} />,
  globe: (c) => <Globe className={c} />,
  panel: (c) => <LayoutDashboard className={c} />,
  "layout-dashboard": (c) => <LayoutDashboard className={c} />,
  git: (c) => <GitBranch className={c} />,
  "git-branch": (c) => <GitBranch className={c} />,
  boxes: (c) => <Boxes className={c} />,
  kubernetes: (c) => <Boxes className={c} />,
  database: (c) => <Database className={c} />,
  cloud: (c) => <Cloud className={c} />,
  workflow: (c) => <Workflow className={c} />,
  activity: (c) => <Activity className={c} />,
  "scroll-text": (c) => <ScrollText className={c} />,
  search: (c) => <Search className={c} />,
};

const byKind: Record<string, string> = {
  Host: "server",
  HostGroup: "layers",
  WireGuard: "shield",
  Tailscale: "shield",
  NetBird: "shield",
  OpenVPN: "shield",
  OpenConnect: "shield",
  OpenFortiVPN: "shield",
  SocksProxy: "waypoints",
  Panel: "panel",
  GitRemote: "git",
  KubernetesCluster: "boxes",
  AnsibleProject: "workflow",
  Prometheus: "activity",
  Loki: "scroll-text",
  Elasticsearch: "search",
};

export function KindIcon({ kind, icon, className = "size-3.5" }: { kind: string; icon?: string; className?: string }) {
  const key = (icon && byIcon[icon] ? icon : byKind[kind]) || "cloud";
  return <>{(byIcon[key] ?? byIcon.cloud)(className)}</>;
}

export function ItemTypeIcon({ type, className = "size-3.5" }: { type: string; className?: string }) {
  switch (type) {
    case "ssh-key":
      return <KeyRound className={className} />;
    case "login":
      return <Lock className={className} />;
    case "token":
      return <Ticket className={className} />;
    case "kubeconfig":
      return <Boxes className={className} />;
    case "vpn":
      return <ShieldCheck className={className} />;
    case "certificate":
      return <FileKey2 className={className} />;
    case "file":
      return <FileLock2 className={className} />;
    case "note":
      return <StickyNote className={className} />;
    default:
      return <FileText className={className} />;
  }
}
