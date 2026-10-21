import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { AlertTriangle, ArrowRight, Bot, CircleCheck, Plus, Power, PowerOff, RefreshCw, Server, SquareTerminal, XCircle } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { NetworkStatus, Topology, TopologyNode } from "../api/types";
import { useApp, useWs } from "../context/app";
import { useTerminals } from "../context/terminals";
import { KindIcon } from "../components/icons";
import { Badge, Button, Card, EmptyState, IconButton, StatusDot, cx } from "../components/ui";
import { useToast } from "../components/overlay";
import { useAsync, useInterval } from "../hooks";
import { fmtTime, timeAgo } from "../lib/format";

export function OverviewPage() {
  const { kinds } = useApp();
  const { name: ws, workspace, resources } = useWs();
  const navigate = useNavigate();
  const terms = useTerminals();
  const toast = useToast();
  const topo = useAsync(() => api.topology(ws), [ws, resources.length]);
  const issues = useAsync(() => api.validate(ws), [ws, resources]);
  const nets = useAsync(() => api.networks(ws), [ws]);
  const audit = useAsync(() => api.audit({ workspace: ws, limit: 12 }), [ws]);
  const [netBusy, setNetBusy] = useState<string | null>(null);
  useInterval(() => void nets.reload(), 10000);

  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    resources.forEach((r) => (c[r.kind] = (c[r.kind] ?? 0) + 1));
    return c;
  }, [resources]);

  async function toggleNet(n: NetworkStatus) {
    setNetBusy(n.name);
    try {
      if (n.status === "up") await api.networkDown(ws, n.name);
      else await api.networkUp(ws, n.name);
      await nets.reload();
    } catch (e) {
      toast.error(`Network ${n.name}`, errMsg(e));
      await nets.reload();
    } finally {
      setNetBusy(null);
    }
  }

  const W = `/w/${encodeURIComponent(ws)}`;
  const firstHost = resources.find((r) => r.kind === "Host");

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="px-6 pt-5 pb-4 border-b border-line bg-panel">
        <div className="flex items-start justify-between gap-6">
          <div className="min-w-0">
            <div className="flex items-center gap-2.5">
              <span className="size-3 rounded-full" style={{ background: workspace?.color }} />
              <h1 className="text-[20px] font-semibold tracking-tight">{workspace?.title || ws}</h1>
              <span className="font-mono text-xs text-faint">{ws}</span>
            </div>
            {workspace?.description && <p className="text-muted mt-1 max-w-3xl">{workspace.description}</p>}
            <div className="flex flex-wrap gap-1.5 mt-2.5">
              {workspace?.environments.map((e) => (
                <Badge key={e.name} tone={e.protected ? "err" : "neutral"} title={e.protected ? "Protected environment" : undefined}>
                  {e.name}
                  {e.protected && " · protected"}
                </Badge>
              ))}
              {workspace?.tags?.map((t) => (
                <Badge key={t} tone="info">
                  #{t}
                </Badge>
              ))}
            </div>
          </div>
          <div className="flex gap-2 shrink-0">
            <Button icon={<Plus className="size-4" />} onClick={() => navigate(`${W}/resources?new=Host`)}>
              Add host
            </Button>
            {firstHost && (
              <Button
                icon={<SquareTerminal className="size-4" />}
                onClick={() => {
                  terms.open(ws, firstHost.name);
                  navigate(`${W}/terminal`);
                }}
              >
                SSH {firstHost.name}
              </Button>
            )}
            <Button variant="primary" icon={<Bot className="size-4" />} onClick={() => navigate(`${W}/agent?new=1`)}>
              Ask the agent
            </Button>
          </div>
        </div>
        <div className="mt-4 grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-2">
          {kinds.map((k) => (
            <Link
              key={k.kind}
              to={`${W}/resources`}
              className="rounded-md border border-line bg-panel-2 px-3 py-2 hover:border-line-strong transition-colors flex items-center gap-2.5"
            >
              <span className="text-muted">
                <KindIcon kind={k.kind} icon={k.icon} className="size-4" />
              </span>
              <span className="flex-1 text-xs text-muted truncate">{k.label}</span>
              <span className="text-[15px] font-semibold tabular-nums">{counts[k.kind] ?? 0}</span>
            </Link>
          ))}
        </div>
      </div>

      <div className="p-5 grid grid-cols-1 xl:grid-cols-3 gap-4">
        <Card
          className="xl:col-span-2"
          title="Topology"
          actions={
            <IconButton title="Refresh" onClick={() => void topo.reload()}>
              <RefreshCw className="size-3.5" />
            </IconButton>
          }
          bodyClass="p-0"
        >
          {topo.data && topo.data.nodes.length ? (
            <TopologyGraph topo={topo.data} />
          ) : (
            <EmptyState icon={<Server />} title="Nothing to draw yet">
              Add networks and hosts with a <span className="font-mono">via</span> to see how everything is reached.
            </EmptyState>
          )}
        </Card>

        <div className="space-y-4">
          <Card title="Networks" bodyClass="p-0">
            {(nets.data ?? []).length === 0 ? (
              <div className="p-4 text-xs text-faint">No WireGuard / proxy networks in this workspace.</div>
            ) : (
              <ul>
                {nets.data!.map((n) => (
                  <li key={n.name} className="flex items-center gap-2.5 px-3 h-11 border-b border-line last:border-0">
                    <StatusDot tone={n.status === "up" ? "ok" : n.status === "error" ? "err" : "neutral"} />
                    <div className="min-w-0 flex-1">
                      <div className="text-[13px] font-medium truncate">{n.name}</div>
                      <div className="text-[11px] text-faint truncate">
                        {n.kind}
                        {n.status === "up" && n.address ? ` · ${n.address}` : ""}
                        {n.status === "up" && n.since ? ` · up ${timeAgo(n.since).replace(" ago", "")}` : ""}
                        {n.status === "error" && n.error ? ` · ${n.error}` : ""}
                      </div>
                    </div>
                    <Button
                      size="xs"
                      variant={n.status === "up" ? "secondary" : "subtle"}
                      loading={netBusy === n.name}
                      icon={n.status === "up" ? <PowerOff className="size-3" /> : <Power className="size-3" />}
                      onClick={() => void toggleNet(n)}
                    >
                      {n.status === "up" ? "Down" : "Up"}
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card title="Validation" bodyClass="p-0">
            {(issues.data ?? []).length === 0 ? (
              <div className="flex items-center gap-2 p-3 text-xs text-ok">
                <CircleCheck className="size-4" /> Workspace is valid
              </div>
            ) : (
              <ul className="max-h-64 overflow-y-auto">
                {issues.data!.map((i, idx) => (
                  <li key={idx} className="flex gap-2 px-3 py-2 border-b border-line last:border-0 text-xs">
                    {i.level === "error" ? <XCircle className="size-3.5 text-err shrink-0 mt-0.5" /> : <AlertTriangle className="size-3.5 text-warn shrink-0 mt-0.5" />}
                    <div className="min-w-0">
                      {i.resource && <span className="font-mono text-muted">{i.resource}: </span>}
                      {i.message}
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>

        <Card
          className="xl:col-span-3"
          title="Recent activity"
          actions={
            <Link to={`${W}/activity`} className="text-xs text-muted hover:text-fg flex items-center gap-1">
              All activity <ArrowRight className="size-3" />
            </Link>
          }
          bodyClass="p-0"
        >
          {(audit.data ?? []).length === 0 ? (
            <div className="p-4 text-xs text-faint">No activity yet.</div>
          ) : (
            <ul>
              {audit.data!.map((a) => (
                <li key={a.seq} className="flex items-center gap-3 px-3 h-9 border-b border-line last:border-0 text-xs">
                  <span className="text-faint w-28 shrink-0 tabular-nums">{fmtTime(a.ts)}</span>
                  <Badge tone={a.actor.startsWith("agent") ? "accent" : "neutral"}>{a.actor}</Badge>
                  <span className="font-medium">{a.action}</span>
                  <span className="font-mono text-muted truncate flex-1">{a.target}</span>
                  <Badge tone={/ok|success|allow|approve/i.test(a.result) ? "ok" : /err|fail|deny/i.test(a.result) ? "err" : "neutral"}>{a.result || "—"}</Badge>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </div>
  );
}

const COL_W = 210;
const ROW_H = 54;
const NODE_W = 170;
const NODE_H = 38;

function TopologyGraph({ topo }: { topo: Topology }) {
  const { name: ws } = useWs();
  const navigate = useNavigate();
  const layout = useMemo(() => {
    const viaOf = new Map<string, string>();
    topo.edges.forEach((e) => viaOf.set(e.from, e.to));
    const depthCache = new Map<string, number>();
    const depth = (id: string, seen = new Set<string>()): number => {
      if (depthCache.has(id)) return depthCache.get(id)!;
      const to = viaOf.get(id);
      let d = 0;
      if (to && !seen.has(to) && topo.nodes.some((n) => n.id === to)) {
        seen.add(id);
        d = depth(to, seen) + 1;
      }
      depthCache.set(id, d);
      return d;
    };
    const sparse: TopologyNode[][] = [];
    for (const n of topo.nodes) {
      const d = depth(n.id) + 1; // column 0 = this machine
      (sparse[d] ??= []).push(n);
    }
    const cols: TopologyNode[][] = Array.from({ length: Math.max(1, sparse.length) }, (_, i) => sparse[i] ?? []);
    const kindOrder = ["WireGuard", "SocksProxy", "Host", "HostGroup", "KubernetesCluster", "Panel", "GitRemote"];
    cols.forEach((c) => c.sort((a, b) => kindOrder.indexOf(a.kind) - kindOrder.indexOf(b.kind) || a.name.localeCompare(b.name)));
    const pos = new Map<string, { x: number; y: number }>();
    const maxRows = Math.max(1, ...cols.map((c) => c.length));
    cols.forEach((c, ci) => {
      const offset = ((maxRows - c.length) * ROW_H) / 2;
      c.forEach((n, ri) => pos.set(n.id, { x: 16 + ci * COL_W, y: 16 + offset + ri * ROW_H }));
    });
    const rootY = 16 + ((maxRows - 1) * ROW_H) / 2;
    pos.set("__local", { x: 16, y: rootY });
    const width = 16 + cols.length * COL_W;
    const height = 32 + maxRows * ROW_H;
    const lines = topo.nodes.map((n) => {
      const to = viaOf.get(n.id);
      const from = pos.get(n.id)!;
      const target = to && pos.has(to) ? pos.get(to)! : pos.get("__local")!;
      return { id: n.id, x1: target.x + NODE_W, y1: target.y + NODE_H / 2, x2: from.x, y2: from.y + NODE_H / 2 };
    });
    return { pos, width, height, lines };
  }, [topo]);

  const kindColor: Record<string, string> = {
    WireGuard: "text-ok",
    SocksProxy: "text-info",
    Host: "text-fg",
    KubernetesCluster: "text-info",
    Panel: "text-warn",
  };

  return (
    <div className="overflow-auto">
      <div className="relative" style={{ width: layout.width, height: layout.height, minWidth: "100%" }}>
        <svg className="absolute inset-0" width={layout.width} height={layout.height}>
          {layout.lines.map((l) => {
            const mx = (l.x1 + l.x2) / 2;
            return (
              <path
                key={l.id}
                d={`M${l.x1},${l.y1} C${mx},${l.y1} ${mx},${l.y2} ${l.x2},${l.y2}`}
                fill="none"
                stroke="var(--c-border-strong)"
                strokeWidth={1.5}
              />
            );
          })}
        </svg>
        <div
          className="absolute rounded-md border border-dashed border-line-strong bg-panel-2 px-2.5 flex items-center gap-2 text-xs text-muted"
          style={{ left: layout.pos.get("__local")!.x, top: layout.pos.get("__local")!.y, width: NODE_W, height: NODE_H }}
        >
          <span className="size-2 rounded-full bg-accent" /> This machine
        </div>
        {topo.nodes.map((n) => {
          const p = layout.pos.get(n.id)!;
          return (
            <button
              key={n.id}
              onClick={() => navigate(`/w/${encodeURIComponent(ws)}/resources?edit=${encodeURIComponent(`${n.kind}/${n.name}`)}`)}
              className="absolute rounded-md border border-line bg-panel px-2.5 flex items-center gap-2 text-left hover:border-accent transition-colors shadow-sm"
              style={{ left: p.x, top: p.y, width: NODE_W, height: NODE_H }}
              title={`${n.kind} ${n.name}`}
            >
              <span className={cx("shrink-0", kindColor[n.kind] ?? "text-muted")}>
                <KindIcon kind={n.kind} className="size-3.5" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block text-xs font-medium truncate leading-tight">{n.name}</span>
                <span className="block text-[10px] text-faint truncate font-mono leading-tight">{n.label || n.kind}</span>
              </span>
              {n.environment && (
                <span
                  className={cx(
                    "size-1.5 rounded-full shrink-0",
                    /prod/i.test(n.environment) ? "bg-err" : /stag/i.test(n.environment) ? "bg-warn" : "bg-info",
                  )}
                  title={n.environment}
                />
              )}
            </button>
          );
        })}
      </div>
    </div>
  );
}
