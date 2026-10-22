import { CircleAlert, CircleCheck, Server, TriangleAlert } from "lucide-react";
import { api } from "../../api/client";
import type { KubeNodeSummary } from "../../api/types";
import { Badge, Card, EmptyState, ErrorBox, Loading, cx } from "../../components/ui";
import { useAsync, useInterval } from "../../hooks";
import { timeAgo } from "../../lib/format";
import { UsageBar } from "../docker/shared";
import { fmtCPU, fmtMem, listNs, useKube } from "./shared";
import { EventTable } from "./ObjectDrawer";

// The cluster at a glance: nodes and their load, pod counts, what is broken
// right now and the latest warnings. Everything is clickable.
export function Overview() {
  const { ws, cluster, ns, open, goto } = useKube();
  const o = useAsync(() => api.kubeOverview(ws, cluster, listNs(ns)), [ws, cluster, ns]);
  useInterval(() => void o.reload(), 10000);
  if (o.error) return <ErrorBox error={o.error} onRetry={() => void o.reload()} />;
  if (!o.data) return <Loading label="Reading the cluster…" />;
  const d = o.data;
  const p = d.pods;
  const w = d.workloads;
  const errors = d.problems.filter((x) => x.severity === "error").length;
  return (
    <div className="h-full overflow-y-auto space-y-4 pr-1">
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <Stat label="Nodes" onClick={() => goto("nodes")} value={`${d.nodes.filter((n) => n.ready).length}/${d.nodes.length}`} sub="ready" tone={d.nodes.every((n) => n.ready) ? "ok" : "err"} />
        <Stat
          label="Pods"
          onClick={() => goto("pods")}
          value={`${p.running}/${p.total}`}
          sub={[p.pending && `${p.pending} pending`, p.failed && `${p.failed} failed`, p.succeeded && `${p.succeeded} completed`].filter(Boolean).join(" · ") || "running"}
          tone={p.failed ? "err" : p.pending ? "warn" : "ok"}
        />
        <Stat label="Workloads" onClick={() => goto("deployments.apps")} value={String(w.deployments + w.statefulsets + w.daemonsets)} sub={`${w.deployments} deploy · ${w.statefulsets} sts · ${w.daemonsets} ds · ${w.cronjobs} cron`} />
        <Stat label="Problems" value={String(d.problems.length)} sub={d.problems.length ? `${errors} errors · ${d.problems.length - errors} warnings` : "all good"} tone={errors ? "err" : d.problems.length ? "warn" : "ok"} />
      </div>

      <Card title={<>Problems {d.namespace && <span className="text-faint font-normal">in {d.namespace}</span>}</>} bodyClass="p-0">
        {d.problems.length === 0 ? (
          <div className="flex items-center gap-2 px-3 py-3 text-[13px] text-ok">
            <CircleCheck className="size-4" /> Nothing is crashing, pending or unavailable.
          </div>
        ) : (
          <div className="divide-y divide-line">
            {d.problems.map((x, i) => (
              <button key={i} className="w-full flex items-start gap-2.5 px-3 py-2 text-left hover:bg-panel-2" onClick={() => open({ type: x.kind, ns: x.namespace ?? "", name: x.name })}>
                {x.severity === "error" ? <CircleAlert className="size-4 text-err shrink-0 mt-0.5" /> : <TriangleAlert className="size-4 text-warn shrink-0 mt-0.5" />}
                <div className="min-w-0">
                  <div className="text-[13px]">
                    <span className="text-faint">{x.kind}</span> <span className="font-medium">{x.namespace ? `${x.namespace}/` : ""}{x.name}</span>{" "}
                    <Badge tone={x.severity === "error" ? "err" : "warn"}>{x.reason}</Badge>
                  </div>
                  {x.message && <div className="text-[11.5px] text-muted truncate">{x.message}</div>}
                </div>
              </button>
            ))}
          </div>
        )}
      </Card>

      <Card title="Nodes" bodyClass="p-0" actions={!d.metrics && <span className="text-[11px] text-faint">no metrics-server: usage unavailable</span>}>
        <div className="divide-y divide-line">
          {d.nodes.map((n) => (
            <NodeRow key={n.name} n={n} onOpen={() => open({ type: "nodes", ns: "", name: n.name })} />
          ))}
        </div>
      </Card>

      <Card title="Recent warnings" bodyClass="p-0 h-80" actions={<button className="text-[11.5px] text-accent hover:underline" onClick={() => goto("events")}>all events</button>}>
        {d.warnings.length === 0 ? (
          <EmptyState title="No warnings in the last hour" />
        ) : (
          <EventTable events={d.warnings} onOpen={(namespace, object) => {
            const [kind, name] = object.split("/");
            open({ type: kind, ns: namespace, name });
          }} />
        )}
      </Card>
      <div className="text-[11px] text-faint">
        Kubernetes {d.version} · {d.platform} · {d.namespaces} namespaces · refreshes every 10 s
      </div>
    </div>
  );
}

function Stat({ label, value, sub, tone, onClick }: { label: string; value: string; sub: string; tone?: "ok" | "warn" | "err"; onClick?: () => void }) {
  return (
    <button disabled={!onClick} onClick={onClick} className={cx("rounded-lg border border-line bg-panel px-4 py-3 text-left", onClick && "hover:border-accent/50")}>
      <div className="text-[11.5px] text-muted">{label}</div>
      <div className={cx("text-2xl font-semibold tabular-nums", tone === "err" && "text-err", tone === "warn" && "text-warn")}>{value}</div>
      <div className="text-[11.5px] text-faint truncate">{sub}</div>
    </button>
  );
}

function NodeRow({ n, onOpen }: { n: KubeNodeSummary; onOpen: () => void }) {
  const cpu = n.capacity.cpu ? (100 * (n.used?.cpu ?? 0)) / n.capacity.cpu : 0;
  const mem = n.capacity.memory ? (100 * (n.used?.memory ?? 0)) / n.capacity.memory : 0;
  const rcpu = n.capacity.cpu ? (100 * n.requested.cpu) / n.capacity.cpu : 0;
  const rmem = n.capacity.memory ? (100 * n.requested.memory) / n.capacity.memory : 0;
  return (
    <button onClick={onOpen} className="w-full grid grid-cols-[minmax(12rem,1.2fr)_1fr_1fr_8rem] gap-4 items-center px-3 py-2.5 text-left hover:bg-panel-2">
      <div className="min-w-0">
        <div className="flex items-center gap-1.5 text-[13px] font-medium">
          <Server className="size-3.5 text-faint" />
          {n.name}
          {n.ready ? <Badge tone="ok">Ready</Badge> : <Badge tone="err">NotReady</Badge>}
          {n.unschedulable && <Badge tone="warn">cordoned</Badge>}
          {(n.pressure ?? []).map((x) => (
            <Badge key={x} tone="err">
              {x}
            </Badge>
          ))}
        </div>
        <div className="text-[11px] text-faint truncate">
          {(n.roles.length ? n.roles : ["worker"]).join(", ")} · {n.version} · {n.internalIP} · up {timeAgo(n.created).replace(" ago", "")}
        </div>
      </div>
      <div>
        <div className="text-[11px] text-muted mb-0.5">
          CPU {n.used ? fmtCPU(n.used.cpu) : "—"} / {fmtCPU(n.capacity.cpu)} <span className="text-faint">· requested {rcpu.toFixed(0)}%</span>
        </div>
        <UsageBar v={cpu} label={n.used ? `${cpu.toFixed(0)}%` : "—"} width="w-full" />
      </div>
      <div>
        <div className="text-[11px] text-muted mb-0.5">
          Memory {n.used ? fmtMem(n.used.memory) : "—"} / {fmtMem(n.capacity.memory)} <span className="text-faint">· requested {rmem.toFixed(0)}%</span>
        </div>
        <UsageBar v={mem} label={n.used ? `${mem.toFixed(0)}%` : "—"} width="w-full" />
      </div>
      <div className="text-[11.5px] text-muted">
        <div>
          Pods {n.pods}/{n.maxPods}
        </div>
        <div className="h-1 rounded-full bg-panel-3 overflow-hidden mt-1">
          <div className="h-full bg-accent" style={{ width: `${Math.min(100, (100 * n.pods) / (n.maxPods || 1))}%` }} />
        </div>
      </div>
    </button>
  );
}
