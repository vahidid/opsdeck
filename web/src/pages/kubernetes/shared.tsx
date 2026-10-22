import { createContext, useContext, useMemo, useState } from "react";
import { api } from "../../api/client";
import { Button, Select } from "../../components/ui";
import type { Tone } from "../../components/ui";
import { LogViewer } from "../../components/LogViewer";
import { fmtBytes } from "../../lib/format";

export type ObjRef = { type: string; ns: string; name: string };
export type ObjTab = "overview" | "yaml" | "events" | "pods" | "logs" | "terminal" | "history" | "data";

export type KubeCtx = {
  ws: string;
  cluster: string;
  ns: string; // "*" = all namespaces
  defaultNs: string;
  open: (o: ObjRef, tab?: ObjTab) => void;
  goto: (view: string, extra?: Record<string, string>) => void;
  apply: (manifest?: string, ns?: string) => void;
};
const Ctx = createContext<KubeCtx | null>(null);
export const KubeProvider = Ctx.Provider;
export function useKube(): KubeCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("KubeProvider missing");
  return c;
}

/** The namespace to send to list APIs ("*" = all). */
export const listNs = (ns: string) => (ns === "*" ? "*" : ns);

export function fmtCPU(m: number | undefined): string {
  if (m === undefined || m === null) return "—";
  if (m < 1000) return `${Math.round(m)}m`;
  return `${(m / 1000).toFixed(m < 10000 ? 2 : 1)}`;
}
export const fmtMem = (b: number | undefined) => (b === undefined ? "—" : fmtBytes(b));

const OK = /^(Running|Ready|Active|Bound|Complete|Completed|Succeeded|True|Available|deployed|Healthy)$/i;
const WARN = /^(Pending|ContainerCreating|PodInitializing|Terminating|Progressing|Init:.*|NotReady,SchedulingDisabled|SchedulingDisabled|Released|pending-.*|superseded|Unknown|Suspended)$/i;
const ERR = /(BackOff|Err|Error|Failed|NotReady|OOMKilled|Evicted|Lost|failed|Unschedulable|Invalid|ContainerCannotRun|DeadlineExceeded)/;

export function kTone(s: unknown): Tone {
  const x = String(s ?? "");
  if (!x) return "neutral";
  if (OK.test(x)) return "ok";
  if (ERR.test(x)) return "err";
  if (WARN.test(x)) return "warn";
  if (/^\d+\/\d+$/.test(x)) {
    const [a, b] = x.split("/").map(Number);
    return a === b ? "ok" : a === 0 ? "err" : "warn";
  }
  return "neutral";
}

export function cellText(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (Array.isArray(v)) return v.join(", ");
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

/** Types that have pods behind them. */
export const HAS_PODS = new Set(["deployments.apps", "statefulsets.apps", "daemonsets.apps", "replicasets.apps", "jobs.batch", "cronjobs.batch", "services", "nodes"]);
export const HAS_LOGS = new Set(["pods", "deployments.apps", "statefulsets.apps", "daemonsets.apps", "replicasets.apps", "jobs.batch", "cronjobs.batch", "services"]);
export const HAS_HISTORY = new Set(["deployments.apps", "statefulsets.apps", "daemonsets.apps"]);
export const SCALABLE = new Set(["deployments.apps", "statefulsets.apps", "replicasets.apps"]);
export const RESTARTABLE = new Set(["deployments.apps", "statefulsets.apps", "daemonsets.apps"]);

/** Logs of a pod or of every pod of a workload, with history and time controls. */
export function KubeLogs({ target, containers, className }: { target: ObjRef; containers?: string[]; className?: string }) {
  const { ws, cluster } = useKube();
  const [tail, setTail] = useState("500");
  const [since, setSince] = useState("");
  const [follow, setFollow] = useState(true);
  const [container, setContainer] = useState("");
  const [previous, setPrevious] = useState(false);
  const base = { type: target.type, namespace: target.ns, name: target.name, container: container || undefined };
  const url = useMemo(
    () => api.kubeLogUrl(ws, cluster, { ...base, lines: tail === "all" ? "all" : Number(tail), since: since || undefined, previous, follow: follow && !previous }),
    [ws, cluster, target.type, target.ns, target.name, container, tail, since, previous, follow], // eslint-disable-line react-hooks/exhaustive-deps
  );
  return (
    <LogViewer
      className={className}
      url={url}
      downloadName={target.name}
      reconnect={follow && !previous ? (ts) => api.kubeLogUrl(ws, cluster, { ...base, lines: "all", since: ts, follow: true }) : undefined}
      toolbarExtra={
        <>
          {containers && containers.length > 1 && (
            <Select className="!h-7 !w-auto text-xs" value={container} onChange={(e) => setContainer(e.target.value)} title="Container">
              <option value="">all containers</option>
              {containers.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </Select>
          )}
          <Select className="!h-7 !w-auto text-xs" value={since} onChange={(e) => setSince(e.target.value)} title="Since">
            <option value="">any time</option>
            <option value="5m">last 5 min</option>
            <option value="15m">last 15 min</option>
            <option value="1h">last hour</option>
            <option value="6h">last 6 hours</option>
            <option value="1d">last day</option>
          </Select>
          <Select className="!h-7 !w-auto text-xs" value={tail} onChange={(e) => setTail(e.target.value)} title="History per container">
            <option value="100">100 lines</option>
            <option value="500">500 lines</option>
            <option value="2000">2000 lines</option>
            <option value="10000">10000 lines</option>
            <option value="all">all</option>
          </Select>
          <Button size="xs" variant={previous ? "subtle" : "ghost"} onClick={() => setPrevious(!previous)} title="Logs of the previous (crashed) container instance">
            Previous
          </Button>
          <Button size="xs" variant={follow ? "subtle" : "ghost"} onClick={() => setFollow(!follow)} title="Keep streaming new lines">
            {follow ? "Live" : "Snapshot"}
          </Button>
        </>
      }
    />
  );
}
