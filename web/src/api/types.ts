// Types mirroring docs/api.md

export type VaultStatus = { initialized: boolean; unlocked: boolean; autoLockMinutes: number };

export type ItemTypeField = { name: string; label: string; secret: boolean; multiline: boolean; required: boolean };
export type ItemType = { type: string; label: string; fields: ItemTypeField[] };

export type Field = { name: string; secret: boolean; value?: string; hasValue: boolean };
export type ItemPolicy = { agentUse: boolean; confirmOnUse: boolean };
export type Item = {
  id: string;
  scope: string;
  type: string;
  name: string;
  tags: string[];
  notes: string;
  fields: Field[];
  policy: ItemPolicy;
  ref: string;
  publicKey?: string;
  fingerprint?: string;
  created: string;
  updated: string;
  revision: number;
};
export type ItemInput = {
  scope: string;
  type: string;
  name: string;
  tags?: string[];
  notes?: string;
  fields: { name: string; value?: string | null; secret: boolean }[];
  policy?: ItemPolicy;
};

export type AgentKind = "claude-code" | "codex" | "gemini-cli" | "acp";
export type ProviderConfig = {
  id: string;
  type: "anthropic" | "openai" | AgentKind;
  name: string;
  baseUrl: string;
  apiKeyRef: string;
  models: string[];
  command?: string;
  args?: string[];
  env?: Record<string, string>;
};
export type InstalledAgent = { kind: AgentKind; name: string; command: string; args: string[] | null; path: string; version: string; login: string; models: string[] };
export const AGENT_KINDS: AgentKind[] = ["claude-code", "codex", "gemini-cli", "acp"];
export const isAgentKind = (t: string): t is AgentKind => (AGENT_KINDS as string[]).includes(t);
export type LocalAccess = { disabled: boolean; allowedDirs: string[]; autoReadCommands: boolean };
export type Settings = {
  autoLockMinutes: number;
  defaultModel: string;
  providers: ProviderConfig[];
  localAccess: LocalAccess;
  vpn: { elevate: "" | "auto" | "sudo" | "pkexec" | "none" };
};
export type ModelInfo = { id: string; provider: string; model: string; label: string };

export type WorkspaceSummary = {
  name: string;
  title: string;
  description: string;
  color: string;
  tags: string[];
  path: string;
  resourceCount: number;
  hostCount: number;
};
export type Environment = { name: string; protected: boolean };
export type Risk = "read" | "change" | "destructive";
export type PermissionRule = {
  effect: "allow" | "ask" | "deny";
  risk?: Risk;
  tool?: string;
  environment?: string[];
  match?: string;
};
export type McpServer = { name: string; command: string; args: string[]; env: Record<string, string> };
export type Workspace = WorkspaceSummary & {
  environments: Environment[];
  settings: { defaultUser: string; defaultAuth: string; defaultModel: string };
  permissions: { rules: PermissionRule[] };
  mcpServers: McpServer[];
};

export type Resource = {
  kind: string;
  name: string;
  environment?: string;
  tags?: string[];
  via?: string;
  description?: string;
  docs?: string;
  spec: Record<string, unknown>;
};

export type KindField = {
  key: string;
  label: string;
  type: "string" | "number" | "bool" | "text" | "secretRef" | "list" | "select" | "resourceRef";
  required?: boolean;
  placeholder?: string;
  help?: string;
  options?: string[];
  refKinds?: string[];
  itemTypes?: string[];
  default?: string;
};
export type KindSchema = {
  kind: string;
  label: string;
  category: "compute" | "network" | "service" | "cluster";
  icon: string;
  canVia: boolean;
  fields: KindField[];
};

export type ValidationIssue = { level: "error" | "warning"; resource: string; message: string };
export type TopologyNode = { id: string; kind: string; name: string; environment: string; label: string };
export type Topology = { nodes: TopologyNode[]; edges: { from: string; to: string }[] };
export type PathHop = { kind: string; name: string; label: string };

export type DocInfo = { path: string; title: string; size: number; updated: string };
export type Skill = { name: string; description: string; path: string };
export type McpStatus = {
  name: string;
  status: string;
  error: string;
  tools: { name: string; description: string; readOnly: boolean }[];
};

export type NetworkStatus = {
  name: string;
  kind: string;
  mode: "" | "userspace" | "system";
  status: "down" | "connecting" | "auth" | "up" | "error";
  error?: string;
  authUrl?: string;
  since?: string;
  address?: string;
  interface?: string;
  server?: string;
  detail?: string;
  proxy?: string;
  lastHandshake?: string;
  rxBytes: number;
  txBytes: number;
  missing?: string;
  usedBy: string[];
};

export const NETWORK_KINDS = ["WireGuard", "Tailscale", "NetBird", "OpenVPN", "OpenConnect", "OpenFortiVPN", "SocksProxy"];
export type VpnDriver = { kind: string; label: string; modes: string[]; missing: Record<string, string> };
export type VpnDrivers = { drivers: VpnDriver[]; privileged: boolean; elevate: string; os: string };
export type LogLine = { time: string; text: string };

export type CheckResult = { reachable: boolean; latencyMs: number; error: string; path: PathHop[] | string[] | null };
export type HostStats = {
  hostname: string;
  os: string;
  kernel: string;
  uptimeSec: number;
  load: number[];
  cpus: number;
  memTotal: number;
  memUsed: number;
  swapTotal: number;
  swapUsed: number;
  disks: { mount: string; size: number; used: number }[];
};
export type ExecResult = { stdout: string; stderr: string; exitCode: number; durationMs: number };
export type FileEntry = { name: string; dir: boolean; size: number; mode: string; modified: string };
export type DirListing = { path: string; entries: FileEntry[] };
export type FileContent = { path: string; content: string; truncated: boolean };

export type Forward = {
  id: string;
  workspace: string;
  target: string;
  remote: string;
  localPort: number;
  url: string;
  created: string;
};

export type Container = { id: string; name: string; image: string; state: string; status: string; ports: string; created: string };
export type DockerImage = { id: string; repository: string; tag: string; size: string | number; created: string };
export type DockerStat = { id: string; name: string; cpu: string; mem: string; memPct: string; net: string; block: string };

export type KubeRow = Record<string, unknown> & { name: string; namespace?: string };

export type SessionMode = "plan" | "ask" | "auto";
export type SessionState = "idle" | "running" | "waiting_approval";
export type Usage = { inputTokens: number; outputTokens: number };
export type SessionInfo = {
  id: string;
  workspace: string;
  title: string;
  model: string;
  mode: SessionMode;
  state: SessionState;
  created: string;
  updated: string;
  usage: Usage;
};

type EventBase = { id: string; ts: string };
export type AgentEvent =
  | (EventBase & { type: "user"; text: string })
  | (EventBase & { type: "assistant_delta"; messageId: string; text: string })
  | (EventBase & { type: "assistant"; messageId: string; text: string })
  | (EventBase & {
      type: "tool_call";
      callId: string;
      tool: string;
      input: unknown;
      risk: Risk;
      target: string;
      environment: string;
    })
  | (EventBase & { type: "tool_result"; callId: string; output: string; isError: boolean; durationMs: number })
  | (EventBase & {
      type: "approval_request";
      approvalId: string;
      callId: string;
      tool: string;
      input: unknown;
      risk: Risk;
      target: string;
      environment: string;
      reason: string;
      preview: string;
      grantLabel?: string;
    })
  | (EventBase & { type: "approval_resolved"; approvalId: string; decision: string; note?: string })
  | (EventBase & { type: "status"; state: SessionState; usage?: Usage })
  | (EventBase & { type: "error"; message: string })
  | (EventBase & { type: "compaction"; summary: string });

export type AuditEntry = {
  seq: number;
  ts: string;
  workspace: string;
  actor: string;
  action: string;
  target: string;
  detail: unknown;
  result: string;
  hash: string;
};
export type AuditVerify = { ok: boolean; entries: number; brokenAt: number | null };
