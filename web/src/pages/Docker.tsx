import { useEffect, useMemo, useState } from "react";
import { Container as ContainerIcon, FileJson, Play, RefreshCw, RotateCw, ScrollText, Square, Trash2 } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { Container } from "../api/types";
import { hostRoles, useWs } from "../context/app";
import {
  Badge,
  Button,
  CodeBlock,
  EmptyState,
  ErrorBox,
  IconButton,
  Loading,
  PageHeader,
  SearchInput,
  Select,
  Table,
  Tabs,
  Td,
  Th,
  Toggle,
  Tr,
} from "../components/ui";
import { Drawer, Modal, useConfirm, useToast } from "../components/overlay";
import { LogViewer } from "../components/LogViewer";
import { useAsync, useInterval, useLocalStorage } from "../hooks";

type Tab = "containers" | "images" | "stats";

export function DockerPage() {
  const { name: ws, hosts } = useWs();
  const sorted = useMemo(
    () => [...hosts].sort((a, b) => Number(hostRoles(b).includes("docker")) - Number(hostRoles(a).includes("docker")) || a.name.localeCompare(b.name)),
    [hosts],
  );
  const [saved, setSaved] = useLocalStorage<string>(`opsdeck.docker.${ws}`, "");
  const [host, setHost] = useState<string>(saved || sorted[0]?.name || "");
  useEffect(() => {
    if (!host && sorted[0]) setHost(sorted[0].name);
  }, [sorted, host]);
  useEffect(() => {
    if (host) setSaved(host);
  }, [host, setSaved]);
  const [tab, setTab] = useState<Tab>("containers");

  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<ContainerIcon />}
        title="Docker"
        subtitle="Containers on a host, managed over SSH with the docker CLI."
        actions={
          <Select value={host} onChange={(e) => setHost(e.target.value)} className="w-56">
            {sorted.length === 0 && <option value="">No hosts</option>}
            {sorted.map((h) => (
              <option key={h.name} value={h.name}>
                {h.name}
                {hostRoles(h).includes("docker") ? " · docker" : ""}
              </option>
            ))}
          </Select>
        }
      />
      <Tabs
        className="px-3"
        value={tab}
        onChange={setTab}
        tabs={[
          { id: "containers", label: "Containers" },
          { id: "images", label: "Images" },
          { id: "stats", label: "Stats" },
        ]}
      />
      <div className="flex-1 min-h-0 p-5">
        {!host ? (
          <EmptyState icon={<ContainerIcon />} title="No hosts">
            Add a host (tip: give it the role <span className="font-mono">docker</span>) to manage its containers.
          </EmptyState>
        ) : tab === "containers" ? (
          <Containers ws={ws} host={host} />
        ) : tab === "images" ? (
          <Images ws={ws} host={host} />
        ) : (
          <Stats ws={ws} host={host} />
        )}
      </div>
    </div>
  );
}

function stateTone(s: string) {
  if (s === "running") return "ok" as const;
  if (s === "restarting" || s === "paused") return "warn" as const;
  if (s === "exited" || s === "dead") return "err" as const;
  return "neutral" as const;
}

function Containers({ ws, host }: { ws: string; host: string }) {
  const [all, setAll] = useState(true);
  const [q, setQ] = useState("");
  const c = useAsync(() => api.containers(ws, host, all), [ws, host, all]);
  const [busy, setBusy] = useState<string | null>(null);
  const [logs, setLogs] = useState<Container | null>(null);
  const [inspect, setInspect] = useState<{ c: Container; json: string } | null>(null);
  const toast = useToast();
  const confirm = useConfirm();

  async function act(ct: Container, action: "start" | "stop" | "restart" | "rm") {
    if (action !== "start") {
      const ok = await confirm({
        title: `${action === "rm" ? "Remove" : action[0].toUpperCase() + action.slice(1)} ${ct.name}?`,
        message: (
          <>
            On host <span className="font-mono text-fg">{host}</span>. {action === "rm" && "The container is deleted (volumes are kept)."}
          </>
        ),
        danger: action !== "restart",
        confirmLabel: action === "rm" ? "Remove" : action[0].toUpperCase() + action.slice(1),
      });
      if (!ok) return;
    }
    setBusy(`${ct.id}:${action}`);
    try {
      await api.containerAction(ws, host, ct.id, action);
      toast.success(`${action} ${ct.name}`, "done");
      await c.reload();
    } catch (e) {
      toast.error(`${action} ${ct.name} failed`, errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  async function doInspect(ct: Container) {
    try {
      const j = await api.inspect(ws, host, ct.id);
      setInspect({ c: ct, json: JSON.stringify(j, null, 2) });
    } catch (e) {
      toast.error("Inspect failed", errMsg(e));
    }
  }

  const rows = (c.data ?? []).filter((x) => !q || x.name.includes(q) || x.image.includes(q));

  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-3">
        <SearchInput value={q} onChange={setQ} placeholder="Filter containers…" className="w-64" />
        <Toggle checked={all} onChange={setAll} label="Show stopped" />
        <div className="flex-1" />
        <span className="text-xs text-faint">{rows.length} containers</span>
        <IconButton title="Refresh" onClick={() => void c.reload()}>
          <RefreshCw className={c.loading ? "size-4 animate-spin" : "size-4"} />
        </IconButton>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {c.error && <ErrorBox error={c.error} onRetry={() => void c.reload()} />}
        {!c.data && c.loading && <Loading label={`Listing containers on ${host}…`} />}
        {c.data && rows.length === 0 && <EmptyState icon={<ContainerIcon />} title="No containers" />}
        {rows.length > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>Name</Th>
                <Th>Image</Th>
                <Th>State</Th>
                <Th>Status</Th>
                <Th>Ports</Th>
                <Th className="w-[1%]" />
              </tr>
            </thead>
            <tbody>
              {rows.map((ct) => (
                <Tr key={ct.id}>
                  <Td>
                    <div className="font-medium">{ct.name}</div>
                    <div className="font-mono text-[10.5px] text-faint">{ct.id.slice(0, 12)}</div>
                  </Td>
                  <Td mono className="max-w-64 truncate" title={ct.image}>
                    {ct.image}
                  </Td>
                  <Td>
                    <Badge tone={stateTone(ct.state)}>{ct.state}</Badge>
                  </Td>
                  <Td className="text-muted">{ct.status}</Td>
                  <Td mono className="text-muted max-w-72 truncate" title={ct.ports}>
                    {ct.ports || "—"}
                  </Td>
                  <Td>
                    <div className="flex items-center gap-0.5 justify-end">
                      <IconButton title="Logs" onClick={() => setLogs(ct)}>
                        <ScrollText className="size-4" />
                      </IconButton>
                      <IconButton title="Inspect" onClick={() => void doInspect(ct)}>
                        <FileJson className="size-4" />
                      </IconButton>
                      {ct.state === "running" ? (
                        <>
                          <IconButton title="Restart" disabled={!!busy} onClick={() => void act(ct, "restart")}>
                            <RotateCw className={busy === `${ct.id}:restart` ? "size-4 animate-spin" : "size-4"} />
                          </IconButton>
                          <IconButton title="Stop" disabled={!!busy} onClick={() => void act(ct, "stop")}>
                            <Square className="size-4" />
                          </IconButton>
                        </>
                      ) : (
                        <IconButton title="Start" disabled={!!busy} onClick={() => void act(ct, "start")}>
                          <Play className="size-4" />
                        </IconButton>
                      )}
                      <IconButton title="Remove" disabled={!!busy} onClick={() => void act(ct, "rm")} className="hover:!text-err">
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
      <Drawer open={!!logs} onClose={() => setLogs(null)} title={`Logs · ${logs?.name}`} subtitle={host} width={980}>
        {logs && (
          <LogViewer
            className="h-[calc(100vh-110px)]"
            url={api.logStreamUrl(ws, { host, source: "docker", container: logs.name, lines: 300, follow: true })}
          />
        )}
      </Drawer>
      <Modal open={!!inspect} onClose={() => setInspect(null)} title={`Inspect · ${inspect?.c.name}`} width={900}>
        {inspect && <CodeBlock maxHeight={600}>{inspect.json}</CodeBlock>}
      </Modal>
    </div>
  );
}

function Images({ ws, host }: { ws: string; host: string }) {
  const im = useAsync(() => api.images(ws, host), [ws, host]);
  return (
    <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
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
            </tr>
          </thead>
          <tbody>
            {im.data.map((i) => (
              <Tr key={i.id + i.repository + i.tag}>
                <Td mono>{i.repository}</Td>
                <Td>
                  <Badge mono>{i.tag}</Badge>
                </Td>
                <Td mono className="text-muted">
                  {i.id.replace("sha256:", "").slice(0, 12)}
                </Td>
                <Td className="tabular-nums">{String(i.size)}</Td>
                <Td className="text-muted">{i.created}</Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}

function Stats({ ws, host }: { ws: string; host: string }) {
  const [auto, setAuto] = useState(true);
  const st = useAsync(() => api.dockerStats(ws, host), [ws, host]);
  useInterval(() => void st.reload(), auto ? 5000 : null);
  const pct = (s: string) => parseFloat(String(s).replace("%", "")) || 0;
  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-3">
        <Toggle checked={auto} onChange={setAuto} label="Auto-refresh (5s)" />
        <div className="flex-1" />
        <Button size="sm" icon={<RefreshCw className="size-3.5" />} onClick={() => void st.reload()} loading={st.loading}>
          Refresh
        </Button>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {st.error && <ErrorBox error={st.error} />}
        {!st.data && st.loading && <Loading label="docker stats…" />}
        {st.data && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>Container</Th>
                <Th>CPU</Th>
                <Th>Memory</Th>
                <Th>Mem %</Th>
                <Th>Net I/O</Th>
                <Th>Block I/O</Th>
              </tr>
            </thead>
            <tbody>
              {st.data.map((s) => (
                <Tr key={s.id}>
                  <Td className="font-medium">{s.name}</Td>
                  <Td>
                    <Bar v={pct(s.cpu)} label={s.cpu} />
                  </Td>
                  <Td mono className="text-muted">
                    {s.mem}
                  </Td>
                  <Td>
                    <Bar v={pct(s.memPct)} label={s.memPct} />
                  </Td>
                  <Td mono className="text-muted">
                    {s.net}
                  </Td>
                  <Td mono className="text-muted">
                    {s.block}
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

function Bar({ v, label }: { v: number; label: string }) {
  const color = v > 90 ? "bg-err" : v > 70 ? "bg-warn" : "bg-accent";
  return (
    <div className="flex items-center gap-2 w-40">
      <div className="flex-1 h-1.5 rounded-full bg-panel-3 overflow-hidden">
        <div className={`h-full ${color}`} style={{ width: `${Math.min(100, v)}%` }} />
      </div>
      <span className="font-mono text-[11px] tabular-nums w-14 text-right">{label}</span>
    </div>
  );
}
