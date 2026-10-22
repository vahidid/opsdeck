import type { ReactNode } from "react";
import { Activity, Bot, Boxes, Container, FileText, GitBranch, KeyRound, LayoutGrid, Network, ScrollText, Server, Settings, ShieldCheck, Sparkles, SquareTerminal } from "lucide-react";

// Sections of a workspace, in sidebar order.
export const NAV: { to: string; label: string; icon: ReactNode }[] = [
  { to: "overview", label: "Overview", icon: <LayoutGrid /> },
  { to: "agent", label: "Agent", icon: <Bot /> },
  { to: "resources", label: "Resources", icon: <Server /> },
  { to: "topology", label: "Topology", icon: <Network /> },
  { to: "vpn", label: "VPN", icon: <ShieldCheck /> },
  { to: "git", label: "Repositories", icon: <GitBranch /> },
  { to: "terminal", label: "Terminal", icon: <SquareTerminal /> },
  { to: "docker", label: "Docker", icon: <Container /> },
  { to: "kubernetes", label: "Kubernetes", icon: <Boxes /> },
  { to: "logs", label: "Logs", icon: <ScrollText /> },
  { to: "docs", label: "Docs", icon: <FileText /> },
  { to: "skills", label: "Skills", icon: <Sparkles /> },
  { to: "vault", label: "Vault", icon: <KeyRound /> },
  { to: "activity", label: "Activity", icon: <Activity /> },
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
