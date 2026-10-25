import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { AlertTriangle, ArrowDown, ArrowRight, ArrowUp, Bot, CircleCheck, GitBranch, Lock, Plus, Power, PowerOff, RefreshCw, Server, SquareTerminal, XCircle } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { NetworkStatus, Topology, TopologyNode } from "../api/types";
import { useApp, useWs } from "../context/app";
import { useTerminals } from "../context/terminals";
import { KindIcon } from "../components/icons";
import { shortLabel } from "../components/AddResource";
import { Badge, Button, Card, EmptyState, IconButton, StatusDot, Textarea, cx } from "../components/ui";
import { useToast } from "../components/overlay";
import { useAsync, useInterval } from "../hooks";
import { fmtTime, timeAgo } from "../lib/format";
import { netStateLabel, netTone, vpnLabel } from "./VPN";

export function OverviewPage() {
  const { kinds } = useApp();
  const { name: ws, workspace, resources } = useWs();
  const navigate = useNavigate();
  const terms = useTerminals();
  const toast = useToast();
  const topo = useAsync(() => api.topology(ws), [ws, resources.length]);
  const issues = useAsync(() => api.validate(ws), [ws, resources]);
  const nets = useAsync(() => api.networks(ws), [ws]);
  const repos = useAsync(() => api.repos(ws), [ws, resources.length]);
  const audit = useAsync(() => api.audit({ workspace: ws, limit: 12 }), [ws]);
  const [netBusy, setNetBusy] = useState<string | null>(null);
  useInterval(() => void nets.reload(), (nets.data ?? []).some((n) => n.status === "connecting" || n.status === "auth") ? 2000 : 8000);

  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    resources.forEach((r) => (c[r.kind] = (c[r.kind] ?? 0) + 1));
    return c;
  }, [resources]);

  async function toggleNet(n: NetworkStatus) {
    setNetBusy(n.name);
    try {
      if (n.status === "up" || n.status === "connecting" || n.status === "auth") await api.networkDown(ws, n.name);
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
  const errors = (issues.data ?? []).filter((i) => i.level === "error").length;
  const warnings = (issues.data ?? []).length - errors;
  const used = kinds.filter((k) => counts[k.kind]);
  const unused = kinds.length - used.length;

  return (
    <div className="flex-1 min-h-0 overflow-y-auto">
      <div className="mx-auto w-full max-w-[1440px] px-4 sm:px-6 py-6 space-y-5">
        <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-4">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2.5">
              <h1 className="text-[24px] font-semibold tracking-tight">{workspace?.title || ws}</h1>
              <span className="font-mono text-xs text-faint">{ws}</span>
              {issues.data &&
                (errors + warnings === 0 ? (
                  <Badge tone="ok">
                    <StatusDot tone="ok" /> Healthy
                  </Badge>
                ) : (
                  <Badge tone={errors ? "err" : "warn"}>
                    {errors ? `${errors} error${errors > 1 ? "s" : ""}` : `${warnings} warning${warnings > 1 ? "s" : ""}`}
                  </Badge>
                ))}
            </div>
            {workspace?.description && <p className="text-muted mt-1 max-w-3xl">{workspace.description}</p>}
            <div className="flex flex-wrap gap-1.5 mt-3">
              {workspace?.environments.map((e) => (
                <Badge key={e.name} tone={e.protected ? "err" : "neutral"} title={e.protected ? "Protected: the agent asks before every change" : undefined}>
                  {e.protected && <Lock className="size-2.5" />}
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
          <div className="flex flex-wrap gap-2">
            <Button icon={<Plus className="size-4" />} onClick={() => navigate(`${W}/resources?add=1`)}>
              Add resource
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
            <Button variant="agent" icon={<Bot className="size-4" />} onClick={() => navigate(`${W}/agent?new=1`)}>
              Ask the agent
            </Button>
          </div>
        </div>

        <section aria-label="Inventory" className="rounded-xl border border-line bg-panel flex flex-wrap overflow-hidden">
          {used.map((k) => (
            <Link
              key={k.kind}
              to={`${W}/resources?kind=${encodeURIComponent(k.kind)}`}
              className="flex-[1_1_130px] min-w-0 px-4 py-3.5 border-r border-b -mb-px border-line hover:bg-panel-2 transition-colors"
            >
              <span className="flex items-center gap-1.5 text-xs text-muted truncate [&_svg]:size-3.5 [&_svg]:text-faint">
                <KindIcon kind={k.kind} icon={k.icon} />
                <span className="truncate">{shortLabel(k.label)}</span>
              </span>
              <span className="block text-[22px] font-semibold tabular-nums leading-tight mt-1">{counts[k.kind]}</span>
            </Link>
          ))}
          <button
            onClick={() => navigate(`${W}/resources?add=1`)}
            className="flex-[1.4_1_180px] min-w-0 px-4 py-3.5 border-b -mb-px border-line text-left hover:bg-panel-2 transition-colors"
          >
            <span className="block text-xs text-muted">{used.length ? `${unused} more types, none yet` : "Nothing here yet"}</span>
            <span className="mt-1 inline-flex items-center gap-1 text-[13px] font-medium text-accent">
              {used.length ? "VPNs, databases, clusters…" : "Add your first resource"} <ArrowRight className="size-3.5" />
            </span>
          </button>
        </section>

      <div className="grid grid-cols-1 xl:grid-cols-3 gap-4">
        <Card
          className="xl:col-span-2 min-w-0"
          title="Topology"
          actions={
            <>
              <Link to={`${W}/topology`} className="text-[11.5px] text-accent hover:underline px-1">
                Open
              </Link>
              <IconButton title="Refresh" onClick={() => void topo.reload()}>
                <RefreshCw className="size-3.5" />
              </IconButton>
            </>
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

        <div className="space-y-4 min-w-0">
          <AskAgentCard ws={ws} />
          <Card
            title="VPN & networks"
            bodyClass="p-0"
            actions={
              <Link to="../vpn" className="text-[11px] text-accent hover:underline">
                Manage
              </Link>
            }
          >
            {(nets.data ?? []).length === 0 ? (
              <div className="p-4 text-xs text-faint">
                No VPN profiles yet.{" "}
                <Link to="../vpn" className="text-accent hover:underline">
                  Add WireGuard, Tailscale, NetBird, OpenVPN, AnyConnect, FortiGate…
                </Link>
              </div>
            ) : (
              <ul>
                {nets.data!.map((n) => {
                  const active = n.status === "up" || n.status === "connecting" || n.status === "auth";
                  const r = resources.find((x) => x.name === n.name && x.kind === n.kind);
                  return (
                    <li key={n.name} className="flex items-center gap-2.5 px-3 min-h-11 py-1.5 border-b border-line last:border-0">
                      <StatusDot tone={netTone(n.status)} pulse={n.status === "connecting" || n.status === "auth"} />
                      <div className="min-w-0 flex-1">
                        <div className="text-[13px] font-medium truncate">
                          {n.name} <span className="text-[11px] font-normal text-faint">{vpnLabel(r, n.kind)}{n.mode ? ` · ${n.mode}` : ""}</span>
                        </div>
                        <div className={cx("text-[11px] truncate", n.status === "error" ? "text-err" : "text-faint")} title={n.error}>
                          {netStateLabel(n)}
                          {n.status === "up" && n.address ? ` · ${n.address}` : ""}
                          {n.status === "up" && n.since ? ` · ${timeAgo(n.since).replace(" ago", "")}` : ""}
                          {n.status === "error" && n.error ? ` · ${n.error}` : ""}
                          {n.status === "down" && n.missing ? ` · can't run here: ${n.missing}` : ""}
                        </div>
                        {n.status === "auth" && n.code && <div className="text-[11px] text-warn">Enter the code the gateway sent you</div>}
                        {n.status === "auth" && n.authWindow && <div className="text-[11px] text-warn">Finish the sign-in in the login window</div>}
                        {n.status === "auth" && n.authUrl && (
                          <a href={n.authUrl} target="_blank" rel="noreferrer" className="text-[11px] text-accent hover:underline">
                            Open login page →
                          </a>
                        )}
                      </div>
                      <Button
                        size="xs"
                        variant={active ? "secondary" : "subtle"}
                        loading={netBusy === n.name}
                        disabled={!active && !!n.missing}
                        icon={active ? <PowerOff className="size-3" /> : <Power className="size-3" />}
                        onClick={() => (!active && n.mode === "system" ? navigate("../vpn") : void toggleNet(n))}
                        title={!active && n.mode === "system" ? "System-mode VPNs are connected from the VPN page" : undefined}
                      >
                        {active ? "Disconnect" : "Connect"}
                      </Button>
                    </li>
                  );
                })}
              </ul>
            )}
          </Card>

          {(repos.data ?? []).length > 0 && (
            <Card
              title="Repositories"
              bodyClass="p-0"
              actions={
                <Link to="../git" className="text-[11px] text-accent hover:underline">
                  Open
                </Link>
              }
            >
              <ul>
                {repos.data!.map((r) => {
                  const st = r.status;
                  return (
                    <li key={r.name}>
                      <Link to={`../git/${encodeURIComponent(r.name)}`} className="flex items-center gap-2.5 px-3 min-h-11 py-1.5 border-b border-line hover:bg-panel-2">
                        <GitBranch className="size-3.5 text-faint shrink-0" />
                        <div className="min-w-0 flex-1">
                          <div className="text-[13px] font-medium truncate">{r.name}</div>
                          <div className="text-[11px] text-faint truncate">
                            {!st?.cloned ? (
                              "not cloned"
                            ) : (
                              <>
                                <span className="font-mono">{st.branch || "detached"}</span>
                                {st.changes.length > 0 && <span className="text-warn"> · {st.changes.length} uncommitted</span>}
                                {r.last && ` · ${r.last.subject}`}
                              </>
                            )}
                          </div>
                        </div>
                        {st?.cloned && st.ahead > 0 && (
                          <span className="inline-flex items-center text-[11px] text-accent font-mono">
                            <ArrowUp className="size-3" />
                            {st.ahead}
                          </span>
                        )}
                        {st?.cloned && st.behind > 0 && (
                          <span className="inline-flex items-center text-[11px] text-warn font-mono">
                            <ArrowDown className="size-3" />
                            {st.behind}
                          </span>
                        )}
                      </Link>
                    </li>
                  );
                })}
              </ul>
            </Card>
          )}

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
          className="xl:col-span-3 min-w-0"
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
                  <Badge tone={a.actor.startsWith("agent") ? "agent" : "neutral"}>{a.actor}</Badge>
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
    </div>
  );
}

const SUGGESTIONS = ["Check disk usage on prod", "Why is the shop slow?", "Summarise the last deploy"];

// AskAgentCard starts an agent session from the overview with a draft.
function AskAgentCard({ ws }: { ws: string }) {
  const navigate = useNavigate();
  const [text, setText] = useState("");
  const go = (t: string) => navigate(`/w/${encodeURIComponent(ws)}/agent?new=1${t.trim() ? `&draft=${encodeURIComponent(t.trim())}` : ""}`);
  return (
    <section className="rounded-xl border border-agent/30 bg-agent/[0.06] p-4 space-y-3">
      <h3 className="text-[13.5px] font-semibold flex items-center gap-2">
        <span className="grid size-6 place-items-center rounded-md bg-agent text-agent-fg">
          <Bot className="size-3.5" />
        </span>
        Ask the agent
      </h3>
      <Textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            go(text);
          }
        }}
        aria-label="Question for the agent"
        placeholder="What should we look at?"
        className="min-h-16 bg-panel resize-none focus:border-agent focus:ring-agent/20"
      />
      <div className="flex flex-wrap gap-1.5">
        {SUGGESTIONS.map((s) => (
          <button key={s} onClick={() => go(s)} className="h-7 px-2.5 rounded-full border border-line bg-panel text-[12px] text-muted hover:text-fg hover:border-agent/50">
            {s}
          </button>
        ))}
      </div>
      <div className="flex items-center justify-between gap-2">
        <span className="text-[11.5px] text-faint">It asks before it changes anything on a protected environment.</span>
        <Button size="sm" variant="agent" onClick={() => go(text)}>
          Send
        </Button>
      </div>
    </section>
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
