import { useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { Eye, FileCode2, History, Info, Layers, ListTree, Pause, Play, Plug, RotateCcw, RotateCw, Scale, ScrollText, ShieldOff, SquareTerminal, Tag, Trash2, Unplug } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { Forward, KubeApplyResult, KubeObject, KubeUsage } from "../../api/types";
import { Badge, Button, CodeBlock, EmptyState, ErrorBox, FormField, Input, Loading, Select, Table, Tabs, Td, Textarea, Th, Tr, cx } from "../../components/ui";
import { Drawer, Modal, useConfirm } from "../../components/overlay";
import { ExecTerminal } from "../../components/ExecTerminal";
import { useAsync, useInterval } from "../../hooks";
import { timeAgo } from "../../lib/format";
import { KV, Section, UsageBar, useRun } from "../docker/shared";
import { HAS_HISTORY, HAS_LOGS, HAS_PODS, KubeLogs, RESTARTABLE, SCALABLE, cellText, fmtCPU, fmtMem, kTone, useKube } from "./shared";
import type { ObjRef, ObjTab } from "./shared";
import { ApplyResults } from "./Apply";

/* eslint-disable @typescript-eslint/no-explicit-any */

export function ObjectDrawer({ o, tab, onTab, onClose }: { o: ObjRef; tab: ObjTab; onTab: (t: ObjTab) => void; onClose: () => void }) {
  const { ws, cluster } = useKube();
  const r = useAsync(() => api.kubeObject(ws, cluster, o.type, o.ns, o.name), [ws, cluster, o.type, o.ns, o.name]);
  useInterval(() => void r.reload(), tab === "overview" || tab === "pods" ? 5000 : null);
  const obj = r.data?.object;
  const rt = r.data?.type;
  const id = rt?.id ?? o.type;
  const kind = obj?.kind ?? rt?.kind ?? o.type;
  const containers = useMemo(() => containerNames(obj), [obj]);
  const tabs: { id: ObjTab; label: ReactNode }[] = [{ id: "overview", label: <L i={<Info />}>Overview</L> }];
  if (id === "configmaps" || id === "secrets") tabs.push({ id: "data", label: <L i={<Eye />}>Data</L> });
  if (HAS_PODS.has(id)) tabs.push({ id: "pods", label: <L i={<Layers />}>Pods</L> });
  if (HAS_LOGS.has(id)) tabs.push({ id: "logs", label: <L i={<ScrollText />}>Logs</L> });
  if (id === "pods") tabs.push({ id: "terminal", label: <L i={<SquareTerminal />}>Terminal</L> });
  if (HAS_HISTORY.has(id)) tabs.push({ id: "history", label: <L i={<History />}>History</L> });
  tabs.push({ id: "events", label: <L i={<ListTree />}>Events</L> }, { id: "yaml", label: <L i={<FileCode2 />}>YAML</L> });
  const status = statusOf(obj);

  return (
    <Drawer
      open
      onClose={onClose}
      title={
        <span className="inline-flex items-center gap-2">
          <span className="text-faint font-normal">{kind}</span> {o.name}
          {status && <Badge tone={kTone(status)}>{status}</Badge>}
        </span>
      }
      subtitle={o.ns ? `namespace ${o.ns} · cluster ${cluster}` : `cluster ${cluster}`}
      width={1180}
    >
      <div className="-m-4 flex flex-col h-[calc(100vh-48px)]">
        {obj && rt && <Actions o={o} obj={obj} id={id} containers={containers} onChanged={() => void r.reload()} onDeleted={onClose} />}
        <Tabs<ObjTab> className="px-3 shrink-0" value={tab} onChange={onTab} tabs={tabs} />
        <div className="flex-1 min-h-0 p-3">
          {r.error && <ErrorBox error={r.error} onRetry={() => void r.reload()} />}
          {!obj && !r.error && <Loading />}
          {obj && tab === "overview" && <Overview o={o} obj={obj} id={id} />}
          {obj && tab === "data" && <Data o={o} obj={obj} id={id} />}
          {obj && tab === "pods" && <Pods o={o} />}
          {obj && tab === "logs" && <KubeLogs target={o} containers={containers} className="h-full" />}
          {obj && tab === "terminal" && <Terminal o={o} containers={containers} />}
          {obj && tab === "history" && <RolloutHistory o={o} onChanged={() => void r.reload()} />}
          {obj && tab === "events" && <Events o={o} />}
          {obj && tab === "yaml" && <Yaml o={o} id={id} yaml={r.data!.yaml} editYaml={r.data!.editYaml} onApplied={() => void r.reload()} />}
        </div>
      </div>
    </Drawer>
  );
}

function L({ i, children }: { i: ReactNode; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5 [&_svg]:size-3.5">
      {i}
      {children}
    </span>
  );
}

function podSpec(obj: KubeObject | undefined): any {
  if (!obj) return undefined;
  return obj.spec?.template?.spec ?? obj.spec?.jobTemplate?.spec?.template?.spec ?? (obj.kind === "Pod" ? obj.spec : undefined);
}

function containerNames(obj: KubeObject | undefined): string[] {
  const s = podSpec(obj);
  return [...(s?.initContainers ?? []), ...(s?.containers ?? [])].map((c: any) => c.name);
}

function statusOf(obj: KubeObject | undefined): string {
  if (!obj) return "";
  const st = obj.status ?? {};
  if (obj.kind === "Pod") {
    if (obj.metadata?.deletionTimestamp) return "Terminating";
    for (const c of st.containerStatuses ?? []) if (c.state?.waiting?.reason) return c.state.waiting.reason;
    return st.phase ?? "";
  }
  if (obj.kind === "Node") {
    const ready = (st.conditions ?? []).find((c: any) => c.type === "Ready");
    return (ready?.status === "True" ? "Ready" : "NotReady") + (obj.spec?.unschedulable ? ",SchedulingDisabled" : "");
  }
  if (obj.kind === "Deployment" || obj.kind === "StatefulSet") return `${st.readyReplicas ?? 0}/${obj.spec?.replicas ?? 1}`;
  if (obj.kind === "DaemonSet") return `${st.numberReady ?? 0}/${st.desiredNumberScheduled ?? 0}`;
  if (st.phase) return st.phase;
  return "";
}

// ---------------------------------------------------------------------------
// Header actions

function Actions({ o, obj, id, containers, onChanged, onDeleted }: { o: ObjRef; obj: KubeObject; id: string; containers: string[]; onChanged: () => void; onDeleted: () => void }) {
  const { ws, cluster } = useKube();
  const { busy, run } = useRun();
  const confirm = useConfirm();
  const [replicas, setReplicas] = useState<string>(String(obj.spec?.replicas ?? ""));
  const [image, setImage] = useState(false);
  const [forward, setForward] = useState(false);
  useEffect(() => setReplicas(String(obj.spec?.replicas ?? "")), [obj.spec?.replicas]);
  const act = (a: string, label: string, b = {}) => run(a, label, () => api.kubeAction(ws, cluster, id, o.ns, o.name, a, b), onChanged);
  async function del() {
    const ok = await confirm({
      title: `Delete ${obj.kind} ${o.name}?`,
      message: id === "namespaces" ? "Everything in the namespace is deleted." : id === "pods" ? "Its controller usually recreates it." : "Dependent objects are deleted in the background.",
      danger: true,
      confirmLabel: "Delete",
      typeToConfirm: id === "pods" ? undefined : o.name,
    });
    if (ok && (await run("delete", `Delete ${o.name}`, () => api.kubeDelete(ws, cluster, id, o.ns, o.name)))) onDeleted();
  }
  const canForward = id === "pods" || id === "services" || id === "deployments.apps" || id === "statefulsets.apps";
  const paused = !!obj.spec?.paused;
  const suspended = !!obj.spec?.suspend;
  const cordoned = !!obj.spec?.unschedulable;
  return (
    <div className="flex items-center gap-2 px-4 py-2 border-b border-line flex-wrap shrink-0">
      {SCALABLE.has(id) && (
        <div className="flex items-center gap-1">
          <Input type="number" min={0} className="!w-20 !h-7 text-xs" value={replicas} onChange={(e) => setReplicas(e.target.value)} title="Replicas" />
          <Button size="sm" icon={<Scale className="size-3.5" />} loading={busy === "scale"} disabled={replicas === String(obj.spec?.replicas ?? "") || replicas === ""} onClick={() => void act("scale", `Scale ${o.name} to ${replicas}`, { replicas: Number(replicas) })}>
            Scale
          </Button>
        </div>
      )}
      {RESTARTABLE.has(id) && (
        <Button
          size="sm"
          icon={<RotateCw className="size-3.5" />}
          loading={busy === "restart"}
          onClick={async () => (await confirm({ title: `Restart ${o.name}?`, message: "Its pods are replaced one by one (rollout restart).", confirmLabel: "Restart" })) && void act("restart", `Restart ${o.name}`)}
        >
          Restart
        </Button>
      )}
      {containers.length > 0 && id !== "pods" && (
        <Button size="sm" icon={<Tag className="size-3.5" />} onClick={() => setImage(true)}>
          Set image
        </Button>
      )}
      {id === "deployments.apps" && (
        <Button size="sm" icon={paused ? <Play className="size-3.5" /> : <Pause className="size-3.5" />} loading={busy === "pause" || busy === "resume"} onClick={() => void act(paused ? "resume" : "pause", `${paused ? "Resume" : "Pause"} ${o.name}`)}>
          {paused ? "Resume rollout" : "Pause rollout"}
        </Button>
      )}
      {id === "cronjobs.batch" && (
        <>
          <Button size="sm" icon={<Play className="size-3.5" />} loading={busy === "trigger"} onClick={() => void act("trigger", `Run ${o.name} now`)}>
            Run now
          </Button>
          <Button size="sm" icon={suspended ? <Play className="size-3.5" /> : <Pause className="size-3.5" />} onClick={() => void act(suspended ? "unsuspend" : "suspend", `${suspended ? "Resume" : "Suspend"} ${o.name}`)}>
            {suspended ? "Resume schedule" : "Suspend"}
          </Button>
        </>
      )}
      {id === "nodes" && (
        <>
          <Button size="sm" icon={cordoned ? <Plug className="size-3.5" /> : <Unplug className="size-3.5" />} onClick={() => void act(cordoned ? "uncordon" : "cordon", `${cordoned ? "Uncordon" : "Cordon"} ${o.name}`)}>
            {cordoned ? "Uncordon" : "Cordon"}
          </Button>
          <Button
            size="sm"
            variant="danger"
            icon={<ShieldOff className="size-3.5" />}
            loading={busy === "drain"}
            onClick={async () =>
              (await confirm({ title: `Drain ${o.name}?`, message: "The node is cordoned and its pods are evicted (DaemonSet and static pods stay; PodDisruptionBudgets are respected).", danger: true, confirmLabel: "Drain", typeToConfirm: o.name })) &&
              void act("drain", `Drain ${o.name}`)
            }
          >
            Drain
          </Button>
        </>
      )}
      {canForward && (
        <Button size="sm" icon={<Plug className="size-3.5" />} onClick={() => setForward(true)}>
          Port forward
        </Button>
      )}
      <div className="flex-1" />
      <Button size="sm" variant="danger" icon={<Trash2 className="size-3.5" />} loading={busy === "delete"} onClick={() => void del()}>
        Delete
      </Button>
      {image && <ImageModal obj={obj} onClose={() => setImage(false)} onSet={(container, img) => (setImage(false), void act("image", `Set image of ${o.name}`, { container, image: img }))} />}
      {forward && <ForwardModal o={o} obj={obj} onClose={() => setForward(false)} />}
    </div>
  );
}

function ImageModal({ obj, onClose, onSet }: { obj: KubeObject; onClose: () => void; onSet: (container: string, image: string) => void }) {
  const cs: any[] = podSpec(obj)?.containers ?? [];
  const [container, setContainer] = useState(cs[0]?.name ?? "");
  const cur = cs.find((c) => c.name === container)?.image ?? "";
  const [img, setImg] = useState(cur);
  useEffect(() => setImg(cur), [cur]);
  return (
    <Modal
      open
      onClose={onClose}
      title={`Set image of ${obj.metadata?.name}`}
      footer={
        <Button variant="primary" disabled={!img || img === cur} onClick={() => onSet(container, img)}>
          Roll out
        </Button>
      }
    >
      <div className="space-y-3">
        {cs.length > 1 && (
          <FormField label="Container">
            <Select value={container} onChange={(e) => setContainer(e.target.value)}>
              {cs.map((c) => (
                <option key={c.name}>{c.name}</option>
              ))}
            </Select>
          </FormField>
        )}
        <FormField label="Image" help="Pods are replaced following the rollout strategy; undo it from History if it goes wrong.">
          <Input mono value={img} onChange={(e) => setImg(e.target.value)} />
        </FormField>
      </div>
    </Modal>
  );
}

function ForwardModal({ o, obj, onClose }: { o: ObjRef; obj: KubeObject; onClose: () => void }) {
  const { ws, cluster } = useKube();
  const ports: { port: number; label: string }[] = useMemo(() => {
    if (obj.kind === "Service") return (obj.spec?.ports ?? []).map((p: any) => ({ port: p.port, label: `${p.port}${p.name ? ` (${p.name})` : ""} → ${p.targetPort}` }));
    const out: { port: number; label: string }[] = [];
    for (const c of podSpec(obj)?.containers ?? []) for (const p of c.ports ?? []) out.push({ port: p.containerPort, label: `${p.containerPort}${p.name ? ` (${p.name})` : ""} · ${c.name}` });
    return out;
  }, [obj]);
  const [port, setPort] = useState(String(ports[0]?.port ?? ""));
  const [fw, setFw] = useState<Forward | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  async function start() {
    setBusy(true);
    setErr("");
    try {
      setFw(await api.kubeForward(ws, cluster, { type: o.type, namespace: o.ns, name: o.name, port: Number(port) }));
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal open onClose={onClose} title={`Port forward to ${o.name}`} icon={<Plug />}>
      {fw ? (
        <div className="space-y-2 text-[13px]">
          <div>
            Forwarding <span className="font-mono">127.0.0.1:{fw.localPort}</span> → <span className="font-mono">{fw.remote}</span>
          </div>
          <a href={fw.url} target="_blank" rel="noreferrer" className="text-accent hover:underline font-mono">
            {fw.url}
          </a>
          <div className="text-[11.5px] text-faint">It keeps running until you stop it under Forwards (top bar) or lock the vault. Other tools can use the local port too.</div>
        </div>
      ) : (
        <div className="space-y-3">
          <FormField label="Port" help={obj.kind === "Service" ? "A ready pod behind the service is used, with the service's target port." : undefined}>
            <div className="flex gap-2">
              {ports.length > 0 && (
                <Select value={ports.some((p) => String(p.port) === port) ? port : ""} onChange={(e) => setPort(e.target.value)}>
                  {ports.map((p) => (
                    <option key={p.label} value={p.port}>
                      {p.label}
                    </option>
                  ))}
                  <option value="">other…</option>
                </Select>
              )}
              <Input mono type="number" className="w-28" value={port} onChange={(e) => setPort(e.target.value)} />
            </div>
          </FormField>
          {err && <div className="text-err text-xs whitespace-pre-wrap">{err}</div>}
          <Button variant="primary" loading={busy} disabled={!port} onClick={() => void start()}>
            Start forwarding
          </Button>
        </div>
      )}
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Overview

function Overview({ o, obj, id }: { o: ObjRef; obj: KubeObject; id: string }) {
  const md = obj.metadata ?? {};
  const labels = Object.entries((md.labels ?? {}) as Record<string, string>);
  const ann = Object.entries((md.annotations ?? {}) as Record<string, string>).filter(([k]) => k !== "kubectl.kubernetes.io/last-applied-configuration");
  const [showAnn, setShowAnn] = useState(false);
  const conds: any[] = obj.status?.conditions ?? [];
  return (
    <div className="h-full overflow-y-auto space-y-3 pr-1">
      {id === "pods" && <PodView o={o} obj={obj} />}
      {(id === "deployments.apps" || id === "statefulsets.apps" || id === "daemonsets.apps" || id === "replicasets.apps") && <WorkloadView obj={obj} />}
      {id === "services" && <ServiceView obj={obj} />}
      {id === "ingresses.networking.k8s.io" && <IngressView obj={obj} />}
      {id === "nodes" && <NodeView obj={obj} />}
      {id === "cronjobs.batch" && <CronJobView obj={obj} />}
      {id === "jobs.batch" && <JobView obj={obj} />}
      {(id === "persistentvolumeclaims" || id === "persistentvolumes") && <VolumeView obj={obj} />}
      {conds.length > 0 && (
        <Section title="Conditions">
          <table className="w-full text-[12px]">
            <tbody>
              {conds.map((c, i) => (
                <tr key={i} className="border-b border-line/40 last:border-0 align-top">
                  <td className="py-1 pr-3 font-medium whitespace-nowrap">{c.type}</td>
                  <td className="pr-3">
                    <Badge tone={c.status === "True" ? (/(Pressure|Unavailable|Failed|Disrupt)/.test(c.type) ? "err" : "ok") : c.status === "False" ? (/(Pressure|Unavailable)/.test(c.type) ? "ok" : "warn") : "neutral"}>{c.status}</Badge>
                  </td>
                  <td className="pr-3 text-muted whitespace-nowrap">{c.reason}</td>
                  <td className="text-faint">{c.message}</td>
                  <td className="text-faint whitespace-nowrap pl-2">{c.lastTransitionTime ? timeAgo(c.lastTransitionTime) : ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}
      <Section title="Metadata">
        {md.namespace && <KV k="Namespace">{md.namespace}</KV>}
        <KV k="Created">{md.creationTimestamp ? `${new Date(md.creationTimestamp).toLocaleString()} (${timeAgo(md.creationTimestamp)})` : "—"}</KV>
        {(md.ownerReferences ?? []).map((r: any) => (
          <KV key={r.uid} k="Owner">
            {r.kind}/{r.name}
          </KV>
        ))}
        <KV k="UID" mono>
          {md.uid}
        </KV>
        {labels.length > 0 && (
          <KV k="Labels">
            {labels.map(([k, v]) => (
              <Badge key={k} mono>
                {k}={v}
              </Badge>
            ))}
          </KV>
        )}
        {ann.length > 0 && (
          <KV k="Annotations">
            <button className="text-[11.5px] text-accent hover:underline" onClick={() => setShowAnn(!showAnn)}>
              {showAnn ? "hide" : `${ann.length} annotations`}
            </button>
          </KV>
        )}
        {showAnn &&
          ann.map(([k, v]) => (
            <KV key={k} k={k} mono>
              <span className="break-all">{v}</span>
            </KV>
          ))}
      </Section>
    </div>
  );
}

function PodView({ o, obj }: { o: ObjRef; obj: KubeObject }) {
  const { ws, cluster } = useKube();
  const m = useAsync(() => api.kubePodMetrics(ws, cluster, o.ns).catch(() => ({}) as Record<string, KubeUsage>), [ws, cluster, o.ns]);
  useInterval(() => void m.reload(), 5000);
  const st = obj.status ?? {};
  const statuses: Record<string, any> = {};
  for (const s of [...(st.initContainerStatuses ?? []), ...(st.containerStatuses ?? [])]) statuses[s.name] = s;
  const containers: any[] = [...(obj.spec?.initContainers ?? []).map((c: any) => ({ ...c, init: true })), ...(obj.spec?.containers ?? [])];
  return (
    <>
      <div className="grid grid-cols-2 gap-3">
        <Section title="Pod">
          <KV k="Phase">
            <Badge tone={kTone(st.phase)}>{st.phase}</Badge>
            {st.reason && <Badge tone="err">{st.reason}</Badge>}
          </KV>
          {st.message && <KV k="Message">{st.message}</KV>}
          <KV k="Node">{obj.spec?.nodeName || "—"}</KV>
          <KV k="Pod IP" mono>
            {(st.podIPs ?? []).map((p: any) => p.ip).join(", ") || st.podIP || "—"}
          </KV>
          <KV k="Host IP" mono>
            {st.hostIP || "—"}
          </KV>
          <KV k="QoS">{st.qosClass}</KV>
          <KV k="Service account">{obj.spec?.serviceAccountName}</KV>
          <KV k="Restart policy">{obj.spec?.restartPolicy}</KV>
        </Section>
        <Section title="Volumes">
          {(obj.spec?.volumes ?? []).length === 0 && <div className="text-faint text-xs">none</div>}
          {(obj.spec?.volumes ?? []).map((v: any) => {
            const t = Object.keys(v).find((k) => k !== "name") ?? "";
            const src = v[t]?.claimName ?? v[t]?.name ?? v[t]?.secretName ?? v[t]?.path ?? "";
            return (
              <KV key={v.name} k={v.name}>
                <Badge>{t}</Badge> <span className="font-mono text-[11.5px]">{src}</span>
              </KV>
            );
          })}
        </Section>
      </div>
      {containers.map((c) => {
        const s = statuses[c.name] ?? {};
        const state = s.state?.running ? "Running" : s.state?.waiting?.reason ?? s.state?.terminated?.reason ?? "—";
        const u = m.data?.[`${o.ns}/${o.name}/${c.name}`];
        const lim = c.resources?.limits ?? {};
        const req = c.resources?.requests ?? {};
        return (
          <Section key={c.name} title={`${c.init ? "Init container" : "Container"} ${c.name}`}>
            <div className="grid grid-cols-2 gap-x-6">
              <div>
                <KV k="Image" mono>
                  <span className="break-all">{c.image}</span>
                </KV>
                <KV k="State">
                  <Badge tone={kTone(state)}>{state}</Badge>
                  {s.ready && <Badge tone="ok">ready</Badge>}
                  {s.state?.running?.startedAt && <span className="text-faint text-[11.5px]">since {timeAgo(s.state.running.startedAt)}</span>}
                </KV>
                {(s.state?.waiting?.message || s.state?.terminated?.message) && <KV k="Message">{s.state?.waiting?.message ?? s.state?.terminated?.message}</KV>}
                <KV k="Restarts">
                  <span className={cx(s.restartCount > 0 && "text-warn")}>{s.restartCount ?? 0}</span>
                  {s.lastState?.terminated && (
                    <span className="text-faint text-[11.5px]">
                      last: {s.lastState.terminated.reason} (exit {s.lastState.terminated.exitCode}) {timeAgo(s.lastState.terminated.finishedAt)}
                    </span>
                  )}
                </KV>
                {(c.ports ?? []).length > 0 && (
                  <KV k="Ports" mono>
                    {(c.ports ?? []).map((p: any) => `${p.containerPort}/${p.protocol ?? "TCP"}${p.name ? ` (${p.name})` : ""}`).join(", ")}
                  </KV>
                )}
                {c.command && (
                  <KV k="Command" mono>
                    <span className="break-all">{[...(c.command ?? []), ...(c.args ?? [])].join(" ")}</span>
                  </KV>
                )}
              </div>
              <div>
                <KV k="CPU">
                  {u ? <UsageBar v={lim.cpu ? (100 * u.cpu) / cpuM(lim.cpu) : 0} label={fmtCPU(u.cpu)} width="w-44" /> : <span className="text-faint">—</span>}
                  <span className="text-faint text-[11px]">
                    req {req.cpu ?? "—"} · limit {lim.cpu ?? "—"}
                  </span>
                </KV>
                <KV k="Memory">
                  {u ? <UsageBar v={lim.memory ? (100 * u.memory) / memB(lim.memory) : 0} label={fmtMem(u.memory)} width="w-44" /> : <span className="text-faint">—</span>}
                  <span className="text-faint text-[11px]">
                    req {req.memory ?? "—"} · limit {lim.memory ?? "—"}
                  </span>
                </KV>
                {(c.volumeMounts ?? []).length > 0 && (
                  <KV k="Mounts" mono>
                    <span className="break-all">{(c.volumeMounts ?? []).map((v: any) => `${v.name}→${v.mountPath}${v.readOnly ? " (ro)" : ""}`).join(", ")}</span>
                  </KV>
                )}
                {(c.env ?? []).length > 0 && <KV k="Env">{(c.env ?? []).length} variables (see YAML)</KV>}
              </div>
            </div>
          </Section>
        );
      })}
    </>
  );
}

const cpuM = (q: string) => (q.endsWith("m") ? parseFloat(q) : parseFloat(q) * 1000);
function memB(q: string): number {
  const m = /^([\d.]+)([KMGTPE]i?)?$/.exec(q);
  if (!m) return parseFloat(q);
  const mult: Record<string, number> = { Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4, K: 1e3, M: 1e6, G: 1e9, T: 1e12 };
  return parseFloat(m[1]) * (m[2] ? mult[m[2]] ?? 1 : 1);
}

function Replicas({ label, v, of }: { label: string; v: number; of: number }) {
  const p = of ? (100 * v) / of : 0;
  return (
    <KV k={label}>
      <div className="w-48 h-1.5 rounded-full bg-panel-3 overflow-hidden">
        <div className={cx("h-full", v >= of ? "bg-ok" : "bg-warn")} style={{ width: `${Math.min(100, p)}%` }} />
      </div>
      <span className="font-mono text-[11.5px]">
        {v}/{of}
      </span>
    </KV>
  );
}

function WorkloadView({ obj }: { obj: KubeObject }) {
  const st = obj.status ?? {};
  const want = obj.kind === "DaemonSet" ? st.desiredNumberScheduled ?? 0 : obj.spec?.replicas ?? 1;
  const cs: any[] = podSpec(obj)?.containers ?? [];
  return (
    <div className="grid grid-cols-2 gap-3">
      <Section title="Replicas">
        {obj.kind === "DaemonSet" ? (
          <>
            <Replicas label="Ready" v={st.numberReady ?? 0} of={want} />
            <Replicas label="Up to date" v={st.updatedNumberScheduled ?? 0} of={want} />
            <Replicas label="Available" v={st.numberAvailable ?? 0} of={want} />
          </>
        ) : (
          <>
            <Replicas label="Ready" v={st.readyReplicas ?? 0} of={want} />
            {st.updatedReplicas !== undefined && <Replicas label="Up to date" v={st.updatedReplicas ?? 0} of={want} />}
            {st.availableReplicas !== undefined && <Replicas label="Available" v={st.availableReplicas ?? 0} of={want} />}
          </>
        )}
        {obj.spec?.paused && (
          <KV k="Rollout">
            <Badge tone="warn">paused</Badge>
          </KV>
        )}
      </Section>
      <Section title="Spec">
        <KV k="Strategy">{obj.spec?.strategy?.type ?? obj.spec?.updateStrategy?.type ?? "—"}</KV>
        <KV k="Selector" mono>
          {Object.entries(obj.spec?.selector?.matchLabels ?? {})
            .map(([k, v]) => `${k}=${v}`)
            .join(", ")}
        </KV>
        {obj.spec?.serviceName && <KV k="Service">{obj.spec.serviceName}</KV>}
        {cs.map((c) => (
          <KV key={c.name} k={c.name} mono>
            <span className="break-all">{c.image}</span>
          </KV>
        ))}
      </Section>
    </div>
  );
}

function ServiceView({ obj }: { obj: KubeObject }) {
  const s = obj.spec ?? {};
  const lb = (obj.status?.loadBalancer?.ingress ?? []).map((i: any) => i.ip ?? i.hostname);
  return (
    <div className="grid grid-cols-2 gap-3">
      <Section title="Service">
        <KV k="Type">{s.type}</KV>
        <KV k="Cluster IP" mono>
          {(s.clusterIPs ?? [s.clusterIP]).join(", ")}
        </KV>
        {lb.length > 0 && (
          <KV k="Load balancer" mono>
            {lb.join(", ")}
          </KV>
        )}
        {(s.externalIPs ?? []).length > 0 && (
          <KV k="External IPs" mono>
            {s.externalIPs.join(", ")}
          </KV>
        )}
        <KV k="Selector" mono>
          {Object.entries(s.selector ?? {})
            .map(([k, v]) => `${k}=${v}`)
            .join(", ") || "—"}
        </KV>
        {s.sessionAffinity && s.sessionAffinity !== "None" && <KV k="Affinity">{s.sessionAffinity}</KV>}
      </Section>
      <Section title="Ports">
        {(s.ports ?? []).map((p: any) => (
          <KV key={p.port + (p.name ?? "")} k={p.name || String(p.port)} mono>
            {p.port}/{p.protocol ?? "TCP"} → {String(p.targetPort)}
            {p.nodePort ? ` · nodePort ${p.nodePort}` : ""}
          </KV>
        ))}
      </Section>
    </div>
  );
}

function IngressView({ obj }: { obj: KubeObject }) {
  const s = obj.spec ?? {};
  const rows: { host: string; path: string; backend: string }[] = [];
  for (const r of s.rules ?? [])
    for (const p of r.http?.paths ?? []) rows.push({ host: r.host ?? "*", path: p.path ?? "/", backend: `${p.backend?.service?.name}:${p.backend?.service?.port?.number ?? p.backend?.service?.port?.name}` });
  const lb = (obj.status?.loadBalancer?.ingress ?? []).map((i: any) => i.ip ?? i.hostname);
  return (
    <Section title="Routing">
      <KV k="Class">{s.ingressClassName ?? "—"}</KV>
      {lb.length > 0 && (
        <KV k="Address" mono>
          {lb.join(", ")}
        </KV>
      )}
      {(s.tls ?? []).map((t: any, i: number) => (
        <KV key={i} k="TLS">
          {(t.hosts ?? []).join(", ")} <span className="text-faint">secret {t.secretName}</span>
        </KV>
      ))}
      <table className="w-full text-[12px] mt-2">
        <tbody>
          {rows.map((r, i) => (
            <tr key={i} className="border-t border-line/40">
              <td className="py-1 pr-3 font-mono">{r.host}</td>
              <td className="pr-3 font-mono">{r.path}</td>
              <td className="font-mono text-muted">→ {r.backend}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  );
}

function NodeView({ obj }: { obj: KubeObject }) {
  const { ws, cluster } = useKube();
  const m = useAsync(() => api.kubeNodeMetrics(ws, cluster).catch(() => ({}) as Record<string, KubeUsage>), [ws, cluster]);
  useInterval(() => void m.reload(), 5000);
  const st = obj.status ?? {};
  const info = st.nodeInfo ?? {};
  const alloc = st.allocatable ?? {};
  const u = m.data?.[obj.metadata?.name];
  return (
    <div className="grid grid-cols-2 gap-3">
      <Section title="Node">
        <KV k="Kubelet">{info.kubeletVersion}</KV>
        <KV k="OS">
          {info.osImage} ({info.architecture})
        </KV>
        <KV k="Kernel">{info.kernelVersion}</KV>
        <KV k="Runtime">{info.containerRuntimeVersion}</KV>
        {(st.addresses ?? []).map((a: any) => (
          <KV key={a.type + a.address} k={a.type} mono>
            {a.address}
          </KV>
        ))}
        {(obj.spec?.taints ?? []).length > 0 && (
          <KV k="Taints">
            {obj.spec.taints.map((t: any) => (
              <Badge key={t.key + t.effect} mono tone="warn">
                {t.key}
                {t.value ? `=${t.value}` : ""}:{t.effect}
              </Badge>
            ))}
          </KV>
        )}
      </Section>
      <Section title="Resources">
        <KV k="CPU">{u ? <UsageBar v={(100 * u.cpu) / cpuM(String(alloc.cpu ?? "1"))} label={fmtCPU(u.cpu)} width="w-48" /> : "—"} <span className="text-faint text-[11px]">of {alloc.cpu}</span></KV>
        <KV k="Memory">
          {u ? <UsageBar v={(100 * u.memory) / memB(String(alloc.memory ?? "1"))} label={fmtMem(u.memory)} width="w-48" /> : "—"} <span className="text-faint text-[11px]">of {fmtMem(memB(String(alloc.memory ?? "0")))}</span>
        </KV>
        <KV k="Pods">max {alloc.pods}</KV>
        <KV k="Ephemeral storage">{alloc["ephemeral-storage"] ? fmtMem(memB(String(alloc["ephemeral-storage"]))) : "—"}</KV>
      </Section>
    </div>
  );
}

function CronJobView({ obj }: { obj: KubeObject }) {
  const s = obj.spec ?? {};
  const st = obj.status ?? {};
  return (
    <Section title="Schedule">
      <KV k="Schedule" mono>
        {s.schedule} {s.timeZone && <span className="text-faint">({s.timeZone})</span>}
      </KV>
      <KV k="State">{s.suspend ? <Badge tone="warn">suspended</Badge> : <Badge tone="ok">active</Badge>}</KV>
      <KV k="Last run">{st.lastScheduleTime ? timeAgo(st.lastScheduleTime) : "never"}</KV>
      <KV k="Last success">{st.lastSuccessfulTime ? timeAgo(st.lastSuccessfulTime) : "—"}</KV>
      <KV k="Running now">{(st.active ?? []).map((a: any) => a.name).join(", ") || "—"}</KV>
      <KV k="Concurrency">{s.concurrencyPolicy}</KV>
    </Section>
  );
}

function JobView({ obj }: { obj: KubeObject }) {
  const s = obj.spec ?? {};
  const st = obj.status ?? {};
  return (
    <Section title="Job">
      <Replicas label="Succeeded" v={st.succeeded ?? 0} of={s.completions ?? 1} />
      <KV k="Active / failed">
        {st.active ?? 0} / <span className={cx((st.failed ?? 0) > 0 && "text-err")}>{st.failed ?? 0}</span> (backoff limit {s.backoffLimit ?? 6})
      </KV>
      <KV k="Started">{st.startTime ? timeAgo(st.startTime) : "—"}</KV>
      <KV k="Completed">{st.completionTime ? timeAgo(st.completionTime) : "—"}</KV>
    </Section>
  );
}

function VolumeView({ obj }: { obj: KubeObject }) {
  const s = obj.spec ?? {};
  const st = obj.status ?? {};
  return (
    <Section title="Volume">
      <KV k="Status">
        <Badge tone={kTone(st.phase)}>{st.phase}</Badge>
      </KV>
      <KV k="Capacity">{st.capacity?.storage ?? s.capacity?.storage ?? s.resources?.requests?.storage ?? "—"}</KV>
      <KV k="Access modes">{(s.accessModes ?? []).join(", ")}</KV>
      <KV k="Storage class">{s.storageClassName ?? "—"}</KV>
      {s.volumeName && <KV k="Volume">{s.volumeName}</KV>}
      {s.claimRef && (
        <KV k="Claim">
          {s.claimRef.namespace}/{s.claimRef.name}
        </KV>
      )}
      {s.persistentVolumeReclaimPolicy && <KV k="Reclaim policy">{s.persistentVolumeReclaimPolicy}</KV>}
    </Section>
  );
}

// ---------------------------------------------------------------------------
// Other tabs

function Data({ o, obj, id }: { o: ObjRef; obj: KubeObject; id: string }) {
  const { ws, cluster } = useKube();
  const [values, setValues] = useState<Record<string, string> | null>(null);
  const { busy, run } = useRun();
  if (id === "configmaps") {
    const entries = Object.entries({ ...(obj.data ?? {}), ...(obj.binaryData ?? {}) }) as [string, string][];
    return (
      <div className="h-full overflow-y-auto space-y-3">
        {entries.length === 0 && <EmptyState title="No data" />}
        {entries.map(([k, v]) => (
          <div key={k}>
            <div className="text-xs font-mono text-muted mb-1">{k}</div>
            <div dir="ltr">
              <CodeBlock maxHeight={400}>{v}</CodeBlock>
            </div>
          </div>
        ))}
      </div>
    );
  }
  const keys = Object.entries((obj.data ?? {}) as Record<string, string>);
  return (
    <div className="h-full overflow-y-auto space-y-3">
      <div className="flex items-center gap-3">
        <span className="text-[12.5px] text-muted">
          Type <span className="font-mono">{obj.type}</span> · {keys.length} key{keys.length === 1 ? "" : "s"}
        </span>
        <div className="flex-1" />
        {values ? (
          <Button size="sm" onClick={() => setValues(null)}>
            Hide values
          </Button>
        ) : (
          <Button
            size="sm"
            icon={<Eye className="size-3.5" />}
            loading={busy === "reveal"}
            onClick={() =>
              void run("reveal", "Reveal secret", async () => {
                setValues(await api.kubeReveal(ws, cluster, o.ns, o.name));
              })
            }
          >
            Reveal values
          </Button>
        )}
      </div>
      {!values && <div className="text-[11.5px] text-faint">Values stay hidden (also from the agent). Revealing them is recorded in the audit log.</div>}
      {keys.map(([k, masked]) => (
        <div key={k}>
          <div className="text-xs font-mono text-muted mb-1">{k}</div>
          <div dir="ltr">{values ? <CodeBlock maxHeight={300}>{values[k] ?? ""}</CodeBlock> : <div className="font-mono text-[11.5px] text-faint">{masked}</div>}</div>
        </div>
      ))}
    </div>
  );
}

function Pods({ o }: { o: ObjRef }) {
  const { ws, cluster, open } = useKube();
  const t = useAsync(() => api.kubePods(ws, cluster, o.type, o.ns, o.name), [ws, cluster, o.type, o.ns, o.name]);
  useInterval(() => void t.reload(), 4000);
  if (t.error) return <ErrorBox error={t.error} />;
  if (!t.data) return <Loading />;
  const cols = t.data.columns.map((c, i) => ({ ...c, i })).filter((c) => c.priority === 0 || c.name === "Node" || c.name === "IP");
  return (
    <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
      {t.data.rows.length === 0 && <EmptyState title="No pods" />}
      {t.data.rows.length > 0 && (
        <Table className="flex-1">
          <thead>
            <tr>
              {o.type === "nodes" && <Th>Namespace</Th>}
              {cols.map((c) => (
                <Th key={c.i}>{c.name}</Th>
              ))}
            </tr>
          </thead>
          <tbody>
            {t.data.rows.map((r) => (
              <Tr key={r.uid} onClick={() => open({ type: "pods", ns: r.namespace ?? "", name: r.name })} className={cx(r.deleting && "opacity-60")}>
                {o.type === "nodes" && <Td className="text-muted">{r.namespace}</Td>}
                {cols.map((c, j) => {
                  const v = cellText(r.cells[c.i]);
                  return (
                    <Td key={c.i} mono={j > 0 && /ip|node/i.test(c.name)}>
                      {c.name === "Status" ? <Badge tone={r.deleting ? "warn" : kTone(v)}>{r.deleting ? "Terminating" : v}</Badge> : j === 0 ? <span className="font-medium">{v}</span> : v}
                    </Td>
                  );
                })}
              </Tr>
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}

function Terminal({ o, containers }: { o: ObjRef; containers: string[] }) {
  const { ws, cluster } = useKube();
  const [container, setContainer] = useState(containers[containers.length - 1] ?? "");
  const [shell, setShell] = useState("auto");
  return (
    <div className="h-full flex flex-col gap-2">
      <div className="flex items-center gap-2">
        {containers.length > 1 && (
          <Select className="!w-auto !h-7 text-xs" value={container} onChange={(e) => setContainer(e.target.value)}>
            {containers.map((c) => (
              <option key={c}>{c}</option>
            ))}
          </Select>
        )}
        <Select className="!w-24 !h-7 text-xs" value={shell} onChange={(e) => setShell(e.target.value)}>
          <option value="auto">auto</option>
          <option value="bash">bash</option>
          <option value="sh">sh</option>
          <option value="ash">ash</option>
        </Select>
        <span className="text-[11.5px] text-faint">kubectl exec -it {o.name}{container ? ` -c ${container}` : ""}</span>
      </div>
      <ExecTerminal key={container + shell} className="flex-1" options={false} url={(cols, rows) => api.kubeExecUrl(ws, cluster, o.ns, o.name, { container, cols, rows, shell })} />
    </div>
  );
}

function RolloutHistory({ o, onChanged }: { o: ObjRef; onChanged: () => void }) {
  const { ws, cluster } = useKube();
  const h = useAsync(() => api.kubeHistory(ws, cluster, o.type, o.ns, o.name), [ws, cluster, o.type, o.ns, o.name]);
  useInterval(() => void h.reload(), 5000);
  const { busy, run } = useRun();
  const confirm = useConfirm();
  if (h.error) return <ErrorBox error={h.error} />;
  if (!h.data) return <Loading />;
  return (
    <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
      <Table className="flex-1">
        <thead>
          <tr>
            <Th>Revision</Th>
            <Th>Images</Th>
            <Th>Created</Th>
            <Th>Change cause</Th>
            <Th className="w-[1%]" />
          </tr>
        </thead>
        <tbody>
          {h.data.map((r) => (
            <Tr key={r.name}>
              <Td>
                <span className="font-mono">{r.revision}</span> {r.current && <Badge tone="ok">current</Badge>}
              </Td>
              <Td mono className="max-w-96">
                <span className="break-all">{(r.images ?? []).join(", ")}</span>
              </Td>
              <Td className="text-muted" title={r.created}>
                {timeAgo(r.created)}
              </Td>
              <Td className="text-muted">{r.cause}</Td>
              <Td>
                {!r.current && (
                  <Button
                    size="xs"
                    icon={<RotateCcw className="size-3" />}
                    loading={busy === `undo:${r.revision}`}
                    onClick={async () =>
                      (await confirm({ title: `Roll ${o.name} back to revision ${r.revision}?`, message: `Its pods get ${(r.images ?? []).join(", ")} again.`, confirmLabel: "Roll back" })) &&
                      void run(`undo:${r.revision}`, `Roll back to revision ${r.revision}`, () => api.kubeAction(ws, cluster, o.type, o.ns, o.name, "undo", { revision: r.revision }), () => {
                        onChanged();
                        void h.reload();
                      })
                    }
                  >
                    Roll back
                  </Button>
                )}
              </Td>
            </Tr>
          ))}
        </tbody>
      </Table>
    </div>
  );
}

function Events({ o }: { o: ObjRef }) {
  const { ws, cluster } = useKube();
  const ev = useAsync(() => api.kubeEvents(ws, cluster, o.type, o.ns, o.name), [ws, cluster, o.type, o.ns, o.name]);
  useInterval(() => void ev.reload(), 5000);
  if (ev.error) return <ErrorBox error={ev.error} />;
  if (!ev.data) return <Loading />;
  if (ev.data.length === 0) return <EmptyState title="No events">Kubernetes keeps events for about an hour.</EmptyState>;
  return <EventTable events={ev.data} />;
}

export function EventTable({ events, onOpen }: { events: { time: string; type: string; reason: string; object: string; namespace?: string; message: string; count: number; source?: string }[]; onOpen?: (namespace: string, object: string) => void }) {
  return (
    <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
      <Table className="flex-1">
        <thead>
          <tr>
            <Th>When</Th>
            <Th>Type</Th>
            <Th>Reason</Th>
            {onOpen && <Th>Object</Th>}
            <Th>Message</Th>
            <Th>Count</Th>
          </tr>
        </thead>
        <tbody>
          {events.map((e, i) => (
            <Tr key={e.time + e.object + e.reason + i} onClick={onOpen ? () => onOpen(e.namespace ?? "", e.object) : undefined}>
              <Td className="text-muted whitespace-nowrap" title={e.time}>
                {timeAgo(e.time)}
              </Td>
              <Td>{e.type === "Warning" ? <Badge tone="warn">{e.type}</Badge> : <Badge>{e.type}</Badge>}</Td>
              <Td className="font-medium whitespace-nowrap">{e.reason}</Td>
              {onOpen && (
                <Td mono className="whitespace-nowrap">
                  {e.namespace && <span className="text-faint">{e.namespace}/</span>}
                  {e.object}
                </Td>
              )}
              <Td className={cx("text-[12px]", e.type === "Warning" ? "text-warn" : "text-muted")}>{e.message}</Td>
              <Td mono>{e.count > 1 ? `×${e.count}` : ""}</Td>
            </Tr>
          ))}
        </tbody>
      </Table>
    </div>
  );
}

function Yaml({ o, id, yaml, editYaml, onApplied }: { o: ObjRef; id: string; yaml: string; editYaml: string; onApplied: () => void }) {
  const { ws, cluster } = useKube();
  const [edit, setEdit] = useState<string | null>(null);
  const [preview, setPreview] = useState<KubeApplyResult[] | null>(null);
  const { busy, run } = useRun();
  const secret = id === "secrets";
  async function dry() {
    if (edit === null) return;
    await run("dry", "Dry run", async () => setPreview(await api.kubeApply(ws, cluster, edit, o.ns || "_", true)));
  }
  async function apply() {
    if (edit === null) return;
    const ok = await run("apply", `Apply ${o.name}`, async () => {
      const r = await api.kubeApply(ws, cluster, edit, o.ns || "_", false);
      const failed = r.find((x) => x.action === "failed");
      if (failed) throw new Error(failed.error);
    });
    if (ok) {
      setEdit(null);
      setPreview(null);
      onApplied();
    }
  }
  return (
    <div className="h-full flex flex-col gap-2 min-h-0">
      <div className="flex items-center gap-2">
        {edit === null ? (
          <>
            <Button size="sm" disabled={secret} onClick={() => setEdit(editYaml)} title={secret ? "Secrets are edited with their values hidden from view: apply a new manifest instead" : undefined}>
              Edit
            </Button>
            {secret && <span className="text-[11.5px] text-faint">Values are masked here. Reveal them in the Data tab.</span>}
          </>
        ) : (
          <>
            <Button size="sm" variant="ghost" onClick={() => (setEdit(null), setPreview(null))}>
              Cancel
            </Button>
            <Button size="sm" loading={busy === "dry"} onClick={() => void dry()}>
              Review changes (dry run)
            </Button>
            <Button size="sm" variant="primary" loading={busy === "apply"} onClick={() => void apply()}>
              Apply
            </Button>
            <span className="text-[11.5px] text-faint">Server-side apply as “opsdeck”. A field you remove is only removed if opsdeck manages it.</span>
          </>
        )}
      </div>
      {preview && (
        <div className="max-h-[40%] overflow-y-auto">
          <ApplyResults results={preview} dry />
        </div>
      )}
      {edit === null ? (
        <div className="flex-1 min-h-0 overflow-auto" dir="ltr">
          <CodeBlock>{yaml}</CodeBlock>
        </div>
      ) : (
        <Textarea mono dir="ltr" spellCheck={false} className="flex-1 !min-h-0 !text-[12px] leading-relaxed resize-none" value={edit} onChange={(e) => (setEdit(e.target.value), setPreview(null))} />
      )}
    </div>
  );
}

