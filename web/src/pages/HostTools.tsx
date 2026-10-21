import { useEffect, useRef, useState } from "react";
import { ArrowUp, File, Folder, Play, RefreshCw, Save } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { CheckResult, DirListing, ExecResult, HostStats, PathHop } from "../api/types";
import { Badge, Button, CodeBlock, EmptyState, ErrorBox, IconButton, Input, Loading, ProgressBar, Textarea, cx } from "../components/ui";
import { Drawer, Modal, useConfirm, useToast } from "../components/overlay";
import { fmtBytes, fmtDuration, fmtMs } from "../lib/format";
import { useAsync } from "../hooks";

export function PathView({ path }: { path: (PathHop | string)[] | null | undefined }) {
  if (!path || !path.length) return <span className="text-faint text-xs">direct</span>;
  return (
    <div className="flex flex-wrap items-center gap-1 text-xs">
      <Badge>this machine</Badge>
      {path.map((h, i) => {
        const label = typeof h === "string" ? h : h.label || `${h.kind}/${h.name}`;
        return (
          <span key={i} className="flex items-center gap-1">
            <span className="text-faint">→</span>
            <Badge tone={i === path.length - 1 ? "accent" : "neutral"} mono>
              {label}
            </Badge>
          </span>
        );
      })}
    </div>
  );
}

export function CheckResultView({ r }: { r: CheckResult }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <Badge tone={r.reachable ? "ok" : "err"}>{r.reachable ? "reachable" : "unreachable"}</Badge>
        {r.reachable && <span className="text-muted text-xs">{fmtMs(r.latencyMs)}</span>}
      </div>
      {r.error && <div className="text-err text-xs whitespace-pre-wrap">{r.error}</div>}
      <PathView path={r.path} />
    </div>
  );
}

export function useHostCheck(ws: string) {
  const toast = useToast();
  return async (host: string) => {
    try {
      const r = await api.checkHost(ws, host);
      if (r.reachable) toast.success(`${host} is reachable`, `${fmtMs(r.latencyMs)}`);
      else toast.error(`${host} unreachable`, r.error);
      return r;
    } catch (e) {
      toast.error(`Check failed: ${host}`, errMsg(e));
      return null;
    }
  };
}

export function StatsDrawer({ ws, host, onClose }: { ws: string; host: string | null; onClose: () => void }) {
  const s = useAsync<HostStats | null>(() => (host ? api.hostStats(ws, host) : Promise.resolve(null)), [ws, host]);
  return (
    <Drawer
      open={!!host}
      onClose={onClose}
      title={`Stats · ${host}`}
      subtitle={s.data ? `${s.data.hostname} · ${s.data.os}` : undefined}
      footer={
        <Button size="sm" icon={<RefreshCw className="size-3.5" />} onClick={() => void s.reload()} loading={s.loading}>
          Refresh
        </Button>
      }
    >
      {s.error && <ErrorBox error={s.error} onRetry={() => void s.reload()} />}
      {!s.data && s.loading && <Loading label="Collecting stats over SSH…" />}
      {s.data && <StatsView st={s.data} />}
    </Drawer>
  );
}

export function StatsView({ st }: { st: HostStats }) {
  const loadPct = st.cpus ? (st.load?.[0] ?? 0) / st.cpus : 0;
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-2 text-xs">
        <Info k="Hostname" v={st.hostname} mono />
        <Info k="Uptime" v={fmtDuration(st.uptimeSec)} />
        <Info k="OS" v={st.os} />
        <Info k="Kernel" v={st.kernel} mono />
        <Info k="CPUs" v={String(st.cpus)} />
        <Info k="Load (1/5/15)" v={(st.load ?? []).map((l) => l.toFixed(2)).join(" / ")} mono />
      </div>
      <Meter label="CPU load (1m / cores)" value={loadPct * 100} max={100} text={`${(loadPct * 100).toFixed(0)}%`} />
      <Meter label="Memory" value={st.memUsed} max={st.memTotal} text={`${fmtBytes(st.memUsed)} / ${fmtBytes(st.memTotal)}`} />
      {st.swapTotal > 0 && <Meter label="Swap" value={st.swapUsed} max={st.swapTotal} text={`${fmtBytes(st.swapUsed)} / ${fmtBytes(st.swapTotal)}`} />}
      <div>
        <div className="text-xs font-medium text-muted mb-2">Disks</div>
        <div className="space-y-2.5">
          {(st.disks ?? []).map((d) => (
            <Meter key={d.mount} label={d.mount} mono value={d.used} max={d.size} text={`${fmtBytes(d.used)} / ${fmtBytes(d.size)}`} />
          ))}
        </div>
      </div>
    </div>
  );
}

function Info({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div className="rounded-md border border-line bg-panel-2 px-2.5 py-1.5">
      <div className="text-faint text-[10.5px] uppercase tracking-wide">{k}</div>
      <div className={cx("truncate", mono && "font-mono")}>{v || "—"}</div>
    </div>
  );
}

function Meter({ label, value, max, text, mono }: { label: string; value: number; max: number; text: string; mono?: boolean }) {
  const pct = max ? (value / max) * 100 : 0;
  return (
    <div>
      <div className="flex justify-between text-xs mb-1">
        <span className={cx("text-muted", mono && "font-mono")}>{label}</span>
        <span className="tabular-nums">
          {text} <span className="text-faint">({pct.toFixed(0)}%)</span>
        </span>
      </div>
      <ProgressBar value={value} max={max} />
    </div>
  );
}

export function ExecModal({ ws, host, onClose, initial = "" }: { ws: string; host: string | null; onClose: () => void; initial?: string }) {
  const [cmd, setCmd] = useState(initial);
  const [res, setRes] = useState<ExecResult | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState<string[]>([]);
  useEffect(() => {
    if (host) {
      setRes(null);
      setErr("");
      setCmd(initial);
    }
  }, [host, initial]);
  async function run() {
    if (!host || !cmd.trim()) return;
    setBusy(true);
    setErr("");
    try {
      setRes(await api.exec(ws, host, cmd));
      setHistory((h) => [cmd, ...h.filter((x) => x !== cmd)].slice(0, 10));
    } catch (e) {
      setErr(errMsg(e));
      setRes(null);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal open={!!host} onClose={onClose} title={`Run command on ${host}`} width={760}>
      <div className="flex gap-2">
        <Input
          mono
          autoFocus
          value={cmd}
          onChange={(e) => setCmd(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void run()}
          placeholder="systemctl status nginx"
        />
        <Button variant="primary" onClick={run} loading={busy} icon={<Play className="size-3.5" />}>
          Run
        </Button>
      </div>
      <div className="text-[11px] text-faint mt-1.5">Runs as you (not the agent) and is recorded in the audit journal.</div>
      {history.length > 0 && (
        <div className="flex flex-wrap gap-1 mt-2">
          {history.map((h) => (
            <button key={h} onClick={() => setCmd(h)} className="font-mono text-[11px] rounded bg-panel-3 px-1.5 h-5 hover:text-accent truncate max-w-60">
              {h}
            </button>
          ))}
        </div>
      )}
      {err && <div className="mt-3 text-err text-xs whitespace-pre-wrap">{err}</div>}
      {res && (
        <div className="mt-3 space-y-2">
          <div className="flex items-center gap-2 text-xs">
            <Badge tone={res.exitCode === 0 ? "ok" : "err"}>exit {res.exitCode}</Badge>
            <span className="text-muted">{fmtMs(res.durationMs)}</span>
          </div>
          <CodeBlock maxHeight={360}>{res.stdout}</CodeBlock>
          {res.stderr && (
            <CodeBlock maxHeight={200} tone="err">
              {res.stderr}
            </CodeBlock>
          )}
        </div>
      )}
    </Modal>
  );
}

export function FilesModal({ ws, host, onClose }: { ws: string; host: string | null; onClose: () => void }) {
  const [path, setPath] = useState("~");
  const [input, setInput] = useState("~");
  const [listing, setListing] = useState<DirListing | null>(null);
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(false);
  const [file, setFile] = useState<{ path: string; content: string; truncated: boolean } | null>(null);
  const [edit, setEdit] = useState("");
  const [saving, setSaving] = useState(false);
  const toast = useToast();
  const confirm = useConfirm();
  const dirty = !!file && edit !== file.content;
  const reqRef = useRef(0);

  async function load(p: string) {
    if (!host) return;
    const my = ++reqRef.current;
    setLoading(true);
    setErr("");
    try {
      const l = await api.listFiles(ws, host, p);
      if (my !== reqRef.current) return;
      const entries = [...(l.entries ?? [])].sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1));
      setListing({ ...l, entries });
      setPath(l.path);
      setInput(l.path);
    } catch (e) {
      if (my === reqRef.current) setErr(errMsg(e));
    } finally {
      if (my === reqRef.current) setLoading(false);
    }
  }

  useEffect(() => {
    if (host) {
      setFile(null);
      setListing(null);
      void load("~");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [host]);

  const join = (a: string, b: string) => (a.endsWith("/") ? a + b : `${a}/${b}`);
  const parent = (p: string) => {
    if (p === "/" || !p) return "/";
    const t = p.replace(/\/+$/, "");
    const i = t.lastIndexOf("/");
    return i <= 0 ? "/" : t.slice(0, i);
  };

  async function openFile(p: string) {
    if (!host) return;
    if (dirty && !(await confirm({ title: "Discard unsaved changes?", danger: true, confirmLabel: "Discard" }))) return;
    try {
      const f = await api.readFile(ws, host, p);
      setFile(f);
      setEdit(f.content);
    } catch (e) {
      toast.error("Cannot open file", errMsg(e));
    }
  }

  async function save() {
    if (!host || !file) return;
    const ok = await confirm({
      title: `Write ${file.path}?`,
      message: `This overwrites the file on ${host}. The change is recorded in the audit journal.`,
      confirmLabel: "Write file",
    });
    if (!ok) return;
    setSaving(true);
    try {
      await api.writeFile(ws, host, file.path, edit);
      setFile({ ...file, content: edit });
      toast.success("File saved", file.path);
    } catch (e) {
      toast.error("Save failed", errMsg(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal open={!!host} onClose={onClose} title={`Files · ${host}`} width={1100}>
      <div className="flex gap-2 mb-2">
        <IconButton title="Parent directory" onClick={() => void load(parent(path))} className="size-8">
          <ArrowUp className="size-4" />
        </IconButton>
        <Input mono value={input} onChange={(e) => setInput(e.target.value)} onKeyDown={(e) => e.key === "Enter" && void load(input)} />
        <IconButton title="Reload" onClick={() => void load(path)} className="size-8">
          <RefreshCw className={cx("size-4", loading && "animate-spin")} />
        </IconButton>
      </div>
      <div className="grid grid-cols-[300px_1fr] gap-3 h-[60vh]">
        <div className="rounded-md border border-line overflow-y-auto">
          {err && <div className="p-3 text-err text-xs">{err}</div>}
          {listing?.entries.map((en) => (
            <button
              key={en.name}
              onClick={() => (en.dir ? void load(join(path, en.name)) : void openFile(join(path, en.name)))}
              className={cx(
                "w-full flex items-center gap-2 px-2.5 h-7 text-left text-xs hover:bg-panel-3",
                file?.path === join(path, en.name) && "bg-accent/10",
              )}
            >
              {en.dir ? <Folder className="size-3.5 text-info shrink-0" /> : <File className="size-3.5 text-faint shrink-0" />}
              <span className="font-mono truncate flex-1">{en.name}</span>
              {!en.dir && <span className="text-faint tabular-nums text-[10.5px]">{fmtBytes(en.size)}</span>}
            </button>
          ))}
          {listing && listing.entries.length === 0 && <div className="p-3 text-faint text-xs">Empty directory</div>}
        </div>
        <div className="flex flex-col min-h-0">
          {file ? (
            <>
              <div className="flex items-center gap-2 mb-2">
                <span className="font-mono text-xs truncate flex-1">{file.path}</span>
                {file.truncated && <Badge tone="warn">truncated — read only</Badge>}
                {dirty && <Badge tone="warn">modified</Badge>}
                <Button size="sm" variant="primary" icon={<Save className="size-3.5" />} disabled={!dirty || file.truncated} loading={saving} onClick={save}>
                  Save
                </Button>
              </div>
              <Textarea mono value={edit} onChange={(e) => setEdit(e.target.value)} className="flex-1 resize-none" spellCheck={false} readOnly={file.truncated} />
            </>
          ) : (
            <EmptyState icon={<File />} title="No file open" className="flex-1 rounded-md border border-dashed border-line">
              Select a file to view or edit it.
            </EmptyState>
          )}
        </div>
      </div>
    </Modal>
  );
}
