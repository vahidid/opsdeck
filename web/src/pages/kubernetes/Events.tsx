import { useEffect, useMemo, useRef, useState } from "react";
import { Pause, Play } from "lucide-react";
import { api } from "../../api/client";
import type { KubeEvent } from "../../api/types";
import { Badge, Button, EmptyState, IconButton, SearchInput, StatusDot, cx } from "../../components/ui";
import { listNs, useKube } from "./shared";
import { EventTable } from "./ObjectDrawer";

// Cluster events, live: the last hour first, then each new or repeated event
// as the API server reports it (one row per event, its count going up).
export function Events() {
  const { ws, cluster, ns, open } = useKube();
  const [events, setEvents] = useState<Map<string, KubeEvent>>(new Map());
  const [state, setState] = useState<"connecting" | "live" | "error">("connecting");
  const [error, setError] = useState("");
  const [paused, setPaused] = useState(false);
  const [warnOnly, setWarnOnly] = useState(false);
  const [q, setQ] = useState("");
  const pausedRef = useRef(false);
  pausedRef.current = paused;
  const [gen, setGen] = useState(0);
  useEffect(() => {
    setEvents(new Map());
    setState("connecting");
    setError("");
    const es = new EventSource(api.kubeEventsUrl(ws, cluster, listNs(ns)), { withCredentials: true });
    es.onopen = () => setState("live");
    es.onmessage = (ev) => {
      if (pausedRef.current) return;
      try {
        const d = JSON.parse(ev.data) as { event?: KubeEvent };
        const e = d.event;
        if (!e) return;
        setEvents((m) => {
          const n = new Map(m);
          n.delete(e.uid);
          n.set(e.uid, e);
          if (n.size > 3000) n.delete(n.keys().next().value!);
          return n;
        });
      } catch {
        /* ignore */
      }
    };
    es.addEventListener("error", (ev) => {
      const me = ev as MessageEvent;
      let msg = "Stream disconnected";
      try {
        if (me.data) msg = JSON.parse(me.data).message ?? msg;
      } catch {
        /* ignore */
      }
      setState("error");
      setError(msg);
      es.close();
    });
    return () => es.close();
  }, [ws, cluster, ns, gen]);
  const rows = useMemo(() => {
    const ql = q.toLowerCase();
    return [...events.values()]
      .filter((e) => (!warnOnly || e.type === "Warning") && (!ql || `${e.namespace ?? ""} ${e.object} ${e.reason} ${e.message}`.toLowerCase().includes(ql)))
      .sort((a, b) => (a.time < b.time ? 1 : a.time > b.time ? -1 : 0));
  }, [events, q, warnOnly]);
  const warnings = useMemo(() => [...events.values()].filter((e) => e.type === "Warning").length, [events]);
  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-3 flex-wrap">
        <StatusDot tone={state === "live" ? "ok" : state === "error" ? "err" : "warn"} pulse={state === "live" && !paused} />
        <span className="text-xs text-muted">{paused ? "paused" : state === "live" ? "live — the last hour, then new events as they happen" : state}</span>
        <SearchInput value={q} onChange={setQ} placeholder="Filter by object, reason, message…" className="w-72" />
        <button onClick={() => setWarnOnly(!warnOnly)}>
          <Badge tone={warnOnly ? "warn" : "neutral"} className={cx(!warnOnly && "opacity-70")}>
            warnings only ({warnings})
          </Badge>
        </button>
        <div className="flex-1" />
        <span className="text-xs text-faint">{rows.length} events</span>
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
      <div className="flex-1 min-h-0">
        {rows.length === 0 ? (
          <div className="h-full rounded-lg border border-line bg-panel">
            <EmptyState title={state === "connecting" ? "Connecting…" : "No events"}>Kubernetes keeps events for about an hour.</EmptyState>
          </div>
        ) : (
          <EventTable
            events={rows}
            onOpen={(namespace, object) => {
              const i = object.indexOf("/");
              open({ type: object.slice(0, i), ns: namespace, name: object.slice(i + 1) });
            }}
          />
        )}
      </div>
    </div>
  );
}
