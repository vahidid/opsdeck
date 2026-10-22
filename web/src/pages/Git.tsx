import { useEffect, useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  BookOpen,
  ChevronDown,
  ChevronRight,
  CircleCheck,
  CircleDot,
  CircleX,
  Download,
  ExternalLink,
  FilePlus2,
  FileText,
  Folder,
  GitBranch,
  GitCommitHorizontal,
  GitMerge,
  GitPullRequest,
  History,
  Pencil,
  Plug,
  Plus,
  RefreshCw,
  Save,
  Trash2,
  Undo2,
  X,
} from "lucide-react";
import { api, errMsg } from "../api/client";
import type { MergeRequest, RemoteRepo, RepoBranch, RepoChange, RepoStatus, RepoSummary, Resource } from "../api/types";
import { NETWORK_KINDS } from "../api/types";
import { useApp, useWs } from "../context/app";
import { useTabTitle } from "../context/tabs";
import { Badge, Button, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, PageHeader, SearchInput, Segmented, Select, Tabs, Textarea, Toggle, cx } from "../components/ui";
import { GitHubMark, GitLabMark, ProviderMark } from "../components/brand";
import { ConnectModal } from "./Connectors";
import type { Tone } from "../components/ui";
import { MenuItem, Modal, Popover, useConfirm, useToast } from "../components/overlay";
import { Markdown } from "../components/Markdown";
import { useAsync, useInterval } from "../hooks";
import { slugify, timeAgo } from "../lib/format";
import { ResourceDrawer } from "./Resources";

type Preset = { id: string; label: string; spec: Record<string, unknown>; hint: string };

export const GIT_PRESETS: Preset[] = [{ id: "url", label: "By URL", spec: {}, hint: "" }];

const PROVIDER_LABEL: Record<string, string> = { gitlab: "GitLab", github: "GitHub", auto: "Git" };

function providerOf(r: RepoSummary): string {
  if (r.provider !== "auto") return PROVIDER_LABEL[r.provider] ?? r.provider;
  if (/github\.com/i.test(r.url)) return "GitHub";
  if (/gitlab/i.test(r.url)) return "GitLab";
  return "Git";
}

export function GitPage() {
  const { repo } = useParams();
  return repo ? <RepoView name={repo} /> : <RepoList />;
}

// ---------------------------------------------------------------------------
// List

function RepoList() {
  const { kindMap } = useApp();
  const { name: ws, resources, reloadResources } = useWs();
  const toast = useToast();
  const confirm = useConfirm();
  const repos = useAsync(() => api.repos(ws), [ws, resources.length]);
  useInterval(() => void repos.reload(), 15000);
  const [adding, setAdding] = useState(false);
  const [edit, setEdit] = useState<{ resource: Resource | null; preset?: Preset } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const schema = kindMap["GitRemote"];

  async function run(name: string, what: string, fn: () => Promise<{ output: string } | unknown>) {
    setBusy(name + what);
    try {
      const out = (await fn()) as { output?: string };
      toast.success(`${name}: ${what}`, out?.output?.split("\n")[0]);
    } catch (e) {
      toast.error(`${name}: ${what} failed`, errMsg(e));
    } finally {
      setBusy(null);
      await repos.reload();
    }
  }

  async function test(name: string) {
    setBusy(name + "test");
    try {
      const c = await api.repoCheck(ws, name);
      if (!c.ok) toast.error(`${name}: cannot reach the repository`, c.error);
      else if (c.apiError) toast.error(`${name}: Git works, the API does not`, `Merge requests need the API: ${c.apiError}`);
      else
        toast.success(
          `${name}: connected`,
          `default branch ${c.defaultBranch || "?"}${c.provider ? ` · ${PROVIDER_LABEL[c.provider] ?? c.provider}` : ""}${c.canPush === false ? " · read-only token" : ""}`,
        );
    } catch (e) {
      toast.error(`${name}: test failed`, errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  async function remove(r: Resource) {
    const ok = await confirm({
      title: `Remove ${r.name}?`,
      message: "The repository is removed from the workspace. Its working copy in repos/ and the vault items stay.",
      confirmLabel: "Remove",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteResource(ws, r.kind, r.name);
      setEdit(null);
      await reloadResources();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  const list = repos.data ?? [];
  const newMenu = (
    <div className="flex items-center gap-2">
      <Link to="/connectors" className="text-xs text-muted hover:text-fg inline-flex items-center gap-1">
        <Plug className="size-3.5" /> Connectors
      </Link>
      <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
        Add repository
      </Button>
    </div>
  );

  return (
    <div className="flex-1 overflow-y-auto">
      <PageHeader
        icon={<GitBranch />}
        title="Repositories"
        subtitle="Deployment repositories on GitLab or GitHub. Their working copies live in this workspace; you and the agent read, change, push and open merge requests."
        actions={newMenu}
      />
      <div className="px-5 pb-8 space-y-3 max-w-5xl">
        {repos.error && <ErrorBox error={repos.error} onRetry={() => void repos.reload()} />}
        {repos.data && list.length === 0 && (
          <EmptyState
            icon={<GitBranch />}
            title="No repositories yet"
            action={
              <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
                Add repository
              </Button>
            }
          >
            Add the repositories that hold your deployment files (Kubernetes manifests, compose files, Helm values, Terraform…) from your GitHub or GitLab account.
          </EmptyState>
        )}
        {list.map((r) => {
          const st = r.status;
          const res = resources.find((x) => x.kind === "GitRemote" && x.name === r.name);
          return (
            <div key={r.name} className="rounded-lg border border-line bg-panel">
              <div className="flex items-center gap-3 px-4 py-3">
                <GitBranch className="size-5 text-muted shrink-0" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 flex-wrap">
                    <Link to={encodeURIComponent(r.name)} className="text-[14px] font-semibold hover:text-accent truncate">
                      {r.name}
                    </Link>
                    <Badge tone="neutral">
                      {(r.provider === "github" || r.provider === "gitlab") && <ProviderMark provider={r.provider} className="size-3" />}
                      {providerOf(r)}
                    </Badge>
                    {r.environment && <Badge tone="info">{r.environment}</Badge>}
                    {r.docs && (
                      <Badge tone="accent">
                        <BookOpen className="size-3" /> docs
                      </Badge>
                    )}
                    {r.via && <Badge tone="neutral">via {r.via}</Badge>}
                    {r.auth === "none" && (
                      <Badge tone="warn" title="No connector for this server and no token: public repositories only">
                        no account
                      </Badge>
                    )}
                  </div>
                  <div className="text-[11.5px] text-faint font-mono truncate flex items-center gap-1">
                    {r.web ? (
                      <a href={r.web} target="_blank" rel="noreferrer" className="hover:text-accent inline-flex items-center gap-1">
                        {r.url} <ExternalLink className="size-3" />
                      </a>
                    ) : (
                      r.url
                    )}
                  </div>
                </div>
                {st?.cloned ? (
                  <>
                    <Button size="sm" variant="secondary" loading={busy === r.name + "pull"} icon={<ArrowDown className="size-3.5" />} onClick={() => void run(r.name, "pull", () => api.repoPull(ws, r.name))}>
                      Pull
                    </Button>
                    <Link to={encodeURIComponent(r.name)}>
                      <Button size="sm" variant="primary">
                        Open
                      </Button>
                    </Link>
                  </>
                ) : (
                  <Button size="sm" variant="primary" loading={busy === r.name + "clone"} icon={<Download className="size-3.5" />} onClick={() => void run(r.name, "clone", () => api.repoClone(ws, r.name))}>
                    Clone
                  </Button>
                )}
                <IconButton title="Test connection" onClick={() => void test(r.name)} disabled={busy === r.name + "test"}>
                  <Plug className="size-4" />
                </IconButton>
                <IconButton title="Edit" onClick={() => res && setEdit({ resource: res })} disabled={!res}>
                  <Pencil className="size-4" />
                </IconButton>
              </div>
              {(st?.cloned || r.error) && (
                <div className="px-4 pb-3 flex items-center gap-x-5 gap-y-1 flex-wrap text-xs text-muted">
                  {st?.cloned && (
                    <>
                      <span className="inline-flex items-center gap-1">
                        <GitBranch className="size-3.5" />
                        <span className="font-mono text-fg">{st.branch || "detached"}</span>
                        {st.default && st.branch !== st.default && <span className="text-faint">(default {st.default})</span>}
                      </span>
                      <SyncInfo st={st} />
                      <span className={cx(st.changes.length > 0 && "text-warn")}>
                        {st.changes.length === 0 ? "clean" : `${st.changes.length} uncommitted change${st.changes.length > 1 ? "s" : ""}`}
                      </span>
                    </>
                  )}
                  {r.last && (
                    <span className="truncate min-w-0 flex-1">
                      <span className="font-mono text-faint">{r.last.short}</span> {r.last.subject} <span className="text-faint">· {r.last.author} · {timeAgo(r.last.date)}</span>
                    </span>
                  )}
                  {r.error && <span className="text-err">{r.error}</span>}
                </div>
              )}
            </div>
          );
        })}
      </div>
      {adding && (
        <AddRepoModal
          onClose={() => setAdding(false)}
          onManual={() => {
            setAdding(false);
            setEdit({ resource: null, preset: GIT_PRESETS[GIT_PRESETS.length - 1] });
          }}
          onAdded={async () => {
            setAdding(false);
            await reloadResources();
            await repos.reload();
          }}
        />
      )}
      {edit && schema && (
        <ResourceDrawer
          schema={schema}
          resource={edit.resource}
          initial={edit.preset ? { spec: edit.preset.spec } : undefined}
          title={edit.preset ? "Add a repository by URL" : undefined}
          onClose={() => setEdit(null)}
          onSaved={async () => {
            setEdit(null);
            await reloadResources();
            await repos.reload();
          }}
          onDelete={edit.resource ? () => void remove(edit.resource!) : undefined}
        />
      )}
    </div>
  );
}

function SyncInfo({ st }: { st: RepoStatus }) {
  if (!st.branch) return null;
  if (!st.upstream) return <span className="text-faint">not pushed yet</span>;
  if (!st.ahead && !st.behind) return <span className="text-faint">in sync with {st.upstream}</span>;
  return (
    <span className="inline-flex items-center gap-2 font-mono">
      {st.ahead > 0 && (
        <span className="inline-flex items-center text-accent" title="commits to push">
          <ArrowUp className="size-3" />
          {st.ahead}
        </span>
      )}
      {st.behind > 0 && (
        <span className="inline-flex items-center text-warn" title="commits to pull">
          <ArrowDown className="size-3" />
          {st.behind}
        </span>
      )}
    </span>
  );
}

// ---------------------------------------------------------------------------
// One repository

type Tab = "files" | "changes" | "history" | "mrs";

function RepoView({ name }: { name: string }) {
  const { name: ws, resources } = useWs();
  const toast = useToast();
  const confirm = useConfirm();
  const [params, setParams] = useSearchParams();
  const tab = (params.get("tab") as Tab) || "files";
  const setTab = (t: Tab) => setParams({ tab: t }, { replace: true });
  useTabTitle(name);
  const res = resources.find((r) => r.kind === "GitRemote" && r.name === name);
  const status = useAsync(() => api.repoStatus(ws, name), [ws, name]);
  useInterval(() => void status.reload(), 5000);
  const st = status.data;
  const [busy, setBusy] = useState<string | null>(null);
  const [branchMenu, setBranchMenu] = useState(false);
  const [pullMenu, setPullMenu] = useState(false);
  const [newBranch, setNewBranch] = useState(false);
  const [mrOpen, setMrOpen] = useState(false);
  const [rev, setRev] = useState(0); // bumps children after operations
  const web = useMemo(() => {
    const u = String(res?.spec.url ?? "");
    const m = u.match(/^(?:https?:\/\/[^/]+\/.+?|git@([^:]+):(.+?)|ssh:\/\/[^@]+@([^:/]+)(?::\d+)?\/(.+?))(?:\.git)?$/);
    if (!m) return "";
    if (u.startsWith("http")) return u.replace(/\.git$/, "");
    const host = m[1] ?? m[3];
    const path = m[2] ?? m[4];
    return host && path ? `https://${host}/${path}` : "";
  }, [res]);

  async function op(label: string, fn: () => Promise<unknown>, quiet = false) {
    setBusy(label);
    try {
      const out = (await fn()) as { output?: string } | undefined;
      if (!quiet) toast.success(label, out?.output?.split("\n")[0]);
      return true;
    } catch (e) {
      toast.error(`${label} failed`, errMsg(e));
      return false;
    } finally {
      setBusy(null);
      setRev((v) => v + 1);
      await status.reload();
    }
  }

  async function push() {
    if (!st) return;
    const toDefault = st.branch === st.default;
    if (toDefault) {
      const ok = await confirm({
        title: `Push straight to ${st.branch}?`,
        message: `${st.branch} is the default branch: the change lands without review and may deploy. Usually you push a branch and open a merge request.`,
        confirmLabel: "Push",
        danger: true,
      });
      if (!ok) return;
    }
    const ok = await op("Push", () => api.repoPush(ws, name));
    if (ok && !toDefault && st.branch) setMrOpen(true);
  }

  if (status.error && !st) return <ErrorBox error={status.error} onRetry={() => void status.reload()} />;
  if (!st) return <Loading />;

  const header = (
    <div className="flex items-center gap-2 px-5 h-14 border-b border-line shrink-0">
      <Link to=".." relative="path" className="text-muted hover:text-fg" title="All repositories">
        <ArrowLeft className="size-4" />
      </Link>
      <GitBranch className="size-4 text-muted" />
      <span className="text-[15px] font-semibold">{name}</span>
      {res?.environment && <Badge tone="info">{res.environment}</Badge>}
      {web && (
        <a href={web} target="_blank" rel="noreferrer" className="text-faint hover:text-accent" title="Open on the server">
          <ExternalLink className="size-3.5" />
        </a>
      )}
      {st.cloned && (
        <>
          <div className="relative ml-3">
            <Button size="sm" variant="secondary" icon={<GitBranch className="size-3.5" />} onClick={() => setBranchMenu((v) => !v)}>
              <span className="font-mono">{st.branch || st.head.slice(0, 8)}</span>
              <ChevronDown className="size-3 text-faint" />
            </Button>
            {branchMenu && (
              <BranchMenu
                ws={ws}
                repo={name}
                current={st.branch}
                onClose={() => setBranchMenu(false)}
                onPick={(b) => {
                  setBranchMenu(false);
                  void op(`Switch to ${b}`, () => api.repoCheckout(ws, name, b), true);
                }}
                onNew={() => {
                  setBranchMenu(false);
                  setNewBranch(true);
                }}
              />
            )}
          </div>
          <SyncInfo st={st} />
          {st.busy && <Badge tone="warn">{st.busy} in progress</Badge>}
        </>
      )}
      <div className="flex-1" />
      {st.cloned && (
        <>
          <div className="relative flex">
            <Button
              size="sm"
              variant="secondary"
              className="rounded-r-none"
              loading={busy === "Pull"}
              icon={<ArrowDown className="size-3.5" />}
              onClick={() => void op("Pull", () => api.repoPull(ws, name))}
            >
              Pull{st.behind > 0 ? ` ${st.behind}` : ""}
            </Button>
            <Button size="sm" variant="secondary" className="rounded-l-none border-l-0 px-1.5" onClick={() => setPullMenu((v) => !v)} title="More">
              <ChevronDown className="size-3" />
            </Button>
            <Popover open={pullMenu} onClose={() => setPullMenu(false)} className="right-0 top-8 w-64">
              <MenuItem
                icon={<ArrowDown />}
                hint="keeps local commits on top"
                onClick={() => {
                  setPullMenu(false);
                  void op("Pull with rebase", () => api.repoPull(ws, name, true));
                }}
              >
                Pull with rebase
              </MenuItem>
              <MenuItem
                icon={<RefreshCw />}
                onClick={() => {
                  setPullMenu(false);
                  void op("Fetch", () => api.repoFetch(ws, name));
                }}
              >
                Fetch
              </MenuItem>
            </Popover>
          </div>
          <Button
            size="sm"
            variant={st.ahead > 0 || (!st.upstream && !!st.branch) ? "primary" : "secondary"}
            loading={busy === "Push"}
            disabled={!st.branch || (st.upstream !== undefined && st.upstream !== "" && st.ahead === 0)}
            icon={<ArrowUp className="size-3.5" />}
            onClick={() => void push()}
          >
            {st.upstream ? `Push${st.ahead ? ` ${st.ahead}` : ""}` : "Publish branch"}
          </Button>
          <Button size="sm" variant="secondary" icon={<GitPullRequest className="size-3.5" />} onClick={() => setMrOpen(true)} disabled={!st.branch || st.branch === st.default}>
            Merge request
          </Button>
        </>
      )}
    </div>
  );

  if (!st.cloned) {
    return (
      <div className="flex flex-col h-full min-h-0">
        {header}
        <EmptyState
          icon={<Download />}
          title="Not cloned yet"
          action={
            <Button variant="primary" loading={busy === "Clone"} icon={<Download className="size-4" />} onClick={() => void op("Clone", () => api.repoClone(ws, name))}>
              Clone {name}
            </Button>
          }
        >
          The working copy goes to <span className="font-mono">repos/{name}</span> in the workspace folder (ignored by the workspace's own Git).
        </EmptyState>
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      {header}
      <Tabs
        className="px-3 shrink-0"
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "files", label: "Files" },
          { id: "changes", label: "Changes", count: st.changes.length },
          { id: "history", label: "History" },
          { id: "mrs", label: "Merge requests" },
        ]}
      />
      <div className="flex-1 min-h-0">
        {tab === "files" && <FilesTab ws={ws} repo={name} rev={rev} onChanged={() => void status.reload()} />}
        {tab === "changes" && <ChangesTab ws={ws} repo={name} st={st} rev={rev} op={op} onPush={() => void push()} />}
        {tab === "history" && <HistoryTab ws={ws} repo={name} branch={st.branch} rev={rev} />}
        {tab === "mrs" && <MRTab ws={ws} repo={name} rev={rev} onNew={() => setMrOpen(true)} canNew={!!st.branch && st.branch !== st.default} />}
      </div>
      <NewBranchModal
        open={newBranch}
        defaultBranch={st.default ?? ""}
        onClose={() => setNewBranch(false)}
        onCreate={async (b, start) => {
          setNewBranch(false);
          await op(`Create ${b}`, () => api.repoCheckout(ws, name, b, true, start), true);
        }}
      />
      {mrOpen && (
        <NewMRModal
          ws={ws}
          repo={name}
          source={st.branch}
          target={st.default ?? ""}
          onClose={() => setMrOpen(false)}
          onCreated={() => {
            setMrOpen(false);
            setTab("mrs");
            setRev((v) => v + 1);
          }}
        />
      )}
    </div>
  );
}

function BranchMenu({ ws, repo, current, onClose, onPick, onNew }: { ws: string; repo: string; current: string; onClose: () => void; onPick: (b: string) => void; onNew: () => void }) {
  const branches = useAsync(() => api.repoBranches(ws, repo), [ws, repo]);
  const [q, setQ] = useState("");
  const list = branches.data ?? [];
  const local = new Set(list.filter((b) => !b.remote).map((b) => b.name));
  const shown = list.filter((b) => (!b.remote || !local.has(b.name)) && b.name.toLowerCase().includes(q.toLowerCase()));
  return (
    <Popover open onClose={onClose} className="left-0 mt-1 w-80">
      <div className="px-2 pb-1">
        <SearchInput value={q} onChange={setQ} placeholder="Find a branch…" />
      </div>
      <div className="max-h-80 overflow-y-auto">
        {branches.loading && !branches.data && <Loading />}
        {shown.map((b: RepoBranch) => (
          <button
            key={(b.remote ? "r:" : "l:") + b.name}
            onClick={() => (b.name === current ? onClose() : onPick(b.name))}
            className="flex w-full items-center gap-2 px-3 py-1.5 text-left hover:bg-panel-3"
          >
            <GitBranch className={cx("size-3.5 shrink-0", b.name === current ? "text-accent" : "text-faint")} />
            <span className="min-w-0 flex-1">
              <span className="block font-mono text-[12.5px] truncate">
                {b.name} {b.remote && <span className="text-faint text-[10.5px] font-sans">remote</span>}
              </span>
              <span className="block text-[10.5px] text-faint truncate">
                {b.subject} · {timeAgo(b.date)}
              </span>
            </span>
            {b.track && <span className="text-[10.5px] text-warn">{b.track}</span>}
          </button>
        ))}
      </div>
      <div className="border-t border-line mt-1 pt-1">
        <MenuItem icon={<Plus />} onClick={onNew}>
          New branch…
        </MenuItem>
      </div>
    </Popover>
  );
}

function NewBranchModal({ open, defaultBranch, onClose, onCreate }: { open: boolean; defaultBranch: string; onClose: () => void; onCreate: (b: string, start: string) => Promise<void> }) {
  const [b, setB] = useState("");
  const [from, setFrom] = useState<"current" | "default">("current");
  useEffect(() => {
    if (open) setB("");
  }, [open]);
  const valid = /^[A-Za-z0-9][A-Za-z0-9._/-]*$/.test(b) && !b.includes("..") && !b.endsWith("/") && !b.endsWith(".lock");
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New branch"
      icon={<GitBranch />}
      width={440}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!valid} onClick={() => void onCreate(b, from === "default" && defaultBranch ? `origin/${defaultBranch}` : "")}>
            Create and switch
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <FormField label="Name">
          <Input mono autoFocus value={b} onChange={(e) => setB(e.target.value)} placeholder="fix/nginx-timeouts" />
        </FormField>
        {defaultBranch && (
          <FormField label="Start from">
            <Segmented
              value={from}
              onChange={setFrom}
              options={[
                { id: "current", label: "current branch" },
                { id: "default", label: `origin/${defaultBranch}` },
              ]}
            />
          </FormField>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Files

type TreeNode = { name: string; path: string; dir: boolean; children: TreeNode[] };

function buildTree(files: string[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", dir: true, children: [] };
  for (const f of files) {
    const parts = f.split("/");
    let node = root;
    parts.forEach((p, i) => {
      const path = parts.slice(0, i + 1).join("/");
      const dir = i < parts.length - 1;
      let child = node.children.find((c) => c.name === p && c.dir === dir);
      if (!child) {
        child = { name: p, path, dir, children: [] };
        node.children.push(child);
      }
      node = child;
    });
  }
  const sort = (n: TreeNode) => {
    n.children.sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : a.name.localeCompare(b.name)));
    n.children.forEach(sort);
  };
  sort(root);
  return root.children;
}

function FilesTab({ ws, repo, rev, onChanged }: { ws: string; repo: string; rev: number; onChanged: () => void }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [params, setParams] = useSearchParams();
  const sel = params.get("path") ?? "";
  const setSel = (p: string) => setParams({ tab: "files", path: p }, { replace: true });
  const files = useAsync(() => api.repoFiles(ws, repo), [ws, repo, rev]);
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [file, setFile] = useState<{ text: string; binary?: boolean; truncated?: boolean; size: number } | null>(null);
  const [fileErr, setFileErr] = useState("");
  const [edit, setEdit] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [newOpen, setNewOpen] = useState(false);
  const [preview, setPreview] = useState(true);
  const all = files.data?.files ?? [];
  const tree = useMemo(() => buildTree(q ? all.filter((f) => f.toLowerCase().includes(q.toLowerCase())) : all), [all, q]);
  const dirty = edit !== null && edit !== file?.text;

  useEffect(() => {
    if (!sel) {
      setFile(null);
      return;
    }
    let cancel = false;
    setFile(null);
    setFileErr("");
    setEdit(null);
    api
      .repoFile(ws, repo, sel)
      .then((f) => !cancel && setFile(f))
      .catch((e) => !cancel && setFileErr(errMsg(e)));
    // open the folders of the selected file
    const parts = sel.split("/");
    setOpen((o) => ({ ...o, ...Object.fromEntries(parts.slice(0, -1).map((_, i) => [parts.slice(0, i + 1).join("/"), true])) }));
    return () => {
      cancel = true;
    };
  }, [ws, repo, sel, rev]);

  async function choose(p: string) {
    if (dirty && !(await confirm({ title: "Discard unsaved changes?", danger: true, confirmLabel: "Discard" }))) return;
    setSel(p);
  }

  async function save(content = edit) {
    if (content === null) return;
    setSaving(true);
    try {
      await api.repoWrite(ws, repo, sel, content);
      setFile((f) => (f ? { ...f, text: content } : f));
      setEdit(null);
      onChanged();
      void files.reload();
    } catch (e) {
      toast.error("Save failed", errMsg(e));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!(await confirm({ title: `Delete ${sel}?`, message: "The file is removed from the working copy; commit to make it permanent, or discard to bring it back.", danger: true, confirmLabel: "Delete" }))) return;
    try {
      await api.repoDelete(ws, repo, sel);
      setSel("");
      onChanged();
      void files.reload();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  const isMd = /\.(md|markdown)$/i.test(sel);
  const renderNode = (n: TreeNode, depth: number): React.ReactNode =>
    n.dir ? (
      <div key={"d:" + n.path}>
        <button
          onClick={() => setOpen((o) => ({ ...o, [n.path]: !(o[n.path] ?? !!q) }))}
          className="flex w-full items-center gap-1 py-0.5 pr-2 text-left text-muted hover:text-fg"
          style={{ paddingInlineStart: depth * 12 + 6 }}
        >
          {(open[n.path] ?? !!q) ? <ChevronDown className="size-3 shrink-0" /> : <ChevronRight className="size-3 shrink-0" />}
          <Folder className="size-3.5 shrink-0 text-faint" />
          <span className="truncate text-[12.5px]">{n.name}</span>
        </button>
        {(open[n.path] ?? !!q) && n.children.map((c) => renderNode(c, depth + 1))}
      </div>
    ) : (
      <button
        key={"f:" + n.path}
        onClick={() => void choose(n.path)}
        className={cx("flex w-full items-center gap-1.5 py-0.5 pr-2 text-left rounded", sel === n.path ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2")}
        style={{ paddingInlineStart: depth * 12 + 22 }}
      >
        <FileText className={cx("size-3.5 shrink-0", sel === n.path ? "text-accent" : "text-faint")} />
        <span className="truncate text-[12.5px] font-mono">{n.name}</span>
      </button>
    );

  return (
    <div className="flex h-full min-h-0">
      <aside className="w-72 shrink-0 border-r border-line bg-panel flex flex-col">
        <div className="flex items-center gap-1 p-2 border-b border-line">
          <div className="flex-1">
            <SearchInput value={q} onChange={setQ} placeholder="Filter files…" />
          </div>
          <IconButton title="New file" onClick={() => setNewOpen(true)}>
            <FilePlus2 className="size-4" />
          </IconButton>
        </div>
        <div className="flex-1 overflow-y-auto py-1">
          {files.loading && !files.data && <Loading />}
          {files.error && <div className="p-2 text-xs text-err">{files.error}</div>}
          {tree.map((n) => renderNode(n, 0))}
          {files.data?.truncated && <div className="px-3 py-2 text-[11px] text-faint">Only the first 20 000 files are listed.</div>}
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col">
        {!sel ? (
          <EmptyState icon={<FileText />} title="Pick a file">
            Edits go to the working copy. Commit and push them from <b>Changes</b>.
          </EmptyState>
        ) : (
          <>
            <div className="flex items-center gap-2 px-4 h-11 border-b border-line shrink-0">
              <span className="font-mono text-[12.5px] truncate">{sel}</span>
              {dirty && <Badge tone="warn">unsaved</Badge>}
              {file?.truncated && <Badge tone="warn">first 2 MB</Badge>}
              <div className="flex-1" />
              {edit !== null ? (
                <>
                  <Button size="sm" variant="ghost" icon={<X className="size-3.5" />} onClick={() => setEdit(null)}>
                    Cancel
                  </Button>
                  <Button size="sm" variant="primary" icon={<Save className="size-3.5" />} loading={saving} disabled={!dirty} onClick={() => void save()}>
                    Save
                  </Button>
                </>
              ) : (
                <>
                  {isMd && (
                    <Segmented
                      size="xs"
                      value={preview ? "preview" : "source"}
                      onChange={(v) => setPreview(v === "preview")}
                      options={[
                        { id: "preview", label: "Preview" },
                        { id: "source", label: "Source" },
                      ]}
                    />
                  )}
                  <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} onClick={() => void remove()} disabled={!file}>
                    Delete
                  </Button>
                  <Button size="sm" icon={<Pencil className="size-3.5" />} onClick={() => setEdit(file?.text ?? "")} disabled={!file || file.binary || file.truncated}>
                    Edit
                  </Button>
                </>
              )}
            </div>
            <div className="flex-1 min-h-0 overflow-auto">
              {fileErr && <ErrorBox error={fileErr} />}
              {!file && !fileErr && <Loading />}
              {file &&
                (edit !== null ? (
                  <div className="h-full p-3">
                    <Textarea
                      mono
                      autoFocus
                      spellCheck={false}
                      value={edit}
                      onChange={(e) => setEdit(e.target.value)}
                      className="h-full resize-none text-[12.5px] leading-relaxed"
                      onKeyDown={(e) => {
                        if ((e.ctrlKey || e.metaKey) && e.key === "s") {
                          e.preventDefault();
                          void save();
                        }
                      }}
                    />
                  </div>
                ) : file.binary ? (
                  <EmptyState title="Binary file">{file.size} bytes</EmptyState>
                ) : isMd && preview ? (
                  <div className="max-w-3xl px-8 py-6">
                    <Markdown>{file.text}</Markdown>
                  </div>
                ) : (
                  <CodeView text={file.text} />
                ))}
            </div>
          </>
        )}
      </section>
      <NewFileModal
        open={newOpen}
        dir={sel.includes("/") ? sel.split("/").slice(0, -1).join("/") + "/" : ""}
        onClose={() => setNewOpen(false)}
        onCreate={async (p) => {
          try {
            await api.repoWrite(ws, repo, p, "");
            setNewOpen(false);
            await files.reload();
            setSel(p);
            onChanged();
          } catch (e) {
            toast.error("Cannot create", errMsg(e));
          }
        }}
      />
    </div>
  );
}

export function CodeView({ text }: { text: string }) {
  const lines = text.split("\n");
  if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
  return (
    <div className="font-mono text-[12px] leading-[1.6] py-2" dir="ltr">
      {lines.map((l, i) => (
        <div key={i} className="flex hover:bg-panel-2">
          <span className="w-12 shrink-0 pe-3 text-end text-faint select-none tabular-nums">{i + 1}</span>
          <span className="whitespace-pre-wrap break-all pe-4">{l || " "}</span>
        </div>
      ))}
    </div>
  );
}

function NewFileModal({ open, dir, onClose, onCreate }: { open: boolean; dir: string; onClose: () => void; onCreate: (p: string) => Promise<void> }) {
  const [p, setP] = useState("");
  useEffect(() => {
    if (open) setP(dir);
  }, [open, dir]);
  const clean = p.trim().replace(/^\/+/, "");
  const valid = clean !== "" && !clean.endsWith("/") && !clean.split("/").some((s) => s === ".." || s.toLowerCase() === ".git" || s === "");
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New file"
      icon={<FilePlus2 />}
      width={460}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!valid} onClick={() => void onCreate(clean)}>
            Create
          </Button>
        </>
      }
    >
      <FormField label="Path" help="Relative to the repository root; folders are created as needed.">
        <Input mono autoFocus value={p} onChange={(e) => setP(e.target.value)} placeholder="k8s/overlays/prod/patch.yaml" />
      </FormField>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Changes

const CHANGE_TONE: Record<RepoChange["status"], Tone> = {
  modified: "warn",
  added: "ok",
  untracked: "ok",
  deleted: "err",
  renamed: "info",
  conflict: "err",
  typechange: "warn",
};
const CHANGE_LETTER: Record<RepoChange["status"], string> = { modified: "M", added: "A", untracked: "U", deleted: "D", renamed: "R", conflict: "!", typechange: "T" };

function ChangesTab({
  ws,
  repo,
  st,
  rev,
  op,
  onPush,
}: {
  ws: string;
  repo: string;
  st: RepoStatus;
  rev: number;
  op: (label: string, fn: () => Promise<unknown>, quiet?: boolean) => Promise<boolean>;
  onPush: () => void;
}) {
  const confirm = useConfirm();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [focus, setFocus] = useState<string>("");
  const [msg, setMsg] = useState("");
  const [pushAfter, setPushAfter] = useState(true);
  const paths = st.changes.map((c) => c.path).join("\n");
  useEffect(() => {
    setSelected(new Set(st.changes.map((c) => c.path)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paths]);
  const diff = useAsync(() => api.repoDiff(ws, repo, focus), [ws, repo, focus, rev, paths]);

  if (st.changes.length === 0) {
    return (
      <EmptyState
        icon={<CircleCheck />}
        title="No uncommitted changes"
        action={
          st.ahead > 0 ? (
            <Button variant="primary" icon={<ArrowUp className="size-4" />} onClick={onPush}>
              Push {st.ahead} commit{st.ahead > 1 ? "s" : ""}
            </Button>
          ) : undefined
        }
      >
        Edit files in the Files tab (or let the agent do it); changes show up here for review before you commit.
      </EmptyState>
    );
  }
  const chosen = st.changes.filter((c) => selected.has(c.path)).map((c) => c.path);
  const all = chosen.length === st.changes.length;

  async function commit() {
    const ok = await op("Commit", () => api.repoCommit(ws, repo, msg, all ? [] : chosen));
    if (ok) {
      setMsg("");
      if (pushAfter) onPush();
    }
  }

  async function discard(list: string[]) {
    const ok = await confirm({
      title: `Discard ${list.length === st.changes.length ? "all changes" : list.length === 1 ? list[0] : `${list.length} changes`}?`,
      message: "Uncommitted edits are lost; new files are deleted.",
      confirmLabel: "Discard",
      danger: true,
    });
    if (ok) await op("Discard", () => api.repoDiscard(ws, repo, list.length === st.changes.length ? [] : list));
  }

  return (
    <div className="flex h-full min-h-0">
      <aside className="w-80 shrink-0 border-r border-line bg-panel flex flex-col">
        <div className="flex items-center gap-2 px-3 h-10 border-b border-line text-xs">
          <input
            type="checkbox"
            className="accent-[var(--color-accent)]"
            checked={all}
            onChange={() => setSelected(all ? new Set() : new Set(st.changes.map((c) => c.path)))}
          />
          <span className="flex-1 text-muted">
            {chosen.length} of {st.changes.length} selected
          </span>
          <Button size="xs" variant="ghost" icon={<Undo2 className="size-3" />} disabled={chosen.length === 0} onClick={() => void discard(chosen)}>
            Discard
          </Button>
        </div>
        <div className="flex-1 overflow-y-auto py-1">
          <button
            onClick={() => setFocus("")}
            className={cx("w-full px-3 py-1 text-left text-[12px]", focus === "" ? "text-fg bg-panel-3" : "text-muted hover:bg-panel-2")}
          >
            All changes
          </button>
          {st.changes.map((c) => (
            <div key={c.path} className={cx("flex items-center gap-2 px-3 py-1", focus === c.path ? "bg-panel-3" : "hover:bg-panel-2")}>
              <input
                type="checkbox"
                className="accent-[var(--color-accent)]"
                checked={selected.has(c.path)}
                onChange={() =>
                  setSelected((s) => {
                    const n = new Set(s);
                    if (n.has(c.path)) n.delete(c.path);
                    else n.add(c.path);
                    return n;
                  })
                }
              />
              <button onClick={() => setFocus(c.path)} className="min-w-0 flex-1 text-left">
                <span className="block truncate font-mono text-[12px]" title={c.orig ? `${c.orig} → ${c.path}` : c.path}>
                  {c.path.split("/").pop()}
                </span>
                <span className="block truncate text-[10.5px] text-faint">{c.path.split("/").slice(0, -1).join("/") || "/"}</span>
              </button>
              <Badge tone={CHANGE_TONE[c.status]} title={c.status} mono>
                {CHANGE_LETTER[c.status]}
              </Badge>
            </div>
          ))}
        </div>
        <div className="border-t border-line p-3 space-y-2">
          <Textarea
            value={msg}
            onChange={(e) => setMsg(e.target.value)}
            rows={4}
            placeholder={"Commit message\n\nWhat changes and why"}
            className="text-[12.5px]"
            onKeyDown={(e) => {
              if ((e.ctrlKey || e.metaKey) && e.key === "Enter" && msg.trim() && chosen.length) {
                e.preventDefault();
                void commit();
              }
            }}
          />
          <Toggle checked={pushAfter} onChange={setPushAfter} label={<span className="text-xs">Push after committing</span>} />
          <Button
            variant="primary"
            className="w-full"
            icon={<GitCommitHorizontal className="size-4" />}
            disabled={!msg.trim() || chosen.length === 0}
            onClick={() => void commit()}
          >
            Commit {all ? "all" : chosen.length} to {st.branch || "HEAD"}
          </Button>
          {st.branch && st.branch === st.default && (
            <div className="text-[11px] text-warn">You are on the default branch. Create a branch first to propose the change in a merge request.</div>
          )}
        </div>
      </aside>
      <section className="flex-1 min-w-0 overflow-auto">
        {diff.error && <ErrorBox error={diff.error} />}
        {diff.loading && !diff.data && <Loading />}
        {diff.data && <DiffView text={diff.data.diff} />}
        {diff.data?.truncated && <div className="px-4 py-2 text-xs text-faint">Diff truncated.</div>}
      </section>
    </div>
  );
}

export function DiffView({ text }: { text: string }) {
  if (!text.trim()) return <div className="p-6 text-muted text-sm">No differences.</div>;
  return (
    <div className="font-mono text-[12px] leading-[1.55] py-2" dir="ltr">
      {text.split("\n").map((l, i) => {
        const file = l.startsWith("diff --git");
        const cls = file
          ? "mt-3 first:mt-0 border-y border-line bg-panel-2 text-fg font-semibold py-1"
          : l.startsWith("+++") || l.startsWith("---") || l.startsWith("index ") || l.startsWith("new file") || l.startsWith("deleted file") || l.startsWith("similarity") || l.startsWith("rename ")
            ? "text-faint"
            : l.startsWith("@@")
              ? "text-info bg-info/5"
              : l.startsWith("+")
                ? "text-ok bg-ok/8"
                : l.startsWith("-")
                  ? "text-err bg-err/8"
                  : "text-muted";
        return (
          <div key={i} className={cx("px-4 whitespace-pre-wrap break-all", cls)}>
            {file ? l.replace(/^diff --git a\/(.*) b\/.*$/, "$1") : l || " "}
          </div>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// History

function HistoryTab({ ws, repo, branch, rev }: { ws: string; repo: string; branch: string; rev: number }) {
  const log = useAsync(() => api.repoLog(ws, repo, "", "", 200), [ws, repo, branch, rev]);
  const [open, setOpen] = useState<string>("");
  const show = useAsync(() => (open ? api.repoShow(ws, repo, open) : Promise.resolve(null)), [ws, repo, open]);
  return (
    <div className="flex h-full min-h-0">
      <aside className="w-[28rem] shrink-0 border-r border-line overflow-y-auto">
        {log.loading && !log.data && <Loading />}
        {log.error && <ErrorBox error={log.error} />}
        {log.data?.length === 0 && <EmptyState icon={<History />} title="No commits yet" />}
        {(log.data ?? []).map((c) => (
          <button
            key={c.hash}
            onClick={() => setOpen(c.hash)}
            className={cx("w-full text-left px-4 py-2 border-b border-line", open === c.hash ? "bg-panel-3" : "hover:bg-panel-2")}
          >
            <div className="text-[13px] truncate">{c.subject}</div>
            <div className="text-[11px] text-faint flex items-center gap-2">
              <span className="font-mono">{c.short}</span>
              <span className="truncate">{c.author}</span>
              <span>{timeAgo(c.date)}</span>
              {c.refs && (
                <span className="truncate text-accent font-mono" title={c.refs}>
                  {c.refs.replace(/HEAD -> /, "")}
                </span>
              )}
            </div>
          </button>
        ))}
      </aside>
      <section className="flex-1 min-w-0 overflow-auto">
        {!open && <EmptyState icon={<GitCommitHorizontal />} title="Pick a commit" />}
        {open && show.loading && <Loading />}
        {show.data && <DiffView text={show.data.diff} />}
      </section>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Merge requests

function checksBadge(c?: string) {
  if (!c) return null;
  const icon = c === "success" ? <CircleCheck className="size-3" /> : c === "failed" ? <CircleX className="size-3" /> : <CircleDot className="size-3" />;
  return (
    <Badge tone={c === "success" ? "ok" : c === "failed" ? "err" : "warn"}>
      {icon} CI {c}
    </Badge>
  );
}

function MRTab({ ws, repo, rev, onNew, canNew }: { ws: string; repo: string; rev: number; onNew: () => void; canNew: boolean }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [state, setState] = useState<"open" | "merged" | "closed" | "all">("open");
  const mrs = useAsync(() => api.mrs(ws, repo, state), [ws, repo, state, rev]);
  const [busy, setBusy] = useState<number | null>(null);
  const [comment, setComment] = useState<MergeRequest | null>(null);
  const provider = mrs.data?.provider;
  const noun = provider === "github" ? "pull request" : "merge request";

  async function merge(m: MergeRequest) {
    let method = "merge";
    let removeSource = true;
    const ok = await confirm({
      title: `Merge !${m.id} into ${m.target}?`,
      message: (
        <div className="space-y-3">
          <div>
            <b>{m.title}</b> — {m.source} → {m.target}. Changes on {m.target} may deploy.
          </div>
          <MergeOptions provider={provider} onChange={(mm, rs) => {
            method = mm;
            removeSource = rs;
          }} />
        </div>
      ),
      confirmLabel: "Merge",
      danger: true,
    });
    if (!ok) return;
    setBusy(m.id);
    try {
      await api.mrMerge(ws, repo, m.id, method, removeSource);
      toast.success(`Merged !${m.id}`, "Pull the target branch to update the working copy.");
      await mrs.reload();
    } catch (e) {
      toast.error(`Merge !${m.id} failed`, errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="h-full overflow-y-auto">
      <div className="flex items-center gap-2 px-5 py-3">
        <Segmented
          value={state}
          onChange={setState}
          options={[
            { id: "open", label: "Open" },
            { id: "merged", label: "Merged" },
            { id: "closed", label: "Closed" },
            { id: "all", label: "All" },
          ]}
        />
        <div className="flex-1" />
        <Button size="sm" variant="ghost" icon={<RefreshCw className="size-3.5" />} onClick={() => void mrs.reload()}>
          Refresh
        </Button>
        <Button size="sm" variant="primary" icon={<GitPullRequest className="size-3.5" />} onClick={onNew} disabled={!canNew} title={canNew ? "" : "Switch to a branch other than the default"}>
          New {noun}
        </Button>
      </div>
      <div className="px-5 pb-6 space-y-2 max-w-5xl">
        {mrs.error && <ErrorBox error={mrs.error} onRetry={() => void mrs.reload()} />}
        {mrs.loading && !mrs.data && <Loading />}
        {mrs.data && (mrs.data.items ?? []).length === 0 && <EmptyState icon={<GitPullRequest />} title={`No ${state === "all" ? "" : state + " "}${noun}s`} />}
        {(mrs.data?.items ?? []).map((m) => (
          <div key={m.id} className="rounded-lg border border-line bg-panel px-4 py-3">
            <div className="flex items-start gap-3">
              {m.state === "merged" ? <GitMerge className="size-4 text-accent mt-0.5" /> : <GitPullRequest className={cx("size-4 mt-0.5", m.state === "open" ? "text-ok" : "text-faint")} />}
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-[13.5px] font-medium">{m.title}</span>
                  <span className="text-faint text-xs">!{m.id}</span>
                  {m.draft && <Badge tone="neutral">draft</Badge>}
                  {m.state !== "open" && <Badge tone={m.state === "merged" ? "accent" : "neutral"}>{m.state}</Badge>}
                  {checksBadge(m.checks)}
                  {m.mergeable && m.state === "open" && <Badge tone={/mergeable|clean/.test(m.mergeable) ? "ok" : "neutral"}>{m.mergeable.replace(/_/g, " ")}</Badge>}
                </div>
                <div className="text-[11.5px] text-faint mt-0.5">
                  <span className="font-mono">{m.source}</span> → <span className="font-mono">{m.target}</span> · {m.author} · updated {timeAgo(m.updated)}
                </div>
              </div>
              {m.url && (
                <a href={m.url} target="_blank" rel="noreferrer">
                  <Button size="sm" variant="ghost" icon={<ExternalLink className="size-3.5" />}>
                    Open
                  </Button>
                </a>
              )}
              {m.state === "open" && (
                <>
                  <Button size="sm" variant="secondary" onClick={() => setComment(m)}>
                    Comment
                  </Button>
                  <Button size="sm" variant="primary" loading={busy === m.id} icon={<GitMerge className="size-3.5" />} onClick={() => void merge(m)}>
                    Merge
                  </Button>
                </>
              )}
            </div>
          </div>
        ))}
      </div>
      {comment && <CommentModal ws={ws} repo={repo} mr={comment} onClose={() => setComment(null)} />}
    </div>
  );
}

function MergeOptions({ provider, onChange }: { provider?: string; onChange: (method: string, removeSource: boolean) => void }) {
  const [method, setMethod] = useState("merge");
  const [rm, setRm] = useState(true);
  useEffect(() => {
    onChange(method, rm);
  }, [method, rm, onChange]);
  const methods = provider === "github" ? ["merge", "squash", "rebase"] : ["merge", "squash"];
  return (
    <div className="flex items-center gap-4 flex-wrap">
      <Segmented value={method} onChange={setMethod} options={methods.map((m) => ({ id: m, label: m }))} />
      <Toggle checked={rm} onChange={setRm} label={<span className="text-xs">Delete the source branch</span>} />
    </div>
  );
}

function CommentModal({ ws, repo, mr, onClose }: { ws: string; repo: string; mr: MergeRequest; onClose: () => void }) {
  const toast = useToast();
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Modal
      open
      onClose={onClose}
      title={`Comment on !${mr.id}`}
      width={560}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={busy}
            disabled={!body.trim()}
            onClick={async () => {
              setBusy(true);
              try {
                await api.mrComment(ws, repo, mr.id, body);
                toast.success("Comment posted");
                onClose();
              } catch (e) {
                toast.error("Comment failed", errMsg(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Post
          </Button>
        </>
      }
    >
      <Textarea autoFocus rows={6} value={body} onChange={(e) => setBody(e.target.value)} placeholder="Markdown" />
    </Modal>
  );
}

function NewMRModal({ ws, repo, source, target, onClose, onCreated }: { ws: string; repo: string; source: string; target: string; onClose: () => void; onCreated: () => void }) {
  const toast = useToast();
  const log = useAsync(() => api.repoLog(ws, repo, source, "", 20), [ws, repo, source]);
  const [title, setTitle] = useState("");
  const [desc, setDesc] = useState("");
  const [tgt, setTgt] = useState(target);
  const [draft, setDraft] = useState(false);
  const [rm, setRm] = useState(true);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!title && log.data?.[0]) setTitle(log.data[0].subject);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [log.data]);
  return (
    <Modal
      open
      onClose={onClose}
      title="New merge request"
      icon={<GitPullRequest />}
      width={620}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={busy}
            disabled={!title.trim() || !tgt || tgt === source}
            onClick={async () => {
              setBusy(true);
              try {
                const m = await api.mrCreate(ws, repo, { title, description: desc, source, target: tgt, draft, removeSource: rm });
                toast.success(`Opened !${m.id}`, m.url);
                onCreated();
              } catch (e) {
                toast.error("Could not open the merge request", errMsg(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Open merge request
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="flex items-center gap-2 text-xs">
          <span className="font-mono rounded bg-panel-3 px-1.5 py-0.5">{source}</span>
          <span className="text-faint">into</span>
          <Input mono value={tgt} onChange={(e) => setTgt(e.target.value)} className="w-48 h-7" />
        </div>
        <div className="text-[11px] text-faint">The branch must be pushed first.</div>
        <FormField label="Title">
          <Input autoFocus value={title} onChange={(e) => setTitle(e.target.value)} />
        </FormField>
        <FormField label="Description" help="What changes, why, how it was verified, how to roll back.">
          <Textarea rows={6} value={desc} onChange={(e) => setDesc(e.target.value)} />
        </FormField>
        <div className="flex items-center gap-5">
          <Toggle checked={draft} onChange={setDraft} label={<span className="text-xs">Draft</span>} />
          <Toggle checked={rm} onChange={setRm} label={<span className="text-xs">Delete the branch after merging (GitLab)</span>} />
        </div>
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Adding a repository: pick it from a connected account.

function AddRepoModal({ onClose, onAdded, onManual }: { onClose: () => void; onAdded: () => void; onManual: () => void }) {
  const { name: ws, workspace, resources } = useWs();
  const toast = useToast();
  const conns = useAsync(() => api.gitConnections(), []);
  const [connId, setConnId] = useState("");
  const [connecting, setConnecting] = useState(false);
  const [via, setVia] = useState("");
  const [q, setQ] = useState("");
  const [dq, setDq] = useState("");
  const [pick, setPick] = useState<RemoteRepo | null>(null);
  const [name, setName] = useState("");
  const [env, setEnv] = useState("");
  const [docs, setDocs] = useState(false);
  const [clone, setClone] = useState(true);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!connId && conns.data?.length) setConnId(conns.data[0].id);
  }, [conns.data, connId]);
  useEffect(() => {
    const t = setTimeout(() => setDq(q), 300);
    return () => clearTimeout(t);
  }, [q]);
  const list = useAsync(() => (connId ? api.gitAccountRepos(ws, connId, dq, via) : Promise.resolve([])), [ws, connId, dq, via]);
  const existing = resources.filter((r) => r.kind === "GitRemote");
  const added = (r: RemoteRepo) => existing.some((x) => [r.httpUrl, r.sshUrl, r.webUrl].includes(String(x.spec.url ?? "").replace(/\/$/, "")));
  const hops = resources.filter((r) => NETWORK_KINDS.includes(r.kind) || r.kind === "Host");
  const conn = conns.data?.find((c) => c.id === connId);

  function choose(r: RemoteRepo) {
    setPick(r);
    let n = slugify(r.name) || "repo";
    const taken = new Set(existing.map((x) => x.name));
    for (let i = 2; taken.has(n); i++) n = `${slugify(r.name)}-${i}`;
    setName(n);
    setDocs(/doc|wiki|runbook|handbook/i.test(r.fullName));
  }

  async function add() {
    if (!pick || !conn) return;
    setBusy(true);
    try {
      const spec: Record<string, unknown> = { connection: conn.id, url: pick.httpUrl };
      if (docs) spec.docs = true;
      await api.createResource(ws, { kind: "GitRemote", name, environment: env || undefined, via: via || undefined, description: pick.description || undefined, spec } as Resource);
      if (clone) {
        try {
          await api.repoClone(ws, name);
        } catch (e) {
          toast.error(`Added ${name}, but cloning failed`, errMsg(e));
          onAdded();
          return;
        }
      }
      toast.success(`Added ${name}`, clone ? `cloned into repos/${name}` : pick.fullName);
      onAdded();
    } catch (e) {
      toast.error("Could not add the repository", errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  if (conns.data && conns.data.length === 0) {
    return connecting ? (
      <ConnectModal
        onClose={() => setConnecting(false)}
        onConnected={(c) => {
          setConnecting(false);
          setConnId(c.id);
          void conns.reload();
        }}
      />
    ) : (
      <Modal open onClose={onClose} title="Add a repository" icon={<GitBranch />} width={520}>
        <div className="text-center py-4 space-y-3">
          <div className="flex justify-center gap-3 text-muted">
            <GitHubMark className="size-7" />
            <GitLabMark className="size-7" />
          </div>
          <div className="text-[13px]">Connect your GitHub or GitLab account first: you then pick repositories from a list.</div>
          <Button variant="primary" icon={<Plug className="size-4" />} onClick={() => setConnecting(true)}>
            Connect an account
          </Button>
          <div>
            <button className="text-xs text-muted hover:text-fg underline" onClick={onManual}>
              or add one by URL
            </button>
          </div>
        </div>
      </Modal>
    );
  }
  if (connecting) {
    return (
      <ConnectModal
        onClose={() => setConnecting(false)}
        onConnected={(c) => {
          setConnecting(false);
          setConnId(c.id);
          void conns.reload();
        }}
      />
    );
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Add a repository"
      icon={<GitBranch />}
      width={680}
      footer={
        <>
          <button className="me-auto text-xs text-muted hover:text-fg underline" onClick={onManual}>
            Add by URL instead
          </button>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!pick || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(name)} loading={busy} onClick={() => void add()}>
            {clone ? "Add and clone" : "Add"}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="flex items-end gap-2">
          <FormField label="Account">
            <Select
              value={connId}
              onChange={(e) => {
                if (e.target.value === "__new") setConnecting(true);
                else {
                  setConnId(e.target.value);
                  setPick(null);
                }
              }}
            >
              {(conns.data ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.provider === "github" ? "GitHub" : "GitLab"} · {c.host} · {c.login}
                </option>
              ))}
              <option value="__new">+ Connect another account…</option>
            </Select>
          </FormField>
          {hops.length > 0 && (
            <FormField label="Reach the server through">
              <Select value={via} onChange={(e) => setVia(e.target.value)}>
                <option value="">direct</option>
                {hops.map((h) => {
                  const ref = `${h.kind === "Host" ? "host" : "network"}/${h.name}`;
                  return (
                    <option key={ref} value={ref}>
                      {ref}
                    </option>
                  );
                })}
              </Select>
            </FormField>
          )}
        </div>
        <SearchInput value={q} onChange={setQ} placeholder="Search your repositories…" autoFocus />
        <div className="rounded-md border border-line max-h-72 overflow-y-auto">
          {list.loading && !list.data && <Loading />}
          {list.error && <div className="p-3 text-xs text-err whitespace-pre-wrap">{list.error}</div>}
          {list.data?.length === 0 && <div className="p-4 text-xs text-faint text-center">No repositories{dq ? ` matching “${dq}”` : ""}.</div>}
          {(list.data ?? []).map((r) => {
            const isAdded = added(r);
            return (
              <button
                key={r.fullName}
                disabled={isAdded}
                onClick={() => choose(r)}
                className={cx(
                  "w-full flex items-center gap-3 px-3 py-2 text-left border-b border-line last:border-0",
                  pick?.fullName === r.fullName ? "bg-accent/10" : "hover:bg-panel-2",
                  isAdded && "opacity-50",
                )}
              >
                <ProviderMark provider={conn?.provider ?? "gitlab"} className="size-4 text-muted shrink-0" />
                <span className="min-w-0 flex-1">
                  <span className="block font-mono text-[12.5px] truncate">{r.fullName}</span>
                  <span className="block text-[11px] text-faint truncate">{r.description || "—"}</span>
                </span>
                {r.private && <Badge tone="neutral">private</Badge>}
                {!r.canPush && <Badge tone="warn">read-only</Badge>}
                {isAdded ? <Badge tone="ok">added</Badge> : <span className="text-[11px] text-faint whitespace-nowrap">{timeAgo(r.updated)}</span>}
              </button>
            );
          })}
        </div>
        {pick && (
          <div className="rounded-md border border-line bg-panel-2 p-3 space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <FormField label="Name in this workspace" help={<span className="font-mono">repos/{name}</span>}>
                <Input mono value={name} onChange={(e) => setName(e.target.value)} />
              </FormField>
              <FormField label="Environment" help="Protected environments make every push ask.">
                <Select value={env} onChange={(e) => setEnv(e.target.value)}>
                  <option value="">—</option>
                  {(workspace?.environments ?? []).map((e) => (
                    <option key={e.name} value={e.name}>
                      {e.name}
                      {e.protected ? " (protected)" : ""}
                    </option>
                  ))}
                </Select>
              </FormField>
            </div>
            <div className="flex items-center gap-5">
              <Toggle checked={docs} onChange={setDocs} label={<span className="text-xs">Use as documentation</span>} />
              <Toggle checked={clone} onChange={setClone} label={<span className="text-xs">Clone now</span>} />
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}
