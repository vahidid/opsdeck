import type { ReactNode } from "react";
import {
  Activity,
  Archive,
  Bot,
  Boxes,
  Brain,
  ChartLine,
  Container,
  Database,
  FileClock,
  FilePen,
  FileText,
  FolderOpen,
  GitBranch,
  Globe,
  Hourglass,
  KeyRound,
  ListChecks,
  ListTodo,
  MessageCircleQuestionMark,
  Network,
  PackageSearch,
  Plug,
  ScrollText,
  Search,
  Shield,
  Sparkles,
  SquareTerminal,
  Workflow,
  Wrench,
} from "lucide-react";

type Meta = { icon: (c: string) => ReactNode; label?: string };

const META: Record<string, Meta> = {
  exec: { icon: (c) => <SquareTerminal className={c} /> },
  local_exec: { icon: (c) => <SquareTerminal className={c} /> },
  read_file: { icon: (c) => <FileText className={c} /> },
  local_read_file: { icon: (c) => <FileText className={c} /> },
  list_dir: { icon: (c) => <FolderOpen className={c} /> },
  local_list_dir: { icon: (c) => <FolderOpen className={c} /> },
  local_search: { icon: (c) => <Search className={c} /> },
  write_file: { icon: (c) => <FilePen className={c} /> },
  edit_file: { icon: (c) => <FilePen className={c} /> },
  local_write_file: { icon: (c) => <FilePen className={c} /> },
  local_edit_file: { icon: (c) => <FilePen className={c} /> },
  write_doc: { icon: (c) => <FilePen className={c} /> },
  read_doc: { icon: (c) => <FileText className={c} /> },
  write_report: { icon: (c) => <FileClock className={c} /> },
  save_artifact: { icon: (c) => <Archive className={c} /> },
  read_output: { icon: (c) => <ScrollText className={c} /> },
  knowledge: { icon: (c) => <Sparkles className={c} /> },
  load_skill: { icon: (c) => <Sparkles className={c} /> },
  memory: { icon: (c) => <Brain className={c} /> },
  web_search: { icon: (c) => <Search className={c} />, label: "web search" },
  web_fetch: { icon: (c) => <Globe className={c} />, label: "web page" },
  software_lookup: { icon: (c) => <PackageSearch className={c} /> },
  http_request: { icon: (c) => <Plug className={c} /> },
  dns: { icon: (c) => <Globe className={c} />, label: "DNS" },
  metrics_query: { icon: (c) => <ChartLine className={c} /> },
  logs_query: { icon: (c) => <ScrollText className={c} /> },
  net_check: { icon: (c) => <Activity className={c} /> },
  wait_for: { icon: (c) => <Hourglass className={c} /> },
  ask_user: { icon: (c) => <MessageCircleQuestionMark className={c} />, label: "question" },
  todo: { icon: (c) => <ListTodo className={c} />, label: "checklist" },
  delegate: { icon: (c) => <Bot className={c} />, label: "subagent" },
  tasks: { icon: (c) => <ListChecks className={c} /> },
  docker: { icon: (c) => <Container className={c} /> },
  kubernetes: { icon: (c) => <Boxes className={c} /> },
  database: { icon: (c) => <Database className={c} /> },
  repo: { icon: (c) => <GitBranch className={c} /> },
  repo_files: { icon: (c) => <GitBranch className={c} /> },
  repo_write: { icon: (c) => <GitBranch className={c} /> },
  merge_request: { icon: (c) => <GitBranch className={c} /> },
  git_sync: { icon: (c) => <GitBranch className={c} /> },
  vpn: { icon: (c) => <Shield className={c} /> },
  topology: { icon: (c) => <Network className={c} /> },
  ansible: { icon: (c) => <Workflow className={c} /> },
  ansible_run: { icon: (c) => <Workflow className={c} /> },
  ansible_vault: { icon: (c) => <KeyRound className={c} /> },
  vault_list: { icon: (c) => <KeyRound className={c} /> },
  vault_add_item: { icon: (c) => <KeyRound className={c} /> },
  vault_import_ssh_key: { icon: (c) => <KeyRound className={c} /> },
};

export function ToolIcon({ tool, className = "size-3.5 text-muted" }: { tool: string; className?: string }) {
  const m = META[tool] ?? (tool.startsWith("mcp__") ? { icon: (c: string) => <Plug className={c} /> } : undefined);
  return <>{m ? m.icon(className) : <Wrench className={className} />}</>;
}

export function toolLabel(tool: string): string {
  if (tool.startsWith("mcp__")) return tool.replace(/^mcp__/, "").replace("__", " · ");
  return META[tool]?.label ?? tool;
}

/** the most telling part of a call's input, for its one-line summary */
export function inputSummary(tool: string, input: unknown): string {
  if (!input || typeof input !== "object") return "";
  const o = input as Record<string, unknown>;
  const s = (k: string) => (typeof o[k] === "string" ? (o[k] as string) : "");
  switch (tool) {
    case "web_search":
      return s("query") + (s("site") ? `  site:${s("site")}` : "");
    case "web_fetch":
      return s("url");
    case "delegate":
      return `${s("agent")}: ${s("task")}`;
    case "ask_user":
      return s("question");
    case "todo":
      return "";
    case "software_lookup":
      return [s("action"), s("product") || s("package") || s("repo"), s("version") || s("since")].filter(Boolean).join(" ");
    case "http_request":
      return `${(s("method") || "GET").toUpperCase()} ${s("path") || "/"}`;
    case "metrics_query":
    case "logs_query":
      return s("query") || s("action");
    case "wait_for":
      return `${s("condition")} ${s("target") || s("command") || s("name")}`.trim();
    case "tasks":
      return `${s("action")} ${s("id")}`.trim();
  }
  if (typeof o.command === "string") return o.command;
  for (const k of ["path", "playbook", "action", "query", "url", "name", "resource", "target", "module"]) {
    if (typeof o[k] === "string" && o[k]) return `${k === "action" ? "" : k + ": "}${String(o[k])}`;
  }
  return "";
}

export function inputCommand(input: unknown): string | null {
  if (input && typeof input === "object") {
    const o = input as Record<string, unknown>;
    if (typeof o.command === "string") return o.command;
    if (typeof o.cmd === "string") return o.cmd;
  }
  return null;
}
