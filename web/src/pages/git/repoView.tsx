import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  ChevronDown,
  Download,
  ExternalLink,
  GitBranch,
  GitPullRequest,
  Plus,
  RefreshCw,
} from "lucide-react";
import type { RepoBranch } from "../../api/types";
import { api, errMsg } from "../../api/client";
import { useTabTitle } from "../../context/tabs";
import { useWs } from "../../context/app";
import { Badge, Button, EmptyState, ErrorBox, FormField, Input, Loading, SearchInput, Segmented, Tabs, cx } from "../../components/ui";
import { MenuItem, Modal, Popover, useConfirm, useToast } from "../../components/overlay";
import { useAsync, useInterval } from "../../hooks";
import { timeAgo } from "../../lib/format";
import { ChangesTab } from "./changesTab";
import { FilesTab } from "./filesTab";
import { HistoryTab } from "./historyTab";
import { MRTab, NewMRModal } from "./mrTab";
import { SyncInfo } from "./repos";

// ---------------------------------------------------------------------------
// One repository

type Tab = "files" | "changes" | "history" | "mrs";

export function RepoView({ name }: { name: string }) {
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
          {res?.spec.path ? (
            <>
              The working copy goes to <span className="font-mono">{String(res.spec.path)}</span> (a folder of its own on this computer).
            </>
          ) : (
            <>
              The working copy goes to <span className="font-mono">repos/{name}</span> in the workspace folder (ignored by the workspace's own Git).
            </>
          )}
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
