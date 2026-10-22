import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowDownToLine, Boxes, Eraser, FileJson, Pause, Play, RefreshCw, Trash2 } from "lucide-react";
import { api } from "../../api/client";
import type { DockerEvent, DockerImage, DockerInfo } from "../../api/types";
import { Badge, Button, EmptyState, ErrorBox, IconButton, Input, Loading, SearchInput, StatusDot, Table, Td, Th, Toggle, Tr, cx } from "../../components/ui";
import type { Tone } from "../../components/ui";
import { Modal, useConfirm } from "../../components/overlay";
import { useAsync, useInterval } from "../../hooks";
import { fmtBytes } from "../../lib/format";
import { InspectJSON, Section, KV, UsageBar, pct, useDocker, useRun } from "./shared";

export function Images() {
  const { ws, host } = useDocker();
  const im = useAsync(() => api.images(ws, host), [ws, host]);
  const [q, setQ] = useState("");
  const [ref, setRef] = useState("");
  const { busy, run } = useRun();
  const confirm = useConfirm();
  const rows = (im.data ?? []).filter((i) => !q || `${i.repository}:${i.tag}`.includes(q) || i.id.includes(q));
  async function remove(i: DockerImage) {
    const name = i.repository === "<none>" ? i.id : `${i.repository}:${i.tag}`;
    const ok = await confirm({ title: `Remove image ${name}?`, message: "Containers using it must be removed first.", danger: true, confirmLabel: "Remove" });
    if (ok) await run(`rm:${i.id}${i.tag}`, `Remove ${name}`, () => api.imageRemove(ws, host, name), () => im.reload());
  }
  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-3">
        <SearchInput value={q} onChange={setQ} placeholder="Filter images…" className="w-64" />
        <div className="flex-1" />
        <Input mono className="w-72" placeholder="nginx:1.27 or ghcr.io/acme/app:2.1" value={ref} onChange={(e) => setRef(e.target.value)} onKeyDown={(e) => e.key === "Enter" && ref && void run("pull", `Pull ${ref}`, () => api.imagePull(ws, host, ref), () => im.reload())} />
        <Button size="sm" icon={<ArrowDownToLine className="size-3.5" />} disabled={!ref} loading={busy === "pull"} onClick={() => void run("pull", `Pull ${ref}`, () => api.imagePull(ws, host, ref), () => im.reload())}>
          Pull
        </Button>
        <IconButton title="Refresh" onClick={() => void im.reload()}>
          <RefreshCw className={cx("size-4", im.loading && "animate-spin")} />
        </IconButton>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {im.error && <ErrorBox error={im.error} onRetry={() => void im.reload()} />}
        {!im.data && im.loading && <Loading />}
        {im.data && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>Repository</Th>
                <Th>Tag</Th>
                <Th>ID</Th>
                <Th>Size</Th>
                <Th>Created</Th>
                <Th className="w-[1%]" />
              </tr>
            </thead>
            <tbody>
              {rows.map((i) => (
                <Tr key={i.id + i.repository + i.tag}>
                  <Td mono className={cx(i.repository === "<none>" && "text-faint")}>
                    {i.repository}
                  </Td>
                  <Td>
                    <Badge mono tone={i.tag === "<none>" ? "warn" : "neutral"}>
                      {i.tag === "<none>" ? "dangling" : i.tag}
                    </Badge>
                  </Td>
                  <Td mono className="text-muted">
                    {i.id.replace("sha256:", "").slice(0, 12)}
                  </Td>
                  <Td className="tabular-nums">{String(i.size)}</Td>
                  <Td className="text-muted">{i.created}</Td>
                  <Td>
                    <div className="flex items-center gap-0.5 justify-end">
                      {i.repository !== "<none>" && (
                        <IconButton title="Pull again (update the tag)" disabled={!!busy} onClick={() => void run(`pull:${i.id}`, `Pull ${i.repository}:${i.tag}`, () => api.imagePull(ws, host, `${i.repository}:${i.tag}`), () => im.reload())}>
                          <ArrowDownToLine className={cx("size-4", busy === `pull:${i.id}` && "animate-pulse")} />
                        </IconButton>
                      )}
                      <IconButton title="Remove" className="hover:!text-err" disabled={!!busy} onClick={() => void remove(i)}>
                        <Trash2 className="size-4" />
                      </IconButton>
                    </div>
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </div>
    </div>
  );
}

export function Networks() {
  const { ws, host } = useDocker();
  const n = useAsync(() => api.dockerNetworks(ws, host), [ws, host]);
  const [inspect, setInspect] = useState<string | null>(null);
  const { busy, run } = useRun();
  const confirm = useConfirm();
  return (
    <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
      {n.error && <ErrorBox error={n.error} onRetry={() => void n.reload()} />}
      {!n.data && n.loading && <Loading />}
      {n.data && (
        <Table className="flex-1">
          <thead>
            <tr>
              <Th>Network</Th>
              <Th>Driver</Th>
              <Th>Scope</Th>
              <Th>Belongs to</Th>
              <Th className="w-[1%]" />
            </tr>
          </thead>
          <tbody>
            {n.data.map((x) => (
              <Tr key={x.id}>
                <Td>
                  <span className={cx("font-mono", x.builtin && "text-muted")}>{x.name}</span>
                  {x.internal && (
                    <Badge tone="info" className="ml-2">
                      internal
                    </Badge>
                  )}
                </Td>
                <Td>{x.driver}</Td>
                <Td className="text-muted">{x.scope}</Td>
                <Td>
                  {x.project && <Badge>compose {x.project}</Badge>}
                  {x.stack && <Badge>stack {x.stack}</Badge>}
                  {x.builtin && <span className="text-faint">built in</span>}
                </Td>
                <Td>
                  <div className="flex items-center gap-0.5 justify-end">
                    <IconButton title="Inspect (containers, subnets)" onClick={() => setInspect(x.name)}>
                      <FileJson className="size-4" />
                    </IconButton>
                    {!x.builtin && (
                      <IconButton
                        title="Remove"
                        className="hover:!text-err"
                        disabled={!!busy}
                        onClick={async () => {
                          if (await confirm({ title: `Remove network ${x.name}?`, danger: true, confirmLabel: "Remove" }))
                            await run(x.id, `Remove ${x.name}`, () => api.dockerNetworkRemove(ws, host, x.id), () => n.reload());
                        }}
                      >
                        <Trash2 className="size-4" />
                      </IconButton>
                    )}
                  </div>
                </Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      )}
      <Modal open={!!inspect} onClose={() => setInspect(null)} title={`Network ${inspect}`} width={900}>
        {inspect && (
          <div className="max-h-[65vh]">
            <InspectJSON kind="network" id={inspect} />
          </div>
        )}
      </Modal>
    </div>
  );
}

const PRUNE: { kind: string; label: string; detail: string; danger?: boolean; typed?: boolean }[] = [
  { kind: "containers", label: "Stopped containers", detail: "every container that is not running" },
  { kind: "images", label: "Dangling images", detail: "untagged layers left by builds and pulls" },
  { kind: "images-unused", label: "Unused images", detail: "every image no container uses (pulled again when needed)", danger: true },
  { kind: "build-cache", label: "Build cache", detail: "BuildKit cache" },
  { kind: "networks", label: "Unused networks", detail: "networks no container uses" },
  { kind: "volumes", label: "Unused volumes", detail: "every volume no container uses — their data is lost", danger: true, typed: true },
];

export function System({ info }: { info: DockerInfo | undefined }) {
  const { ws, host } = useDocker();
  const df = useAsync(() => api.dockerDf(ws, host), [ws, host]);
  const { busy, run } = useRun();
  const confirm = useConfirm();
  async function prune(p: (typeof PRUNE)[number]) {
    const ok = await confirm({
      title: `Remove ${p.label.toLowerCase()} on ${host}?`,
      message: p.detail + ".",
      danger: true,
      confirmLabel: "Remove",
      typeToConfirm: p.typed ? host : undefined,
    });
    if (ok) await run(p.kind, `Prune ${p.label.toLowerCase()}`, () => api.dockerPrune(ws, host, p.kind), () => df.reload());
  }
  return (
    <div className="h-full overflow-y-auto space-y-4">
      {info && (
        <div className="grid grid-cols-2 gap-3">
          <Section title="Engine">
            <KV k="Version">
              {info.serverVersion} {info.compose && <span className="text-faint">· compose {info.compose}</span>}
            </KV>
            <KV k="System">
              {info.os} ({info.arch})
            </KV>
            <KV k="Resources">
              {info.cpus} CPUs · {fmtBytes(info.memTotal)}
            </KV>
            <KV k="Storage">
              {info.storageDriver} in <span className="font-mono">{info.rootDir}</span>
            </KV>
            <KV k="Access">{info.sudo ? <Badge tone="warn">through sudo -n</Badge> : "docker group / root"}</KV>
          </Section>
          <Section title="Objects">
            <KV k="Containers">
              {info.running} running · {info.paused} paused · {info.stopped} stopped
            </KV>
            <KV k="Images">{info.images}</KV>
            <KV k="Swarm">
              {info.swarm.state}
              {info.swarm.state === "active" && ` · ${info.swarm.manager ? "manager" : "worker"} · ${info.swarm.nodes} nodes`}
            </KV>
          </Section>
        </div>
      )}
      {info?.warnings && info.warnings.length > 0 && (
        <Section title="Warnings">
          {info.warnings.map((w) => (
            <div key={w} className="text-[12px] text-warn py-0.5">
              {w}
            </div>
          ))}
        </Section>
      )}
      <Section title="Disk usage" action={<IconButton title="Refresh" onClick={() => void df.reload()}><RefreshCw className={cx("size-3.5", df.loading && "animate-spin")} /></IconButton>}>
        {df.error && <div className="text-err text-xs">{df.error}</div>}
        {!df.data && df.loading && <Loading />}
        {df.data && (
          <table className="w-full text-[12.5px]">
            <thead>
              <tr className="text-left text-[11px] text-faint">
                <th className="py-1 font-medium">Type</th>
                <th className="font-medium">Total</th>
                <th className="font-medium">Active</th>
                <th className="font-medium">Size</th>
                <th className="font-medium">Reclaimable</th>
              </tr>
            </thead>
            <tbody>
              {df.data.map((u) => (
                <tr key={u.type} className="border-t border-line/50">
                  <td className="py-1.5">{u.type}</td>
                  <td>{u.total}</td>
                  <td>{u.active}</td>
                  <td className="tabular-nums">{u.size}</td>
                  <td className="tabular-nums">{u.reclaimable}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
      <Section title="Clean up">
        <div className="grid grid-cols-2 xl:grid-cols-3 gap-2">
          {PRUNE.map((p) => (
            <div key={p.kind} className="rounded-md border border-line p-2.5 flex items-start gap-2">
              <div className="flex-1 min-w-0">
                <div className="text-[12.5px] font-medium">{p.label}</div>
                <div className="text-[11px] text-faint">{p.detail}</div>
              </div>
              <Button size="xs" variant={p.danger ? "danger" : "secondary"} icon={<Eraser className="size-3" />} loading={busy === p.kind} disabled={!!busy} onClick={() => void prune(p)}>
                Prune
              </Button>
            </div>
          ))}
        </div>
      </Section>
    </div>
  );
}

export function Stats() {
  const { ws, host, openContainer } = useDocker();
  const [auto, setAuto] = useState(true);
  const st = useAsync(() => api.dockerStats(ws, host), [ws, host]);
  useInterval(() => void st.reload(), auto ? 3000 : null);
  const [sort, setSort] = useState<"cpu" | "mem" | "name">("cpu");
  const rows = useMemo(() => {
    const r = [...(st.data ?? [])];
    if (sort === "cpu") r.sort((a, b) => pct(b.cpu) - pct(a.cpu));
    else if (sort === "mem") r.sort((a, b) => pct(b.memPct) - pct(a.memPct));
    else r.sort((a, b) => a.name.localeCompare(b.name));
    return r;
  }, [st.data, sort]);
  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-3">
        <Toggle checked={auto} onChange={setAuto} label="Live (3s)" />
        <div className="flex-1" />
        <Button size="sm" icon={<RefreshCw className="size-3.5" />} onClick={() => void st.reload()} loading={st.loading}>
          Refresh
        </Button>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {st.error && <ErrorBox error={st.error} />}
        {!st.data && st.loading && <Loading label="docker stats…" />}
        {st.data && st.data.length === 0 && <EmptyState icon={<Boxes />} title="No running containers" />}
        {rows.length > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>
                  <button onClick={() => setSort("name")}>Container{sort === "name" && " ↓"}</button>
                </Th>
                <Th>
                  <button onClick={() => setSort("cpu")}>CPU{sort === "cpu" && " ↓"}</button>
                </Th>
                <Th>
                  <button onClick={() => setSort("mem")}>Memory{sort === "mem" && " ↓"}</button>
                </Th>
                <Th>Mem %</Th>
                <Th>Net I/O</Th>
                <Th>Block I/O</Th>
                <Th>PIDs</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((s) => (
                <Tr key={s.id} onClick={() => openContainer(s.id, s.name, "overview")}>
                  <Td className="font-medium">{s.name}</Td>
                  <Td>
                    <UsageBar v={pct(s.cpu)} label={s.cpu} />
                  </Td>
                  <Td mono className="text-muted">
                    {s.mem}
                  </Td>
                  <Td>
                    <UsageBar v={pct(s.memPct)} label={s.memPct} />
                  </Td>
                  <Td mono className="text-muted">
                    {s.net}
                  </Td>
                  <Td mono className="text-muted">
                    {s.block}
                  </Td>
                  <Td mono className="text-muted">
                    {s.pids}
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </div>
    </div>
  );
}

const TYPE_TONE: Record<string, Tone> = { container: "accent", image: "info", volume: "neutral", network: "neutral", service: "ok", node: "warn", config: "neutral", secret: "neutral" };
function actionTone(a: string): Tone {
  if (/die|kill|oom|destroy|delete|remove|unhealthy/.test(a)) return "err";
  if (/stop|pause|health_status: starting/.test(a)) return "warn";
  if (/start|create|healthy|unpause|pull/.test(a)) return "ok";
  return "neutral";
}

export function Events() {
  const { ws, host, openContainer } = useDocker();
  const [events, setEvents] = useState<DockerEvent[]>([]);
  const [state, setState] = useState<"connecting" | "live" | "error">("connecting");
  const [error, setError] = useState("");
  const [paused, setPaused] = useState(false);
  const [types, setTypes] = useState<Set<string>>(new Set());
  const [q, setQ] = useState("");
  const pausedRef = useRef(false);
  pausedRef.current = paused;
  const [gen, setGen] = useState(0);
  useEffect(() => {
    setEvents([]);
    setState("connecting");
    setError("");
    const es = new EventSource(api.logStreamUrl(ws, { host, source: "events", since: "1h", follow: true }), { withCredentials: true });
    es.onopen = () => setState("live");
    es.onmessage = (ev) => {
      if (pausedRef.current) return;
      try {
        const d = JSON.parse(ev.data) as { event?: DockerEvent };
        if (d.event) setEvents((l) => [d.event!, ...l].slice(0, 2000));
      } catch {
        /* ignore */
      }
    };
    es.addEventListener("error", (ev) => {
      const me = ev as MessageEvent;
      setState("error");
      setError(me.data ? String(me.data) : "Stream disconnected");
      es.close();
    });
    return () => es.close();
  }, [ws, host, gen]);
  const all = useMemo(() => [...new Set(events.map((e) => e.type))].sort(), [events]);
  const rows = events.filter((e) => (types.size === 0 || types.has(e.type)) && (!q || `${e.name} ${e.action} ${e.id}`.toLowerCase().includes(q.toLowerCase())));
  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-3 flex-wrap">
        <StatusDot tone={state === "live" ? "ok" : state === "error" ? "err" : "warn"} pulse={state === "live" && !paused} />
        <span className="text-xs text-muted">{paused ? "paused" : state === "live" ? "live — the last hour, then new events as they happen" : state}</span>
        <SearchInput value={q} onChange={setQ} placeholder="Filter…" className="w-56" />
        {all.map((t) => (
          <button key={t} onClick={() => setTypes((s) => { const n = new Set(s); if (n.has(t)) n.delete(t); else n.add(t); return n; })}>
            <Badge tone={types.size === 0 || types.has(t) ? TYPE_TONE[t] ?? "neutral" : "neutral"} className={cx(types.size > 0 && !types.has(t) && "opacity-40")}>
              {t}
            </Badge>
          </button>
        ))}
        <div className="flex-1" />
        <IconButton title={paused ? "Resume" : "Pause"} active={paused} onClick={() => setPaused(!paused)}>
          {paused ? <Play className="size-4" /> : <Pause className="size-4" />}
        </IconButton>
        {state === "error" && (
          <Button size="sm" onClick={() => setGen((g) => g + 1)}>
            Reconnect
          </Button>
        )}
      </div>
      {error && <div className="text-xs text-err">{error}</div>}
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {rows.length === 0 && <EmptyState title="No events yet">Start, stop or deploy something on {host} and it shows up here.</EmptyState>}
        {rows.length > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>Time</Th>
                <Th>Type</Th>
                <Th>Action</Th>
                <Th>Object</Th>
                <Th>Details</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((e, i) => (
                <Tr key={e.time + e.id + e.action + i} onClick={e.type === "container" ? () => openContainer(e.id, e.name || e.id, "overview") : undefined}>
                  <Td mono className="text-muted whitespace-nowrap" title={e.time}>
                    {new Date(e.time).toLocaleTimeString()}
                  </Td>
                  <Td>
                    <Badge tone={TYPE_TONE[e.type] ?? "neutral"}>{e.type}</Badge>
                  </Td>
                  <Td>
                    <Badge tone={actionTone(e.action)}>{e.action}</Badge>
                  </Td>
                  <Td>
                    <span className="font-medium">{e.name || "—"}</span> <span className="font-mono text-[10.5px] text-faint">{e.id.slice(0, 12)}</span>
                  </Td>
                  <Td className="text-[11px] text-faint max-w-96 truncate" title={Object.entries(e.attrs ?? {}).map(([k, v]) => `${k}=${v}`).join("\n")}>
                    {Object.entries(e.attrs ?? {})
                      .filter(([k]) => !k.startsWith("com.docker.compose.") && k !== "name" && !k.startsWith("org.opencontainers"))
                      .slice(0, 4)
                      .map(([k, v]) => `${k}=${v}`)
                      .join(" · ")}
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </div>
    </div>
  );
}
