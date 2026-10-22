import type {
  LogLine,
  VpnDrivers,
  MergeRequest,
  GitConnection,
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
  ExecResult,
  FileContent,
  Forward,
  HostStats,
  Item,
  ItemInput,
  ItemType,
  KindSchema,
  KubeRow,
  McpStatus,
  ModelInfo,
  NetworkStatus,
  PathHop,
  Resource,
  SessionInfo,
  SessionMode,
  Settings,
  Skill,
  Topology,
  ValidationIssue,
  VaultStatus,
  Workspace,
  WorkspaceSummary,
  InstalledAgent,
} from "./types";

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, message: string, code = "") {
    super(message);
    this.status = status;
    this.code = code;
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
    throw new ApiError(res.status, msg, code);
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
const S = (ws: string, id: string) => `${W(ws)}/sessions/${e(id)}`;
const R = (ws: string, repo: string) => `${W(ws)}/repos/${e(repo)}`;
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
  saveSettings: (s: Settings) => put<Settings>("/api/settings", s),
  detectAgents: () => arr(get<InstalledAgent[]>("/api/agents/detect")),
  models: () => arr(get<ModelInfo[]>("/api/models")),

  // workspaces
  workspaces: () => arr(get<WorkspaceSummary[]>("/api/workspaces")),
  createWorkspace: (b: { name: string; title: string; description: string; color: string }) =>
    post<WorkspaceSummary>("/api/workspaces", b),
  importWorkspace: (path: string) => post<WorkspaceSummary>("/api/workspaces/import", { path }),
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
  mcp: (ws: string) => arr(get<McpStatus[]>(`${W(ws)}/mcp`)),
  trustMcp: (ws: string, name: string) => arr(post<McpStatus[]>(`${W(ws)}/mcp/${e(name)}/trust`)),

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
  vpnDrivers: () => get<VpnDrivers>(`/api/vpn/drivers`),

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
  containers: (ws: string, host: string, all = true) =>
    arr(get<Container[]>(`${H(ws, host)}/docker/containers${qs({ all: all ? 1 : undefined })}`)),
  images: (ws: string, host: string) => arr(get<DockerImage[]>(`${H(ws, host)}/docker/images`)),
  dockerStats: (ws: string, host: string) => arr(get<DockerStat[]>(`${H(ws, host)}/docker/stats`)),
  inspect: (ws: string, host: string, id: string) => get<unknown>(`${H(ws, host)}/docker/containers/${e(id)}/inspect`),
  containerAction: (ws: string, host: string, id: string, action: "start" | "stop" | "restart" | "rm") =>
    post(`${H(ws, host)}/docker/containers/${e(id)}/${action}`),

  // kubernetes
  namespaces: (ws: string, c: string) => arr(get<string[]>(`${C(ws, c)}/namespaces`)),
  kubeList: (ws: string, c: string, type: string, namespace: string) =>
    arr(get<KubeRow[]>(`${C(ws, c)}/list/${e(type)}${qs({ namespace })}`)),
  kubeYaml: (ws: string, c: string, type: string, ns: string, name: string) =>
    get<{ yaml: string }>(`${C(ws, c)}/yaml/${e(type)}/${e(ns || "_")}/${e(name)}`),
  podLogs: (ws: string, c: string, ns: string, pod: string, container: string, tail: number) =>
    get<{ lines: string[] }>(`${C(ws, c)}/logs/${e(ns)}/${e(pod)}${qs({ container, tail })}`),
  restartDeployment: (ws: string, c: string, ns: string, name: string) =>
    post(`${C(ws, c)}/deployments/${e(ns)}/${e(name)}/restart`),
  scaleDeployment: (ws: string, c: string, ns: string, name: string, replicas: number) =>
    post(`${C(ws, c)}/deployments/${e(ns)}/${e(name)}/scale`, { replicas }),
  deletePod: (ws: string, c: string, ns: string, name: string) => del(`${C(ws, c)}/pods/${e(ns)}/${e(name)}`),

  // logs
  logStreamUrl: (
    ws: string,
    o: { host: string; source: string; unit?: string; path?: string; container?: string; lines?: number; follow?: boolean },
  ) =>
    `${W(ws)}/logs/stream${qs({
      host: o.host,
      source: o.source,
      unit: o.unit,
      path: o.path,
      container: o.container,
      lines: o.lines,
      follow: o.follow ? 1 : 0,
    })}`,

  // agent
  sessions: (ws: string) => arr(get<SessionInfo[]>(`${W(ws)}/sessions`)),
  createSession: (ws: string, b: { model?: string; mode?: SessionMode }) => post<SessionInfo>(`${W(ws)}/sessions`, b),
  session: (ws: string, id: string) =>
    get<{ session: SessionInfo; events: AgentEvent[] | null }>(S(ws, id)).then((r) => ({ ...r, events: r.events ?? [] })),
  updateSession: (ws: string, id: string, b: { title?: string; model?: string; mode?: SessionMode }) =>
    put<SessionInfo>(S(ws, id), b),
  deleteSession: (ws: string, id: string) => del(S(ws, id)),
  sendMessage: (ws: string, id: string, text: string) => post(`${S(ws, id)}/messages`, { text }),
  interrupt: (ws: string, id: string) => post(`${S(ws, id)}/interrupt`),
  approve: (ws: string, id: string, approvalId: string, decision: "approve" | "deny" | "always", note?: string) =>
    post(`${S(ws, id)}/approvals/${e(approvalId)}`, { decision, note }),
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
