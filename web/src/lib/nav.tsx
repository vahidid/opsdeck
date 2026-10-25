import type { ReactNode } from "react";
import { Activity, BadgeCheck, Bot, Boxes, Container, Database, FileCode2, FileText, GitBranch, Globe, KeyRound, LayoutGrid, Network, ScrollText, Server, Settings, ShieldCheck, Sparkles, SquareTerminal, Workflow } from "lucide-react";

export type NavGroup = "workspace" | "infra" | "deploy" | "library" | "security";

// Groups of the sidebar, in order (the first has no heading).
export const NAV_GROUPS: { id: NavGroup; label: string }[] = [
  { id: "workspace", label: "Workspace" },
  { id: "infra", label: "Infrastructure" },
  { id: "deploy", label: "Deploy" },
  { id: "library", label: "Library" },
  { id: "security", label: "Security" },
];

// Sections of a workspace, in sidebar order.
export const NAV: { to: string; label: string; icon: ReactNode; group: NavGroup }[] = [
  { to: "overview", label: "Overview", icon: <LayoutGrid />, group: "workspace" },
  { to: "agent", label: "Agent", icon: <Bot />, group: "workspace" },
  { to: "topology", label: "Topology", icon: <Network />, group: "workspace" },
  { to: "resources", label: "Resources", icon: <Server />, group: "infra" },
  { to: "vpn", label: "VPN", icon: <ShieldCheck />, group: "infra" },
  { to: "terminal", label: "Terminal", icon: <SquareTerminal />, group: "infra" },
  { to: "logs", label: "Logs", icon: <ScrollText />, group: "infra" },
  { to: "databases", label: "Databases", icon: <Database />, group: "infra" },
  { to: "domains", label: "Domains", icon: <Globe />, group: "infra" },
  { to: "certificates", label: "Certificates", icon: <BadgeCheck />, group: "infra" },
  { to: "git", label: "Repositories", icon: <GitBranch />, group: "deploy" },
  { to: "docker", label: "Docker", icon: <Container />, group: "deploy" },
  { to: "kubernetes", label: "Kubernetes", icon: <Boxes />, group: "deploy" },
  { to: "ansible", label: "Ansible", icon: <Workflow />, group: "deploy" },
  { to: "docs", label: "Docs", icon: <FileText />, group: "library" },
  { to: "skills", label: "Skills", icon: <Sparkles />, group: "library" },
  { to: "scripts", label: "Scripts", icon: <FileCode2 />, group: "library" },
  { to: "vault", label: "Vault", icon: <KeyRound />, group: "security" },
  { to: "activity", label: "Activity", icon: <Activity />, group: "security" },
];

export const SETTINGS_NAV = { to: "settings", label: "Workspace settings", icon: <Settings /> };

// Sections that exist once per workspace (the terminal manages its own sessions).
export const SINGLETON_SECTIONS = new Set(["terminal"]);

export function sectionOf(path: string): string {
  return path.split(/[/?#]/)[0] ?? "";
}

export function sectionInfo(path: string) {
  const s = sectionOf(path);
  return s === "settings" ? SETTINGS_NAV : NAV.find((n) => n.to === s);
}
