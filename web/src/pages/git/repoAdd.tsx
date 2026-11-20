import { useEffect, useState } from "react";
import {
  CircleCheck,
  Folder,
  GitBranch,
  Plug,
} from "lucide-react";
import type { RemoteRepo, RepoFolder, Resource } from "../../api/types";
import { NETWORK_KINDS } from "../../api/types";
import { api, errMsg } from "../../api/client";
import { useWs } from "../../context/app";
import { Badge, Button, FormField, Input, Loading, SearchInput, Segmented, Select, Toggle, cx } from "../../components/ui";
import { GitHubMark, GitLabMark, ProviderMark } from "../../components/brand";
import { Modal, useToast } from "../../components/overlay";
import { useAsync } from "../../hooks";
import { slugify, timeAgo } from "../../lib/format";
import { ConnectModal } from "../Connectors";

// ---------------------------------------------------------------------------
// Adding a repository: pick it from a connected account.

export function AddRepoModal({ onClose, onAdded, onManual, onExisting }: { onClose: () => void; onAdded: () => void; onManual: () => void; onExisting: () => void }) {
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
  const [where, setWhere] = useState<"workspace" | "folder">("workspace");
  const [path, setPath] = useState("");
  const [folder, setFolder] = useState<RepoFolder | null>(null);
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
      const inFolder = where === "folder";
      if (inFolder) spec.path = path.trim();
      const doClone = inFolder ? !!folder?.empty : clone;
      await api.createResource(ws, { kind: "GitRemote", name, environment: env || undefined, via: via || undefined, description: pick.description || undefined, spec } as Resource);
      if (doClone) {
        try {
          await api.repoClone(ws, name);
        } catch (e) {
          toast.error(`Added ${name}, but cloning failed`, errMsg(e));
          onAdded();
          return;
        }
      }
      const dir = inFolder ? folder?.path ?? path.trim() : `repos/${name}`;
      toast.success(`Added ${name}`, doClone ? `cloned into ${dir}` : inFolder ? `working copy ${dir}` : pick.fullName);
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
          <div className="flex justify-center gap-4">
            <button className="text-xs text-muted hover:text-fg underline" onClick={onManual}>
              or add one by URL
            </button>
            <button className="text-xs text-muted hover:text-fg underline" onClick={onExisting}>
              use an existing clone on this computer
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
          <div className="me-auto flex items-center gap-3">
            <button className="text-xs text-muted hover:text-fg underline" onClick={onManual}>
              Add by URL instead
            </button>
            <button className="text-xs text-muted hover:text-fg underline" onClick={onExisting}>
              Use an existing clone
            </button>
          </div>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={!pick || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(name) || (where === "folder" && !folder?.cloned && !folder?.empty)}
            loading={busy}
            onClick={() => void add()}
          >
            {(where === "folder" ? folder?.empty : clone) ? "Add and clone" : "Add"}
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
                  {c.title ? `${c.title} · ` : ""}
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
              <FormField label="Name in this workspace" help={where === "workspace" ? <span className="font-mono">repos/{name}</span> : undefined}>
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
            <FormField label="Working copy">
              <Segmented
                options={[
                  { id: "workspace", label: "Clone into the workspace" },
                  { id: "folder", label: "Existing folder", title: "A clone you already have elsewhere on this computer" },
                ]}
                value={where}
                onChange={setWhere}
              />
            </FormField>
            {where === "folder" && <FolderField ws={ws} path={path} onPath={setPath} folder={folder} onFolder={setFolder} expect={[pick.httpUrl, pick.sshUrl, pick.webUrl]} />}
            <div className="flex items-center gap-5">
              <Toggle checked={docs} onChange={setDocs} label={<span className="text-xs">Use as documentation</span>} />
              {where === "workspace" && <Toggle checked={clone} onChange={setClone} label={<span className="text-xs">Clone now</span>} />}
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// A working copy the user already has elsewhere on this computer.

// repoKey reduces a Git URL to host/path, so https and SSH URLs of the same
// repository compare equal.
function repoKey(u?: string): string {
  let s = (u ?? "").trim().toLowerCase().replace(/\/+$/, "").replace(/\.git$/, "");
  const scp = s.match(/^[^/@]+@([^:/]+):(.+)$/);
  if (scp) return `${scp[1]}/${scp[2]}`;
  s = s.replace(/^[a-z+]+:\/\//, "").replace(/^[^@/]+@/, "");
  return s.replace(/^([^/:]+):\d+\//, "$1/");
}

function FolderField({
  ws,
  path,
  onPath,
  folder,
  onFolder,
  expect,
}: {
  ws: string;
  path: string;
  onPath: (p: string) => void;
  folder: RepoFolder | null;
  onFolder: (f: RepoFolder | null) => void;
  expect?: string[];
}) {
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const p = path.trim();
    onFolder(null);
    setError("");
    if (!p) return;
    let cancelled = false;
    const t = setTimeout(async () => {
      setChecking(true);
      try {
        const f = await api.repoFolder(ws, p);
        if (!cancelled) onFolder(f);
      } catch (e) {
        if (!cancelled) setError(errMsg(e));
      } finally {
        if (!cancelled) setChecking(false);
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(t);
    };
  }, [ws, path]);
  const mismatch = folder?.cloned && folder.origin && expect?.length && !expect.some((u) => u && repoKey(u) === repoKey(folder.origin));
  return (
    <FormField label="Folder" help="Absolute path or ~/…, on this computer. opsdeck works in it in place and never deletes it.">
      <Input mono value={path} onChange={(e) => onPath(e.target.value)} placeholder="~/projects/deploy" autoFocus />
      <div className="mt-1.5 text-xs">
        {checking && <span className="text-faint">Checking…</span>}
        {!checking && error && <span className="text-err">{error}</span>}
        {!checking && folder?.cloned && (
          <span className="text-ok inline-flex items-center gap-1 flex-wrap">
            <CircleCheck className="size-3.5" /> Git working copy
            {folder.branch && (
              <>
                {" "}
                on <span className="font-mono">{folder.branch}</span>
              </>
            )}
            {folder.origin && (
              <span className="text-faint">
                {" "}
                · origin <span className="font-mono">{folder.origin}</span>
              </span>
            )}
          </span>
        )}
        {!checking && mismatch && (
          <div className="text-warn mt-1">Its origin is not the repository you picked: pull and push go to the picked repository.</div>
        )}
        {!checking && folder?.empty && <span className="text-muted">Empty or missing: opsdeck clones the repository into it.</span>}
        {!checking && folder && !folder.cloned && !folder.empty && <span className="text-err">This folder is neither a Git working copy nor empty.</span>}
      </div>
    </FormField>
  );
}

export function ExistingRepoModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const { name: ws, workspace, resources } = useWs();
  const toast = useToast();
  const [path, setPath] = useState("");
  const [folder, setFolder] = useState<RepoFolder | null>(null);
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [env, setEnv] = useState("");
  const [via, setVia] = useState("");
  const [docs, setDocs] = useState(false);
  const [busy, setBusy] = useState(false);
  const existing = resources.filter((r) => r.kind === "GitRemote");
  const hops = resources.filter((r) => NETWORK_KINDS.includes(r.kind) || r.kind === "Host");
  useEffect(() => {
    if (!folder?.cloned) return;
    setUrl(folder.url ?? "");
    const base = slugify(folder.path.split(/[\\/]/).filter(Boolean).pop() ?? "") || "repo";
    const taken = new Set(existing.map((x) => x.name));
    let n = base;
    for (let i = 2; taken.has(n); i++) n = `${base}-${i}`;
    setName(n);
    setDocs(/doc|wiki|runbook|handbook/i.test(folder.path));
  }, [folder]);
  const dup = folder && existing.find((x) => x.spec.path && String(x.spec.path).replace(/\/+$/, "") === folder.path);

  async function add() {
    if (!folder?.cloned) return;
    setBusy(true);
    try {
      const spec: Record<string, unknown> = { url: url.trim(), path: path.trim() };
      if (docs) spec.docs = true;
      await api.createResource(ws, { kind: "GitRemote", name, environment: env || undefined, via: via || undefined, spec } as Resource);
      toast.success(`Added ${name}`, `working copy ${folder.path}`);
      onAdded();
    } catch (e) {
      toast.error("Could not add the repository", errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Use an existing clone"
      icon={<Folder />}
      width={600}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!folder?.cloned || !url.trim() || !!dup || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(name)} loading={busy} onClick={() => void add()}>
            Add
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="text-xs text-muted">
          A GitHub or GitLab repository you already cloned somewhere on this computer. opsdeck and the agent work in that folder in place, with the connector of its server (or the token set on the repository).
        </div>
        <FolderField ws={ws} path={path} onPath={setPath} folder={folder} onFolder={setFolder} />
        {dup && <div className="text-xs text-err">This folder is already the working copy of {dup.name}.</div>}
        {folder?.empty && <div className="text-xs text-muted">To clone a repository into this folder, pick it under Add repository and choose Existing folder.</div>}
        {folder?.cloned && (
          <div className="rounded-md border border-line bg-panel-2 p-3 space-y-3">
            <FormField label="Repository URL" help={folder.connection ? `Served by the connector ${folder.connection}` : "No connector serves this server: set a token on the repository for private ones"}>
              <Input mono value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://gitlab.example.com/group/project.git" />
            </FormField>
            <div className="grid grid-cols-2 gap-3">
              <FormField label="Name in this workspace">
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
            <Toggle checked={docs} onChange={setDocs} label={<span className="text-xs">Use as documentation</span>} />
          </div>
        )}
      </div>
    </Modal>
  );
}

