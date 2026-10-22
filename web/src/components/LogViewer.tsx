import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ArrowDown, ArrowDownToLine, Clock, Copy, Download, Pause, Play, Regex, Trash2, WrapText } from "lucide-react";
import { Badge, IconButton, SearchInput, Segmented, StatusDot, cx } from "./ui";
import { ansiCss, hasAnsi, parseAnsi, stripAnsi } from "../lib/ansi";

const MAX_LINES = 10000;
const ROW_H = 19; // px, one unwrapped line (virtualised rendering)

type StreamState = "idle" | "connecting" | "streaming" | "reconnecting" | "ended" | "error";
type Level = "error" | "warn" | "info" | "debug" | "";

// A line from the stream: plain {line} for journal/file sources, with ts
// (RFC3339), src (compose service / swarm task) and stderr for docker.
type Incoming = { line?: string; ts?: string; src?: string; stderr?: boolean };
type Line = { seq: number; text: string; plain: string; low: string; ts?: string; src?: string; stderr?: boolean; level: Level; ansi: boolean };

const LEVEL_RE: [Level, RegExp][] = [
  ["error", /\b(ERROR|ERR|FATAL|CRIT(ICAL)?|PANIC|EMERG|ALERT|SEVERE|Exception|Traceback)\b|level=(error|fatal|panic|crit)|"(level|severity|lvl)":\s*"(error|fatal|panic|crit\w*)"/i],
  ["warn", /\b(WARN(ING)?)\b|level=warn|"(level|severity|lvl)":\s*"warn/i],
  ["debug", /\b(DEBUG|TRACE)\b|level=(debug|trace)|"(level|severity|lvl)":\s*"(debug|trace)"/i],
  ["info", /\b(INFO|NOTICE)\b|level=info|"(level|severity|lvl)":\s*"info"/i],
];

function levelOf(plain: string, stderr?: boolean): Level {
  for (const [lv, re] of LEVEL_RE) if (re.test(plain)) return lv;
  return stderr ? "warn" : "";
}

const SRC_COLORS = ["#60a5fa", "#34d399", "#fbbf24", "#c084fc", "#22d3ee", "#f472b6", "#a3e635", "#fb923c", "#818cf8", "#2dd4bf"];
function srcColor(src: string): string {
  let h = 0;
  for (let i = 0; i < src.length; i++) h = (h * 31 + src.charCodeAt(i)) | 0;
  return SRC_COLORS[Math.abs(h) % SRC_COLORS.length];
}

function fmtTs(ts: string): string {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  const p = (n: number, l = 2) => String(n).padStart(l, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
}

export function LogViewer({
  url,
  className,
  toolbarExtra,
  reconnect,
  downloadName = "logs",
}: {
  url: string | null;
  className?: string;
  toolbarExtra?: React.ReactNode;
  /** URL to resume a dropped stream after the last received timestamp. */
  reconnect?: (lastTs: string) => string;
  downloadName?: string;
}) {
  const [lines, setLines] = useState<Line[]>([]);
  const [state, setState] = useState<StreamState>("idle");
  const [error, setError] = useState("");
  const [paused, setPaused] = useState(false);
  const [filter, setFilter] = useState("");
  const [useRegex, setUseRegex] = useState(false);
  const [wrap, setWrap] = useState(false);
  const [follow, setFollow] = useState(true);
  const [showTs, setShowTs] = useState(true);
  const [minLevel, setMinLevel] = useState<"all" | "warn" | "error">("all");
  const [stream, setStream] = useState<"all" | "out" | "err">("all");
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [unseen, setUnseen] = useState(0);
  const bufRef = useRef<Line[]>([]);
  const pausedRef = useRef(false);
  pausedRef.current = paused;
  const followRef = useRef(true);
  followRef.current = follow;
  const reconnectRef = useRef(reconnect);
  reconnectRef.current = reconnect;
  const scrollRef = useRef<HTMLDivElement>(null);
  const [view, setView] = useState({ top: 0, height: 600 });

  useEffect(() => {
    setLines([]);
    bufRef.current = [];
    setError("");
    setExpanded(new Set());
    setHidden(new Set());
    setUnseen(0);
    if (!url) {
      setState("idle");
      return;
    }
    let seq = 0;
    let lastTs = "";
    let resume: { ts: string; seen: Set<string> } | null = null;
    let recent: Line[] = []; // lines with the latest timestamp, to skip duplicates after a resume
    let es: EventSource | null = null;
    let timer = 0;
    let raf = 0;
    let closed = false;
    let pending: Line[] = [];
    let retries = 0;

    const flush = () => {
      raf = 0;
      if (pausedRef.current) {
        bufRef.current.push(...pending);
        pending = [];
        return;
      }
      const add = pending;
      pending = [];
      if (!followRef.current) setUnseen((n) => n + add.length);
      setLines((l) => {
        let n = l.concat(add);
        // docker sends a container's stdout and stderr history (and compose
        // each container's) one after the other: keep them in time order
        const last = l.length ? l[l.length - 1].ts : undefined;
        if (add.some((x, i) => x.ts && ((i === 0 && last && x.ts < last) || (i > 0 && add[i - 1].ts && x.ts < add[i - 1].ts!)))) {
          n = n.slice().sort((a, b) => (a.ts && b.ts && a.ts !== b.ts ? (a.ts < b.ts ? -1 : 1) : a.seq - b.seq));
        }
        return n.length > MAX_LINES ? n.slice(-MAX_LINES) : n;
      });
    };
    const push = (d: Incoming) => {
      const text = d.line ?? "";
      if (resume && d.ts) {
        if (d.ts < resume.ts || (d.ts === resume.ts && resume.seen.has(text))) return;
        if (d.ts > resume.ts) resume = null;
      }
      const ansi = hasAnsi(text);
      const plain = ansi ? stripAnsi(text) : text;
      const ln: Line = { seq: seq++, text, plain, low: plain.toLowerCase(), ts: d.ts, src: d.src, stderr: d.stderr, level: levelOf(plain, d.stderr), ansi };
      if (d.ts) {
        if (d.ts !== lastTs) recent = [];
        lastTs = d.ts;
        recent.push(ln);
      }
      pending.push(ln);
      if (!raf) raf = requestAnimationFrame(flush);
    };
    const open = (u: string) => {
      es = new EventSource(u, { withCredentials: true });
      let ended = false;
      es.onopen = () => {
        setState("streaming");
        setError("");
      };
      es.onmessage = (ev) => {
        retries = 0;
        try {
          push(JSON.parse(ev.data) as Incoming);
        } catch {
          push({ line: ev.data });
        }
      };
      es.addEventListener("end", () => {
        ended = true;
        setState("ended");
        es?.close();
      });
      es.addEventListener("error", (ev) => {
        const me = ev as MessageEvent;
        let retry = false;
        if (me.data) {
          let msg = String(me.data);
          try {
            const d = JSON.parse(me.data) as { message?: string; retry?: boolean };
            msg = d.message ?? msg;
            retry = !!d.retry;
          } catch {
            /* plain text */
          }
          if (!retry || !reconnectRef.current || !lastTs) {
            setError(msg);
            setState("error");
            ended = true;
            es?.close();
            return;
          }
          setError(`${msg} — reconnecting…`);
        }
        if (ended || closed) return;
        es?.close();
        // the connection dropped (network, VPN, SSH, laptop sleep): resume where we were
        const again = reconnectRef.current;
        if (again && lastTs && retries < 30) {
          retries++;
          setState("reconnecting");
          resume = { ts: lastTs, seen: new Set(recent.map((l) => l.text)) };
          timer = window.setTimeout(() => !closed && open(again(lastTs)), Math.min(1000 * retries, 10000));
        } else {
          setState((s) => (s === "streaming" ? "ended" : "error"));
          setError((e) => e || "Stream disconnected");
        }
      });
    };
    setState("connecting");
    open(url);
    return () => {
      closed = true;
      es?.close();
      clearTimeout(timer);
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

  const sources = useMemo(() => {
    const s = new Set<string>();
    for (const l of lines) if (l.src) s.add(l.src);
    return [...s].sort();
  }, [lines]);
  const hasTs = useMemo(() => lines.some((l) => l.ts), [lines]);
  const hasStderr = useMemo(() => lines.some((l) => l.stderr), [lines]);

  const matcher = useMemo(() => {
    if (!filter) return null;
    if (useRegex) {
      try {
        const re = new RegExp(filter, "i");
        return { test: (l: Line) => re.test(l.plain), re };
      } catch {
        return { test: () => false, re: null, bad: true };
      }
    }
    const f = filter.toLowerCase();
    return { test: (l: Line) => l.low.includes(f), re: null };
  }, [filter, useRegex]);

  const visible = useMemo(() => {
    return lines.filter((l) => {
      if (matcher && !matcher.test(l)) return false;
      if (minLevel === "error" && l.level !== "error") return false;
      if (minLevel === "warn" && l.level !== "error" && l.level !== "warn") return false;
      if (stream === "out" && l.stderr) return false;
      if (stream === "err" && !l.stderr) return false;
      if (l.src && hidden.has(l.src)) return false;
      return true;
    });
  }, [lines, matcher, minLevel, stream, hidden]);

  // follow the tail
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (follow && el) el.scrollTop = el.scrollHeight;
  }, [visible, follow]);

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setView({ top: el.scrollTop, height: el.clientHeight }));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const onScroll = useCallback(
    (e: React.UIEvent<HTMLDivElement>) => {
      const el = e.currentTarget;
      setView({ top: el.scrollTop, height: el.clientHeight });
      const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 30;
      if (!atBottom && follow) setFollow(false);
      if (atBottom && !follow) {
        setFollow(true);
        setUnseen(0);
      }
    },
    [follow],
  );

  function jumpToEnd() {
    setFollow(true);
    setUnseen(0);
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }

  function download() {
    const text = visible.map((l) => [l.ts, l.src, l.stderr ? "[stderr]" : "", l.plain].filter(Boolean).join(" ")).join("\n") + "\n";
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    a.download = `${downloadName.replace(/[^\w.-]+/g, "_")}-${new Date().toISOString().replace(/[:.]/g, "-")}.log`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }

  const toggleExpand = (seq: number) =>
    setExpanded((s) => {
      const n = new Set(s);
      if (n.has(seq)) n.delete(seq);
      else n.add(seq);
      return n;
    });

  // Virtualised rendering for unwrapped, unexpanded lines; wrapped lines
  // render the latest part of the buffer.
  const virtual = !wrap && expanded.size === 0;
  let start = 0;
  let end = visible.length;
  if (virtual) {
    start = Math.max(0, Math.floor(view.top / ROW_H) - 30);
    end = Math.min(visible.length, start + Math.ceil(view.height / ROW_H) + 60);
  } else if (visible.length > 3000) {
    start = visible.length - 3000;
  }
  const srcWidth = Math.min(22, Math.max(6, ...sources.map((s) => s.length)));
  const tone = state === "streaming" ? "ok" : state === "connecting" || state === "reconnecting" ? "warn" : state === "error" ? "err" : "neutral";
  const showTime = showTs && hasTs;

  return (
    <div className={cx("flex flex-col min-h-0 rounded-lg border border-line bg-panel overflow-hidden", className)}>
      <div className="flex items-center gap-2 px-2 min-h-10 py-1 border-b border-line shrink-0 flex-wrap">
        <StatusDot tone={tone} pulse={state === "streaming" && !paused} />
        <span className="text-xs text-muted w-20">{paused ? "paused" : state}</span>
        <SearchInput value={filter} onChange={setFilter} placeholder={useRegex ? "Regex…" : "Filter…"} className="w-52" />
        <IconButton title="Regular expression" active={useRegex} onClick={() => setUseRegex((r) => !r)}>
          <Regex className="size-3.5" />
        </IconButton>
        {matcher && "bad" in matcher && matcher.bad ? <Badge tone="err">bad regex</Badge> : (filter || minLevel !== "all" || hidden.size > 0 || stream !== "all") && <Badge>{visible.length} lines</Badge>}
        <Segmented
          size="xs"
          value={minLevel}
          onChange={setMinLevel}
          options={[
            { id: "all", label: "All" },
            { id: "warn", label: "Warn+" },
            { id: "error", label: "Errors" },
          ]}
        />
        {hasStderr && (
          <Segmented
            size="xs"
            value={stream}
            onChange={setStream}
            options={[
              { id: "all", label: "out+err" },
              { id: "out", label: "stdout" },
              { id: "err", label: "stderr" },
            ]}
          />
        )}
        <div className="flex-1" />
        {toolbarExtra}
        <span className="text-[11px] text-faint tabular-nums">{lines.length.toLocaleString()} lines</span>
        <IconButton title={paused ? "Resume" : "Pause"} onClick={() => setPaused((p) => !p)} active={paused}>
          {paused ? <Play className="size-3.5" /> : <Pause className="size-3.5" />}
        </IconButton>
        {hasTs && (
          <IconButton title="Timestamps" onClick={() => setShowTs((v) => !v)} active={showTs}>
            <Clock className="size-3.5" />
          </IconButton>
        )}
        <IconButton title="Wrap lines" onClick={() => setWrap((w) => !w)} active={wrap}>
          <WrapText className="size-3.5" />
        </IconButton>
        <IconButton title="Auto-scroll" onClick={() => (follow ? setFollow(false) : jumpToEnd())} active={follow}>
          <ArrowDownToLine className="size-3.5" />
        </IconButton>
        <IconButton title="Download the visible lines" onClick={download} disabled={visible.length === 0}>
          <Download className="size-3.5" />
        </IconButton>
        <IconButton title="Clear" onClick={() => setLines([])}>
          <Trash2 className="size-3.5" />
        </IconButton>
      </div>
      {sources.length > 1 && (
        <div className="flex items-center gap-1 px-2 py-1 border-b border-line overflow-x-auto shrink-0">
          {sources.map((s) => (
            <button
              key={s}
              onClick={(e) =>
                setHidden((h) => {
                  // alt/shift-click: show only this one
                  if (e.altKey || e.shiftKey) return new Set(sources.filter((x) => x !== s));
                  const n = new Set(h);
                  if (n.has(s)) n.delete(s);
                  else n.add(s);
                  return n;
                })
              }
              title="Click to hide/show, shift-click to show only this one"
              className={cx("inline-flex items-center gap-1.5 rounded px-1.5 h-5 font-mono text-[11px] border border-line whitespace-nowrap", hidden.has(s) ? "opacity-40 line-through" : "hover:bg-panel-3")}
            >
              <span className="size-2 rounded-full" style={{ background: srcColor(s) }} />
              {s}
            </button>
          ))}
        </div>
      )}
      {error && <div className="px-3 py-2 text-xs text-err border-b border-line bg-err/5 whitespace-pre-wrap">{error}</div>}
      <div className="relative flex-1 min-h-0">
        <div ref={scrollRef} onScroll={onScroll} className="absolute inset-0 overflow-auto font-mono text-[11.5px]" dir="ltr">
          {!url && <div className="p-4 text-faint text-xs">Select a source to start streaming.</div>}
          {url && lines.length === 0 && state !== "error" && <div className="p-4 text-faint text-xs">Waiting for log lines…</div>}
          <div style={virtual ? { height: visible.length * ROW_H + 8, position: "relative" } : { paddingBottom: 8 }}>
            <div style={virtual ? { position: "absolute", top: start * ROW_H, left: 0, right: 0 } : undefined}>
              {visible.slice(start, end).map((l) => (
                <LogRow
                  key={l.seq}
                  l={l}
                  wrap={wrap}
                  showTime={showTime}
                  srcWidth={sources.length > 0 ? srcWidth : 0}
                  filter={matcher ? filter : ""}
                  re={matcher?.re ?? null}
                  expanded={expanded.has(l.seq)}
                  onToggle={() => toggleExpand(l.seq)}
                />
              ))}
            </div>
          </div>
        </div>
        {!follow && unseen > 0 && (
          <button onClick={jumpToEnd} className="absolute bottom-3 right-5 inline-flex items-center gap-1.5 rounded-full bg-accent text-accent-fg px-3 h-7 text-xs shadow-lg">
            <ArrowDown className="size-3.5" /> {unseen.toLocaleString()} new
          </button>
        )}
      </div>
    </div>
  );
}

const LEVEL_BAR: Record<Level, string> = { error: "bg-err", warn: "bg-warn", info: "bg-info/60", debug: "bg-faint/50", "": "bg-transparent" };

function LogRow({
  l,
  wrap,
  showTime,
  srcWidth,
  filter,
  re,
  expanded,
  onToggle,
}: {
  l: Line;
  wrap: boolean;
  showTime: boolean;
  srcWidth: number;
  filter: string;
  re: RegExp | null;
  expanded: boolean;
  onToggle: () => void;
}) {
  const json = useMemo(() => {
    if (!expanded) return null;
    const t = l.plain.trim();
    const i = t.indexOf("{");
    if (i < 0 || !t.endsWith("}")) return null;
    try {
      return JSON.stringify(JSON.parse(t.slice(i)), null, 2);
    } catch {
      return null;
    }
  }, [expanded, l.plain]);
  const tint = l.level === "error" ? "bg-err/6" : l.level === "warn" ? "bg-warn/5" : "";
  const color = l.ansi ? "" : l.level === "error" ? "text-err" : l.level === "warn" ? "text-warn" : l.level === "debug" ? "text-faint" : "";
  return (
    <div className={cx("group", tint)}>
      <div onClick={onToggle} className={cx("flex gap-2 pr-3 hover:bg-panel-2 cursor-default", wrap || expanded ? "items-start" : "items-center")} style={wrap || expanded ? undefined : { height: ROW_H }}>
        <span className={cx("w-[3px] self-stretch shrink-0", LEVEL_BAR[l.level])} />
        {showTime && (
          <span className="text-faint tabular-nums shrink-0 select-none" title={l.ts}>
            {l.ts ? fmtTs(l.ts) : "            "}
          </span>
        )}
        {srcWidth > 0 && (
          <span className="shrink-0 truncate" style={{ width: `${srcWidth}ch`, color: l.src ? srcColor(l.src) : undefined }} title={l.src}>
            {l.src ?? ""}
          </span>
        )}
        {l.stderr && (
          <span className="shrink-0 text-err/80 select-none" title="standard error">
            ›
          </span>
        )}
        <span className={cx("min-w-0 flex-1", wrap || expanded ? "whitespace-pre-wrap break-all" : "whitespace-pre", color)}>
          {filter ? highlight(l.plain, filter, re) : l.ansi ? <Ansi text={l.text} /> : l.text}
        </span>
        <button
          title="Copy line"
          className="opacity-0 group-hover:opacity-100 text-faint hover:text-fg shrink-0"
          onClick={(e) => {
            e.stopPropagation();
            void navigator.clipboard?.writeText(l.plain);
          }}
        >
          <Copy className="size-3" />
        </button>
      </div>
      {expanded && (
        <div className="pl-6 pr-3 pb-1.5 text-[11px] text-muted">
          {l.ts && <div className="text-faint">{l.ts}</div>}
          {json && <pre className="whitespace-pre-wrap break-all text-fg/90 bg-panel-2 rounded p-2 mt-1">{json}</pre>}
        </div>
      )}
    </div>
  );
}

function Ansi({ text }: { text: string }) {
  return (
    <>
      {parseAnsi(text).map((s, i) =>
        s.style ? (
          <span key={i} style={ansiCss(s.style)}>
            {s.text}
          </span>
        ) : (
          s.text
        ),
      )}
    </>
  );
}

function highlight(line: string, q: string, re: RegExp | null) {
  const out: React.ReactNode[] = [];
  let k = 0;
  if (re) {
    const g = new RegExp(re.source, "gi");
    let last = 0;
    let m: RegExpExecArray | null;
    while ((m = g.exec(line)) && m[0].length > 0) {
      out.push(line.slice(last, m.index));
      out.push(
        <mark key={k++} className="bg-warn/35 text-fg rounded-sm">
          {m[0]}
        </mark>,
      );
      last = m.index + m[0].length;
    }
    out.push(line.slice(last));
    return out;
  }
  const low = line.toLowerCase();
  const ql = q.toLowerCase();
  let i = 0;
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
