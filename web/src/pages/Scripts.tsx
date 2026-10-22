import { useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { BadgeCheck, BookCopy, FileCode2, History, Pencil, Play, Plus, Trash2, Upload, User, FolderGit2, X } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { Script, ScriptParam, ScriptResult, ScriptRun } from "../api/types";
import { useWs } from "../context/app";
import { useTabTitle } from "../context/tabs";
import {
  Badge,
  Button,
  ChipInput,
  CodeBlock,
  EmptyState,
  ErrorBox,
  FormField,
  IconButton,
  Input,
  Loading,
  SearchInput,
  Select,
  Tabs,
  Textarea,
  Toggle,
  cx,
} from "../components/ui";
import { useConfirm, useToast } from "../components/overlay";
import { SecretRefPicker } from "../components/VaultItemForm";
import { useAsync } from "../hooks";
import { timeAgo } from "../lib/format";

type Scope = "user" | "workspace";
type View = "script" | "run" | "history";

const TEMPLATE = `#!/usr/bin/env bash
# Parameters arrive as environment variables of the same name.
set -euo pipefail

echo "setting up $OPSDECK_HOST"
`;

// Scripts: reusable scripts that run on hosts as one unit — the user's own
// library (every workspace) and the workspace's scripts/ (shared with the
// team). The agent can save drafts; the user reviews and approves them.
export function ScriptsPage() {
  const { name: ws } = useWs();
  const toast = useToast();
  const confirm = useConfirm();
  const list = useAsync(() => api.scripts(ws), [ws]);
  const [params, setParams] = useSearchParams();
  const sel = params.get("script") ?? "";
  const selScope = (params.get("scope") as Scope) || "workspace";
  const view = (params.get("view") as View) || "script";
  const select = (name: string, scope: Scope, v: View = "script") => setParams({ script: name, scope, view: v }, { replace: true });
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<{ scope: Scope; draft: Script; isNew: boolean } | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  useTabTitle(editing ? editing.draft.name || "New script" : sel || "Scripts");

  const user = list.data?.user ?? [];
  const own = list.data?.workspace ?? [];
  const ownNames = useMemo(() => new Set(own.map((s) => s.name)), [own]);
  const match = (s: Script) => !q || (s.name + " " + s.description + " " + (s.tags ?? []).join(" ") + " " + (s.when ?? []).join(" ")).toLowerCase().includes(q.toLowerCase());

  const current = useAsync(() => (sel ? api.script(ws, selScope, sel) : Promise.resolve(null)), [ws, sel, selScope]);
  const s = current.data ?? null;

  function startNew(scope: Scope, body = TEMPLATE, name = "") {
    setEditing({ scope, isNew: true, draft: { name, description: "", version: 0, scope, hash: "", status: "draft", body, params: [] } });
  }

  async function upload(f: File) {
    const text = await f.text();
    const name = f.name
      .replace(/\.[^.]+$/, "")
      .toLowerCase()
      .replace(/[^a-z0-9._-]+/g, "-")
      .replace(/^-+|-+$/g, "");
    startNew("user", text, name);
  }

  async function saved(sc: Script) {
    setEditing(null);
    await list.reload();
    select(sc.name, sc.scope);
    await current.reload();
  }

  async function approve(sc: Script) {
    try {
      await api.approveScript(ws, sc.scope, sc.name, sc.hash);
      toast.success(`${sc.name} v${sc.version} approved`, "The agent can now run it with the usual approval rules.");
      await Promise.all([list.reload(), current.reload()]);
    } catch (e) {
      toast.error("Could not approve", errMsg(e));
    }
  }

  async function copy(sc: Script) {
    try {
      const c = await api.copyScript(ws, sc.scope, sc.name);
      toast.success(
        c.scope === "workspace" ? `Pinned ${sc.name} v${sc.version} into this workspace` : `Saved ${sc.name} to your library`,
        c.scope === "workspace" ? "The workspace copy is shared with your team and replaces your own one here." : "It is now available in every workspace.",
      );
      await list.reload();
      select(c.name, c.scope);
    } catch (e) {
      toast.error("Copy failed", errMsg(e));
    }
  }

  async function remove(sc: Script) {
    const ok = await confirm({
      title: `Delete ${sc.name}?`,
      message: sc.scope === "user" ? "It is removed from your library in every workspace, with its earlier versions." : "It is removed from this workspace's scripts/ folder, with its earlier versions.",
      confirmLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteScript(ws, sc.scope, sc.name);
      setParams({}, { replace: true });
      await list.reload();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  return (
    <div className="flex h-full min-h-0">
      <aside className="w-80 shrink-0 border-r border-line bg-panel flex flex-col">
        <div className="px-3 pt-3 pb-2 space-y-2 border-b border-line">
          <div className="font-semibold flex items-center gap-2">
            <FileCode2 className="size-4 text-muted" /> Scripts
            <div className="flex-1" />
            <IconButton title="Upload a script file" onClick={() => fileRef.current?.click()}>
              <Upload className="size-4" />
            </IconButton>
            <IconButton title="New script" onClick={() => startNew("user")}>
              <Plus className="size-4" />
            </IconButton>
            <input
              ref={fileRef}
              type="file"
              className="hidden"
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) void upload(f);
                e.target.value = "";
              }}
            />
          </div>
          <SearchInput value={q} onChange={setQ} placeholder="Search scripts…" />
        </div>
        <div className="flex-1 overflow-y-auto p-2 space-y-3">
          {list.loading && !list.data && <Loading />}
          {list.error && <ErrorBox error={list.error} onRetry={list.reload} />}
          <Group label="This workspace" icon={<FolderGit2 className="size-3" />}>
            {own.filter(match).length === 0 && <div className="text-[11px] text-faint px-2">Scripts of this project, in scripts/ — shared with the team through git.</div>}
            {own.filter(match).map((x) => (
              <Item key={"w/" + x.name} s={x} active={sel === x.name && selScope === "workspace"} onClick={() => select(x.name, "workspace")} />
            ))}
          </Group>
          <Group label="My library" icon={<User className="size-3" />}>
            {user.filter(match).length === 0 && <div className="text-[11px] text-faint px-2">Your own scripts, available in every workspace: new, upload, or ask the agent to save one.</div>}
            {user.filter(match).map((x) => (
              <Item
                key={"u/" + x.name}
                s={x}
                active={sel === x.name && selScope === "user"}
                onClick={() => select(x.name, "user")}
                dim={ownNames.has(x.name)}
                note={ownNames.has(x.name) ? "replaced here" : undefined}
              />
            ))}
          </Group>
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col">
        {editing ? (
          <ScriptEditor
            ws={ws}
            scope={editing.scope}
            isNew={editing.isNew}
            initial={editing.draft}
            onCancel={() => setEditing(null)}
            onSaved={(sc) => void saved(sc)}
          />
        ) : !sel ? (
          <EmptyState
            icon={<FileCode2 />}
            title="Scripts run on your servers as one reviewed unit"
            action={
              <div className="flex gap-2">
                <Button icon={<Plus className="size-3.5" />} onClick={() => startNew("user")}>
                  New script
                </Button>
                <Button icon={<Upload className="size-3.5" />} onClick={() => fileRef.current?.click()}>
                  Upload
                </Button>
              </div>
            }
          >
            Keep your server bootstrap, hardening and install scripts here. The agent runs them with run_script — e.g. on a host you just added — and can save new ones as drafts for you to review.
          </EmptyState>
        ) : current.error ? (
          <ErrorBox error={current.error} onRetry={current.reload} />
        ) : !s ? (
          <Loading />
        ) : (
          <>
            <div className="flex items-center gap-2 px-5 h-12 border-b border-line shrink-0">
              <span className="font-mono text-[13px] truncate">
                {s.name}
                <span className="text-faint">@v{s.version}</span>
              </span>
              <Badge tone={s.scope === "user" ? "accent" : "neutral"}>{s.scope === "user" ? "my library" : "workspace"}</Badge>
              <StatusBadge s={s} />
              {s.scope === "user" && ownNames.has(s.name) && <Badge tone="warn">replaced in this workspace</Badge>}
              <div className="flex-1" />
              {s.status === "draft" && (
                <Button size="sm" variant="primary" icon={<BadgeCheck className="size-3.5" />} onClick={() => void approve(s)}>
                  Approve v{s.version}
                </Button>
              )}
              <Button size="sm" icon={<Pencil className="size-3.5" />} onClick={() => setEditing({ scope: s.scope, isNew: false, draft: s })}>
                Edit
              </Button>
              {(s.scope === "workspace" || !ownNames.has(s.name)) && (
                <Button
                  size="sm"
                  icon={<BookCopy className="size-3.5" />}
                  title={s.scope === "user" ? "Copy into this workspace (scripts/) to share it with the team at this version" : "Copy into your library to use it in every workspace"}
                  onClick={() => void copy(s)}
                >
                  {s.scope === "user" ? "Pin to workspace" : "Save to my library"}
                </Button>
              )}
              <IconButton title="Delete" onClick={() => void remove(s)}>
                <Trash2 className="size-4" />
              </IconButton>
            </div>
            <Tabs<View>
              className="px-3 shrink-0"
              tabs={[
                { id: "script", label: "Script" },
                { id: "run", label: "Run" },
                { id: "history", label: "History" },
              ]}
              value={view}
              onChange={(v) => select(s.name, s.scope, v)}
            />
            <div className="flex-1 min-h-0 overflow-y-auto">
              {view === "script" && <ScriptView ws={ws} s={s} onApprove={() => void approve(s)} />}
              {view === "run" && <RunPanel ws={ws} s={s} onDone={() => void current.reload()} />}
              {view === "history" && <HistoryPanel ws={ws} script={s.name} />}
            </div>
          </>
        )}
      </section>
    </div>
  );
}

function StatusBadge({ s }: { s: Script }) {
  return s.status === "approved" ? (
    <Badge tone="ok" title={`approved content ${s.hash}`}>
      approved
    </Badge>
  ) : (
    <Badge tone="warn" title="not reviewed yet: each agent run needs approval and it cannot run on protected environments">
      draft
    </Badge>
  );
}

function Group({ label, icon, children }: { label: string; icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <div>
      <div className="px-2 mb-1 text-[10.5px] uppercase tracking-wider text-faint flex items-center gap-1">
        {icon}
        {label}
      </div>
      <div className="space-y-px">{children}</div>
    </div>
  );
}

function Item({ s, active, onClick, dim, note }: { s: Script; active: boolean; onClick: () => void; dim?: boolean; note?: string }) {
  return (
    <button
      onClick={onClick}
      title={s.description}
      className={cx("w-full text-left px-2 py-1 rounded-md", active ? "bg-panel-3" : "hover:bg-panel-2", dim && !active && "opacity-55")}
    >
      <span className={cx("flex items-center gap-1.5 font-mono text-[12.5px]", active ? "text-fg" : "text-muted")}>
        <span className="truncate">{s.name}</span>
        <span className="text-[10px] text-faint">v{s.version}</span>
        {s.status === "draft" && <span className="text-[10px] text-warn font-sans">draft</span>}
        {note && <span className="text-[10px] text-warn font-sans">{note}</span>}
      </span>
      <span className="block truncate text-[10.5px] text-faint">{s.description}</span>
    </button>
  );
}

function Meta({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex gap-3 text-[12.5px] py-0.5">
      <span className="w-28 shrink-0 text-faint">{label}</span>
      <span className="min-w-0">{children}</span>
    </div>
  );
}

function ScriptView({ ws, s, onApprove }: { ws: string; s: Script; onApprove: () => void }) {
  const prevVersion = s.versions?.length ? s.versions[s.versions.length - 1] : 0;
  const prev = useAsync(() => (prevVersion && s.status === "draft" ? api.script(ws, s.scope, s.name, prevVersion) : Promise.resolve(null)), [ws, s.scope, s.name, s.hash, prevVersion]);
  const [showDiff, setShowDiff] = useState(true);
  const diff = useMemo(() => (prev.data ? lineDiff(prev.data.body ?? "", s.body ?? "") : null), [prev.data, s.body]);
  return (
    <div className="max-w-4xl px-6 py-5 space-y-4">
      {s.status === "draft" && (
        <div className="rounded-md border border-warn/30 bg-warn/8 p-3 text-[12.5px] flex items-start gap-3">
          <div className="flex-1">
            <div className="font-medium text-warn">
              Draft — {s.updatedBy === "agent" ? "written or changed by the agent" : "changed outside opsdeck (hand edit or a commit)"}, not reviewed yet
            </div>
            <div className="text-muted mt-0.5">
              Read the script{prev.data ? ` and the changes since v${prevVersion}` : ""} below. Until you approve it, every run by the agent asks you first and it cannot run on protected environments.
            </div>
          </div>
          <Button size="sm" variant="primary" icon={<BadgeCheck className="size-3.5" />} onClick={onApprove}>
            Approve
          </Button>
        </div>
      )}
      <div className="text-[13px]">{s.description}</div>
      <div className="rounded-md border border-line bg-panel p-3">
        {(s.when?.length ?? 0) > 0 && <Meta label="Suggested for">{s.when!.join(", ")}</Meta>}
        <Meta label="Systems">{s.os?.length ? <span className="font-mono">{s.os.join(", ")}</span> : <span className="text-faint">any (not checked)</span>}</Meta>
        <Meta label="Runs as">{s.sudo ? "root (sudo -n, needs NOPASSWD)" : "the host's SSH user"}</Meta>
        <Meta label="Risk">{s.risk === "destructive" ? <Badge tone="err">destructive — always asks</Badge> : <Badge tone="warn">change</Badge>}</Meta>
        {s.check && (
          <Meta label="Already applied if">
            <code className="font-mono text-[12px]">{s.check}</code>
          </Meta>
        )}
        <Meta label="Timeout">{Math.round((s.timeoutSec || 1800) / 60)} min</Meta>
        <Meta label="Version">
          v{s.version} · <span className="font-mono text-faint">{s.hash}</span>
          {s.updated && (
            <span className="text-faint">
              {" "}
              · updated {timeAgo(s.updated)} by {s.updatedBy || "?"}
            </span>
          )}
          {(s.versions?.length ?? 0) > 0 && <span className="text-faint"> · earlier: {s.versions!.map((v) => "v" + v).join(", ")}</span>}
        </Meta>
        {(s.tags?.length ?? 0) > 0 && <Meta label="Tags">{s.tags!.join(", ")}</Meta>}
      </div>
      {(s.params?.length ?? 0) > 0 && (
        <div>
          <div className="text-xs font-medium text-muted mb-1">Parameters (environment variables)</div>
          <div className="rounded-md border border-line divide-y divide-line">
            {s.params!.map((p) => (
              <div key={p.name} className="flex items-baseline gap-3 px-3 py-1.5 text-[12.5px]">
                <span className="font-mono w-44 shrink-0">
                  {p.name}
                  {p.required && <span className="text-err">*</span>}
                </span>
                <Badge tone={p.type === "secret" ? "accent" : "neutral"}>{p.type || "string"}</Badge>
                <span className="text-muted flex-1 min-w-0">{p.description}</span>
                {p.default && <span className="font-mono text-[11.5px] text-faint truncate max-w-56">default {p.default}</span>}
              </div>
            ))}
          </div>
        </div>
      )}
      {diff && diff.changed && (
        <div>
          <div className="flex items-center gap-2 mb-1">
            <div className="text-xs font-medium text-muted">
              Changes since v{prevVersion} <span className="text-ok">+{diff.added}</span> <span className="text-err">−{diff.removed}</span>
            </div>
            <button className="text-[11px] text-accent hover:underline" onClick={() => setShowDiff(!showDiff)}>
              {showDiff ? "hide" : "show"}
            </button>
          </div>
          {showDiff && (
            <pre className="rounded-md border border-line bg-panel-2 py-2 font-mono text-[11.5px] leading-relaxed overflow-auto max-h-96" dir="ltr">
              {diff.lines.map((l, i) => (
                <div key={i} className={cx("px-3 whitespace-pre-wrap break-all", l.op === "+" && "bg-ok/12 text-ok", l.op === "-" && "bg-err/10 text-err")}>
                  {l.op}
                  {l.text}
                </div>
              ))}
            </pre>
          )}
        </div>
      )}
      <div>
        <div className="text-xs font-medium text-muted mb-1 font-mono">{s.file || "script.sh"}</div>
        <div dir="ltr">
          <CodeBlock>{s.body ?? ""}</CodeBlock>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------

function ScriptEditor({
  ws,
  scope: initialScope,
  isNew,
  initial,
  onCancel,
  onSaved,
}: {
  ws: string;
  scope: Scope;
  isNew: boolean;
  initial: Script;
  onCancel: () => void;
  onSaved: (s: Script) => void;
}) {
  const [d, setD] = useState<Script>(() => ({ ...initial, params: (initial.params ?? []).map((p) => ({ ...p })) }));
  const [scope, setScope] = useState<Scope>(initialScope);
  const [err, setErr] = useState("");
  const [saving, setSaving] = useState(false);
  const set = (patch: Partial<Script>) => setD((x) => ({ ...x, ...patch }));
  const setParam = (i: number, patch: Partial<ScriptParam>) => set({ params: (d.params ?? []).map((p, j) => (j === i ? { ...p, ...patch } : p)) });

  async function save() {
    setSaving(true);
    setErr("");
    try {
      const sc = await api.saveScript(ws, scope, d.name, {
        description: d.description,
        body: d.body,
        file: d.file,
        os: d.os,
        when: d.when,
        tags: d.tags,
        sudo: d.sudo,
        risk: d.risk,
        timeoutSec: d.timeoutSec,
        check: d.check,
        params: (d.params ?? []).filter((p) => p.name.trim()),
      });
      onSaved(sc);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <div className="flex items-center gap-2 px-5 h-12 border-b border-line shrink-0">
        <span className="font-medium text-[13px]">{isNew ? "New script" : `Edit ${initial.name}`}</span>
        {!isNew && <Badge tone="neutral">saving makes v{initial.version + 1} if what runs changes</Badge>}
        <div className="flex-1" />
        <Button size="sm" variant="ghost" icon={<X className="size-3.5" />} onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" variant="primary" loading={saving} disabled={!d.name || !d.description.trim() || !(d.body ?? "").trim()} onClick={() => void save()}>
          Save
        </Button>
      </div>
      <div className="flex-1 min-h-0 overflow-y-auto">
        {err && <ErrorBox error={err} />}
        <div className="max-w-4xl px-6 py-5 space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Name" required help="lowercase letters, digits, . _ -">
              <Input mono value={d.name} disabled={!isNew} onChange={(e) => set({ name: e.target.value.toLowerCase() })} placeholder="server-bootstrap" />
            </FormField>
            <FormField label="Save in" help={scope === "user" ? "Your library: available in every workspace, only on this computer." : "This workspace's scripts/: shared with the team through git."}>
              <Select value={scope} disabled={!isNew} onChange={(e) => setScope(e.target.value as Scope)}>
                <option value="user">My library (all workspaces)</option>
                <option value="workspace">This workspace</option>
              </Select>
            </FormField>
          </div>
          <FormField label="Description" required help="What it does and when to use it — the agent decides from this.">
            <Input value={d.description} onChange={(e) => set({ description: e.target.value })} placeholder="Initial setup of a fresh Ubuntu server: deploy user, SSH hardening, ufw, docker" />
          </FormField>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <FormField label="Supported systems" help="ID[-VERSION_ID] from /etc/os-release, e.g. ubuntu, ubuntu-24.04, debian-12. Other systems are refused.">
                <span />
              </FormField>
              <ChipInput mono value={d.os ?? []} onChange={(v) => set({ os: v })} placeholder="ubuntu, debian-12…" />
            </div>
            <div>
              <FormField label="Suggest when" help="Hints for the agent, e.g. new-host, hardening, monitoring.">
                <span />
              </FormField>
              <ChipInput value={d.when ?? []} onChange={(v) => set({ when: v })} placeholder="new-host" />
            </div>
          </div>
          <div className="grid grid-cols-3 gap-3 items-end">
            <div className="pb-1.5">
              <Toggle checked={!!d.sudo} onChange={(v) => set({ sudo: v })} label="Run as root (sudo -n)" />
            </div>
            <FormField label="Risk">
              <Select value={d.risk || "change"} onChange={(e) => set({ risk: e.target.value as Script["risk"] })}>
                <option value="change">change — follows the approval mode</option>
                <option value="destructive">destructive — always asks</option>
              </Select>
            </FormField>
            <FormField label="Timeout (minutes)">
              <Input type="number" min={1} max={120} value={d.timeoutSec ? Math.round(d.timeoutSec / 60) : ""} placeholder="30" onChange={(e) => set({ timeoutSec: e.target.value ? Number(e.target.value) * 60 : 0 })} />
            </FormField>
          </div>
          <FormField label="Already applied if" help="Optional shell command that exits 0 when the script was already applied on a host; that host is then skipped (unless forced).">
            <Input mono value={d.check ?? ""} onChange={(e) => set({ check: e.target.value })} placeholder="test -f /etc/opsdeck/bootstrap-v1" />
          </FormField>
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs font-medium text-muted">Parameters — passed as environment variables; secrets are vault:// references, never values</span>
              <Button size="xs" icon={<Plus className="size-3" />} onClick={() => set({ params: [...(d.params ?? []), { name: "", type: "" }] })}>
                Add
              </Button>
            </div>
            {(d.params ?? []).length === 0 && <div className="text-[11.5px] text-faint">No parameters.</div>}
            <div className="space-y-1.5">
              {(d.params ?? []).map((p, i) => (
                <div key={i} className="grid grid-cols-[10rem_6.5rem_1fr_11rem_auto_auto] gap-1.5 items-center">
                  <Input mono value={p.name} placeholder="DEPLOY_USER" onChange={(e) => setParam(i, { name: e.target.value })} />
                  <Select value={p.type || "string"} onChange={(e) => setParam(i, { type: e.target.value === "string" ? "" : (e.target.value as ScriptParam["type"]) })}>
                    <option value="string">string</option>
                    <option value="int">int</option>
                    <option value="bool">bool</option>
                    <option value="secret">secret</option>
                  </Select>
                  <Input value={p.description ?? ""} placeholder="description" onChange={(e) => setParam(i, { description: e.target.value })} />
                  <Input mono value={p.default ?? ""} placeholder={p.type === "secret" ? "vault://… (optional)" : "default"} onChange={(e) => setParam(i, { default: e.target.value })} />
                  <label className="flex items-center gap-1 text-[11.5px] text-muted px-1">
                    <input type="checkbox" checked={!!p.required} onChange={(e) => setParam(i, { required: e.target.checked })} /> required
                  </label>
                  <IconButton title="Remove" onClick={() => set({ params: (d.params ?? []).filter((_, j) => j !== i) })}>
                    <X className="size-3.5" />
                  </IconButton>
                </div>
              ))}
            </div>
          </div>
          <FormField label="Script" required help="Starts with a #! line (default bash). Make it idempotent: safe to run twice on the same host.">
            <Textarea mono dir="ltr" spellCheck={false} className="min-h-96 !font-mono !text-[12px] leading-relaxed" value={d.body ?? ""} onChange={(e) => set({ body: e.target.value })} />
          </FormField>
        </div>
      </div>
    </>
  );
}

// ---------------------------------------------------------------------------

function RunPanel({ ws, s, onDone }: { ws: string; s: Script; onDone: () => void }) {
  const { resources, workspace } = useWs();
  const toast = useToast();
  const confirm = useConfirm();
  const hosts = resources.filter((r) => r.kind === "Host");
  const groups = resources.filter((r) => r.kind === "HostGroup");
  const [targets, setTargets] = useState<string[]>([]);
  const [values, setValues] = useState<Record<string, string>>({});
  const [force, setForce] = useState(false);
  const [running, setRunning] = useState(false);
  const [results, setResults] = useState<ScriptResult[] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    setResults(null);
    setErr("");
  }, [s.name, s.scope]);
  const toggle = (t: string) => setTargets((x) => (x.includes(t) ? x.filter((y) => y !== t) : [...x, t]));
  const protectedEnvs = new Set((workspace?.environments ?? []).filter((e) => e.protected).map((e) => e.name));
  const chosenProtected = hosts.filter((h) => targets.includes(h.name) && h.environment && protectedEnvs.has(h.environment));

  async function run() {
    if (s.status === "draft" || chosenProtected.length > 0) {
      const ok = await confirm({
        title: `Run ${s.name} v${s.version}?`,
        message: (
          <>
            {s.status === "draft" && <div>This version is a draft that has not been approved yet.</div>}
            {chosenProtected.length > 0 && <div>It runs on protected hosts: {chosenProtected.map((h) => h.name).join(", ")}.</div>}
          </>
        ),
        confirmLabel: "Run",
        danger: true,
      });
      if (!ok) return;
    }
    setRunning(true);
    setErr("");
    setResults(null);
    try {
      const params: Record<string, string> = {};
      for (const [k, v] of Object.entries(values)) if (v !== "") params[k] = v;
      const r = await api.runScript(ws, s.scope, s.name, { target: targets.join(","), params, force, hash: s.hash });
      setResults(r.results);
      const failed = r.results.filter((x) => x.exitCode !== 0 || x.error).length;
      if (failed) toast.error(`${s.name}: ${failed} of ${r.results.length} host(s) failed`);
      else toast.success(`${s.name} finished on ${r.results.length} host(s)`);
      onDone();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className="max-w-4xl px-6 py-5 space-y-4">
      <div>
        <div className="text-xs font-medium text-muted mb-1">Hosts</div>
        <div className="rounded-md border border-line bg-panel-2 max-h-56 overflow-y-auto p-1">
          {hosts.length === 0 && <div className="text-xs text-faint p-2">No hosts in this workspace yet.</div>}
          {groups.map((g) => (
            <TargetRow key={"g/" + g.name} checked={targets.includes("group/" + g.name)} onChange={() => toggle("group/" + g.name)} label={`group/${g.name}`} sub="host group" />
          ))}
          {hosts.map((h) => (
            <TargetRow
              key={h.name}
              checked={targets.includes(h.name)}
              onChange={() => toggle(h.name)}
              label={h.name}
              sub={[String(h.spec.address ?? ""), h.environment].filter(Boolean).join(" · ")}
              warn={!!h.environment && protectedEnvs.has(h.environment)}
            />
          ))}
        </div>
      </div>
      {(s.params?.length ?? 0) > 0 && (
        <div className="space-y-2">
          <div className="text-xs font-medium text-muted">Parameters</div>
          {s.params!.map((p) => (
            <div key={p.name} className="grid grid-cols-[12rem_1fr] gap-3 items-start">
              <div className="pt-1.5">
                <div className="font-mono text-[12.5px]">
                  {p.name}
                  {p.required && <span className="text-err">*</span>}
                </div>
                {p.description && <div className="text-[11px] text-faint">{p.description}</div>}
              </div>
              {p.type === "secret" ? (
                <SecretParam ws={ws} value={values[p.name] ?? ""} placeholder={p.default ? `default: ${p.default}` : "— choose a vault item —"} onChange={(v) => setValues({ ...values, [p.name]: v })} />
              ) : p.type === "bool" ? (
                <Select value={values[p.name] ?? ""} onChange={(e) => setValues({ ...values, [p.name]: e.target.value })}>
                  <option value="">default ({p.default || "false"})</option>
                  <option value="true">true</option>
                  <option value="false">false</option>
                </Select>
              ) : (
                <Input mono type={p.type === "int" ? "number" : "text"} value={values[p.name] ?? ""} placeholder={p.default ? `default: ${p.default}` : ""} onChange={(e) => setValues({ ...values, [p.name]: e.target.value })} />
              )}
            </div>
          ))}
        </div>
      )}
      <div className="flex items-center gap-4">
        <Button variant="primary" icon={<Play className="size-3.5" />} loading={running} disabled={targets.length === 0} onClick={() => void run()}>
          Run v{s.version}
        </Button>
        {s.check && <Toggle checked={force} onChange={setForce} label={<span className="text-[12px]">Run even where already applied</span>} />}
        {running && <span className="text-[12px] text-muted">Running… it keeps going if you leave this page; the result goes to History.</span>}
      </div>
      {err && <ErrorBox error={err} />}
      {results && <Results results={results} />}
    </div>
  );
}

function SecretParam({ ws, value, placeholder, onChange }: { ws: string; value: string; placeholder: string; onChange: (v: string) => void }) {
  const [ref, field = ""] = value.split("#");
  return (
    <div className="flex gap-1.5">
      <div className="flex-1 min-w-0">
        <SecretRefPicker ws={ws} value={ref ?? ""} placeholder={placeholder} onChange={(r) => onChange(r ? (field ? `${r}#${field}` : r) : "")} />
      </div>
      <Input mono className="w-32" value={field} placeholder="#field" title="Field of the item (empty = its secret value), e.g. public_key" onChange={(e) => onChange(ref ? (e.target.value ? `${ref}#${e.target.value}` : ref) : "")} />
    </div>
  );
}

function TargetRow({ checked, onChange, label, sub, warn }: { checked: boolean; onChange: () => void; label: string; sub?: string; warn?: boolean }) {
  return (
    <label className="flex items-center gap-2 px-2 h-7 rounded hover:bg-panel-3 cursor-pointer text-xs">
      <input type="checkbox" checked={checked} onChange={onChange} />
      <span className="font-mono">{label}</span>
      {sub && <span className="text-faint truncate">{sub}</span>}
      {warn && <Badge tone="err">protected</Badge>}
    </label>
  );
}

function Results({ results }: { results: ScriptResult[] }) {
  return (
    <div className="space-y-3">
      {results.map((r) => (
        <div key={r.host} className="rounded-md border border-line">
          <div className="flex items-center gap-2 px-3 h-9 border-b border-line">
            <span className="font-mono text-[12.5px]">{r.host}</span>
            {r.skipped ? (
              <Badge tone="info">already applied — skipped</Badge>
            ) : r.exitCode === 97 ? (
              <Badge tone="err">unsupported system</Badge>
            ) : r.exitCode === 0 && !r.error ? (
              <Badge tone="ok">exit 0</Badge>
            ) : (
              <Badge tone="err">{r.error ? "error" : `exit ${r.exitCode}`}</Badge>
            )}
            <span className="text-[11px] text-faint">{(r.durationMs / 1000).toFixed(1)}s</span>
            <div className="flex-1" />
            {r.log && <span className="font-mono text-[11px] text-faint truncate">{r.log}</span>}
          </div>
          <div className="p-2 space-y-2" dir="ltr">
            {r.error && <CodeBlock tone="err">{r.error}</CodeBlock>}
            {r.stdout && <CodeBlock maxHeight={360}>{r.stdout}</CodeBlock>}
            {r.stderr && (
              <CodeBlock maxHeight={240} tone={r.exitCode !== 0 ? "err" : undefined}>
                {r.stderr}
              </CodeBlock>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

function HistoryPanel({ ws, script }: { ws: string; script: string }) {
  const runs = useAsync(() => api.scriptRuns(ws, script), [ws, script]);
  if (runs.error) return <ErrorBox error={runs.error} onRetry={runs.reload} />;
  if (!runs.data) return <Loading />;
  if (runs.data.length === 0)
    return (
      <EmptyState icon={<History />} title="Not run yet">
        Runs from this page and by the agent are recorded here, with the full output saved as an artifact (Docs → Artifacts).
      </EmptyState>
    );
  return (
    <div className="px-6 py-4">
      <table className="w-full text-[12.5px] [&_td]:pr-4 [&_th]:pr-4">
        <thead>
          <tr className="text-left text-[11px] text-faint border-b border-line">
            <th className="py-1.5 font-medium">When</th>
            <th className="font-medium">Host</th>
            <th className="font-medium">Version</th>
            <th className="font-medium">Result</th>
            <th className="font-medium">By</th>
            <th className="font-medium">Parameters</th>
            <th className="font-medium">Output</th>
          </tr>
        </thead>
        <tbody>
          {runs.data.map((r: ScriptRun, i) => (
            <tr key={i} className="border-b border-line/60 align-top">
              <td className="py-1.5 whitespace-nowrap" title={r.time}>
                {timeAgo(r.time)}
              </td>
              <td className="font-mono">{r.host}</td>
              <td className="font-mono" title={r.hash}>
                v{r.version}
              </td>
              <td>{r.skipped ? <Badge tone="info">skipped</Badge> : r.exitCode === 0 && !r.error ? <Badge tone="ok">ok</Badge> : <Badge tone="err">{r.error ? "error" : `exit ${r.exitCode}`}</Badge>}</td>
              <td>{r.actor === "agent" ? "agent" : "you"}</td>
              <td className="font-mono text-[11px] text-muted max-w-64 break-all">
                {Object.entries(r.params ?? {})
                  .map(([k, v]) => `${k}=${v}`)
                  .join(" ")}
              </td>
              <td className="font-mono text-[11px] text-faint break-all">{r.log}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// lineDiff is a small LCS line diff for reviewing a draft against the
// previous version (scripts are small).
function lineDiff(a: string, b: string): { lines: { op: " " | "+" | "-"; text: string }[]; added: number; removed: number; changed: boolean } {
  const x = a.split("\n");
  const y = b.split("\n");
  if (x.length * y.length > 4_000_000) {
    return { lines: [{ op: " ", text: "(too large to compare)" }], added: 0, removed: 0, changed: a !== b };
  }
  const n = x.length;
  const m = y.length;
  const dp: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = x[i] === y[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  const out: { op: " " | "+" | "-"; text: string }[] = [];
  let i = 0;
  let j = 0;
  let added = 0;
  let removed = 0;
  while (i < n || j < m) {
    if (i < n && j < m && x[i] === y[j]) {
      out.push({ op: " ", text: x[i] });
      i++;
      j++;
    } else if (j < m && (i >= n || dp[i][j + 1] >= dp[i + 1][j])) {
      out.push({ op: "+", text: y[j++] });
      added++;
    } else {
      out.push({ op: "-", text: x[i++] });
      removed++;
    }
  }
  return { lines: out, added, removed, changed: added + removed > 0 };
}
