import { useEffect, useMemo, useState } from "react";
import { Boxes, FileCode2, RefreshCw, RotateCw, Scale, ScrollText, Trash2 } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { KubeRow } from "../api/types";
import { useWs } from "../context/app";
import {
  Badge,
  Button,
  CodeBlock,
  EmptyState,
  ErrorBox,
  FormField,
  IconButton,
  Input,
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
import { useAsync, useInterval, useLocalStorage } from "../hooks";
import { str } from "../lib/format";

const TYPES = ["pods", "deployments", "statefulsets", "daemonsets", "services", "ingresses", "jobs", "cronjobs", "configmaps", "events", "nodes"] as const;
type KType = (typeof TYPES)[number];
const CLUSTER_SCOPED: KType[] = ["nodes"];
const PREFERRED = ["name", "namespace", "status", "ready", "replicas", "restarts", "age", "type", "clusterIP", "ports", "node", "image", "reason", "message"];

export function KubernetesPage() {
  const { name: ws, byKind } = useWs();
  const clusters = byKind("KubernetesCluster");
  const [cluster, setCluster] = useLocalStorage<string>(`opsdeck.k8s.${ws}`, "");
  const c = clusters.find((x) => x.name === cluster)?.name ?? clusters[0]?.name ?? "";
  const [ns, setNs] = useState<string>("");
  const [type, setType] = useState<KType>("pods");
  const [q, setQ] = useState("");
  const [auto, setAuto] = useState(false);
  const toast = useToast();
  const confirm = useConfirm();
  const nss = useAsync(() => (c ? api.namespaces(ws, c) : Promise.resolve([])), [ws, c]);
  const rows = useAsync<KubeRow[]>(() => (c ? api.kubeList(ws, c, type, CLUSTER_SCOPED.includes(type) ? "" : ns) : Promise.resolve([])), [ws, c, type, ns]);
  useInterval(() => void rows.reload(), auto ? 5000 : null);
  const [yaml, setYaml] = useState<{ title: string; yaml: string } | null>(null);
  const [logs, setLogs] = useState<KubeRow | null>(null);
  const [scale, setScale] = useState<KubeRow | null>(null);

  useEffect(() => {
    if (!ns) {
      const def = String(clusters.find((x) => x.name === c)?.spec?.namespace ?? "");
      if (def) setNs(def);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [c]);

  const filtered = useMemo(() => {
    const t = q.toLowerCase();
    return (rows.data ?? []).filter((r) => !t || JSON.stringify(r).toLowerCase().includes(t));
  }, [rows.data, q]);

  const columns = useMemo(() => {
    const keys = new Set<string>();
    filtered.forEach((r) => Object.keys(r).forEach((k) => keys.add(k)));
    if (ns) keys.delete("namespace");
    const ordered = PREFERRED.filter((k) => keys.has(k));
    keys.forEach((k) => !ordered.includes(k) && ordered.push(k));
    return ordered;
  }, [filtered, ns]);

  async function showYaml(r: KubeRow) {
    try {
      const y = await api.kubeYaml(ws, c, type, r.namespace ?? "", r.name);
      setYaml({ title: `${type}/${r.name}`, yaml: y.yaml });
    } catch (e) {
      toast.error("Cannot load YAML", errMsg(e));
    }
  }
  async function restart(r: KubeRow) {
    if (!(await confirm({ title: `Rollout restart ${r.name}?`, message: `Namespace ${r.namespace} on cluster ${c}.`, confirmLabel: "Restart" }))) return;
    try {
      await api.restartDeployment(ws, c, String(r.namespace), r.name);
      toast.success("Rollout restart triggered", r.name);
      void rows.reload();
    } catch (e) {
      toast.error("Restart failed", errMsg(e));
    }
  }
  async function deletePod(r: KubeRow) {
    if (!(await confirm({ title: `Delete pod ${r.name}?`, message: "Its controller will usually recreate it.", danger: true, confirmLabel: "Delete pod" }))) return;
    try {
      await api.deletePod(ws, c, String(r.namespace), r.name);
      toast.success("Pod deleted", r.name);
      void rows.reload();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  if (!clusters.length) {
    return (
      <div className="flex flex-col h-full">
        <PageHeader icon={<Boxes />} title="Kubernetes" />
        <EmptyState icon={<Boxes />} title="No clusters in this workspace">
          Add a <span className="font-mono">KubernetesCluster</span> resource with a kubeconfig stored in the vault. Its API is reached through
          the workspace networks.
        </EmptyState>
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<Boxes />}
        title="Kubernetes"
        actions={
          <>
            <Select value={c} onChange={(e) => setCluster(e.target.value)} className="w-48">
              {clusters.map((x) => (
                <option key={x.name} value={x.name}>
                  {x.name}
                </option>
              ))}
            </Select>
            <Select value={ns} onChange={(e) => setNs(e.target.value)} className="w-48" disabled={CLUSTER_SCOPED.includes(type)}>
              <option value="">All namespaces</option>
              {(nss.data ?? []).map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </Select>
          </>
        }
      />
      <Tabs className="px-3" value={type} onChange={setType} tabs={TYPES.map((t) => ({ id: t, label: t }))} />
      <div className="flex items-center gap-3 px-5 py-2.5">
        <SearchInput value={q} onChange={setQ} placeholder={`Filter ${type}…`} className="w-64" />
        <Toggle checked={auto} onChange={setAuto} label="Auto-refresh" />
        <div className="flex-1" />
        <span className="text-xs text-faint">{filtered.length} items</span>
        <IconButton title="Refresh" onClick={() => void rows.reload()}>
          <RefreshCw className={rows.loading ? "size-4 animate-spin" : "size-4"} />
        </IconButton>
      </div>
      <div className="flex-1 min-h-0 px-5 pb-5">
        <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
          {(rows.error || nss.error) && <ErrorBox error={rows.error || nss.error || ""} onRetry={() => void rows.reload()} />}
          {!rows.data && rows.loading && <Loading label={`Loading ${type}…`} />}
          {rows.data && filtered.length === 0 && !rows.error && <EmptyState title={`No ${type}`} />}
          {filtered.length > 0 && (
            <Table className="flex-1">
              <thead>
                <tr>
                  {columns.map((k) => (
                    <Th key={k}>{k}</Th>
                  ))}
                  <Th className="w-[1%]" />
                </tr>
              </thead>
              <tbody>
                {filtered.map((r, i) => (
                  <Tr key={`${r.namespace ?? ""}/${r.name}/${i}`}>
                    {columns.map((k) => (
                      <Td key={k} mono={k !== "message" && k !== "status"} className={k === "message" ? "max-w-[480px] truncate" : "max-w-72 truncate"} title={str(r[k])}>
                        {k === "status" ? <Badge tone={statusTone(str(r[k]))}>{str(r[k]) || "—"}</Badge> : k === "name" ? <span className="font-medium font-sans">{str(r[k])}</span> : str(r[k])}
                      </Td>
                    ))}
                    <Td>
                      <div className="flex items-center gap-0.5 justify-end">
                        {type === "pods" && (
                          <IconButton title="Logs" onClick={() => setLogs(r)}>
                            <ScrollText className="size-4" />
                          </IconButton>
                        )}
                        {type !== "events" && (
                          <IconButton title="YAML" onClick={() => void showYaml(r)}>
                            <FileCode2 className="size-4" />
                          </IconButton>
                        )}
                        {type === "deployments" && (
                          <>
                            <IconButton title="Rollout restart" onClick={() => void restart(r)}>
                              <RotateCw className="size-4" />
                            </IconButton>
                            <IconButton title="Scale" onClick={() => setScale(r)}>
                              <Scale className="size-4" />
                            </IconButton>
                          </>
                        )}
                        {type === "pods" && (
                          <IconButton title="Delete pod" onClick={() => void deletePod(r)} className="hover:!text-err">
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
        </div>
      </div>
      <Modal open={!!yaml} onClose={() => setYaml(null)} title={yaml?.title ?? ""} width={900}>
        {yaml && <CodeBlock maxHeight={620}>{yaml.yaml}</CodeBlock>}
      </Modal>
      {logs && <PodLogs ws={ws} cluster={c} pod={logs} onClose={() => setLogs(null)} />}
      {scale && (
        <ScaleModal
          row={scale}
          onClose={() => setScale(null)}
          onSubmit={async (n) => {
            try {
              await api.scaleDeployment(ws, c, String(scale.namespace), scale.name, n);
              toast.success(`Scaled ${scale.name} to ${n}`);
              setScale(null);
              void rows.reload();
            } catch (e) {
              toast.error("Scale failed", errMsg(e));
            }
          }}
        />
      )}
    </div>
  );
}

function statusTone(s: string) {
  if (/^(Running|Ready|Active|Succeeded|Complete|True|Bound|Normal)$/i.test(s)) return "ok" as const;
  if (/Pending|ContainerCreating|Terminating|Progressing|Warning/i.test(s)) return "warn" as const;
  if (/Error|CrashLoop|Failed|BackOff|NotReady|Evicted|OOM/i.test(s)) return "err" as const;
  return "neutral" as const;
}

function PodLogs({ ws, cluster, pod, onClose }: { ws: string; cluster: string; pod: KubeRow; onClose: () => void }) {
  const containers = useMemo(() => {
    const v = pod.containers;
    if (Array.isArray(v)) return v.map(String);
    if (typeof v === "string" && v) return v.split(/[,\s]+/).filter(Boolean);
    return [];
  }, [pod]);
  const [container, setContainer] = useState(containers[0] ?? "");
  const [tail, setTail] = useState(500);
  const [auto, setAuto] = useState(false);
  const l = useAsync(() => api.podLogs(ws, cluster, String(pod.namespace), pod.name, container, tail), [container, tail]);
  useInterval(() => void l.reload(), auto ? 3000 : null);
  return (
    <Drawer open onClose={onClose} title={`Logs · ${pod.name}`} subtitle={`${pod.namespace} · ${cluster}`} width={1000}>
      <div className="flex items-end gap-3 mb-3">
        {containers.length > 0 && (
          <FormField label="Container" className="w-56">
            <Select value={container} onChange={(e) => setContainer(e.target.value)}>
              {containers.map((c) => (
                <option key={c}>{c}</option>
              ))}
            </Select>
          </FormField>
        )}
        <FormField label="Tail" className="w-28">
          <Input type="number" value={tail} onChange={(e) => setTail(Number(e.target.value) || 100)} />
        </FormField>
        <div className="h-8 flex items-center">
          <Toggle checked={auto} onChange={setAuto} label="Auto-refresh (3s)" />
        </div>
        <Button size="md" icon={<RefreshCw className="size-3.5" />} onClick={() => void l.reload()} loading={l.loading}>
          Refresh
        </Button>
      </div>
      {l.error && <ErrorBox error={l.error} />}
      <pre className="rounded-md border border-line bg-panel-2 p-3 font-mono text-[11.5px] leading-relaxed overflow-auto h-[calc(100vh-200px)] whitespace-pre-wrap break-all">
        {(l.data?.lines ?? []).join("\n") || (l.loading ? "Loading…" : "(no output)")}
      </pre>
    </Drawer>
  );
}

function ScaleModal({ row, onClose, onSubmit }: { row: KubeRow; onClose: () => void; onSubmit: (n: number) => Promise<void> }) {
  const cur = parseInt(String(row.replicas ?? row.ready ?? "1").split("/").pop() ?? "1", 10) || 0;
  const [n, setN] = useState(cur);
  const [busy, setBusy] = useState(false);
  return (
    <Modal
      open
      onClose={onClose}
      title={`Scale ${row.name}`}
      width={380}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={busy}
            onClick={async () => {
              setBusy(true);
              await onSubmit(n);
              setBusy(false);
            }}
          >
            Scale to {n}
          </Button>
        </>
      }
    >
      <FormField label="Replicas" help={`Currently ${str(row.replicas ?? row.ready)}`}>
        <Input type="number" min={0} value={n} autoFocus onChange={(e) => setN(Math.max(0, Number(e.target.value) || 0))} />
      </FormField>
      {n === 0 && <div className="mt-2 text-xs text-warn">Scaling to 0 stops all pods of this deployment.</div>}
    </Modal>
  );
}
