import { createContext, useContext, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { Eye, EyeOff, FileJson, Info, ListTree, ScrollText, SquareTerminal } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { DockerProcess } from "../../api/types";
import { Badge, Button, CodeBlock, ErrorBox, Loading, Select, Table, Tabs, Td, Th, Tr, cx } from "../../components/ui";
import type { Tone } from "../../components/ui";
import { Drawer, Modal, useToast } from "../../components/overlay";
import { LogViewer } from "../../components/LogViewer";
import { ExecTerminal } from "../../components/ExecTerminal";
import { useAsync, useInterval } from "../../hooks";

// The host whose engine the page manages, and how to switch tabs.
export type DockerCtx = {
  ws: string;
  host: string;
  goto: (tab: string, extra?: Record<string, string>) => void;
  openContainer: (id: string, name: string, view?: ContainerView) => void;
};
const Ctx = createContext<DockerCtx | null>(null);
export const DockerProvider = Ctx.Provider;
export function useDocker(): DockerCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("DockerProvider missing");
  return c;
}

export function stateTone(s: string): Tone {
  const x = s.toLowerCase();
  if (x === "running" || x.startsWith("running") || x === "ready" || x === "active") return "ok";
  if (x === "restarting" || x === "paused" || x.startsWith("starting") || x.startsWith("preparing") || x === "pending" || x === "pause") return "warn";
  if (x === "exited" || x === "dead" || x.startsWith("failed") || x.startsWith("rejected") || x === "down" || x === "drain") return "err";
  return "neutral";
}

export function StateBadge({ state, health }: { state: string; health?: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <Badge tone={stateTone(state)}>{state}</Badge>
      {health && <Badge tone={health === "healthy" ? "ok" : health === "unhealthy" ? "err" : "warn"}>{health}</Badge>}
    </span>
  );
}

/** "running(2), exited(1)" → badges. */
export function ComposeStatus({ status }: { status: string }) {
  return (
    <span className="inline-flex flex-wrap gap-1">
      {status
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean)
        .map((s) => {
          const m = /^(\w+)\((\d+)\)$/.exec(s);
          return (
            <Badge key={s} tone={stateTone(m ? m[1] : s)}>
              {m ? `${m[2]} ${m[1]}` : s}
            </Badge>
          );
        })}
    </span>
  );
}

export function UsageBar({ v, label, width = "w-36" }: { v: number; label: string; width?: string }) {
  const color = v > 90 ? "bg-err" : v > 70 ? "bg-warn" : "bg-accent";
  return (
    <div className={cx("flex items-center gap-2", width)}>
      <div className="flex-1 h-1.5 rounded-full bg-panel-3 overflow-hidden">
        <div className={`h-full ${color} transition-[width] duration-500`} style={{ width: `${Math.min(100, Math.max(0, v))}%` }} />
      </div>
      <span className="font-mono text-[11px] tabular-nums w-14 text-right">{label}</span>
    </div>
  );
}

export const pct = (s: string | undefined) => parseFloat(String(s ?? "").replace("%", "")) || 0;

/** Short "8080→80" form of docker's port column. */
export function shortPorts(ports: string): string[] {
  const out = new Set<string>();
  for (const p of ports.split(",")) {
    const m = /(?:[\d.]+|\[::\]|::):(\d+)->(\d+)\/(\w+)/.exec(p.trim());
    if (m) out.add(m[1] === m[2] ? `${m[1]}/${m[3]}` : `${m[1]}→${m[2]}/${m[3]}`);
    else if (p.trim() && !p.includes("->")) out.add(p.trim());
  }
  return [...out];
}

// ---------------------------------------------------------------------------
// Output of long actions (compose up, pull, prune…)

type OutputState = { title: string; output: string; error?: boolean } | null;
const OutCtx = createContext<(o: OutputState) => void>(() => {});
export function OutputProvider({ children }: { children: ReactNode }) {
  const [o, setO] = useState<OutputState>(null);
  return (
    <OutCtx.Provider value={setO}>
      {children}
      <Modal open={!!o} onClose={() => setO(null)} title={o?.title ?? ""} width={820}>
        {o && (
          <div dir="ltr">
            <CodeBlock maxHeight={520} tone={o.error ? "err" : undefined}>
              {o.output.trim() || "(no output)"}
            </CodeBlock>
          </div>
        )}
      </Modal>
    </OutCtx.Provider>
  );
}

/** run wraps an action: busy state, toast, and docker's output on demand. */
export function useRun() {
  const show = useContext(OutCtx);
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  async function run(key: string, title: string, fn: () => Promise<unknown>, after?: () => unknown): Promise<boolean> {
    setBusy(key);
    try {
      const r = (await fn()) as { output?: string } | undefined;
      const out = r && typeof r === "object" && "output" in r ? String(r.output ?? "") : "";
      if (out.trim().split("\n").length > 2) {
        toast.success(title, "Done — see the output");
        show({ title, output: out });
      } else toast.success(title, out.trim() || "done");
      await after?.();
      return true;
    } catch (e) {
      const msg = errMsg(e);
      if (msg.split("\n").length > 3) {
        toast.error(`${title} failed`);
        show({ title: `${title} failed`, output: msg, error: true });
      } else toast.error(`${title} failed`, msg);
      await after?.();
      return false;
    } finally {
      setBusy(null);
    }
  }
  return { busy, run };
}

// ---------------------------------------------------------------------------
// Logs with history and time controls

export type LogTarget = { source: "docker" | "compose" | "service"; container?: string; project?: string; service?: string };

export function DockerLogs({ ws, host, target, className, name }: { ws: string; host: string; target: LogTarget; className?: string; name: string }) {
  const [tail, setTail] = useState("500");
  const [since, setSince] = useState("");
  const [follow, setFollow] = useState(true);
  const url = useMemo(
    () => api.logStreamUrl(ws, { host, ...target, lines: tail === "all" ? "all" : Number(tail), since: since || undefined, follow }),
    [ws, host, target.source, target.container, target.project, target.service, tail, since, follow], // eslint-disable-line react-hooks/exhaustive-deps
  );
  return (
    <LogViewer
      className={className}
      url={url}
      downloadName={name}
      reconnect={follow ? (ts) => api.logStreamUrl(ws, { host, ...target, lines: "all", since: ts, follow: true }) : undefined}
      toolbarExtra={
        <>
          <Select className="!h-7 !w-auto text-xs" value={since} onChange={(e) => setSince(e.target.value)} title="Since">
            <option value="">any time</option>
            <option value="5m">last 5 min</option>
            <option value="15m">last 15 min</option>
            <option value="1h">last hour</option>
            <option value="6h">last 6 hours</option>
            <option value="1d">last day</option>
            <option value="7d">last week</option>
          </Select>
          <Select className="!h-7 !w-auto text-xs" value={tail} onChange={(e) => setTail(e.target.value)} title="History per container">
            <option value="100">100 lines</option>
            <option value="500">500 lines</option>
            <option value="2000">2000 lines</option>
            <option value="10000">10000 lines</option>
            <option value="all">all</option>
          </Select>
          <Button size="xs" variant={follow ? "subtle" : "ghost"} onClick={() => setFollow(!follow)} title="Keep streaming new lines">
            {follow ? "Live" : "Snapshot"}
          </Button>
        </>
      }
    />
  );
}

// ---------------------------------------------------------------------------
// Container details: overview, logs, terminal, processes, inspect

export type ContainerView = "overview" | "logs" | "terminal" | "top" | "inspect";

type Inspect = {
  Id: string;
  Name: string;
  Created: string;
  Path: string;
  Args: string[];
  State: { Status: string; Running: boolean; ExitCode: number; StartedAt: string; FinishedAt: string; OOMKilled: boolean; Error: string; Health?: { Status: string; FailingStreak: number; Log?: { Start: string; ExitCode: number; Output: string }[] } };
  RestartCount: number;
  Config: { Image: string; Env?: string[]; Cmd?: string[]; Entrypoint?: string[]; WorkingDir: string; User: string; Labels?: Record<string, string>; Hostname: string };
  HostConfig: { RestartPolicy?: { Name: string; MaximumRetryCount: number }; Memory: number; NanoCpus: number; Privileged: boolean; NetworkMode: string };
  Mounts?: { Type: string; Name?: string; Source: string; Destination: string; RW: boolean }[];
  NetworkSettings: { Ports?: Record<string, { HostIp: string; HostPort: string }[] | null>; Networks?: Record<string, { IPAddress: string; Gateway: string; Aliases?: string[] | null }> };
};

export function ContainerDrawer({ id, name, view, onView, onClose }: { id: string; name: string; view: ContainerView; onView: (v: ContainerView) => void; onClose: () => void }) {
  const { ws, host } = useDocker();
  return (
    <Drawer open onClose={onClose} title={name} subtitle={`container on ${host}`} width={1100}>
      <div className="-m-4 flex flex-col h-[calc(100vh-48px)]">
        <Tabs<ContainerView>
          className="px-3 shrink-0"
          value={view}
          onChange={onView}
          tabs={[
            { id: "overview", label: <Lbl icon={<Info />}>Overview</Lbl> },
            { id: "logs", label: <Lbl icon={<ScrollText />}>Logs</Lbl> },
            { id: "terminal", label: <Lbl icon={<SquareTerminal />}>Terminal</Lbl> },
            { id: "top", label: <Lbl icon={<ListTree />}>Processes</Lbl> },
            { id: "inspect", label: <Lbl icon={<FileJson />}>Inspect</Lbl> },
          ]}
        />
        <div className="flex-1 min-h-0 p-3">
          {view === "overview" && <Overview id={id} />}
          {view === "logs" && <DockerLogs ws={ws} host={host} target={{ source: "docker", container: id }} className="h-full" name={name} />}
          {view === "terminal" && <ExecTerminal className="h-full" url={(cols, rows, o) => api.dockerExecUrl(ws, host, id, { cols, rows, user: o.user || undefined, shell: o.shell })} />}
          {view === "top" && <Processes id={id} />}
          {view === "inspect" && <InspectJSON kind="container" id={id} />}
        </div>
      </div>
    </Drawer>
  );
}

function Lbl({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5 [&_svg]:size-3.5">
      {icon}
      {children}
    </span>
  );
}

export function InspectJSON({ kind, id }: { kind: string; id: string }) {
  const { ws, host } = useDocker();
  const r = useAsync(() => api.dockerInspect(ws, host, kind, id), [ws, host, kind, id]);
  if (r.error) return <ErrorBox error={r.error} />;
  if (!r.data) return <Loading />;
  return (
    <div className="h-full overflow-auto" dir="ltr">
      <CodeBlock>{JSON.stringify(r.data, null, 2)}</CodeBlock>
    </div>
  );
}

function Processes({ id }: { id: string }) {
  const { ws, host } = useDocker();
  const r = useAsync(() => api.containerTop(ws, host, id), [ws, host, id]);
  useInterval(() => void r.reload(), 5000);
  if (r.error) return <ErrorBox error={r.error} />;
  if (!r.data) return <Loading />;
  return (
    <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
      <Table className="flex-1">
        <thead>
          <tr>
            <Th>PID</Th>
            <Th>User</Th>
            <Th>CPU %</Th>
            <Th>Mem %</Th>
            <Th>Started</Th>
            <Th>Time</Th>
            <Th>Command</Th>
          </tr>
        </thead>
        <tbody>
          {r.data.map((p: DockerProcess, i) => (
            <Tr key={p.pid + i}>
              <Td mono>{p.pid}</Td>
              <Td>{p.user}</Td>
              <Td mono>{p.cpu}</Td>
              <Td mono>{p.mem}</Td>
              <Td className="text-muted whitespace-nowrap">{p.started}</Td>
              <Td mono>{p.time}</Td>
              <Td mono className="max-w-[480px] truncate" title={p.command}>
                {p.command}
              </Td>
            </Tr>
          ))}
        </tbody>
      </Table>
    </div>
  );
}

function Overview({ id }: { id: string }) {
  const { ws, host, goto } = useDocker();
  const r = useAsync(() => api.inspect(ws, host, id) as Promise<Inspect[]>, [ws, host, id]);
  const stats = useAsync(() => api.dockerStats(ws, host, [id]), [ws, host, id]);
  useInterval(() => {
    void r.reload();
    void stats.reload();
  }, 5000);
  const [showEnv, setShowEnv] = useState(false);
  if (r.error) return <ErrorBox error={r.error} />;
  const c = r.data?.[0];
  if (!c) return <Loading />;
  const st = stats.data?.[0];
  const cmd = [...(c.Config.Entrypoint ?? []), ...(c.Config.Cmd ?? [])].join(" ");
  const labels = Object.entries(c.Config.Labels ?? {});
  return (
    <div className="h-full overflow-y-auto space-y-4 pr-1">
      <div className="grid grid-cols-2 gap-3">
        <Section title="State">
          <KV k="Status">
            <StateBadge state={c.State.Status} health={c.State.Health?.Status} />
            {c.State.OOMKilled && <Badge tone="err">OOM killed</Badge>}
          </KV>
          <KV k={c.State.Running ? "Started" : "Finished"}>{new Date(c.State.Running ? c.State.StartedAt : c.State.FinishedAt).toLocaleString()}</KV>
          {!c.State.Running && <KV k="Exit code">{c.State.ExitCode}</KV>}
          {c.State.Error && <KV k="Error">{c.State.Error}</KV>}
          <KV k="Restarts">
            {c.RestartCount} · policy {c.HostConfig.RestartPolicy?.Name || "no"}
          </KV>
          <KV k="Created">{new Date(c.Created).toLocaleString()}</KV>
        </Section>
        <Section title="Usage">
          {st ? (
            <>
              <KV k="CPU">
                <UsageBar v={pct(st.cpu)} label={st.cpu} width="w-56" />
              </KV>
              <KV k="Memory">
                <UsageBar v={pct(st.memPct)} label={st.memPct} width="w-56" />
                <span className="text-faint font-mono text-[11px] ml-2">{st.mem}</span>
              </KV>
              <KV k="Network I/O">{st.net}</KV>
              <KV k="Block I/O">{st.block}</KV>
              {st.pids && <KV k="Processes">{st.pids}</KV>}
            </>
          ) : (
            <div className="text-faint text-xs">{c.State.Running ? "sampling…" : "not running"}</div>
          )}
          {c.HostConfig.Memory > 0 && <KV k="Memory limit">{(c.HostConfig.Memory / 1024 / 1024).toFixed(0)} MiB</KV>}
          {c.HostConfig.NanoCpus > 0 && <KV k="CPU limit">{c.HostConfig.NanoCpus / 1e9}</KV>}
        </Section>
      </div>
      <Section title="Image & command">
        <KV k="Image">
          <span className="font-mono">{c.Config.Image}</span>
        </KV>
        <KV k="Command">
          <span className="font-mono break-all">{cmd || "—"}</span>
        </KV>
        {c.Config.WorkingDir && <KV k="Working dir">{c.Config.WorkingDir}</KV>}
        {c.Config.User && <KV k="User">{c.Config.User}</KV>}
        {c.HostConfig.Privileged && (
          <KV k="Privileged">
            <Badge tone="err">yes</Badge>
          </KV>
        )}
      </Section>
      {c.State.Health?.Log && c.State.Health.Log.length > 0 && (
        <Section title="Health checks">
          {c.State.Health.Log.slice(-3).map((h, i) => (
            <div key={i} className="text-[12px] py-1 border-b border-line/50 last:border-0">
              <Badge tone={h.ExitCode === 0 ? "ok" : "err"}>exit {h.ExitCode}</Badge> <span className="text-faint">{new Date(h.Start).toLocaleTimeString()}</span>
              <pre className="font-mono text-[11px] text-muted whitespace-pre-wrap mt-0.5">{h.Output.trim()}</pre>
            </div>
          ))}
        </Section>
      )}
      <div className="grid grid-cols-2 gap-3">
        <Section title="Ports">
          {Object.entries(c.NetworkSettings.Ports ?? {}).length === 0 && <div className="text-faint text-xs">none</div>}
          {Object.entries(c.NetworkSettings.Ports ?? {}).map(([cp, binds]) => (
            <KV key={cp} k={cp}>
              {binds?.length ? binds.map((b) => `${b.HostIp || "0.0.0.0"}:${b.HostPort}`).join(", ") : <span className="text-faint">not published</span>}
            </KV>
          ))}
        </Section>
        <Section title="Networks">
          {Object.entries(c.NetworkSettings.Networks ?? {}).map(([n, v]) => (
            <KV key={n} k={n}>
              <span className="font-mono">{v.IPAddress || "—"}</span>
              {v.Aliases && v.Aliases.length > 0 && <span className="text-faint ml-2">aliases {v.Aliases.join(", ")}</span>}
            </KV>
          ))}
        </Section>
      </div>
      <Section title="Mounts">
        {(c.Mounts ?? []).length === 0 && <div className="text-faint text-xs">none</div>}
        {(c.Mounts ?? []).map((m, i) => (
          <div key={i} className="flex items-center gap-2 text-[12px] py-1 border-b border-line/50 last:border-0 font-mono">
            <Badge>{m.Type}</Badge>
            {m.Type === "volume" && m.Name ? (
              <button className="text-accent hover:underline truncate max-w-80" onClick={() => goto("volumes", { volume: m.Name! })} title="Browse the volume">
                {m.Name}
              </button>
            ) : (
              <span className="truncate max-w-80" title={m.Source}>
                {m.Source}
              </span>
            )}
            <span className="text-faint">→</span>
            <span>{m.Destination}</span>
            {!m.RW && <Badge tone="warn">read-only</Badge>}
          </div>
        ))}
      </Section>
      <Section
        title={`Environment (${c.Config.Env?.length ?? 0})`}
        action={
          <Button size="xs" variant="ghost" icon={showEnv ? <EyeOff className="size-3" /> : <Eye className="size-3" />} onClick={() => setShowEnv(!showEnv)}>
            {showEnv ? "Hide values" : "Show values"}
          </Button>
        }
      >
        {(c.Config.Env ?? []).map((e) => {
          const i = e.indexOf("=");
          return (
            <KV key={e} k={e.slice(0, i)} mono>
              <span className="break-all">{showEnv ? e.slice(i + 1) : "••••••"}</span>
            </KV>
          );
        })}
      </Section>
      {labels.length > 0 && (
        <Section title={`Labels (${labels.length})`}>
          {labels.map(([k, v]) => (
            <KV key={k} k={k} mono>
              <span className="break-all">{v}</span>
            </KV>
          ))}
        </Section>
      )}
    </div>
  );
}

export function Section({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return (
    <section className="rounded-lg border border-line bg-panel">
      <header className="flex items-center justify-between px-3 h-8 border-b border-line">
        <h3 className="text-[11px] uppercase tracking-wide text-faint font-medium">{title}</h3>
        {action}
      </header>
      <div className="px-3 py-2">{children}</div>
    </section>
  );
}

export function KV({ k, children, mono }: { k: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className="flex gap-3 text-[12.5px] py-0.5 items-baseline">
      <span className={cx("w-36 shrink-0 text-faint truncate", mono && "font-mono text-[11.5px]")} title={k}>
        {k}
      </span>
      <span className={cx("min-w-0 flex-1 flex items-center flex-wrap gap-1", mono && "font-mono text-[11.5px]")}>{children}</span>
    </div>
  );
}
