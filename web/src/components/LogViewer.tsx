import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowDownToLine, Pause, Play, Trash2, WrapText } from "lucide-react";
import { Badge, IconButton, SearchInput, StatusDot, cx } from "./ui";

const MAX_LINES = 5000;

type StreamState = "idle" | "connecting" | "streaming" | "ended" | "error";

export function LogViewer({ url, className, toolbarExtra }: { url: string | null; className?: string; toolbarExtra?: React.ReactNode }) {
  const [lines, setLines] = useState<string[]>([]);
  const [state, setState] = useState<StreamState>("idle");
  const [error, setError] = useState("");
  const [paused, setPaused] = useState(false);
  const [filter, setFilter] = useState("");
  const [wrap, setWrap] = useState(false);
  const [follow, setFollow] = useState(true);
  const bufRef = useRef<string[]>([]);
  const pausedRef = useRef(false);
  pausedRef.current = paused;
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setLines([]);
    bufRef.current = [];
    setError("");
    if (!url) {
      setState("idle");
      return;
    }
    setState("connecting");
    const es = new EventSource(url, { withCredentials: true });
    let ended = false;
    let pending: string[] = [];
    let raf = 0;
    const flush = () => {
      raf = 0;
      if (pausedRef.current) {
        bufRef.current.push(...pending);
        pending = [];
        return;
      }
      const add = pending;
      pending = [];
      setLines((l) => {
        const n = l.concat(add);
        return n.length > MAX_LINES ? n.slice(n.length - MAX_LINES) : n;
      });
    };
    es.onopen = () => setState("streaming");
    es.onmessage = (ev) => {
      try {
        const d = JSON.parse(ev.data) as { line?: string };
        if (d.line !== undefined) pending.push(d.line);
      } catch {
        pending.push(ev.data);
      }
      if (!raf) raf = requestAnimationFrame(flush);
    };
    es.addEventListener("end", () => {
      ended = true;
      setState("ended");
      es.close();
    });
    es.addEventListener("error", (ev) => {
      const me = ev as MessageEvent;
      if (me.data) {
        try {
          setError((JSON.parse(me.data) as { message?: string }).message ?? String(me.data));
        } catch {
          setError(String(me.data));
        }
        setState("error");
        ended = true;
        es.close();
      } else if (!ended) {
        // connection-level error; EventSource would retry and replay — stop instead
        setState((s) => (s === "streaming" ? "ended" : "error"));
        if (es.readyState !== EventSource.OPEN) {
          setError((e) => e || "Stream disconnected");
        }
        es.close();
      }
    });
    return () => {
      es.close();
      if (raf) cancelAnimationFrame(raf);
    };
  }, [url]);

  useEffect(() => {
    if (!paused && bufRef.current.length) {
      const add = bufRef.current;
      bufRef.current = [];
      setLines((l) => l.concat(add).slice(-MAX_LINES));
    }
  }, [paused]);

  useEffect(() => {
    if (follow && scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
  }, [lines, follow]);

  const visible = useMemo(() => {
    if (!filter) return lines.map((l, i) => ({ l, i }));
    const f = filter.toLowerCase();
    return lines.map((l, i) => ({ l, i })).filter((x) => x.l.toLowerCase().includes(f));
  }, [lines, filter]);

  const tone = state === "streaming" ? "ok" : state === "connecting" ? "warn" : state === "error" ? "err" : "neutral";

  return (
    <div className={cx("flex flex-col min-h-0 rounded-lg border border-line bg-panel overflow-hidden", className)}>
      <div className="flex items-center gap-2 px-2 h-10 border-b border-line shrink-0">
        <StatusDot tone={tone} pulse={state === "streaming" && !paused} />
        <span className="text-xs text-muted w-20">{paused ? "paused" : state}</span>
        <SearchInput value={filter} onChange={setFilter} placeholder="Filter…" className="w-56" />
        {filter && <Badge>{visible.length} matches</Badge>}
        <div className="flex-1" />
        {toolbarExtra}
        <IconButton title={paused ? "Resume" : "Pause"} onClick={() => setPaused((p) => !p)} active={paused}>
          {paused ? <Play className="size-3.5" /> : <Pause className="size-3.5" />}
        </IconButton>
        <IconButton title="Wrap lines" onClick={() => setWrap((w) => !w)} active={wrap}>
          <WrapText className="size-3.5" />
        </IconButton>
        <IconButton title="Auto-scroll" onClick={() => setFollow((f) => !f)} active={follow}>
          <ArrowDownToLine className="size-3.5" />
        </IconButton>
        <IconButton title="Clear" onClick={() => setLines([])}>
          <Trash2 className="size-3.5" />
        </IconButton>
      </div>
      {error && <div className="px-3 py-2 text-xs text-err border-b border-line bg-err/5 whitespace-pre-wrap">{error}</div>}
      <div
        ref={scrollRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 30;
          if (!atBottom && follow) setFollow(false);
        }}
        className="flex-1 min-h-0 overflow-auto font-mono text-[11.5px] leading-[1.55] py-1"
      >
        {!url && <div className="p-4 text-faint text-xs">Select a source to start streaming.</div>}
        {url && lines.length === 0 && state !== "error" && <div className="p-4 text-faint text-xs">Waiting for log lines…</div>}
        {visible.map(({ l, i }) => (
          <div key={i} className={cx("px-3 hover:bg-panel-2 flex gap-3", wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre", levelClass(l))}>
            <span className="text-faint select-none tabular-nums w-10 text-right shrink-0">{i + 1}</span>
            <span>{filter ? highlight(l, filter) : l}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function levelClass(l: string): string {
  if (/\b(ERROR|ERR|FATAL|CRIT(ICAL)?|PANIC|EMERG|ALERT)\b|level=error|"level":"error"/i.test(l)) return "text-err";
  if (/\b(WARN(ING)?)\b|level=warn|"level":"warn/i.test(l)) return "text-warn";
  if (/\b(DEBUG|TRACE)\b|level=debug/i.test(l)) return "text-faint";
  return "";
}

function highlight(line: string, q: string) {
  const out: React.ReactNode[] = [];
  const low = line.toLowerCase();
  const ql = q.toLowerCase();
  let i = 0;
  let k = 0;
  while (true) {
    const j = low.indexOf(ql, i);
    if (j < 0) break;
    out.push(line.slice(i, j));
    out.push(
      <mark key={k++} className="bg-warn/35 text-fg rounded-sm">
        {line.slice(j, j + q.length)}
      </mark>,
    );
    i = j + q.length;
  }
  out.push(line.slice(i));
  return out;
}
