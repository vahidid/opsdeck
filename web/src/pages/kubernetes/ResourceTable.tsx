import { useMemo, useState } from "react";
import { Boxes, MoreHorizontal, Play, RefreshCw, RotateCw, ScrollText, SquareTerminal, Trash2 } from "lucide-react";
import { api } from "../../api/client";
import type { KubeResourceType, KubeTableRow, KubeUsage } from "../../api/types";
import { Badge, EmptyState, ErrorBox, IconButton, Input, Loading, SearchInput, Table, Td, Th, Toggle, Tr, cx } from "../../components/ui";
import { MenuItem, Popover, useConfirm } from "../../components/overlay";
import { useAsync, useInterval, useLocalStorage } from "../../hooks";
import { UsageBar, useRun } from "../docker/shared";
import { HAS_LOGS, RESTARTABLE, cellText, fmtCPU, fmtMem, kTone, listNs, useKube } from "./shared";

const STATUS_COL = /^(status|ready|phase|state|available|completions|conditions)$/i;

export function ResourceTable({ rt }: { rt: KubeResourceType }) {
  const { ws, cluster, ns, open } = useKube();
  const [q, setQ] = useState("");
  const [sel, setSel] = useState("");
  const [applied, setApplied] = useState("");
  const [wide, setWide] = useLocalStorage("opsdeck.k8s.wide", false);
  const [live, setLive] = useLocalStorage("opsdeck.k8s.live", true);
  const namespace = rt.namespaced ? listNs(ns) : "";
  const t = useAsync(() => api.kubeList(ws, cluster, rt.id, { namespace, labelSelector: applied || undefined }), [ws, cluster, rt.id, namespace, applied]);
  const withMetrics = rt.id === "pods" || rt.id === "nodes";
  const m = useAsync(
    () =>
      withMetrics && live
        ? (rt.id === "pods" ? api.kubePodMetrics(ws, cluster, namespace) : api.kubeNodeMetrics(ws, cluster)).catch(() => ({}) as Record<string, KubeUsage>)
        : Promise.resolve({} as Record<string, KubeUsage>),
    [ws, cluster, rt.id, namespace, live],
  );
  useInterval(() => {
    void t.reload();
    if (withMetrics) void m.reload();
  }, live ? 5000 : null);
  const { busy, run } = useRun();
  const confirm = useConfirm();

  const cols = useMemo(() => (t.data?.columns ?? []).map((c, i) => ({ ...c, i })).filter((c) => wide || c.priority === 0), [t.data, wide]);
  const rows = useMemo(() => {
    const ql = q.toLowerCase();
    return (t.data?.rows ?? []).filter((r) => !ql || `${r.namespace ?? ""}/${r.name} ${r.cells.map(cellText).join(" ")}`.toLowerCase().includes(ql));
  }, [t.data, q]);
  const showNs = rt.namespaced && ns === "*";
  const metrics = m.data ?? {};
  const hasMetrics = withMetrics && live && Object.keys(metrics).length > 0;

  async function act(r: KubeTableRow, action: string) {
    const label = `${action} ${r.name}`;
    if (action === "delete") {
      const ok = await confirm({
        title: `Delete ${rt.kind} ${r.name}?`,
        message: rt.id === "pods" ? "Its controller usually recreates it." : rt.id === "namespaces" ? "Everything in the namespace is deleted." : "Dependent objects are deleted in the background.",
        danger: true,
        confirmLabel: "Delete",
        typeToConfirm: rt.id === "pods" ? undefined : r.name,
      });
      if (!ok) return;
      await run(`${r.uid}:delete`, label, () => api.kubeDelete(ws, cluster, rt.id, r.namespace ?? "", r.name), () => t.reload());
      return;
    }
    if (action === "restart" && !(await confirm({ title: `Restart ${r.name}?`, message: "Its pods are replaced one by one (rollout restart).", confirmLabel: "Restart" }))) return;
    await run(`${r.uid}:${action}`, label, () => api.kubeAction(ws, cluster, rt.id, r.namespace ?? "", r.name, action), () => t.reload());
  }

  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-3 flex-wrap">
        <SearchInput value={q} onChange={setQ} placeholder={`Filter ${rt.resource}…`} className="w-60" />
        <form
          className="flex items-center gap-1"
          onSubmit={(e) => {
            e.preventDefault();
            setApplied(sel.trim());
          }}
        >
          <Input mono className="w-56 !h-8" placeholder="label selector: app=web" value={sel} onChange={(e) => setSel(e.target.value)} onBlur={() => setApplied(sel.trim())} />
        </form>
        <Toggle checked={wide} onChange={setWide} label="Wide" />
        <Toggle checked={live} onChange={setLive} label={withMetrics ? "Live + usage" : "Live"} />
        <div className="flex-1" />
        <span className="text-xs text-faint">
          {rows.length}
          {rows.length !== (t.data?.rows.length ?? 0) && ` of ${t.data?.rows.length}`} {rt.resource}
        </span>
        <IconButton title="Refresh" onClick={() => void t.reload()}>
          <RefreshCw className={cx("size-4", t.loading && "animate-spin")} />
        </IconButton>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {t.error && <ErrorBox error={t.error} onRetry={() => void t.reload()} />}
        {!t.data && t.loading && <Loading label={`Listing ${rt.resource}…`} />}
        {t.data && rows.length === 0 && (
          <EmptyState icon={<Boxes />} title={`No ${rt.resource}`}>
            {rt.namespaced && ns !== "*" ? `in namespace ${ns}` : ""}
          </EmptyState>
        )}
        {rows.length > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                {showNs && <Th>Namespace</Th>}
                {cols.map((c) => (
                  <Th key={c.i}>
                    <span title={c.description}>{c.name}</span>
                  </Th>
                ))}
                {hasMetrics && <Th>CPU</Th>}
                {hasMetrics && <Th>Memory</Th>}
                <Th className="w-[1%]" />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const key = rt.id === "nodes" ? r.name : `${r.namespace}/${r.name}`;
                const u = metrics[key];
                return (
                  <Tr key={r.uid || key} onClick={() => open({ type: rt.id, ns: r.namespace ?? "", name: r.name })} className={cx(r.deleting && "opacity-60")}>
                    {showNs && <Td className="text-muted">{r.namespace}</Td>}
                    {cols.map((c, j) => {
                      const v = cellText(r.cells[c.i]);
                      if (j === 0)
                        return (
                          <Td key={c.i}>
                            <div className="font-medium">{v}</div>
                            {r.owner && <div className="text-[10.5px] text-faint">{r.owner}</div>}
                          </Td>
                        );
                      if (STATUS_COL.test(c.name) && v)
                        return (
                          <Td key={c.i}>
                            <Badge tone={r.deleting ? "warn" : kTone(v)}>{r.deleting && c.name === "Status" ? "Terminating" : v}</Badge>
                          </Td>
                        );
                      return (
                        <Td key={c.i} mono={c.type !== "string" || /ip|port|image|selector/i.test(c.name)} className={cx("max-w-80 truncate", c.name === "Age" && "text-muted")} title={v}>
                          {c.name === "Restarts" && v !== "0" && v ? <span className="text-warn">{v}</span> : v}
                        </Td>
                      );
                    })}
                    {hasMetrics && <Td mono>{u ? fmtCPU(u.cpu) : "—"}</Td>}
                    {hasMetrics && <Td mono>{u ? fmtMem(u.memory) : "—"}</Td>}
                    <Td>
                      <div className="flex items-center gap-0.5 justify-end" onClick={(e) => e.stopPropagation()}>
                        {HAS_LOGS.has(rt.id) && (
                          <IconButton title="Logs" onClick={() => open({ type: rt.id, ns: r.namespace ?? "", name: r.name }, "logs")}>
                            <ScrollText className="size-4" />
                          </IconButton>
                        )}
                        {rt.id === "pods" && (
                          <IconButton title="Terminal (exec)" onClick={() => open({ type: rt.id, ns: r.namespace ?? "", name: r.name }, "terminal")}>
                            <SquareTerminal className="size-4" />
                          </IconButton>
                        )}
                        {RESTARTABLE.has(rt.id) && (
                          <IconButton title="Rollout restart" disabled={!!busy} onClick={() => void act(r, "restart")}>
                            <RotateCw className={cx("size-4", busy === `${r.uid}:restart` && "animate-spin")} />
                          </IconButton>
                        )}
                        {rt.id === "cronjobs.batch" && (
                          <IconButton title="Run now" disabled={!!busy} onClick={() => void act(r, "trigger")}>
                            <Play className="size-4" />
                          </IconButton>
                        )}
                        <RowMenu rt={rt} onAct={(a) => void act(r, a)} />
                      </div>
                    </Td>
                  </Tr>
                );
              })}
            </tbody>
          </Table>
        )}
      </div>
    </div>
  );
}

function RowMenu({ rt, onAct }: { rt: KubeResourceType; onAct: (a: string) => void }) {
  const [open, setOpen] = useState(false);
  const pick = (a: string) => {
    setOpen(false);
    onAct(a);
  };
  const items: [string, string, boolean?][] = [];
  if (rt.id === "nodes") items.push(["cordon", "Cordon"], ["uncordon", "Uncordon"]);
  if (rt.id === "deployments.apps") items.push(["pause", "Pause rollout"], ["resume", "Resume rollout"]);
  if (rt.id === "cronjobs.batch") items.push(["suspend", "Suspend"], ["unsuspend", "Resume schedule"]);
  if (rt.verbs.includes("delete")) items.push(["delete", "Delete", true]);
  if (!items.length) return null;
  return (
    <div className="relative">
      <IconButton title="More" onClick={() => setOpen((o) => !o)}>
        <MoreHorizontal className="size-4" />
      </IconButton>
      <Popover open={open} onClose={() => setOpen(false)} className="right-0 mt-1 w-44">
        {items.map(([a, label, danger]) => (
          <MenuItem key={a} danger={danger} icon={danger ? <Trash2 /> : undefined} onClick={() => pick(a)}>
            {label}
          </MenuItem>
        ))}
      </Popover>
    </div>
  );
}

export function UsageCell({ used, cap }: { used?: number; cap?: number }) {
  if (used === undefined || !cap) return <span className="text-faint">—</span>;
  const p = (100 * used) / cap;
  return <UsageBar v={p} label={`${p.toFixed(0)}%`} width="w-32" />;
}
