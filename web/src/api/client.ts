import type {
  AccessKey,
  LogLine,
  VpnDrivers,
  PendingCode,
  VpnCodes,
  MergeRequest,
  TopologyDoc,
  TopoCatalog,
  GitConnection,
  DNSProvider,
  DNSConnection,
  DNSConnInput,
  DNSZone,
  DNSRecord,
  DNSRecordInput,
  DomainInfo,
  DeployPlatformType,
  DeployPlatformInfo,
  DeployTarget,
  DeployDetail,
  DeployActionInput,
  DeployResult,
  AcmeCA,
  CertStatus,
  CertDetail,
  CertJob,
  CertDeploy,
  CertDeployResult,
  ConnInput,
  ConnOptions,
  OAuthStart,
  OAuthStatus,
  RemoteRepo,
  RepoBranch,
  RepoCheck,
  RepoCommit,
  RepoFile,
  RepoStatus,
  RepoSummary,
  AgentEvent,
  AuditEntry,
  AuditVerify,
  CheckResult,
  Container,
  DirListing,
  DocInfo,
  DockerImage,
  DockerStat,
  DockerInfo,
  DockerUsage,
  DockerProcess,
  ComposeProject,
  ComposeContainer,
  ComposeFile,
  SwarmInfo,
  SwarmService,
  SwarmTask,
  SwarmStack,
  SwarmNode,
  DockerVolume,
  VolumeEntry,
  DockerNetwork,
  ExecResult,
  FileContent,
  Forward,
  HostStats,
  Item,
  ItemInput,
  ItemType,
  KindSchema,
  KubeResourceType,
  KubeTable,
  KubeObject,
  KubeEvent,
  KubeUsage,
  KubeOverview,
  KubeApplyResult,
  KubeRevision,
  HelmRelease,
  HelmDetail,
  McpStatus,
  ModelInfo,
  NetworkStatus,
  PathHop,
  Resource,
  SessionInfo,
  HarnessInfo,
  TaskInfo,
  SessionMode,
  Settings,
  Skill,
  Script,
  ScriptResult,
  ScriptRun,
  Topology,
  ValidationIssue,
  VaultStatus,
  UpdateStatus,
  Workspace,
  WorkspaceSummary,
  InstalledAgent,
  DbAnswer,
  DbInfo,
  DbTable,
  DbTableInfo,
  DbNotRead,
  ProviderModels,
  ProviderConfig,
  AnsibleProject,
  AnsibleFile,
  AnsibleFileContent,
  AnsibleInventory,
  AnsibleRun,
  AnsibleRunOptions,
  AnsibleModule,
  AnsibleModuleDoc,
} from "./types";

export class ApiError extends Error {
  status: number;
  code: string;
  /** the error response's other fields (e.g. vaultId) */
  data: Record<string, unknown>;
  constructor(status: number, message: string, code = "", data: Record<string, unknown> = {}) {
    super(message);
    this.status = status;
    this.code = code;
    this.data = data;
  }
}

export type GlobalSignal = "unauthorized" | "locked" | "uninitialized";
type Listener = (s: GlobalSignal) => void;
const listeners = new Set<Listener>();
export function onGlobalSignal(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
function emit(s: GlobalSignal) {
  listeners.forEach((l) => l(s));
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (e) {
    throw new ApiError(0, "Cannot reach opsdeck server. Is `opsdeck serve` running?", "network");
  }
  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const obj = (data && typeof data === "object" ? data : {}) as { error?: string; code?: string };
    const msg = obj.error || (typeof data === "string" && data) || `${res.status} ${res.statusText}`;
    const code = obj.code || "";
    if (res.status === 401) emit("unauthorized");
    if (res.status === 423) emit(code === "vault_uninitialized" ? "uninitialized" : "locked");
    throw new ApiError(res.status, msg, code, obj as Record<string, unknown>);
  }
  return data as T;
}

const get = <T>(p: string) => request<T>("GET", p);
const post = <T>(p: string, b?: unknown) => request<T>("POST", p, b ?? {});
const put = <T>(p: string, b?: unknown) => request<T>("PUT", p, b ?? {});
const del = <T>(p: string) => request<T>("DELETE", p);

const e = encodeURIComponent;
const qs = (o: Record<string, string | number | boolean | undefined | null>) => {
  const parts = Object.entries(o)
    .filter(([, v]) => v !== undefined && v !== null && v !== "")
    .map(([k, v]) => `${e(k)}=${e(String(v))}`);
  return parts.length ? `?${parts.join("&")}` : "";
};
const W = (ws: string) => `/api/workspaces/${e(ws)}`;
const H = (ws: string, host: string) => `${W(ws)}/hosts/${e(host)}`;
const C = (ws: string, c: string) => `${W(ws)}/clusters/${e(c)}`;
const D = (ws: string, db: string) => `${W(ws)}/databases/${e(db)}`;
const S = (ws: string, id: string) => `${W(ws)}/sessions/${e(id)}`;
const R = (ws: string, repo: string) => `${W(ws)}/repos/${e(repo)}`;
const AP = (ws: string, p: string) => `${W(ws)}/ansible/p/${e(p)}`;
const DK = (ws: string, host: string) => `${H(ws, host)}/docker`;
const KO = (ws: string, c: string, type: string, ns: string, name: string) => `${C(ws, c)}/object/${e(type)}/${e(ns || "_")}/${e(name)}`;
type Out = { output: string };

const arr = <T>(p: Promise<T[] | null>) => p.then((v) => v ?? []);

export const api = {
  // auth
  login: (token: string) => post<{ ok: boolean }>("/api/auth/login", { token }),
  authCheck: () => get<{ ok: boolean }>("/api/auth/check"),

  // vault
  vaultStatus: () => get<VaultStatus>("/api/vault/status"),
  vaultInit: (password: string) => post<VaultStatus>("/api/vault/init", { password }),
  vaultUnlock: (password: string) => post<VaultStatus>("/api/vault/unlock", { password }),
  vaultLock: () => post<VaultStatus>("/api/vault/lock"),
  vaultChangePassword: (oldPassword: string, newPassword: string) =>
    post("/api/vault/password", { oldPassword, newPassword }),
  itemTypes: () => arr(get<ItemType[]>("/api/vault/types")),
  items: (scope: string) => arr(get<Item[]>(`/api/vault/items${qs({ scope })}`)),
  createItem: (i: ItemInput) => post<Item>("/api/vault/items", i),
  updateItem: (id: string, i: ItemInput) => put<Item>(`/api/vault/items/${e(id)}`, i),
  deleteItem: (id: string) => del(`/api/vault/items/${e(id)}`),
  reveal: (id: string, field: string) => post<{ value: string }>(`/api/vault/items/${e(id)}/reveal`, { field }),
  totp: (id: string) => get<{ code: string; period: number; remaining: number }>(`/api/vault/items/${e(id)}/totp`),
  sshGenerate: (b: { scope: string; name: string; algorithm: string; comment: string; tags: string[] }) =>
    post<Item>("/api/vault/ssh/generate", b),
  sshImport: (b: { scope: string; name: string; privateKey: string; passphrase: string; tags: string[] }) =>
    post<Item>("/api/vault/ssh/import", b),
  generatePassword: (length: number, symbols: boolean) =>
    post<{ password: string }>("/api/vault/password-generator", { length, symbols }),

  // settings
  settings: () => get<Settings>("/api/settings"),
  accessKeys: () => get<{ keys: AccessKey[]; endpoint: string }>("/api/access-keys"),
  createAccessKey: (b: { label: string; workspace: string; mode: string }) => post<{ key: AccessKey; secret: string; endpoint: string }>("/api/access-keys", b),
  updateAccessKey: (id: string, b: { label?: string; mode?: string }) => request<AccessKey>("PATCH", `/api/access-keys/${id}`, b),
  revokeAccessKey: (id: string) => del<{ ok: boolean }>(`/api/access-keys/${id}`),
  saveSettings: (s: Settings) => put<Settings>("/api/settings", s),
  updateStatus: (refresh = false) => get<UpdateStatus>(`/api/update${refresh ? "?refresh=1" : ""}`),
  installUpdate: () => post<UpdateStatus>("/api/update/install"),
  detectAgents: () => arr(get<InstalledAgent[]>("/api/agents/detect")),
  models: () => arr(get<ModelInfo[]>("/api/models")),
  providerModels: (p: ProviderConfig) => post<ProviderModels>("/api/providers/models", p),

  // workspaces
  workspaces: () => arr(get<WorkspaceSummary[]>("/api/workspaces")),
  createWorkspace: (b: { name: string; title: string; description: string; color: string }) =>
    post<WorkspaceSummary>("/api/workspaces", b),
  importWorkspace: (path: string) => post<WorkspaceSummary>("/api/workspaces/import", { path }),
  /** the workspace and its vault scope as one file encrypted with password */
  exportWorkspace: (ws: string, password: string) => post<unknown>(`${W(ws)}/export`, { password }),
  importWorkspaceFile: (bundle: unknown, password: string) =>
    post<{ workspace: WorkspaceSummary; items: number; skipped: number }>("/api/workspaces/import-file", { bundle, password }),
  workspace: (ws: string) => get<Workspace>(W(ws)),
  saveWorkspace: (ws: string, w: Workspace) => put<Workspace>(W(ws), w),
  deleteWorkspace: (ws: string, purgeVault: boolean) => del(`${W(ws)}${qs({ purgeVault: purgeVault ? 1 : undefined })}`),
  kinds: () => arr(get<KindSchema[]>("/api/kinds")),
  resources: (ws: string) => arr(get<Resource[]>(`${W(ws)}/resources`)),
  createResource: (ws: string, r: Resource) => post<Resource>(`${W(ws)}/resources`, r),
  updateResource: (ws: string, kind: string, name: string, r: Resource) =>
    put<Resource>(`${W(ws)}/resources/${e(kind)}/${e(name)}`, r),
  deleteResource: (ws: string, kind: string, name: string) => del(`${W(ws)}/resources/${e(kind)}/${e(name)}`),
  validate: (ws: string) => arr(get<ValidationIssue[]>(`${W(ws)}/validate`)),
  topology: (ws: string) => get<Topology>(`${W(ws)}/topology`),
  resolve: (ws: string, kind: string, name: string) =>
    get<{ path: PathHop[] }>(`${W(ws)}/resolve/${e(kind)}/${e(name)}`),
  docs: (ws: string) => arr(get<DocInfo[]>(`${W(ws)}/docs`)),
  doc: (ws: string, path: string) => get<{ path: string; content: string }>(`${W(ws)}/docs/file${qs({ path })}`),
  saveDoc: (ws: string, path: string, content: string) => put(`${W(ws)}/docs/file`, { path, content }),
  deleteDoc: (ws: string, path: string) => del(`${W(ws)}/docs/file${qs({ path })}`),
  artifacts: (ws: string) => arr(get<{ path: string; size: number; updated: string }[]>(`${W(ws)}/artifacts`)),
  artifact: (ws: string, path: string) => get<{ path: string; content: string; truncated: boolean }>(`${W(ws)}/artifacts/file${qs({ path })}`),
  deleteArtifact: (ws: string, path: string) => del(`${W(ws)}/artifacts/file${qs({ path })}`),
  memory: (ws: string) => get<{ content: string }>(`${W(ws)}/memory`),
  saveMemory: (ws: string, content: string) => put(`${W(ws)}/memory`, { content }),
  skills: (ws: string) => arr(get<Skill[]>(`${W(ws)}/skills`)),
  skillLibrary: () => get<{ skills: Skill[]; attribution: string }>("/api/skills/library"),
  skillLibraryFile: (name: string, file = "") => get<{ content: string }>(`/api/skills/library/${e(name)}${qs({ file })}`),
  skillCopy: (ws: string, name: string) => post<{ path: string }>(`${W(ws)}/skills/${e(name)}/copy`),
  scripts: (ws: string) => get<{ user: Script[] | null; workspace: Script[] | null }>(`${W(ws)}/scripts`).then((r) => ({ user: r.user ?? [], workspace: r.workspace ?? [] })),
  script: (ws: string, scope: string, name: string, version?: number) => get<Script>(`${W(ws)}/scripts/${e(scope)}/${e(name)}${qs({ version })}`),
  saveScript: (ws: string, scope: string, name: string, s: Partial<Script>) => put<Script>(`${W(ws)}/scripts/${e(scope)}/${e(name)}`, s),
  deleteScript: (ws: string, scope: string, name: string) => del(`${W(ws)}/scripts/${e(scope)}/${e(name)}`),
  approveScript: (ws: string, scope: string, name: string, hash: string) => post<Script>(`${W(ws)}/scripts/${e(scope)}/${e(name)}/approve`, { hash }),
  copyScript: (ws: string, scope: string, name: string) => post<Script>(`${W(ws)}/scripts/${e(scope)}/${e(name)}/copy`),
  runScript: (ws: string, scope: string, name: string, b: { target: string; params: Record<string, string>; force: boolean; hash: string }) =>
    post<{ script: string; results: ScriptResult[] }>(`${W(ws)}/scripts/${e(scope)}/${e(name)}/run`, b),
  scriptRuns: (ws: string, script = "", host = "") => arr(get<ScriptRun[]>(`${W(ws)}/script-runs${qs({ script, host })}`)),
  mcp: (ws: string) => arr(get<McpStatus[]>(`${W(ws)}/mcp`)),

  // ansible
  ansibleProjects: (ws: string) => arr(get<AnsibleProject[]>(`${W(ws)}/ansible`)),
  ansibleProject: (ws: string, p: string) => get<AnsibleProject>(AP(ws, p)),
  ansibleCreate: (ws: string, b: { name: string; source: string; repo?: string; path?: string; inventory?: string; environment?: string; description?: string; via?: string; scaffold?: boolean }) =>
    post<AnsibleProject>(`${W(ws)}/ansible/projects`, b),
  ansibleFiles: (ws: string, p: string) => get<{ files: AnsibleFile[]; truncated: boolean }>(`${AP(ws, p)}/files`),
  ansibleFile: (ws: string, p: string, path: string) => get<AnsibleFileContent>(`${AP(ws, p)}/file${qs({ path })}`),
  ansibleWrite: (ws: string, p: string, path: string, content: string, create = false) => put(`${AP(ws, p)}/file`, { path, content, create }),
  ansibleDelete: (ws: string, p: string, path: string) => del(`${AP(ws, p)}/file${qs({ path })}`),
  ansibleRename: (ws: string, p: string, from: string, to: string) => post(`${AP(ws, p)}/rename`, { from, to }),
  ansibleSearch: (ws: string, p: string, q: string, regex = false) =>
    arr(get<{ path: string; line: number; text: string }[]>(`${AP(ws, p)}/search${qs({ q, regex: regex ? 1 : undefined })}`)),
  ansibleUnlock: (ws: string, p: string, b: { vaultId: string; password: string; save?: boolean; agentUse?: boolean }) =>
    post<{ ok: boolean; checked: boolean; saved: string }>(`${AP(ws, p)}/unlock`, b),
  ansibleLock: (ws: string, p: string, vaultId = "") => del(`${AP(ws, p)}/unlock${qs({ vaultId })}`),
  ansibleVault: <T = { ok: boolean }>(
    ws: string,
    p: string,
    action: "view" | "save" | "set" | "encrypt" | "decrypt" | "rekey" | "encrypt-string",
    b: { path?: string; text?: string; key?: string; value?: string; name?: string; vaultId?: string; password?: string; newPassword?: string; newVaultId?: string },
  ) => post<T>(`${AP(ws, p)}/vault/${action}`, b),
  ansibleInventory: (ws: string, p: string, inventory: string) => get<AnsibleInventory>(`${AP(ws, p)}/inventory${qs({ inventory })}`),
  ansibleHostVars: (ws: string, p: string, inventory: string, host: string) => get<{ vars: string }>(`${AP(ws, p)}/inventory/host${qs({ inventory, host })}`),
  ansibleCheck: (ws: string, p: string, b: { kind: string; playbook: string; inventory?: string; limit?: string; tags?: string }) =>
    post<{ ok: boolean; output: string; error?: string }>(`${AP(ws, p)}/check`, b),
  ansibleRun: (ws: string, p: string, o: AnsibleRunOptions & { password?: string; vaultId?: string }) => post<AnsibleRun>(`${AP(ws, p)}/runs`, o),
  ansibleRuns: (ws: string, project = "", limit = 100) => arr(get<AnsibleRun[]>(`${W(ws)}/ansible/runs${qs({ project, limit })}`)),
  ansibleRunInfo: (ws: string, id: string) => get<AnsibleRun>(`${W(ws)}/ansible/runs/${e(id)}`),
  ansibleRunCancel: (ws: string, id: string) => post(`${W(ws)}/ansible/runs/${e(id)}/cancel`),
  ansibleRunLogUrl: (ws: string, id: string, since = "") => `${W(ws)}/ansible/runs/${e(id)}/log${qs({ since })}`,
  ansibleRunLogTextUrl: (ws: string, id: string) => `${W(ws)}/ansible/runs/${e(id)}/log.txt`,
  ansibleModules: (ws: string, p: string, refresh = false) => arr(get<AnsibleModule[]>(`${AP(ws, p)}/modules${qs({ refresh: refresh ? 1 : undefined })}`)),
  ansibleModuleDoc: (ws: string, p: string, name: string) => get<AnsibleModuleDoc>(`${AP(ws, p)}/modules/doc${qs({ name })}`),
  trustMcp: (ws: string, name: string) => arr(post<McpStatus[]>(`${W(ws)}/mcp/${e(name)}/trust`)),

  // topology
  topologyCatalog: () => get<TopoCatalog>("/api/topology/catalog"),
  topologyDoc: (ws: string) => get<{ topology: TopologyDoc; rev: string; text: string }>(`${W(ws)}/diagram`),
  saveTopology: (ws: string, topology: TopologyDoc, rev: string, force = false) => put<{ rev: string; text: string }>(`${W(ws)}/diagram`, { topology, rev, force }),
  topologyImport: (ws: string, topology: TopologyDoc) => post<{ topology: TopologyDoc; added: string[] }>(`${W(ws)}/diagram/import-inventory`, { topology }),
  topologyExportUrl: (ws: string, format: "md" | "yaml") => `${W(ws)}/diagram/export?format=${format}`,

  // git connectors
  gitConnections: () => arr(get<GitConnection[]>("/api/git/connections")),
  gitConnOptions: (c: ConnInput) => post<ConnOptions>("/api/git/connections/options", c),
  gitConnect: (c: ConnInput) => post<GitConnection>("/api/git/connections", c),
  gitConnTest: (id: string) => post<{ ok: boolean; login?: string; name?: string; error?: string }>(`/api/git/connections/${e(id)}/test`),
  gitDisconnect: (id: string) => del(`/api/git/connections/${e(id)}`),
  gitOAuthStart: (c: ConnInput) => post<OAuthStart>("/api/git/oauth", c),
  gitOAuthStatus: (flow: string) => get<OAuthStatus>(`/api/git/oauth/${e(flow)}`),
  gitOAuthCancel: (flow: string) => del(`/api/git/oauth/${e(flow)}`),
  gitAccountRepos: (ws: string, id: string, q = "", via = "") => arr(get<RemoteRepo[]>(`${W(ws)}/git/connections/${e(id)}/repos${qs({ q, via })}`)),

  // dns connectors and domains
  dnsProviders: () => arr(get<DNSProvider[]>("/api/dns/providers")),
  dnsConnections: () => arr(get<DNSConnection[]>("/api/dns/connections")),
  dnsConnect: (c: DNSConnInput) => post<DNSConnection>("/api/dns/connections", c),
  dnsConnTest: (id: string) => post<{ ok: boolean; account?: string; zones?: number; error?: string }>(`/api/dns/connections/${e(id)}/test`),
  dnsDisconnect: (id: string) => del(`/api/dns/connections/${e(id)}`),
  dnsAccountZones: (id: string, q = "") => arr(get<DNSZone[]>(`/api/dns/connections/${e(id)}/zones${qs({ q })}`)),
  domains: (ws: string) => arr(get<DomainInfo[]>(`${W(ws)}/domains`)),
  domain: (ws: string, name: string) => get<DomainInfo>(`${W(ws)}/domains/${e(name)}`),
  dnsRecords: (ws: string, name: string, type = "", q = "") => arr(get<DNSRecord[]>(`${W(ws)}/domains/${e(name)}/records${qs({ type, q })}`)),
  dnsRecordCreate: (ws: string, name: string, r: DNSRecordInput) => post<DNSRecord>(`${W(ws)}/domains/${e(name)}/records`, r),
  dnsRecordUpdate: (ws: string, name: string, id: string, r: DNSRecordInput) => put<DNSRecord>(`${W(ws)}/domains/${e(name)}/records/${e(id)}`, r),
  dnsRecordDelete: (ws: string, name: string, id: string) => del(`${W(ws)}/domains/${e(name)}/records/${e(id)}`),

  // deployment platforms
  deployPlatformTypes: () => arr(get<DeployPlatformType[]>("/api/deploy/platforms")),
  deployPlatforms: (ws: string) => arr(get<DeployPlatformInfo[]>(`${W(ws)}/deploy`)),
  deployTargets: (ws: string, name: string) => arr(get<DeployTarget[]>(`${W(ws)}/deploy/${e(name)}/targets`)),
  deployTarget: (ws: string, name: string, id: string) => get<DeployDetail>(`${W(ws)}/deploy/${e(name)}/target${qs({ id })}`),
  deployLogs: (ws: string, name: string, id: string, service = "", tail = 300) => get<{ text: string }>(`${W(ws)}/deploy/${e(name)}/logs${qs({ id, service, tail: String(tail) })}`),
  deployDiff: (ws: string, name: string, id: string) => get<{ supported: boolean; diff?: string }>(`${W(ws)}/deploy/${e(name)}/diff${qs({ id })}`),
  deployAction: (ws: string, name: string, a: DeployActionInput) => post<DeployResult>(`${W(ws)}/deploy/${e(name)}/action`, a),

  // certificates (ACME)
  acmeCAs: () => arr(get<AcmeCA[]>("/api/acme/cas")),
  certificates: (ws: string) => arr(get<CertStatus[]>(`${W(ws)}/certificates`)),
  certificate: (ws: string, name: string) => get<CertDetail>(`${W(ws)}/certificates/${e(name)}`),
  certIssue: (ws: string, name: string) => post<CertJob>(`${W(ws)}/certificates/${e(name)}/issue`),
  certJob: (ws: string, id: string, since = 0, wait = false) => get<CertJob>(`${W(ws)}/certificates/jobs/${e(id)}${qs({ since, wait: wait ? 1 : undefined })}`),
  certJobs: (ws: string) => arr(get<CertJob[]>(`${W(ws)}/certificates/jobs`)),
  certDeploy: (ws: string, name: string, d: CertDeploy) => post<CertDeployResult>(`${W(ws)}/certificates/${e(name)}/deploy`, d),

  // git repositories
  repos: (ws: string) => arr(get<RepoSummary[]>(`${W(ws)}/repos`)),
  repoCheck: (ws: string, r: string) => post<RepoCheck>(`${R(ws, r)}/check`),
  repoClone: (ws: string, r: string, branch = "") => post<Out>(`${R(ws, r)}/clone`, { branch }),
  repoRemove: (ws: string, r: string) => del(`${R(ws, r)}/clone`),
  repoFetch: (ws: string, r: string) => post<Out>(`${R(ws, r)}/fetch`),
  repoPull: (ws: string, r: string, rebase = false) => post<Out>(`${R(ws, r)}/pull`, { rebase }),
  repoStatus: (ws: string, r: string) => get<RepoStatus>(`${R(ws, r)}/status`),
  repoBranches: (ws: string, r: string) => arr(get<RepoBranch[]>(`${R(ws, r)}/branches`)),
  repoCheckout: (ws: string, r: string, branch: string, create = false, start = "") => post<Out>(`${R(ws, r)}/checkout`, { branch, create, start }),
  repoFiles: (ws: string, r: string, ref = "") => get<{ files: string[] | null; truncated?: boolean }>(`${R(ws, r)}/files${qs({ ref })}`),
  repoFile: (ws: string, r: string, path: string, ref = "") => get<RepoFile>(`${R(ws, r)}/file${qs({ path, ref })}`),
  repoWrite: (ws: string, r: string, path: string, content: string) => put(`${R(ws, r)}/file`, { path, content }),
  repoDelete: (ws: string, r: string, path: string) => del(`${R(ws, r)}/file${qs({ path })}`),
  repoDiff: (ws: string, r: string, path = "") => get<{ diff: string; truncated: boolean }>(`${R(ws, r)}/diff${qs({ path })}`),
  repoLog: (ws: string, r: string, ref = "", path = "", n = 100) => arr(get<RepoCommit[]>(`${R(ws, r)}/log${qs({ ref, path, n })}`)),
  repoShow: (ws: string, r: string, rev: string) => get<{ diff: string; truncated: boolean }>(`${R(ws, r)}/commits/${e(rev)}`),
  repoCommit: (ws: string, r: string, message: string, paths: string[] = []) => post<Out>(`${R(ws, r)}/commit`, { message, paths }),
  repoPush: (ws: string, r: string, branch = "", force = false) => post<Out>(`${R(ws, r)}/push`, { branch, force }),
  repoDiscard: (ws: string, r: string, paths: string[] = []) => post<Out>(`${R(ws, r)}/discard`, { paths }),
  mrs: (ws: string, r: string, state = "open") => get<{ provider: string; items: MergeRequest[] | null }>(`${R(ws, r)}/mrs${qs({ state })}`),
  mr: (ws: string, r: string, id: number) => get<MergeRequest>(`${R(ws, r)}/mrs/${id}`),
  mrCreate: (ws: string, r: string, m: { title: string; description: string; source: string; target: string; draft: boolean; removeSource: boolean }) =>
    post<MergeRequest>(`${R(ws, r)}/mrs`, m),
  mrComment: (ws: string, r: string, id: number, body: string) => post(`${R(ws, r)}/mrs/${id}/comment`, { body }),
  mrMerge: (ws: string, r: string, id: number, method: string, removeSource: boolean) => post<MergeRequest>(`${R(ws, r)}/mrs/${id}/merge`, { method, removeSource }),

  // networks
  networks: (ws: string) => arr(get<NetworkStatus[]>(`${W(ws)}/networks`)),
  networkUp: (ws: string, name: string) => post(`${W(ws)}/networks/${e(name)}/up`),
  networkDown: (ws: string, name: string) => post(`${W(ws)}/networks/${e(name)}/down`),
  networkLog: (ws: string, name: string, lines = 300) => arr(get<LogLine[]>(`${W(ws)}/networks/${e(name)}/log?lines=${lines}`)),
  networkCode: (ws: string, name: string, code: string) => post<NetworkStatus[]>(`${W(ws)}/networks/${e(name)}/code`, { code }),
  vpnDrivers: () => get<VpnDrivers>(`/api/vpn/drivers`),
  vpnCodes: () => arr(get<PendingCode[]>(`/api/vpn/codes`)),
  vpnCodesTest: (c: VpnCodes) => post<{ code: string; source: string }>(`/api/vpn/codes/test`, c),

  // hosts
  checkHost: (ws: string, host: string) => post<CheckResult>(`${H(ws, host)}/check`),
  hostStats: (ws: string, host: string) => get<HostStats>(`${H(ws, host)}/stats`),
  exec: (ws: string, host: string, command: string, timeoutSec = 60) =>
    post<ExecResult>(`${H(ws, host)}/exec`, { command, timeoutSec }),
  listFiles: (ws: string, host: string, path: string) => get<DirListing>(`${H(ws, host)}/files${qs({ path })}`),
  readFile: (ws: string, host: string, path: string) =>
    get<FileContent>(`${H(ws, host)}/files/content${qs({ path })}`),
  writeFile: (ws: string, host: string, path: string, content: string) =>
    put(`${H(ws, host)}/files/content`, { path, content }),
  forgetHostKey: (ws: string, host: string) => del(`${W(ws)}/known-hosts${qs({ host })}`),
  terminalUrl: (ws: string, host: string, cols: number, rows: number) => {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}${H(ws, host)}/terminal${qs({ cols, rows })}`;
  },

  // forwards
  createForward: (ws: string, b: { target: string; remoteHost?: string; remotePort?: number }) =>
    post<Forward>(`${W(ws)}/forwards`, b),
  forwards: () => arr(get<Forward[]>("/api/forwards")),
  deleteForward: (id: string) => del(`/api/forwards/${e(id)}`),
  openPanel: (ws: string, name: string) => post<{ url: string }>(`${W(ws)}/panels/${e(name)}/open`),

  // docker
  dockerInfo: (ws: string, host: string) => get<DockerInfo>(`${DK(ws, host)}/info`),
  dockerDf: (ws: string, host: string) => arr(get<DockerUsage[]>(`${DK(ws, host)}/df`)),
  dockerPrune: (ws: string, host: string, kind: string) => post<Out>(`${DK(ws, host)}/prune`, { kind }),
  dockerInspect: (ws: string, host: string, kind: string, id: string) => get<unknown>(`${DK(ws, host)}/inspect/${e(kind)}/${e(id)}`),
  containers: (ws: string, host: string, all = true) =>
    arr(get<Container[]>(`${DK(ws, host)}/containers${qs({ all: all ? 1 : undefined })}`)),
  images: (ws: string, host: string) => arr(get<DockerImage[]>(`${DK(ws, host)}/images`)),
  imagePull: (ws: string, host: string, ref: string) => post<Out>(`${DK(ws, host)}/images/pull`, { ref }),
  imageRemove: (ws: string, host: string, ref: string, force = false) => del(`${DK(ws, host)}/images${qs({ ref, force: force ? 1 : undefined })}`),
  dockerStats: (ws: string, host: string, ids: string[] = []) => arr(get<DockerStat[]>(`${DK(ws, host)}/stats${qs({ ids: ids.join(",") })}`)),
  inspect: (ws: string, host: string, id: string) => get<unknown>(`${DK(ws, host)}/containers/${e(id)}/inspect`),
  containerTop: (ws: string, host: string, id: string) => arr(get<DockerProcess[]>(`${DK(ws, host)}/containers/${e(id)}/top`)),
  containerAction: (ws: string, host: string, id: string, action: string) => post(`${DK(ws, host)}/containers/${e(id)}/${e(action)}`),
  dockerExecUrl: (ws: string, host: string, id: string, o: { cols: number; rows: number; user?: string; shell?: string }) => {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}${DK(ws, host)}/containers/${e(id)}/exec${qs(o)}`;
  },
  dockerNetworks: (ws: string, host: string) => arr(get<DockerNetwork[]>(`${DK(ws, host)}/networks`)),
  dockerNetworkRemove: (ws: string, host: string, id: string) => del(`${DK(ws, host)}/networks/${e(id)}`),
  volumes: (ws: string, host: string) => arr(get<DockerVolume[]>(`${DK(ws, host)}/volumes`)),
  volumeCreate: (ws: string, host: string, name: string) => post(`${DK(ws, host)}/volumes`, { name }),
  volumeRemove: (ws: string, host: string, name: string) => del(`${DK(ws, host)}/volumes/${e(name)}`),
  volumeSize: (ws: string, host: string, name: string) => get<{ bytes: number }>(`${DK(ws, host)}/volumes/${e(name)}/size`),
  volumeFiles: (ws: string, host: string, name: string, path: string) =>
    get<{ access: string; entries: VolumeEntry[] | null }>(`${DK(ws, host)}/volumes/${e(name)}/files${qs({ path })}`).then((r) => ({ ...r, entries: r.entries ?? [] })),
  volumeFile: (ws: string, host: string, name: string, path: string) =>
    get<{ path: string; content: string; binary: boolean; truncated: boolean; size: number }>(`${DK(ws, host)}/volumes/${e(name)}/file${qs({ path })}`),
  volumeDownloadUrl: (ws: string, host: string, name: string, path: string) => `${DK(ws, host)}/volumes/${e(name)}/file${qs({ path, download: 1 })}`,
  volumeSaveFile: (ws: string, host: string, name: string, path: string, content: string) => put(`${DK(ws, host)}/volumes/${e(name)}/file`, { path, content }),
  volumeMkdir: (ws: string, host: string, name: string, path: string, dir: string) => post(`${DK(ws, host)}/volumes/${e(name)}/mkdir`, { path, name: dir }),
  volumeDelete: (ws: string, host: string, name: string, path: string) => del(`${DK(ws, host)}/volumes/${e(name)}/file${qs({ path })}`),
  volumeUpload: async (ws: string, host: string, name: string, path: string, files: File[]) => {
    const fd = new FormData();
    for (const f of files) fd.append("file", f, f.name);
    const res = await fetch(`${DK(ws, host)}/volumes/${e(name)}/upload${qs({ path })}`, { method: "POST", body: fd, credentials: "same-origin" });
    if (!res.ok) {
      let msg = `${res.status} ${res.statusText}`;
      try {
        msg = ((await res.json()) as { error?: string }).error || msg;
      } catch {
        /* keep status */
      }
      throw new ApiError(res.status, msg);
    }
    return (await res.json()) as { saved: string[] };
  },
  composeProjects: (ws: string, host: string) => arr(get<ComposeProject[]>(`${DK(ws, host)}/compose`)),
  composeCreate: (ws: string, host: string, b: { name: string; dir: string; content: string; start: boolean }) => post<Out>(`${DK(ws, host)}/compose`, b),
  composePs: (ws: string, host: string, project: string) => arr(get<ComposeContainer[]>(`${DK(ws, host)}/compose/${e(project)}`)),
  composeFiles: (ws: string, host: string, project: string) => arr(get<ComposeFile[]>(`${DK(ws, host)}/compose/${e(project)}/files`)),
  composeFile: (ws: string, host: string, project: string, path: string) =>
    get<{ path: string; content: string }>(`${DK(ws, host)}/compose/${e(project)}/file${qs({ path })}`),
  composeSaveFile: (ws: string, host: string, project: string, path: string, content: string, deploy: boolean) =>
    put<Out>(`${DK(ws, host)}/compose/${e(project)}/file`, { path, content, deploy }),
  composeAction: (ws: string, host: string, project: string, action: string, services: string[] = []) =>
    post<Out>(`${DK(ws, host)}/compose/${e(project)}/${e(action)}`, { services }),
  swarm: (ws: string, host: string) => get<SwarmInfo>(`${DK(ws, host)}/swarm`),
  services: (ws: string, host: string) => arr(get<SwarmService[]>(`${DK(ws, host)}/services`)),
  serviceTasks: (ws: string, host: string, name: string) => arr(get<SwarmTask[]>(`${DK(ws, host)}/services/${e(name)}/tasks`)),
  serviceAction: (ws: string, host: string, name: string, action: string, arg = "") => post<Out>(`${DK(ws, host)}/services/${e(name)}/${e(action)}`, { arg }),
  stacks: (ws: string, host: string) => arr(get<SwarmStack[]>(`${DK(ws, host)}/stacks`)),
  stackTasks: (ws: string, host: string, name: string) => arr(get<SwarmTask[]>(`${DK(ws, host)}/stacks/${e(name)}/tasks`)),
  stackDeploy: (ws: string, host: string, b: { name: string; file?: string; content?: string; prune?: boolean }) => post<Out>(`${DK(ws, host)}/stacks`, b),
  stackRemove: (ws: string, host: string, name: string) => del<Out>(`${DK(ws, host)}/stacks/${e(name)}`),
  nodes: (ws: string, host: string) => arr(get<SwarmNode[]>(`${DK(ws, host)}/nodes`)),
  nodeAvailability: (ws: string, host: string, id: string, availability: string) => post(`${DK(ws, host)}/nodes/${e(id)}/availability`, { availability }),

  // kubernetes (type is a resource id like "deployments.apps"; ns "_" = cluster scoped)
  kubeOverview: (ws: string, c: string, namespace: string) => get<KubeOverview>(`${C(ws, c)}/overview${qs({ namespace })}`),
  // databases
  dbInfo: (ws: string, db: string, database?: string) => get<DbInfo>(`${D(ws, db)}/info${qs({ database })}`),
  dbDatabases: (ws: string, db: string) => arr(get<string[]>(`${D(ws, db)}/databases`)),
  dbTables: (ws: string, db: string, database?: string, match?: string) => arr(get<DbTable[]>(`${D(ws, db)}/tables${qs({ database, match })}`)),
  dbTable: (ws: string, db: string, schema: string, name: string, database?: string) => get<DbTableInfo>(`${D(ws, db)}/table${qs({ database, schema, name })}`),
  dbClassify: (ws: string, db: string, sql: string) => post<DbNotRead>(`${D(ws, db)}/classify`, { sql }),
  dbQuery: (ws: string, db: string, b: { sql: string; database?: string; write?: boolean; maxRows?: number }) => post<DbAnswer>(`${D(ws, db)}/query`, b),

  kubeResources: (ws: string, c: string, refresh = false) => arr(get<KubeResourceType[]>(`${C(ws, c)}/resources${qs({ refresh: refresh ? 1 : undefined })}`)),
  kubeNamespaces: (ws: string, c: string) => get<{ namespaces: string[] | null; default: string }>(`${C(ws, c)}/namespaces`).then((r) => ({ ...r, namespaces: r.namespaces ?? [] })),
  kubeList: (ws: string, c: string, type: string, o: { namespace?: string; labelSelector?: string; fieldSelector?: string }) =>
    get<KubeTable>(`${C(ws, c)}/list/${e(type)}${qs(o)}`).then((t) => ({ ...t, rows: t.rows ?? [], columns: t.columns ?? [] })),
  kubeObject: (ws: string, c: string, type: string, ns: string, name: string) =>
    get<{ object: KubeObject; yaml: string; editYaml: string; type: KubeResourceType }>(`${KO(ws, c, type, ns, name)}`),
  kubeEvents: (ws: string, c: string, type: string, ns: string, name: string) => arr(get<KubeEvent[]>(`${KO(ws, c, type, ns, name)}/events`)),
  kubePods: (ws: string, c: string, type: string, ns: string, name: string) =>
    get<KubeTable>(`${KO(ws, c, type, ns, name)}/pods`).then((t) => ({ ...t, rows: t.rows ?? [], columns: t.columns ?? [] })),
  kubeHistory: (ws: string, c: string, type: string, ns: string, name: string) => arr(get<KubeRevision[]>(`${KO(ws, c, type, ns, name)}/history`)),
  kubeReveal: (ws: string, c: string, ns: string, name: string) => post<Record<string, string>>(`${KO(ws, c, "secrets", ns, name)}/reveal`),
  kubeDelete: (ws: string, c: string, type: string, ns: string, name: string, force = false) => del(`${KO(ws, c, type, ns, name)}${qs({ force: force ? 1 : undefined })}`),
  kubeAction: (ws: string, c: string, type: string, ns: string, name: string, action: string, b: { replicas?: number; container?: string; image?: string; revision?: number } = {}) =>
    post<{ ok?: boolean; output?: string }>(`${KO(ws, c, type, ns, name)}/${e(action)}`, b),
  kubeApply: (ws: string, c: string, manifest: string, namespace: string, dryRun: boolean) =>
    arr(post<KubeApplyResult[]>(`${C(ws, c)}/apply`, { manifest, namespace, dryRun })),
  kubePodMetrics: (ws: string, c: string, namespace: string) => get<Record<string, KubeUsage>>(`${C(ws, c)}/metrics/pods${qs({ namespace })}`),
  kubeNodeMetrics: (ws: string, c: string) => get<Record<string, KubeUsage>>(`${C(ws, c)}/metrics/nodes`),
  helmReleases: (ws: string, c: string, namespace: string) => arr(get<HelmRelease[]>(`${C(ws, c)}/helm${qs({ namespace })}`)),
  helmRelease: (ws: string, c: string, ns: string, name: string, revision = 0) =>
    get<HelmDetail>(`${C(ws, c)}/helm/${e(ns)}/${e(name)}${qs({ revision: revision || undefined })}`),
  kubeForward: (ws: string, c: string, b: { type: string; namespace: string; name: string; port: number }) => post<Forward>(`${C(ws, c)}/forward`, b),
  kubeLogUrl: (ws: string, c: string, o: { type: string; namespace: string; name: string; container?: string; lines?: number | "all"; since?: string; previous?: boolean; follow?: boolean }) =>
    `${C(ws, c)}/logs/stream${qs({ ...o, previous: o.previous ? 1 : undefined, follow: o.follow ? 1 : 0 })}`,
  kubeEventsUrl: (ws: string, c: string, namespace: string) => `${C(ws, c)}/events/stream${qs({ namespace })}`,
  kubeExecUrl: (ws: string, c: string, ns: string, pod: string, o: { container?: string; cols: number; rows: number; shell?: string }) => {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}${C(ws, c)}/exec/${e(ns)}/${e(pod)}${qs(o)}`;
  },

  // logs
  logStreamUrl: (
    ws: string,
    o: {
      host: string;
      source: string;
      unit?: string;
      path?: string;
      container?: string;
      project?: string;
      service?: string;
      lines?: number | "all";
      since?: string;
      follow?: boolean;
    },
  ) =>
    `${W(ws)}/logs/stream${qs({
      host: o.host,
      source: o.source,
      unit: o.unit,
      path: o.path,
      container: o.container,
      project: o.project,
      service: o.service,
      lines: o.lines,
      since: o.since,
      follow: o.follow ? 1 : 0,
    })}`,

  // agent
  sessions: (ws: string) => arr(get<SessionInfo[]>(`${W(ws)}/sessions`)),
  createSession: (ws: string, b: { model?: string; mode?: SessionMode }) => post<SessionInfo>(`${W(ws)}/sessions`, b),
  harness: (ws: string) => get<HarnessInfo>(`${W(ws)}/harness`),
  session: (ws: string, id: string) =>
    get<{ session: SessionInfo; events: AgentEvent[] | null }>(S(ws, id)).then((r) => ({ ...r, events: r.events ?? [] })),
  updateSession: (ws: string, id: string, b: { title?: string; model?: string; mode?: SessionMode }) =>
    put<SessionInfo>(S(ws, id), b),
  deleteSession: (ws: string, id: string) => del(S(ws, id)),
  sendMessage: (ws: string, id: string, text: string) => post(`${S(ws, id)}/messages`, { text }),
  interrupt: (ws: string, id: string) => post(`${S(ws, id)}/interrupt`),
  approve: (ws: string, id: string, approvalId: string, decision: "approve" | "deny" | "always", note?: string, secret?: string, canary?: boolean) =>
    post(`${S(ws, id)}/approvals/${e(approvalId)}`, { decision, note, secret, canary }),
  answer: (ws: string, id: string, questionId: string, a: { choices?: string[]; text?: string; skipped?: boolean }) =>
    post(`${S(ws, id)}/questions/${e(questionId)}`, a),
  tasks: (ws: string, id: string) => arr(get<TaskInfo[]>(`${S(ws, id)}/tasks`)),
  task: (ws: string, id: string, task: string) => get<{ task: TaskInfo; output: string }>(`${S(ws, id)}/tasks/${e(task)}`),
  stopTask: (ws: string, id: string, task: string) => post(`${S(ws, id)}/tasks/${e(task)}/stop`),
  outputUrl: (ws: string, id: string, output: string, download?: boolean) => `${S(ws, id)}/outputs/${e(output)}${download ? "?download=1" : ""}`,
  forkSession: (ws: string, id: string, at?: string) => post<{ session: SessionInfo; text: string }>(`${S(ws, id)}/fork`, { at }),
  exportSession: (ws: string, id: string, as: "report" | "runbook" | "skill", title?: string) => post<{ path: string }>(`${S(ws, id)}/export`, { as, title }),
  shell: (ws: string, id: string, command: string, target?: string) => post<AgentEvent>(`${S(ws, id)}/shell`, { command, target }),
  compactSession: (ws: string, id: string) => post(`${S(ws, id)}/compact`),
  trustHooks: (ws: string) => post<Workspace>(`${W(ws)}/hooks/trust`),
  sessionStreamUrl: (ws: string, id: string) => `${S(ws, id)}/stream`,

  // audit
  audit: (o: { workspace?: string; limit?: number; before?: number }) =>
    arr(get<AuditEntry[]>(`/api/audit${qs({ workspace: o.workspace, limit: o.limit, before: o.before })}`)),
  auditVerify: () => get<AuditVerify>("/api/audit/verify"),
};

export function errMsg(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}
