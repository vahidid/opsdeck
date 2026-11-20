import { useState } from "react";
import { Link } from "react-router-dom";
import {
  ArrowDown,
  ArrowUp,
  BookOpen,
  Download,
  ExternalLink,
  Folder,
  GitBranch,
  Pencil,
  Plug,
  Plus,
} from "lucide-react";
import type { RepoStatus, RepoSummary, Resource } from "../../api/types";
import { api, errMsg } from "../../api/client";
import { useApp, useWs } from "../../context/app";
import { Badge, Button, EmptyState, ErrorBox, IconButton, PageHeader, cx } from "../../components/ui";
import { ProviderMark } from "../../components/brand";
import { useConfirm, useToast } from "../../components/overlay";
import { useAsync, useInterval } from "../../hooks";
import { timeAgo } from "../../lib/format";
import { ResourceDrawer } from "../Resources";
import { AddRepoModal, ExistingRepoModal } from "./repoAdd";

type Preset = { id: string; label: string; spec: Record<string, unknown>; hint: string };

const GIT_PRESETS: Preset[] = [{ id: "url", label: "By URL", spec: {}, hint: "" }];

const PROVIDER_LABEL: Record<string, string> = { gitlab: "GitLab", github: "GitHub", auto: "Git" };

function providerOf(r: RepoSummary): string {
  if (r.provider !== "auto") return PROVIDER_LABEL[r.provider] ?? r.provider;
  if (/github\.com/i.test(r.url)) return "GitHub";
  if (/gitlab/i.test(r.url)) return "GitLab";
  return "Git";
}


// ---------------------------------------------------------------------------
// List

export function RepoList() {
  const { kindMap } = useApp();
  const { name: ws, resources, reloadResources } = useWs();
  const toast = useToast();
  const confirm = useConfirm();
  const repos = useAsync(() => api.repos(ws), [ws, resources.length]);
  useInterval(() => void repos.reload(), 15000);
  const [adding, setAdding] = useState(false);
  const [existing, setExisting] = useState(false);
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
      message: r.spec.path
        ? `The repository is removed from the workspace. Its working copy in ${String(r.spec.path)} and the vault items stay.`
        : "The repository is removed from the workspace. Its working copy in repos/ and the vault items stay.",
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
        subtitle="Deployment repositories on GitLab or GitHub. Their working copies live in this workspace or in a folder you cloned them to; you and the agent read, change, push and open merge requests."
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
                  {r.path && (
                    <div className="text-[11.5px] text-faint font-mono truncate inline-flex items-center gap-1 max-w-full" title="Working copy: an existing folder on this computer">
                      <Folder className="size-3 shrink-0" /> <span className="truncate">{r.path}</span>
                    </div>
                  )}
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
          onExisting={() => {
            setAdding(false);
            setExisting(true);
          }}
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
      {existing && (
        <ExistingRepoModal
          onClose={() => setExisting(false)}
          onAdded={async () => {
            setExisting(false);
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

export function SyncInfo({ st }: { st: RepoStatus }) {
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

