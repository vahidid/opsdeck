// Rich blocks of an answer or report. The agent writes them as fenced code
// blocks in Markdown (```chart, ```stats, ```timeline with a JSON body,
// ```widget with HTML) and Markdown.tsx renders them here; tables and long
// code blocks get their behaviour without any syntax. Everything is plain DOM
// and SVG: no chart library, and nothing is parsed until its block is complete.
import { Children, cloneElement, createContext, isValidElement, memo, useContext, useEffect, useId, useMemo, useRef, useState } from "react";
import type { ReactElement, ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { ArrowRight, Check, CircleAlert } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { Resource, TopologyDoc } from "../api/types";
import { useWsMaybe } from "../context/app";
import { Button, CopyButton, cx } from "./ui";
import { SERIES, fnum, useWidth } from "../lib/viz";
import { pendingWidgets, themeVars, vazirFace } from "../lib/exportDoc";

const TONES: Record<string, string> = { ok: "var(--c-ok)", warn: "var(--c-warn)", err: "var(--c-err)", info: "var(--c-info)" };
const tone = (t: unknown, fallback = "var(--c-border-strong)") => TONES[String(t)] ?? fallback;
const text = (v: unknown) => (v === null || v === undefined ? "" : String(v));

export const RICH_LANGS = ["chart", "stats", "timeline", "widget", "actions", "topology"];

/** what a click on an action button does with its prompt: the chat sends it; elsewhere it opens as a draft */
export const ActionCtx = createContext<((prompt: string) => void) | null>(null);

/** a block that is still being written */
export function PendingBlock({ lang }: { lang: string }) {
  return (
    <div className="rb-skeleton rounded-lg border border-line px-3.5 py-3" role="status">
      <div className="text-[11.5px] text-faint">Drawing the {lang}</div>
      <div className="mt-2.5 h-2 w-2/3 rounded-full bg-line-strong/70" />
      <div className="mt-2 h-2 w-2/5 rounded-full bg-line-strong/50" />
    </div>
  );
}

export const RichBlock = memo(function RichBlock({ lang, src, rtl }: { lang: string; src: string; rtl: boolean }) {
  const el = useMemo(() => {
    try {
      if (lang === "widget") return src.trim() ? <Widget src={src} rtl={rtl} /> : null;
      const j: unknown = JSON.parse(src);
      if (lang === "chart") {
        if (typeof (j as Item)?.data === "string") return <DataChart o={j as Item} />;
        const spec = parseChart(j);
        return spec && <Chart spec={spec} />;
      }
      if (lang === "topology") {
        const o = j as Item;
        const paths = (Array.isArray(o?.paths) ? o.paths : [o?.path]).filter((p) => Array.isArray(p) && p.length > 0) as unknown[][];
        return paths.length > 0 ? <TopoPaths paths={paths.map((p) => p.map(text))} tones={(o.tones ?? {}) as Item} notes={(o.notes ?? {}) as Item} /> : null;
      }
      const items = Array.isArray(j) ? j.filter((x) => x && typeof x === "object") : [];
      if (items.length === 0) return null;
      if (lang === "actions") return <Actions items={items} rtl={rtl} />;
      return lang === "stats" ? <Stats items={items} rtl={rtl} /> : <Timeline items={items} rtl={rtl} />;
    } catch {
      return null;
    }
  }, [lang, src, rtl]);
  // what cannot be rendered stays readable as the code it is
  return el ?? <Code text={src.replace(/\n$/, "")}>{src}</Code>;
});

// ---------------------------------------------------------------- stats

type Item = Record<string, unknown>;

// One surface divided by hairlines, not a row of cards: the figures read as a
// set. The tone is a dot beside the label; the number keeps the text colour.
function Stats({ items, rtl }: { items: Item[]; rtl: boolean }) {
  return (
    <div className="rb-keep flex flex-wrap gap-px rounded-lg border border-line bg-line overflow-hidden" dir={rtl ? "rtl" : "auto"}>
      {items.map((s, i) => (
        <div key={i} className="bg-panel-2 px-3.5 py-3 grow basis-[9.5rem] min-w-0">
          <div className="flex items-center gap-1.5 text-[11.5px] text-muted leading-snug">
            {TONES[String(s.tone)] && <span className="size-1.5 rounded-full shrink-0" style={{ background: tone(s.tone) }} />}
            {text(s.label)}
          </div>
          <div className="text-[22px] font-semibold tracking-tight leading-none mt-2 tabular-nums">{text(s.value)}</div>
          {!!s.hint && <div className="text-[11px] text-faint leading-snug mt-1.5">{text(s.hint)}</div>}
        </div>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------- timeline

function Timeline({ items, rtl }: { items: Item[]; rtl: boolean }) {
  return (
    <div className="grid grid-cols-[auto_11px_1fr] gap-x-3" dir={rtl ? "rtl" : "auto"}>
      {items.map((e, i) => {
        const t = TONES[String(e.tone)];
        return (
          <div key={i} className="rb-keep contents">
            <span dir="ltr" className="text-[11.5px] text-faint tabular-nums text-end leading-[1.6] pt-px">
              {text(e.time)}
            </span>
            <span className="relative flex justify-center">
              {i < items.length - 1 && <span className="absolute top-[14px] -bottom-[4px] w-px bg-line-strong" />}
              <span className="relative mt-[6px] size-[9px] rounded-full border-2" style={{ borderColor: t ?? "var(--c-faint)", background: t ?? "transparent" }} />
            </span>
            <div className={cx("min-w-0 leading-[1.6]", i < items.length - 1 && "pb-3.5")}>
              <div className="font-medium">{text(e.title)}</div>
              {!!e.text && <div className="text-muted text-[0.94em]">{text(e.text)}</div>}
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------- chart

type ChartSpec = {
  type: "line" | "area" | "bar";
  title: string;
  unit: string;
  x: string[];
  series: { name: string; data: (number | null)[] }[];
  marks: { i: number; label: string; tone: unknown }[];
};

/** times: the moment of every x (data read from a table), so a mark can name a time between two points */
function parseChart(j: unknown, times?: number[]): ChartSpec | null {
  const o = j as Record<string, unknown>;
  if (!o || !Array.isArray(o.x) || !Array.isArray(o.series)) return null;
  const x = o.x.slice(0, 2000).map(text);
  const series = (o.series as Item[])
    .filter((s) => s && Array.isArray(s.data))
    .slice(0, SERIES.length)
    .map((s) => {
      const d = s.data as unknown[];
      return { name: text(s.name), data: x.map((_, i) => (typeof d[i] === "number" && Number.isFinite(d[i]) ? (d[i] as number) : null)) };
    });
  if (x.length === 0 || series.length === 0) return null;
  const marks = (Array.isArray(o.marks) ? (o.marks as Item[]) : [])
    .filter((m) => m && typeof m === "object")
    .map((m) => {
      const at = times ? Date.parse(text(m.x)) : NaN;
      const i = Number.isNaN(at) ? x.indexOf(text(m.x)) : times!.reduce((best, t, k) => (Math.abs(t - at) < Math.abs(times![best] - at) ? k : best), 0);
      return { i, label: text(m.label), tone: m.tone };
    })
    .filter((m) => m.i >= 0);
  return { type: o.type === "bar" || o.type === "area" ? o.type : "line", title: text(o.title), unit: text(o.unit), x, series, marks };
}

/** CSV rows; quoted fields may hold commas and doubled quotes */
function csvRows(src: string): string[][] {
  return src
    .split(/\r?\n/)
    .filter((l) => l.trim())
    .map((l) => (l.match(/("([^"]|"")*"|[^,]*)(,|$)/g) ?? []).slice(0, -1).map((c) => c.replace(/,$/, "").replace(/^"([\s\S]*)"$/, "$1").replace(/""/g, '"')));
}

/** a table as chart data: the first column is x (times become clock labels), every other column a series */
function csvChart(src: string): { x: string[]; series: Item[]; times?: number[] } {
  const [head = [], ...rows] = csvRows(src);
  const stamps = rows.map((r) => (/^\d{4}-\d\d-\d\d[T ]/.test(r[0] ?? "") ? Date.parse(r[0]) : NaN));
  const timed = rows.length > 0 && stamps.every((t) => !Number.isNaN(t));
  const days = timed && stamps[stamps.length - 1] - stamps[0] > 2 * 86400e3;
  const clock = (t: number) => {
    const d = new Date(t);
    const hm = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });
    return days ? `${d.getMonth() + 1}/${d.getDate()} ${hm}` : hm;
  };
  return {
    x: rows.map((r, i) => (timed ? clock(stamps[i]) : (r[0] ?? ""))),
    series: head.slice(1).map((name, k) => ({ name, data: rows.map((r) => (r[k + 1] === undefined || r[k + 1].trim() === "" ? null : Number(r[k + 1]))) })),
    times: timed ? stamps : undefined,
  };
}

const tables = new Map<string, Promise<string>>();

/** a chart whose points are a saved artifact (metrics_query save=true, or any CSV), not numbers typed by the agent */
function DataChart({ o }: { o: Item }) {
  const ws = useWsMaybe()?.name;
  const path = text(o.data);
  const [state, setState] = useState<ChartSpec | string | null>(null);
  useEffect(() => {
    if (!ws) return setState("charts of saved data need a workspace");
    const key = ws + "\n" + path;
    let p = tables.get(key);
    if (!p) tables.set(key, (p = api.artifact(ws, path).then((a) => a.content)));
    let live = true;
    p.then(
      (csv) => {
        const t = csvChart(csv);
        if (live) setState(parseChart({ ...o, x: t.x, series: t.series }, t.times) ?? "no numeric columns in " + path);
      },
      (e) => {
        tables.delete(key);
        if (live) setState(errMsg(e));
      },
    );
    return () => {
      live = false;
    };
  }, [ws, path, o]);
  if (state === null) return <PendingBlock lang="chart" />;
  if (typeof state === "string")
    return (
      <div title={state} className="rounded-md border border-warn/40 bg-panel-2 px-3 py-2 text-[12px] text-muted">
        The chart's data, <code>{path}</code>, could not be read.
      </div>
    );
  return <Chart spec={state} />;
}

/** runs of consecutive points, so a null leaves a gap in the line */
function runs(d: (number | null)[]): number[][] {
  const out: number[][] = [];
  let cur: number[] = [];
  d.forEach((v, i) => {
    if (v !== null) cur.push(i);
    else if (cur.length) {
      out.push(cur);
      cur = [];
    }
  });
  if (cur.length) out.push(cur);
  return out;
}

/** a bar with rounded data-end, square on the baseline */
function barPath(x: number, w: number, y: number, base: number): string {
  const r = Math.min(4, w / 2, Math.abs(base - y));
  const d = y < base ? r : -r;
  return `M${x},${base}V${y + d}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + d}V${base}Z`;
}

function Chart({ spec }: { spec: ChartSpec }) {
  const gid = useId();
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const [focus, setFocus] = useState<number | null>(null);
  const { x, series, marks } = spec;
  const bar = spec.type === "bar";
  const n = x.length;
  const H = 210;
  const M = { l: 46, r: 14, t: marks.some((m) => m.label) ? 24 : 10, b: 24 };
  const vals = series.flatMap((s) => s.data).filter((v): v is number => v !== null);
  // round ticks: a step of 1, 2, 2.5 or 5 times a power of ten
  const raw = (Math.max(0, ...vals) - Math.min(0, ...vals)) / 4 || 1;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((v) => v >= raw)!;
  const lo = Math.floor(Math.min(0, ...vals) / step) * step;
  const hi = Math.max(lo + step, Math.ceil(Math.max(0, ...vals) / step) * step);
  const ticks = Array.from({ length: Math.round((hi - lo) / step) + 1 }, (_, i) => lo + i * step);
  const iw = Math.max(50, width - M.l - M.r);
  const ih = H - M.t - M.b;
  const band = iw / n;
  const px = (i: number) => (bar ? M.l + band * (i + 0.5) : M.l + (n === 1 ? iw / 2 : (i / (n - 1)) * iw));
  const py = (v: number) => M.t + ih - ((v - lo) / (hi - lo)) * ih;
  const pt = (s: (number | null)[], i: number) => `${px(i).toFixed(1)},${py(s[i]!).toFixed(1)}`;
  const every = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(iw / 76))));
  const barW = Math.min(44, (band * 0.76) / series.length);
  const barX = (i: number, k: number) => M.l + band * i + (band - barW * series.length) / 2 + barW * k + 1;
  const dim = (k: number) => (focus === null || focus === k ? 1 : 0.2);
  const pct = (v: number) => `${(v / width) * 100}%`;
  return (
    <figure className="rounded-lg border border-line bg-panel-2 px-3.5 pt-3 pb-2.5" dir="ltr">
      {(spec.title || spec.unit) && (
        <figcaption className="flex items-baseline gap-2 mb-2">
          <span dir="auto" className="font-semibold text-[13px] flex-1 text-start">
            {spec.title}
          </span>
          {spec.unit && <span className="text-[11px] text-faint shrink-0 rounded-full border border-line px-1.5 leading-[1.5]">{spec.unit}</span>}
        </figcaption>
      )}
      <div ref={ref} className="relative">
        <svg viewBox={`0 0 ${width} ${H}`} className="block w-full h-auto overflow-visible select-none" role="img" aria-label={spec.title || "Chart"}>
          {spec.type === "area" && (
            <defs>
              {series.map((_, k) => (
                <linearGradient key={k} id={gid + k} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0" style={{ stopColor: SERIES[k], stopOpacity: 0.3 }} />
                  <stop offset="1" style={{ stopColor: SERIES[k], stopOpacity: 0.02 }} />
                </linearGradient>
              ))}
            </defs>
          )}
          {bar && hover !== null && <rect x={M.l + band * hover} y={M.t} width={band} height={ih} rx={4} fill="var(--c-text)" opacity={0.06} />}
          {ticks.map((t, i) => (
            <g key={i}>
              <line x1={M.l} x2={M.l + iw} y1={py(t)} y2={py(t)} stroke={t === 0 ? "var(--c-border-strong)" : "var(--c-border)"} opacity={t === 0 ? 1 : 0.7} />
              <text x={M.l - 6} y={py(t)} dy="0.32em" textAnchor="end" className="fill-[var(--c-faint)] text-[10px] tabular-nums">
                {fnum(t)}
              </text>
            </g>
          ))}
          {x.map(
            (label, i) =>
              i % every === 0 && (
                <text key={i} x={px(i)} y={H - 6} textAnchor="middle" className="fill-[var(--c-faint)] text-[10px] tabular-nums">
                  {label.length > 14 ? label.slice(0, 13) + "…" : label}
                </text>
              ),
          )}
          {series.map((s, k) =>
            bar ? (
              <g key={k} fill={SERIES[k]} opacity={dim(k)}>
                {s.data.map(
                  (v, i) =>
                    v !== null && (
                      <path key={i} d={barPath(barX(i, k), Math.max(1, barW - 2), py(v), py(0))}>
                        <title>{`${x[i]}: ${s.name ? s.name + " " : ""}${fnum(v)}${spec.unit ? " " + spec.unit : ""}`}</title>
                      </path>
                    ),
                )}
              </g>
            ) : (
              <g key={k} opacity={dim(k)}>
                {spec.type === "area" &&
                  runs(s.data).map((r, j) => (
                    <path key={j} d={`M${px(r[0]).toFixed(1)},${py(0)}L${r.map((i) => pt(s.data, i)).join("L")}L${px(r[r.length - 1]).toFixed(1)},${py(0)}Z`} fill={`url(#${gid}${k})`} />
                  ))}
                <path
                  d={runs(s.data)
                    .map((r) => "M" + r.map((i) => pt(s.data, i)).join("L"))
                    .join("")}
                  fill="none"
                  stroke={SERIES[k]}
                  strokeWidth={2}
                  strokeLinejoin="round"
                  strokeLinecap="round"
                />
                {n <= 16 && s.data.map((v, i) => v !== null && <circle key={i} cx={px(i)} cy={py(v)} r={3} fill={SERIES[k]} stroke="var(--c-panel-2)" strokeWidth={1.5} />)}
              </g>
            ),
          )}
          {marks.map((m, i) => (
            <line key={i} x1={px(m.i)} x2={px(m.i)} y1={M.t} y2={M.t + ih} stroke={tone(m.tone, "var(--c-muted)")} strokeWidth={1.5} strokeDasharray="4 3" />
          ))}
          {hover !== null && (
            <g>
              {!bar && <line x1={px(hover)} x2={px(hover)} y1={M.t} y2={M.t + ih} stroke="var(--c-muted)" />}
              {!bar && series.map((s, k) => s.data[hover] !== null && <circle key={k} cx={px(hover)} cy={py(s.data[hover]!)} r={4} fill={SERIES[k]} stroke="var(--c-panel-2)" strokeWidth={2} />)}
            </g>
          )}
          <rect
            x={M.l}
            y={M.t}
            width={iw}
            height={ih}
            fill="transparent"
            onPointerMove={(e) => {
              const r = e.currentTarget.getBoundingClientRect();
              const f = Math.min(0.9999, Math.max(0, (e.clientX - r.left) / r.width));
              setHover(bar ? Math.floor(f * n) : Math.round(f * (n - 1)));
            }}
            onPointerLeave={() => setHover(null)}
          />
        </svg>
        {/* labels are HTML, so Persian text is shaped and ordered by the browser */}
        {marks.map((m, i) => {
          const f = px(m.i) / width;
          return (
            m.label && (
              <span
                key={i}
                dir="auto"
                className="absolute top-0 text-[10.5px] leading-none font-medium whitespace-nowrap pointer-events-none"
                style={{ left: pct(px(m.i)), color: tone(m.tone, "var(--c-muted)"), transform: `translateX(${f < 0.18 ? "0" : f > 0.82 ? "-100%" : "-50%"})` }}
              >
                {m.label}
              </span>
            )
          );
        })}
        {hover !== null && (
          <div
            className="no-export pointer-events-none absolute top-6 z-10 rounded-md border border-line-strong bg-panel px-2.5 py-1.5 shadow-lg text-[11px] min-w-32 max-w-72"
            style={px(hover) > width / 2 ? { right: `calc(${pct(width - px(hover))} + 12px)` } : { left: `calc(${pct(px(hover))} + 12px)` }}
          >
            <div dir="auto" className="text-faint mb-1 tabular-nums text-start">
              {x[hover]}
            </div>
            {series.map((s, k) => (
              <div key={k} className="flex items-center gap-1.5">
                <span className="size-2 rounded-full shrink-0" style={{ background: SERIES[k] }} />
                <span dir="auto" className="truncate text-muted flex-1 text-start">
                  {s.name}
                </span>
                <span className="tabular-nums font-medium text-fg">
                  {s.data[hover] === null ? "–" : fnum(s.data[hover]!)}
                  {spec.unit && s.data[hover] !== null ? " " + spec.unit : ""}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
      {(series.length > 1 || series[0].name) && (
        <div className="mt-2 pt-2 border-t border-line flex flex-wrap gap-x-3.5 gap-y-1 text-[11px]">
          {series.map((s, k) => (
            <span key={k} className={cx("flex items-center gap-1.5 cursor-default", focus === k ? "text-fg" : "text-muted")} onMouseEnter={() => setFocus(k)} onMouseLeave={() => setFocus(null)}>
              <span className="size-2 rounded-full" style={{ background: SERIES[k] }} />
              <span dir="auto">{s.name || `#${k + 1}`}</span>
            </span>
          ))}
        </div>
      )}
    </figure>
  );
}

// ---------------------------------------------------------------- actions

/** next steps as buttons: a click sends the prompt to the agent (changes still ask for approval as always) */
function Actions({ items, rtl }: { items: Item[]; rtl: boolean }) {
  const send = useContext(ActionCtx);
  const ws = useWsMaybe()?.name;
  const navigate = useNavigate();
  const [sent, setSent] = useState<number | null>(null);
  const list = items.filter((a) => text(a.label) && text(a.prompt));
  if (list.length === 0) return null;
  return (
    <div className="no-export flex flex-wrap gap-1.5" dir={rtl ? "rtl" : "auto"}>
      {list.map((a, i) => (
        <Button
          key={i}
          size="sm"
          variant="agentSoft"
          title={text(a.prompt)}
          disabled={sent !== null || (!send && !ws)}
          icon={sent === i ? <Check className="size-3.5" /> : <span className="size-1.5 rounded-full" style={{ background: tone(a.tone, "var(--c-agent-ink)") }} />}
          onClick={() => {
            if (send) {
              setSent(i);
              send(text(a.prompt));
            } else navigate(`/w/${encodeURIComponent(ws!)}/agent?new=1&draft=${encodeURIComponent(text(a.prompt))}`);
          }}
        >
          {text(a.label)}
        </Button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------- topology

// ponytail: the diagram is read once per workspace and kept for the session; reload the app after editing it
const diagrams = new Map<string, Promise<TopologyDoc>>();

/** paths through the workspace's diagram (request flow, blast radius), with the failing hops marked */
function TopoPaths({ paths, tones, notes }: { paths: string[][]; tones: Item; notes: Item }) {
  const ws = useWsMaybe()?.name;
  const [doc, setDoc] = useState<TopologyDoc | null>(null);
  useEffect(() => {
    if (!ws) return;
    let p = diagrams.get(ws);
    if (!p) diagrams.set(ws, (p = api.topologyDoc(ws).then((d) => d.topology)));
    let live = true;
    p.then(
      (d) => live && setDoc(d),
      () => diagrams.delete(ws), // without the diagram the hops keep the names they were given
    );
    return () => {
      live = false;
    };
  }, [ws]);
  const find = (id: string) => doc?.nodes.find((n) => n.id === id || n.label === id || n.resource === id || n.resource?.endsWith("/" + id));
  return (
    <figure className="rb-keep rounded-lg border border-line bg-panel-2 p-3.5 space-y-2.5" dir="ltr">
      {paths.map((path, k) => (
        <div key={k} className="flex flex-wrap items-stretch gap-y-2">
          {path.map((id, i) => {
            const n = find(id);
            const sub = n ? n.tech?.[0] || n.vendor || n.type : "";
            const t = TONES[String(tones[id])];
            return (
              <div key={i} className="flex items-center">
                {i > 0 && <ArrowRight className="size-3.5 mx-1 text-faint shrink-0" />}
                <div
                  className="rounded-lg border px-2.5 py-1.5 leading-snug max-w-56"
                  style={{ borderColor: t ? `color-mix(in srgb, ${t} 55%, var(--c-border))` : "var(--c-border-strong)", background: t ? `color-mix(in srgb, ${t} 10%, var(--c-panel))` : "var(--c-panel)" }}
                >
                  <div className="flex items-center gap-1.5">
                    {t && (tones[id] === "ok" ? <Check className="size-3 shrink-0" style={{ color: t }} /> : <CircleAlert className="size-3 shrink-0" style={{ color: t }} />)}
                    <span dir="auto" className="font-medium text-[12.5px]">
                      {n?.label || id}
                    </span>
                    {sub && <span className="text-[10.5px] text-faint">{sub}</span>}
                  </div>
                  {!!notes[id] && (
                    <div dir="auto" className="text-[11px] text-start" style={{ color: t ?? "var(--c-muted)" }}>
                      {text(notes[id])}
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      ))}
      {ws && (
        <Link to={`/w/${encodeURIComponent(ws)}/topology`} className="no-export block text-[11px] !text-faint hover:!text-fg !no-underline">
          Open the topology
        </Link>
      )}
    </figure>
  );
}

// ---------------------------------------------------------------- resources

const byName = new WeakMap<Resource[], Map<string, Resource>>();

/** inline code; the name of a workspace resource links to it */
export function InlineCode({ children, ...props }: React.HTMLAttributes<HTMLElement>) {
  const w = useWsMaybe();
  let r: Resource | undefined;
  if (w && typeof children === "string" && children.length < 80) {
    let m = byName.get(w.resources);
    if (!m) byName.set(w.resources, (m = new Map(w.resources.map((x) => [x.name, x]).reverse() as [string, Resource][])));
    r = m.get(children);
  }
  const code = (
    <code dir="ltr" {...props}>
      {children}
    </code>
  );
  if (!r) return code;
  return (
    <Link to={`/w/${encodeURIComponent(w!.name)}/resources?edit=${encodeURIComponent(r.kind + "/" + r.name)}`} title={[r.kind, r.environment, r.description].filter(Boolean).join(" · ")} className="res-chip">
      {code}
    </Link>
  );
}

// ---------------------------------------------------------------- widget

// The agent's own component: its HTML runs in a frame without the app's origin
// (sandbox) and without network access (CSP), so it can read nothing of
// opsdeck and send nothing out; it only reports its height.
const WIDGET_CSS = `*{box-sizing:border-box}html{accent-color:var(--c-accent)}
body{margin:0;padding:12px;font:13px/1.7 Vazirmatn,system-ui,-apple-system,"Segoe UI",sans-serif;color:var(--c-text);background:var(--c-panel-2)}
button,input,select,textarea{font:inherit;color:inherit;background:var(--c-panel);border:1px solid var(--c-border-strong);border-radius:6px;padding:3px 10px}
button{cursor:pointer}button:hover{border-color:var(--c-accent)}input[type=range],input[type=checkbox],input[type=radio]{padding:0}
table{border-collapse:collapse}th,td{border:1px solid var(--c-border);padding:4px 8px;text-align:start}th{background:var(--c-panel-3)}
code,pre{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12px}a{color:var(--c-accent)}h1,h2,h3,p{margin:0 0 8px}`;

function widgetDoc(src: string, rtl: boolean): string {
  return `<!doctype html><html${rtl ? ' dir="rtl" lang="fa"' : ""}><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; form-action 'none'; base-uri 'none'"><base target="_blank"><style>${themeVars()}${vazirFace}${WIDGET_CSS}</style></head><body dir="auto">${src}<script>(function(){function h(){parent.postMessage({opsdeckWidgetHeight:document.body.offsetHeight},"*")}h();addEventListener("load",h);new ResizeObserver(h).observe(document.body)})()</script></body></html>`;
}

function Widget({ src, rtl }: { src: string; rtl: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  const frame = useRef<HTMLIFrameElement>(null);
  const [seen, setSeen] = useState(false);
  const [h, setH] = useState(120);
  // a frame is the one costly block: it starts when it first scrolls into view
  useEffect(() => {
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && setSeen(true), { rootMargin: "300px" });
    io.observe(box.current!);
    return () => io.disconnect();
  }, []);
  useEffect(() => {
    const on = (e: MessageEvent) => {
      const v = (e.data as { opsdeckWidgetHeight?: unknown } | null)?.opsdeckWidgetHeight;
      if (e.source === frame.current?.contentWindow && typeof v === "number") setH(Math.min(900, Math.max(40, Math.ceil(v))));
    };
    window.addEventListener("message", on);
    return () => window.removeEventListener("message", on);
  }, []);
  useEffect(() => {
    pendingWidgets.set(box.current!, () => widgetDoc(src, rtl));
  }, [src, rtl]);
  // ponytail: the frame keeps the theme it started with; remount (reopen the chat) after switching themes
  const doc = useMemo(() => (seen ? widgetDoc(src, rtl) : ""), [seen, src, rtl]);
  return (
    <div ref={box} className="rb-widget rb-keep rounded-lg border border-line overflow-hidden bg-panel-2" style={{ height: h }}>
      {seen && <iframe ref={frame} title="Interactive component" sandbox="allow-scripts" srcDoc={doc} className="block w-full border-0" style={{ height: h }} />}
    </div>
  );
}

// ---------------------------------------------------------------- code

const FOLD_LINES = 24;

/** a code block with a copy button; long ones fold */
export function Code({ text: src, lang, children, ...props }: { text: string; lang?: string; children: ReactNode } & React.HTMLAttributes<HTMLPreElement>) {
  const [open, setOpen] = useState(false);
  const lines = src.split("\n").length;
  const long = lines > FOLD_LINES;
  return (
    <div className="rb-code group relative">
      <pre dir="ltr" {...props} style={long && !open ? { maxHeight: 340, overflowY: "auto" } : undefined}>
        {lang === "diff" ? (
          <code>
            {src.split("\n").map((l, i) => (
              <span key={i} className={cx("block", /^(\+\+\+|---) /.test(l) ? "diff-file" : l[0] === "+" ? "diff-add" : l[0] === "-" ? "diff-del" : l.startsWith("@@") && "diff-hunk")}>
                {l || " "}
              </span>
            ))}
          </code>
        ) : (
          children
        )}
      </pre>
      {src && (
        <div className="no-export absolute top-1 right-1 opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity">
          <CopyButton text={src} />
        </div>
      )}
      {long && (
        <button onClick={() => setOpen((o) => !o)} className="no-export mt-1 text-[11px] text-accent hover:underline">
          {open ? "Show less" : `Show all ${lines} lines`}
        </button>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- table

type Hast = { type?: string; tagName?: string; value?: string; children?: Hast[] };

const hastText = (n: Hast): string => (n.type === "text" ? (n.value ?? "") : (n.children ?? []).map(hastText).join(""));

/** the number a cell starts with (Persian digits too), or NaN */
function cellNumber(s: string): number {
  const t = s
    .replace(/[۰-۹]/g, (d) => String(d.charCodeAt(0) - 0x06f0))
    .replace(/[,٬]/g, "")
    .trim();
  return /^[-+]?\.?\d/.test(t) ? parseFloat(t) : NaN;
}

const FILTER_ROWS = 8;

/** a Markdown table: click a header to sort, and filter when it is long */
export function DataTable({ node, children, dir, ...props }: { node?: Hast; children?: ReactNode } & React.TableHTMLAttributes<HTMLTableElement>) {
  const ref = useRef<HTMLTableElement>(null);
  const [sort, setSort] = useState<{ col: number; desc: boolean } | null>(null);
  const [q, setQ] = useState("");
  const rows = useMemo(
    () =>
      (node?.children?.find((c) => c.tagName === "tbody")?.children ?? [])
        .filter((c) => c.tagName === "tr")
        .map((tr) => (tr.children ?? []).filter((c) => c.type === "element").map(hastText)),
    [node],
  );
  // dir="auto" on a table skips its cells (they carry their own dir): take the direction of the header
  const tableDir = dir === "auto" && node ? (/^[^A-Za-z\u0590-\u08ff]*[\u0590-\u08ff]/.test(hastText(node)) ? "rtl" : "ltr") : dir;
  const parts = Children.toArray(children).filter(isValidElement) as ReactElement<{ children?: ReactNode }>[];
  const body = parts.find((p) => p.type === "tbody");
  const trs = body ? Children.toArray(body.props.children).filter(isValidElement) : [];
  const sortable = rows.length > 1 && trs.length === rows.length;
  let order = rows.map((_, i) => i);
  if (sortable) {
    const needle = q.trim().toLowerCase();
    if (needle) order = order.filter((i) => rows[i].join(" ").toLowerCase().includes(needle));
    if (sort) {
      const key = (i: number) => rows[i][sort.col] ?? "";
      order.sort((a, b) => {
        const na = cellNumber(key(a));
        const nb = cellNumber(key(b));
        const c = !Number.isNaN(na) && !Number.isNaN(nb) ? na - nb : key(a).localeCompare(key(b), undefined, { numeric: true });
        return sort.desc ? -c : c;
      });
    }
  }
  // the header cells are rendered by react-markdown: mark the sorted one on the DOM
  useEffect(() => {
    ref.current?.querySelectorAll("thead th").forEach((th, i) => {
      if (sort?.col === i) th.setAttribute("aria-sort", sort.desc ? "descending" : "ascending");
      else th.removeAttribute("aria-sort");
    });
  });
  return (
    <div dir={tableDir}>
      {sortable && rows.length > FILTER_ROWS && (
        <input
          dir="auto"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder={`Filter ${rows.length} rows…`}
          aria-label="Filter rows"
          className="no-export mb-1.5 h-7 w-56 max-w-full rounded-md border border-line bg-panel-2 px-2 text-[12px] outline-none focus:border-accent"
        />
      )}
      <div className="rb-table">
        <table
          ref={ref}
          dir={tableDir}
          {...props}
          data-sortable={sortable || undefined}
          onClick={(e) => {
            const th = (e.target as HTMLElement).closest("th");
            if (!sortable || !th) return;
            const col = th.cellIndex;
            setSort((s) => (s?.col !== col ? { col, desc: false } : s.desc ? null : { col, desc: true }));
          }}
        >
          {sortable ? parts.map((p) => (p === body ? cloneElement(body, undefined, order.map((i) => trs[i])) : p)) : children}
        </table>
      </div>
    </div>
  );
}
