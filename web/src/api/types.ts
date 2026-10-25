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
  /** display names of models, as the provider or agent CLI listed them */
  modelNames?: Record<string, string>;
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
  agent?: AgentSettings;
  web?: WebSettings;
  vpn: { elevate: "" | "auto" | "sudo" | "pkexec" | "none"; browser: string; codes: VpnCodes };
  git: { name: string; email: string };
  updates?: { noCheck: boolean };
};
export type HookDef = {
  name: string;
  event: "before_tool" | "after_tool" | "turn_end";
  command: string;
  tool?: string;
  risk?: "" | Risk;
  environment?: string[];
  workspace?: string;
  timeoutSec?: number;
  disabled?: boolean;
};
export type AgentSettings = { autoServerChanges: boolean; subagentModel?: string; noSubagents?: boolean; hooks?: HookDef[] };
export type WebSettings = {
  access: "" | "off" | "search" | "fetch";
  search: "" | "duckduckgo" | "searxng" | "brave" | "tavily";
  searchUrl: string;
  keyRef: string;
  proxy: string;
  domains: string[];
  noDefaultDomains: boolean;
};
export type UpdateRelease = { version: string; name: string; notes: string; url: string; published: string };
export type UpdateStatus = {
  current: string;
  latest?: UpdateRelease;
  available: boolean;
  checkedAt?: string;
  error?: string;
  /** how it installs here; empty: by hand (see manual) */
  method?: "installer" | "portable" | "app" | "deb" | "rpm" | "binary";
  manual?: string;
  state?: "downloading" | "installing" | "restarting" | "done" | "failed";
  progress: number;
  message?: string;
};
export type ModelInfo = { id: string; provider: string; providerName: string; model: string; name: string; label: string };
/** Models a provider offers: reported by the agent CLI or the API, or the built-in list when it could not be asked. */
export type ProviderModels = { models: { id: string; name: string; description?: string }[]; source: "cli" | "api" | "builtin"; error?: string };

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
  settings: {
    defaultUser: string;
    defaultAuth: string;
    defaultModel: string;
    noBuiltinSkills?: boolean;
    /** the agent's web access here ("" = the app default) */
    web?: "" | "off" | "search" | "fetch";
    /** route web requests through one of the workspace's networks */
    webVia?: string;
    /** models sessions may use (patterns like ollama/*; empty = any) */
    models?: string[];
  };
  permissions: { rules: PermissionRule[] };
  mcpServers: McpServer[];
  hooks?: HookDef[];
  /** indexes of hooks that came from the folder and wait for trust */
  untrustedHooks?: number[];
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
  type: "string" | "number" | "bool" | "text" | "secretRef" | "list" | "select" | "resourceRef" | "gitConnection" | "dnsConnection";
  required?: boolean;
  placeholder?: string;
  help?: string;
  options?: string[];
  refKinds?: string[];
  itemTypes?: string[];
  default?: string;
  showIf?: string;
  advanced?: boolean;
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
export type Skill = {
  name: string;
  description: string;
  path: string;
  category?: string;
  files?: string[];
  builtin?: boolean;
  source?: string;
  license?: string;
};
export type ScriptParam = {
  name: string;
  description?: string;
  type?: "" | "string" | "int" | "bool" | "secret";
  default?: string;
  required?: boolean;
};
export type Script = {
  name: string;
  description: string;
  version: number;
  file?: string;
  os?: string[];
  when?: string[];
  tags?: string[];
  sudo?: boolean;
  risk?: "" | "change" | "destructive";
  timeoutSec?: number;
  check?: string;
  params?: ScriptParam[];
  approved?: string;
  createdBy?: string;
  updatedBy?: string;
  updated?: string;
  scope: "user" | "workspace";
  hash: string;
  status: "draft" | "approved";
  overrides?: boolean;
  body?: string;
  versions?: number[];
};
export type ScriptResult = {
  host: string;
  exitCode: number;
  skipped?: boolean;
  stdout: string;
  stderr: string;
  durationMs: number;
  error?: string;
  log?: string;
};
export type ScriptRun = {
  time: string;
  script: string;
  version: number;
  hash: string;
  scope: string;
  host: string;
  params?: Record<string, string>;
  exitCode: number;
  skipped?: boolean;
  durationMs: number;
  error?: string;
  actor: string;
  session?: string;
  log?: string;
};
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
  authWindow?: boolean;
  code?: CodeWait;
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

// A one-time code (SMS, email) a VPN login waits for.
export type CodeWait = { message?: string; label?: string; choices?: string[]; retry?: boolean; since: string; expires: string; sources?: string[] };
export type PendingCode = CodeWait & { workspace: string; name: string; kind: string };
export type VpnCodes = { ntfyUrl: string; ntfyTokenRef: string; command: string; pattern: string };

export const NETWORK_KINDS = ["WireGuard", "Tailscale", "NetBird", "OpenVPN", "OpenConnect", "OpenFortiVPN", "SocksProxy"];
export type VpnDriver = { kind: string; label: string; modes: string[]; missing: Record<string, string> };
export type VpnDrivers = { drivers: VpnDriver[]; privileged: boolean; elevate: string; os: string; browser: string };
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

export type Container = {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  health?: string;
  ports: string;
  created: string;
  command?: string;
  mounts?: string;
  networks?: string;
  project?: string;
  service?: string;
  stack?: string;
};
export type DockerImage = { id: string; repository: string; tag: string; size: string | number; created: string; containers?: string };
export type DockerStat = { id: string; name: string; cpu: string; mem: string; memPct: string; net: string; block: string; pids?: string };
export type SwarmInfo = { state: string; manager: boolean; nodeId?: string; nodeAddr?: string; nodes: number; managers: number; error?: string };
export type DockerInfo = {
  serverVersion: string;
  os: string;
  arch: string;
  cpus: number;
  memTotal: number;
  containers: number;
  running: number;
  paused: number;
  stopped: number;
  images: number;
  storageDriver: string;
  rootDir: string;
  swarm: SwarmInfo;
  compose?: string;
  sudo: boolean;
  warnings?: string[];
};
export type DockerUsage = { type: string; total: string; active: string; size: string; reclaimable: string };
export type DockerProcess = { pid: string; user: string; cpu: string; mem: string; started: string; time: string; command: string };
export type ComposeProject = { name: string; status: string; configFiles: string[]; workingDir: string };
export type ComposeContainer = { id: string; name: string; service: string; image: string; state: string; status: string; health?: string; exitCode: number; ports?: string };
export type ComposeFile = { path: string; kind: "compose" | "env"; exists: boolean };
export type SwarmService = { id: string; name: string; mode: string; replicas: string; running: number; desired: number; image: string; ports?: string; stack?: string };
export type SwarmTask = { id: string; name: string; image: string; node: string; desiredState: string; currentState: string; error?: string; ports?: string };
export type SwarmStack = { name: string; services: number };
export type SwarmNode = { id: string; hostname: string; status: string; availability: string; managerStatus?: string; engineVersion?: string; self?: boolean };
export type DockerVolume = {
  name: string;
  driver: string;
  scope: string;
  mountpoint: string;
  labels?: Record<string, string>;
  project?: string;
  stack?: string;
  containers: string[];
  anonymous?: boolean;
};
export type VolumeEntry = { name: string; type: "file" | "dir" | "link" | "other"; size: number; modified: string; mode: string; owner: string };
export type DockerNetwork = { id: string; name: string; driver: string; scope: string; internal?: boolean; builtin?: boolean; project?: string; stack?: string; created: string };
export type DockerEvent = { time: string; type: string; action: string; id: string; name: string; scope?: string; attrs?: Record<string, string> };

export type KubeResourceType = {
  id: string;
  group: string;
  version: string;
  resource: string;
  kind: string;
  namespaced: boolean;
  verbs: string[];
  shortNames?: string[];
  category: string;
};
export type KubeColumn = { name: string; type: string; format?: string; description?: string; priority: number };
export type KubeTableRow = {
  name: string;
  namespace?: string;
  uid: string;
  created: string;
  labels?: Record<string, string>;
  owner?: string;
  deleting?: boolean;
  cells: unknown[];
};
export type KubeTable = { type: KubeResourceType; columns: KubeColumn[]; rows: KubeTableRow[] };
export type KubeObject = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any
export type KubeEvent = { time: string; type: string; reason: string; object: string; namespace?: string; message: string; count: number; source?: string; uid: string };
export type KubeUsage = { cpu: number; memory: number };
export type KubeNodeSummary = {
  name: string;
  ready: boolean;
  unschedulable?: boolean;
  roles: string[];
  version: string;
  internalIP?: string;
  os?: string;
  runtime?: string;
  capacity: KubeUsage;
  used?: KubeUsage;
  requested: KubeUsage;
  pods: number;
  maxPods: number;
  pressure?: string[];
  created: string;
};
export type KubeProblem = { severity: "error" | "warning"; kind: string; namespace?: string; name: string; reason: string; message?: string };
export type KubeOverview = {
  version: string;
  platform: string;
  namespace?: string;
  namespaces: number;
  nodes: KubeNodeSummary[];
  pods: { total: number; running: number; pending: number; succeeded: number; failed: number; unknown: number; restarts: number };
  workloads: { deployments: number; statefulsets: number; daemonsets: number; jobs: number; cronjobs: number; services: number };
  problems: KubeProblem[];
  warnings: KubeEvent[];
  metrics: boolean;
};
export type KubeApplyResult = { kind: string; name: string; namespace?: string; action: "created" | "configured" | "unchanged" | "failed"; diff?: string; error?: string; note?: string };
export type KubeRevision = { revision: number; name: string; created: string; images: string[]; cause?: string; current?: boolean; replicas: number };
export type HelmRelease = { name: string; namespace: string; revision: number; status: string; chart: string; chartVersion: string; appVersion?: string; updated: string; description?: string };
export type HelmDetail = HelmRelease & { values: string; manifest: string; notes?: string; history: HelmRelease[] };

export type SessionMode = "plan" | "ask" | "auto" | "full";
export type SessionState = "idle" | "running" | "waiting_approval" | "waiting_input";
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
  /** opsdeck run: nothing waits for an approval */
  headless?: boolean;
  forkedFrom?: string;
};

/** set on events of a subagent: the delegate call it runs in */
type EventBase = { id: string; ts: string; parent?: string };
/** workspace knowledge opsdeck found for a message and gave the agent with it */
export type RecallItem = { kind: "skill" | "doc" | "report" | "script" | "memory"; ref: string; title?: string };

export type QuestionOption = { label: string; description?: string };
export type TodoItem = { text: string; status: "pending" | "in_progress" | "done" | "skipped" };
export type TaskInfo = {
  id: string;
  callId: string;
  tool: string;
  target: string;
  command: string;
  notifyOn?: string;
  started: string;
  ended?: string;
  state: "running" | "done" | "failed" | "stopped";
  exitCode: number;
  matched?: string;
  error?: string;
  lines: number;
};

/** structured data a tool attaches to its result for the chat (never sent to the model) */
export type ToolView =
  | { kind: "search"; query: string; backend: string; results: { title: string; url: string; snippet: string; domain: string }[] }
  | { kind: "page"; url: string; final: string; title: string; status: number; contentType: string; total: number; offset: number; shown: number }
  | { kind: "http"; method: string; url: string; status: number; durationMs: number; contentType: string; bytes: number }
  | { kind: "timeseries"; query: string; step: string; total: number; series: { labels: string; points: [number, number][]; last: number; max: number }[] }
  | { kind: "vector"; query: string; total: number; rows: { labels: string; value: string }[] }
  | { kind: "alerts"; alerts: { name: string; state: string; since: string; severity?: string; summary?: string; labels: string }[] }
  | { kind: "logs"; query: string; from: string; to: string; lines: { ts: string; labels?: string; line: string; level?: string }[] }
  | { kind: "eol"; product: string; version: string; cycles: { cycle: string; release?: string; latest?: string; eol?: string | boolean; support?: string | boolean; lts?: boolean; match?: boolean }[] }
  | { kind: "vulns"; package: string; vulns: { id: string; aliases?: string[]; summary: string; severity?: string; fixed?: string[]; published?: string }[] }
  | { kind: "releases"; repo: string; releases: { tag: string; name?: string; published?: string; prerelease?: boolean; url: string }[] }
  | { kind: "dns"; zone: string; records: DNSRecord[] };

export type AgentEvent =
  | (EventBase & { type: "user"; text: string; msgIndex?: number; command?: string; prompt?: string })
  | (EventBase & { type: "notification"; text: string })
  | (EventBase & { type: "shell"; command: string; target: string; label: string; output: string; exitCode: number; durationMs: number })
  | (EventBase & { type: "recall"; items: RecallItem[]; preloaded?: string })
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
      reasons?: string[];
      agent?: string;
    })
  | (EventBase & {
      type: "tool_result";
      callId: string;
      output: string;
      isError: boolean;
      durationMs: number;
      view?: ToolView;
      /** the whole output is kept (it was cut for the model) */
      outputId?: string;
      outputSize?: number;
    })
  | (EventBase & { type: "tool_progress"; callId: string; text: string })
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
      reasons?: string[];
      preview: string;
      grantLabel?: string;
      /** set when the card asks the user for a password (an Ansible Vault password) */
      secret?: { label: string; rememberLabel?: string };
      /** the hosts of a change over several hosts (a canary rollout can be chosen) */
      hosts?: string[];
      /** the agent asked for a canary rollout */
      canary?: boolean;
      /** "next": a further step of a running call (the rest of a canary rollout) */
      stage?: string;
      title?: string;
    })
  | (EventBase & { type: "approval_resolved"; approvalId: string; decision: string; note?: string; canary?: boolean })
  | (EventBase & { type: "question"; questionId: string; callId: string; question: string; options: QuestionOption[]; multiple: boolean; allowText: boolean })
  | (EventBase & { type: "question_answered"; questionId: string; choices?: string[]; text?: string; skipped?: boolean })
  | (EventBase & { type: "todo"; callId: string; items: TodoItem[] })
  | (EventBase & {
      type: "subagent";
      callId: string;
      agent: string;
      phase: "start" | "end";
      model?: string;
      readOnly?: boolean;
      state?: string;
      steps?: number;
      toolCalls?: number;
      inputTokens?: number;
      outputTokens?: number;
    })
  | (EventBase & { type: "task"; task: TaskInfo })
  | (EventBase & { type: "hook"; callId?: string; hook: string; event: string; outcome: "ok" | "deny" | "ask" | "allow" | "error"; message?: string })
  | (EventBase & { type: "status"; state: SessionState; usage?: Usage })
  | (EventBase & { type: "error"; message: string })
  | (EventBase & { type: "compaction"; summary: string });

/** a workspace's slash commands, subagents and access, for the chat */
export type WorkspaceCommand = { name: string; description: string; argumentHint?: string; prompt: string; path: string };
export type SubagentDef = { name: string; description: string; readOnly: boolean; model?: string; tools?: string[]; path?: string };
export type HarnessInfo = { commands: WorkspaceCommand[]; subagents: SubagentDef[]; web: "off" | "search" | "fetch"; search?: string; local: boolean; models: string[] };

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

// Git repositories (GitRemote resources) and their working copies.
export type RepoChange = { path: string; orig?: string; status: "modified" | "added" | "deleted" | "renamed" | "untracked" | "conflict" | "typechange"; staged?: boolean };
export type RepoStatus = {
  cloned: boolean;
  branch: string;
  head: string;
  upstream?: string;
  ahead: number;
  behind: number;
  default?: string;
  changes: RepoChange[];
  busy?: string;
};
export type RepoCommit = { hash: string; short: string; author: string; email: string; date: string; subject: string; refs?: string };
export type RepoSummary = {
  name: string;
  url: string;
  web: string;
  provider: string;
  via?: string;
  environment?: string;
  description?: string;
  docs: boolean;
  auth: "token" | "deployKey" | "connector" | "none";
  connection?: string;
  error?: string;
  status?: RepoStatus;
  last?: RepoCommit;
};
export type RepoBranch = { name: string; remote?: boolean; current?: boolean; commit: string; upstream?: string; track?: string; subject: string; date: string };
export type RepoFile = { path: string; ref?: string; text: string; size: number; binary?: boolean; truncated?: boolean };
export type RepoCheck = { ok: boolean; defaultBranch: string; error?: string; provider?: string; canPush?: boolean; webUrl?: string; apiError?: string };
export type MergeRequest = {
  id: number;
  title: string;
  description: string;
  state: "open" | "merged" | "closed";
  draft: boolean;
  source: string;
  target: string;
  author: string;
  url: string;
  mergeable?: string;
  checks?: "success" | "pending" | "failed" | "";
  created: string;
  updated: string;
};

// Git connectors: GitHub / GitLab accounts connected on this computer.
export type GitConnection = {
  id: string;
  provider: "github" | "gitlab";
  url: string;
  host: string;
  apiUrl: string;
  caCert: string;
  credential: string;
  method: "token" | "oauth" | "gh" | "glab";
  oauthClientId: string;
  login: string;
  name: string;
  created: string;
  tokenPage: string;
};
export type ConnInput = { provider: "github" | "gitlab"; url: string; apiUrl?: string; caCert?: string; method?: "token" | "cli"; token?: string; clientId?: string };
export type ConnOptions = {
  url: string;
  host: string;
  tokenPage: string;
  clientId: string;
  oauthAppPage: string;
  cli: { tool: string; ready: boolean; reason?: string };
};
export type OAuthStart = { flow: string; userCode: string; verificationUri: string; verificationUriComplete?: string; expiresIn: number };
export type OAuthStatus = { status: "pending" | "done" | "error"; error: string; connection?: GitConnection };
export type RemoteRepo = {
  fullName: string;
  name: string;
  description: string;
  httpUrl: string;
  sshUrl: string;
  webUrl: string;
  defaultBranch: string;
  private: boolean;
  archived: boolean;
  canPush: boolean;
  updated: string;
};

// DNS connectors: Cloudflare / ArvanCloud accounts connected on this
// computer, and the zones and records of a workspace's domains.
export type DNSProvider = {
  id: "cloudflare" | "arvancloud";
  label: string;
  apiUrl: string;
  tokenPage: string;
  dashboard: string;
  tokenHelp: string;
  tokenPlaceholder: string;
  proxy: string;
  ttls?: number[];
  comments: boolean;
};
export type DNSConnection = {
  id: string;
  provider: "cloudflare" | "arvancloud";
  apiUrl: string;
  proxy: string;
  credential: string;
  method: string;
  account: string;
  zones: number;
  created: string;
  label: string;
  tokenPage: string;
  dashboard: string;
};
export type DNSConnInput = { id?: string; provider: "cloudflare" | "arvancloud"; apiUrl?: string; proxy?: string; token: string };
export type DNSZone = {
  id: string;
  name: string;
  status: string;
  paused?: boolean;
  plan?: string;
  nameServers: string[];
  currentNameServers?: string[];
  account?: string;
  modified?: string;
  url?: string;
};
export type DNSRecord = {
  id: string;
  type: string;
  name: string;
  fqdn: string;
  content: string;
  ttl: number;
  priority?: number;
  proxied: boolean;
  proxiable: boolean;
  comment?: string;
  locked?: boolean;
  modified?: string;
  raw?: unknown;
};
export type DNSRecordInput = { type: string; name: string; content: string; ttl: number; priority: number; proxied?: boolean; comment?: string };
export type DomainInfo = {
  name: string;
  zone: string;
  environment?: string;
  protected?: boolean;
  description?: string;
  connection?: string;
  provider?: "cloudflare" | "arvancloud";
  account?: string;
  info?: DNSZone;
  error?: string;
  proxyLabel?: string;
  ttls?: number[];
  comments: boolean;
};
export const DNS_RECORD_TYPES = ["A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA", "PTR"];

// Topology: the workspace's infrastructure diagram (topology.yaml).
export type TopoBox = { x: number; y: number; w?: number; h?: number };
export type TopoZone = { id: string; label?: string; kind?: string; parent?: string; cidr?: string; provider?: string; notes?: string };
export type TopoDeployment = { name: string; kind?: string; image?: string; ports?: string[]; replicas?: number; repo?: string; path?: string; notes?: string };
export type TopoNode = {
  id: string;
  type: string;
  variant?: string;
  label?: string;
  zone?: string;
  vendor?: string;
  tech?: string[];
  resource?: string;
  addresses?: string[];
  environment?: string;
  deployments?: TopoDeployment[];
  tags?: string[];
  notes?: string;
};
export type TopoLink = {
  id: string;
  from: string;
  to: string;
  kind?: string;
  label?: string;
  port?: string;
  encrypted?: boolean;
  bidirectional?: boolean;
  bandwidth?: string;
  vlan?: string;
  notes?: string;
};
export type TopologyDoc = { version: number; zones: TopoZone[]; nodes: TopoNode[]; links: TopoLink[]; layout?: Record<string, TopoBox> | null };
export type TopoNodeType = { type: string; label: string; category: string; icon: string; variants?: string[]; tech?: string[]; hint?: string };
export type TopoLinkKind = { kind: string; label: string; category: string; port?: string };
export type TopoCatalog = {
  nodeTypes: TopoNodeType[];
  linkKinds: TopoLinkKind[];
  zoneKinds: { id: string; label: string }[];
  deploymentKinds: { id: string; label: string }[];
  providers?: { id: string; label: string }[];
};

// ---------------------------------------------------------------------------
// Ansible

export type AnsibleVersion = { installed: boolean; version?: string; path?: string; python?: string; lint: boolean; error?: string };
export type AnsibleProject = {
  name: string;
  environment?: string;
  description?: string;
  source: "folder" | "repo";
  repo?: string;
  path?: string;
  root: string;
  external: boolean;
  exists: boolean;
  via?: string;
  inventory?: string;
  inventories: string[];
  playbooks: string[];
  vaultFiles: number;
  inlineVars: number;
  vaultIds: string[];
  unlocked: string[];
  saved: string[];
  remembered: string[];
  config: boolean;
  requirements: string[];
  files: number;
  error?: string;
  ansible: AnsibleVersion;
};
export type AnsibleFileType = "playbook" | "inventory" | "vars" | "role" | "template" | "config" | "requirements" | "file";
export type AnsibleFile = { path: string; size: number; modified: string; type: AnsibleFileType; encrypted?: boolean; vaultId?: string; inline?: number };
export type AnsibleInline = { key: string; line: number; end: number; vaultId: string };
export type AnsibleFileContent = {
  path: string;
  text: string;
  size: number;
  binary?: boolean;
  truncated?: boolean;
  encrypted?: boolean;
  vaultId?: string;
  inline?: AnsibleInline[];
};
export type AnsibleRunKind = "playbook" | "adhoc" | "galaxy" | "lint";
export type AnsibleRunOptions = {
  kind: AnsibleRunKind;
  playbook?: string;
  inventory?: string;
  limit?: string;
  tags?: string;
  skipTags?: string;
  extraVars?: string;
  check?: boolean;
  diff?: boolean;
  verbosity?: number;
  forks?: number;
  startAt?: string;
  become?: boolean;
  pattern?: string;
  module?: string;
  args?: string;
  requirements?: string;
  force?: boolean;
};
export type AnsibleRecap = { host: string; ok: number; changed: number; unreachable: number; failed: number; skipped: number; rescued: number; ignored: number };
export type AnsibleRunStatus = "running" | "ok" | "failed" | "unreachable" | "canceled" | "error" | "interrupted";
export type AnsibleRun = {
  id: string;
  workspace: string;
  project: string;
  options: AnsibleRunOptions;
  command: string;
  actor: string;
  started: string;
  ended?: string;
  status: AnsibleRunStatus;
  exitCode: number;
  recap?: AnsibleRecap[];
  error?: string;
  logLines: number;
  hosts?: string[];
  durationMs?: number;
};
export type AnsibleInventory = {
  inventory: string;
  graph: string;
  groups: Record<string, string[]>;
  children: Record<string, string[]>;
  hosts: string[];
  generated?: string;
  error?: string;
};

export type AnsibleModule = { name: string; description: string };
export type AnsibleModuleOption = {
  name: string;
  type?: string;
  elements?: string;
  required?: boolean;
  default?: unknown;
  choices?: unknown[];
  aliases?: string[];
  description?: string;
};
export type AnsibleModuleDoc = { name: string; short: string; description?: string; options: AnsibleModuleOption[]; freeForm?: boolean; examples?: string };

// ---------------------------------------------------------------- databases

export type DbEngine = "postgres" | "mysql" | "mongodb" | "redis";
export type DbInfo = {
  info: { engine: DbEngine; version: string; user: string; database: string; address: string };
  path: string[];
  environment: string;
  protected: boolean;
  /** a change script runs as one transaction (SQL); MongoDB and Redis run statement by statement */
  atomic: boolean;
};
/** A table or view, a MongoDB collection, or a Redis key. */
export type DbTable = { schema: string; name: string; type: string; rows: number; bytes: number; /** what shows its content */ query: string };
export type DbColumn = { name: string; type: string; nullable: boolean; default: string | null; primary: boolean };
export type DbIndex = { name: string; definition: string; unique: boolean; primary: boolean };
export type DbTableInfo = {
  schema: string;
  name: string;
  kind: "table" | "collection" | "key";
  columns: DbColumn[];
  indexes: DbIndex[];
  properties?: { name: string; value: string }[];
  note?: string;
};
export type DbRisk = "read" | "change" | "destructive";
export type DbResult = {
  statement: string;
  columns?: { name: string; type: string }[];
  rows?: unknown[][];
  truncated?: boolean;
  /** -1 for result sets */
  rowsAffected: number;
  durationMs: number;
  error?: string;
  note?: string;
};
export type DbAnswer = { risk: DbRisk; reasons: string[]; results: DbResult[]; database: string; durationMs: number; committed: boolean; error?: string };
/** What a script that is not a read needs to be confirmed (409 not_read). */
export type DbNotRead = { risk: DbRisk; reasons: string[]; statements: number; environment: string; protected: boolean; atomic: boolean };
