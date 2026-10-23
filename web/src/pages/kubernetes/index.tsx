import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { Activity, Boxes, ChevronDown, ChevronRight, LayoutDashboard, Package, RefreshCw, Upload } from "lucide-react";
import { api } from "../../api/client";
import type { KubeResourceType } from "../../api/types";
import { useWs } from "../../context/app";
import { useTabTitle } from "../../context/tabs";
import { Badge, Button, EmptyState, ErrorBox, IconButton, Loading, PageHeader, SearchInput, Select, cx } from "../../components/ui";
import { useAsync, useLocalStorage } from "../../hooks";
import { OutputProvider } from "../docker/shared";
import { KubeProvider } from "./shared";
import type { KubeCtx, ObjRef, ObjTab } from "./shared";
import { ResourceTable } from "./ResourceTable";
import { ObjectDrawer } from "./ObjectDrawer";
import { Overview } from "./Overview";
import { Events } from "./Events";
import { Helm } from "./Helm";
import { ApplyModal } from "./Apply";

const VIEWS: Record<string, { label: string; icon: ReactNode }> = {
  overview: { label: "Overview", icon: <LayoutDashboard /> },
  events: { label: "Events", icon: <Activity /> },
  helm: { label: "Helm releases", icon: <Package /> },
};
// The live Events view replaces the plain event lists.
const HIDDEN = new Set(["events", "events.events.k8s.io"]);
const COLLAPSED = new Set(["Other"]);

// Kubernetes clusters of the workspace: an overview, every resource type the
// API server offers (custom resources included) as live tables, an object
// drawer with logs, terminal, YAML editing, rollout history and actions,
// live events, Helm releases and apply with a server-side dry run. What is
// open lives in the URL, so each view can be its own workspace tab.
export function KubernetesPage() {
  const { name: ws, byKind } = useWs();
  const clusters = byKind("KubernetesCluster");
  const [params, setParams] = useSearchParams();
  const [saved, setSaved] = useLocalStorage<string>(`opsdeck.k8s.${ws}`, "");
  const cluster = params.get("cluster") || (clusters.some((c) => c.name === saved) ? saved : clusters[0]?.name) || "";
  const [savedNs, setSavedNs] = useLocalStorage<Record<string, string>>(`opsdeck.k8s.ns.${ws}`, {});
  const view = params.get("view") || "overview";
  const set = useCallback(
    (patch: Record<string, string | null>) => {
      setParams(
        (p) => {
          const n = new URLSearchParams(p);
          for (const [k, v] of Object.entries(patch)) {
            if (v === null || v === "") n.delete(k);
            else n.set(k, v);
          }
          return n;
        },
        { replace: true },
      );
    },
    [setParams],
  );
  useEffect(() => {
    if (cluster) setSaved(cluster);
  }, [cluster, setSaved]);

  const nss = useAsync(() => (cluster ? api.kubeNamespaces(ws, cluster) : Promise.resolve(undefined)), [ws, cluster]);
  const refresh = useRef(false);
  const types = useAsync(() => {
    const r = refresh.current;
    refresh.current = false;
    return cluster ? api.kubeResources(ws, cluster, r) : Promise.resolve([] as KubeResourceType[]);
  }, [ws, cluster]);
  const defaultNs = nss.data?.default || "default";
  const ns = params.get("ns") || savedNs[cluster] || "*";
  const setNs = (v: string) => {
    setSavedNs({ ...savedNs, [cluster]: v });
    set({ ns: v });
  };

  const objParam = params.get("obj");
  const obj: ObjRef | null = useMemo(() => {
    if (!objParam) return null;
    const [type, ons, ...rest] = objParam.split("|");
    return { type, ns: ons, name: rest.join("|") };
  }, [objParam]);
  const objTab = (params.get("otab") as ObjTab) || "overview";
  const [apply, setApply] = useState<{ manifest?: string; ns: string } | null>(null);

  const rt = useMemo(() => (VIEWS[view] ? undefined : (types.data ?? []).find((t) => t.id === view)), [types.data, view]);
  useTabTitle(
    obj
      ? `${obj.name} · k8s`
      : !cluster
        ? "Kubernetes"
        : rt
          ? `${plural(rt)} · ${cluster}`
          : VIEWS[view] && view !== "overview"
            ? `${VIEWS[view].label} · ${cluster}`
            : `Kubernetes · ${cluster}`,
  );

  const ctx: KubeCtx = useMemo(
    () => ({
      ws,
      cluster,
      ns,
      defaultNs,
      open: (o, tab) => set({ obj: `${o.type}|${o.ns}|${o.name}`, otab: tab && tab !== "overview" ? tab : null }),
      goto: (v, extra = {}) => set({ view: v === "overview" ? null : v, obj: null, otab: null, ...extra }),
      apply: (manifest, n) => setApply({ manifest, ns: n ?? (ns === "*" ? defaultNs : ns) }),
    }),
    [ws, cluster, ns, defaultNs, set],
  );

  if (!clusters.length) {
    return (
      <div className="flex flex-col h-full">
        <PageHeader icon={<Boxes />} title="Kubernetes" />
        <EmptyState icon={<Boxes />} title="No clusters in this workspace">
          Add a <span className="font-mono">KubernetesCluster</span> resource with a kubeconfig stored in the vault. Its API is reached through the workspace networks (or a bastion with{" "}
          <span className="font-mono">via</span>).
        </EmptyState>
      </div>
    );
  }

  return (
    <KubeProvider value={ctx}>
      <OutputProvider>
        <div className="flex flex-col h-full min-h-0">
          <PageHeader
            icon={<Boxes />}
            title="Kubernetes"
            subtitle={
              nss.error ? (
                <span className="text-err">{nss.error}</span>
              ) : (
                <span className="inline-flex items-center gap-1.5">
                  Workloads, networking, config and custom resources — live.
                  {types.data && <Badge>{types.data.length} resource types</Badge>}
                </span>
              )
            }
            actions={
              <>
                <Button size="sm" icon={<Upload className="size-3.5" />} onClick={() => ctx.apply()}>
                  Apply YAML
                </Button>
                <Select value={ns} onChange={(e) => setNs(e.target.value)} className="w-52" title="Namespace">
                  <option value="*">All namespaces</option>
                  {(nss.data?.namespaces ?? []).map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                  {ns !== "*" && nss.data && !nss.data.namespaces.includes(ns) && <option value={ns}>{ns}</option>}
                </Select>
                <Select value={cluster} onChange={(e) => set({ cluster: e.target.value, ns: null, obj: null, otab: null })} className="w-48" title="Cluster">
                  {clusters.map((c) => (
                    <option key={c.name} value={c.name}>
                      {c.name}
                    </option>
                  ))}
                </Select>
              </>
            }
          />
          <div className="flex flex-1 min-h-0">
            <Nav types={types.data} loading={types.loading} error={types.error} view={view} onReload={() => ((refresh.current = true), void types.reload())} onPick={(v) => ctx.goto(v)} />
            <main className="flex-1 min-w-0 p-5">
              {view === "overview" ? (
                <Overview />
              ) : view === "events" ? (
                <Events />
              ) : view === "helm" ? (
                <Helm />
              ) : rt ? (
                <ResourceTable key={rt.id} rt={rt} />
              ) : types.loading ? (
                <Loading />
              ) : (
                <EmptyState title="Unknown resource type">{view} is not served by this cluster.</EmptyState>
              )}
            </main>
          </div>
        </div>
        {obj && <ObjectDrawer key={objParam!} o={obj} tab={objTab} onTab={(t) => set({ otab: t === "overview" ? null : t })} onClose={() => set({ obj: null, otab: null })} />}
        {apply && <ApplyModal initial={apply.manifest} namespace={apply.ns} onClose={() => setApply(null)} />}
      </OutputProvider>
    </KubeProvider>
  );
}

function Nav({
  types,
  loading,
  error,
  view,
  onPick,
  onReload,
}: {
  types?: KubeResourceType[];
  loading: boolean;
  error?: string | null;
  view: string;
  onPick: (v: string) => void;
  onReload: () => void;
}) {
  const [q, setQ] = useState("");
  const [open, setOpen] = useLocalStorage<Record<string, boolean>>("opsdeck.k8s.nav", {});
  const groups = useMemo(() => {
    const ql = q.toLowerCase();
    const m = new Map<string, KubeResourceType[]>();
    for (const t of types ?? []) {
      if (HIDDEN.has(t.id)) continue;
      if (ql && !`${t.kind} ${t.resource} ${t.group} ${(t.shortNames ?? []).join(" ")}`.toLowerCase().includes(ql)) continue;
      if (!m.has(t.category)) m.set(t.category, []);
      m.get(t.category)!.push(t);
    }
    return [...m.entries()];
  }, [types, q]);
  const item = (id: string, label: ReactNode, icon?: ReactNode, title?: string) => (
    <button
      key={id}
      title={title}
      onClick={() => onPick(id)}
      className={cx(
        "w-full flex items-center gap-2 px-2.5 h-7 rounded-md text-[12.5px] text-left truncate [&_svg]:size-3.5 [&_svg]:shrink-0",
        view === id ? "bg-accent/15 text-fg font-medium" : "text-muted hover:bg-panel-2 hover:text-fg",
      )}
    >
      {icon}
      <span className="truncate">{label}</span>
    </button>
  );
  return (
    <aside className="w-56 shrink-0 border-r border-line flex flex-col min-h-0">
      <div className="p-2 space-y-0.5 border-b border-line">{Object.entries(VIEWS).map(([id, v]) => item(id, v.label, v.icon))}</div>
      <div className="p-2 flex items-center gap-1">
        <SearchInput value={q} onChange={setQ} placeholder="Resource types…" className="flex-1" />
        <IconButton title="Re-read the API's resource types (after installing CRDs)" onClick={onReload}>
          <RefreshCw className={cx("size-3.5", loading && "animate-spin")} />
        </IconButton>
      </div>
      <div className="flex-1 overflow-y-auto px-2 pb-3">
        {error && <ErrorBox error={error} />}
        {groups.map(([cat, ts]) => {
          const shown = q ? true : (open[cat] ?? !COLLAPSED.has(cat));
          return (
            <div key={cat} className="mt-2">
              <button className="w-full flex items-center gap-1 px-1 text-[10.5px] uppercase tracking-wide text-faint hover:text-muted" onClick={() => setOpen({ ...open, [cat]: !shown })}>
                {shown ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
                {cat}
                <span className="ml-auto normal-case tracking-normal">{ts.length}</span>
              </button>
              {shown && (
                <div className="mt-0.5 space-y-px">
                  {ts.map((t) => item(t.id, cat === "Custom resources" || cat === "Other" ? t.kind : plural(t), undefined, `${t.kind} · ${t.group || "core"}/${t.version}${t.shortNames?.length ? ` · ${t.shortNames.join(", ")}` : ""}`))}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </aside>
  );
}

const LABELS: Record<string, string> = {
  endpoints: "Endpoints",
  "horizontalpodautoscalers.autoscaling": "HPAs",
  "poddisruptionbudgets.policy": "Pod disruption budgets",
  "persistentvolumeclaims": "Persistent volume claims",
  "persistentvolumes": "Persistent volumes",
  "customresourcedefinitions.apiextensions.k8s.io": "CRDs",
  "mutatingwebhookconfigurations.admissionregistration.k8s.io": "Mutating webhooks",
  "validatingwebhookconfigurations.admissionregistration.k8s.io": "Validating webhooks",
};
function plural(t: KubeResourceType): string {
  if (LABELS[t.id]) return LABELS[t.id];
  const words = t.kind.replace(/([a-z])([A-Z])/g, "$1 $2");
  const p = /(s|x|ch|sh)$/.test(words) ? `${words}es` : /[^aeiou]y$/.test(words) ? `${words.slice(0, -1)}ies` : `${words}s`;
  return p.charAt(0) + p.slice(1).toLowerCase().replace(/\bip\b/g, "IP").replace(/\bcsi\b/g, "CSI");
}
