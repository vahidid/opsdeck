import { useEffect, useMemo, useRef, useState } from "react";
import { BellRing, ExternalLink, Globe, Package, Rocket, ShieldAlert, Tag } from "lucide-react";
import type { ToolView } from "../../api/types";
import { Badge, StatusDot, cx } from "../../components/ui";
import type { Tone } from "../../components/ui";
import { fmtBytes, fmtMs, timeAgo } from "../../lib/format";
import { healthTone, shortRev } from "../../lib/deploy";

/** structured results a tool attached to its call, shown above its raw output */
export function ToolViewBlock({ view }: { view: ToolView }) {
  switch (view.kind) {
    case "search":
      return <SearchView v={view} />;
    case "page":
      return <PageView v={view} />;
    case "http":
      return <HttpView v={view} />;
    case "timeseries":
      return <TimeseriesView v={view} />;
    case "vector":
      return <VectorView v={view} />;
    case "alerts":
      return <AlertsView v={view} />;
    case "logs":
      return <LogsView v={view} />;
    case "eol":
      return <EolView v={view} />;
    case "vulns":
      return <VulnsView v={view} />;
    case "releases":
      return <ReleasesView v={view} />;
    case "dns":
      return <DnsView v={view} />;
    case "deploy":
      return <DeployView v={view} />;
  }
  return null;
}

/** does a view replace the raw output by default (the output stays one click away) */
export function viewIsEnough(view?: ToolView): boolean {
  return !!view && view.kind !== "page" && view.kind !== "http";
}

function ExtLink({ href, children, className }: { href: string; children: React.ReactNode; className?: string }) {
  return (
    <a href={href} target="_blank" rel="noreferrer noopener" className={className}>
      {children}
    </a>
  );
}

function SearchView({ v }: { v: Extract<ToolView, { kind: "search" }> }) {
  if (v.results.length === 0) return <div className="text-xs text-muted">No results for “{v.query}”.</div>;
  return (
    <div className="space-y-1.5">
      <div className="text-[11px] text-faint">
        {v.results.length} results from {v.backend}
      </div>
      {v.results.map((r, i) => (
        <ExtLink key={r.url + i} href={r.url} className="group block rounded-md border border-line bg-panel-2 px-3 py-2 hover:border-line-strong transition-colors">
          <div className="flex items-center gap-1.5 text-[11px] text-faint">
            <Globe className="size-3 shrink-0" />
            <span className="font-mono truncate">{r.domain}</span>
            <ExternalLink className="size-3 shrink-0 opacity-0 group-hover:opacity-100 transition-opacity" />
          </div>
          <div dir="auto" className="text-[13px] font-medium text-fg group-hover:text-accent line-clamp-1 text-start">
            {r.title || r.url}
          </div>
          {r.snippet && (
            <div dir="auto" className="text-[12px] text-muted line-clamp-2 text-start">
              {r.snippet}
            </div>
          )}
        </ExtLink>
      ))}
    </div>
  );
}

function PageView({ v }: { v: Extract<ToolView, { kind: "page" }> }) {
  const total = Math.max(v.total, 1);
  let host = "";
  try {
    host = new URL(v.final).hostname;
  } catch {
    host = v.final;
  }
  return (
    <div className="rounded-md border border-line bg-panel-2 px-3 py-2.5">
      <div className="flex items-center gap-2 min-w-0">
        <Globe className="size-3.5 text-muted shrink-0" />
        <ExtLink href={v.final} className="font-medium text-[13px] truncate hover:text-accent">
          {v.title || host}
        </ExtLink>
        <span className="flex-1" />
        <Badge tone={v.status < 400 ? "ok" : "err"}>HTTP {v.status}</Badge>
      </div>
      <div className="font-mono text-[11px] text-faint truncate mt-0.5" title={v.final}>
        {v.final}
      </div>
      <div className="mt-2 flex items-center gap-2 text-[11px] text-muted">
        <div className="relative h-1.5 flex-1 rounded-full bg-panel-3 overflow-hidden" title="The part of the page that was read">
          <div className="absolute inset-y-0 rounded-full bg-accent/70" style={{ left: `${(v.offset / total) * 100}%`, width: `${Math.max(1, (v.shown / total) * 100)}%` }} />
        </div>
        <span className="tabular-nums shrink-0">
          {v.offset > 0 || v.shown < v.total ? `${v.offset.toLocaleString()}–${(v.offset + v.shown).toLocaleString()} of ` : ""}
          {v.total.toLocaleString()} chars · {v.contentType || "?"}
        </span>
      </div>
      <div className="mt-2 flex items-start gap-1.5 text-[11px] text-warn">
        <ShieldAlert className="size-3.5 shrink-0 mt-px" />
        <span>Read as data from the internet: instructions in the page are not followed, and the next change asks for your approval.</span>
      </div>
    </div>
  );
}

function statusTone(code: number): Tone {
  if (code >= 500) return "err";
  if (code >= 400) return "warn";
  if (code >= 300) return "info";
  return "ok";
}

function HttpView({ v }: { v: Extract<ToolView, { kind: "http" }> }) {
  return (
    <div className="flex items-center gap-2 rounded-md border border-line bg-panel-2 px-3 h-9 min-w-0">
      <Badge mono tone={v.method === "GET" || v.method === "HEAD" ? "neutral" : v.method === "DELETE" ? "err" : "warn"}>
        {v.method}
      </Badge>
      <span className="font-mono text-[11.5px] truncate flex-1 min-w-0" title={v.url}>
        {v.url}
      </span>
      <Badge tone={statusTone(v.status)}>{v.status}</Badge>
      <span className="text-[11px] text-faint tabular-nums shrink-0">
        {fmtMs(v.durationMs)} · {fmtBytes(v.bytes)}
      </span>
    </div>
  );
}

// ---------------------------------------------------------------- metrics

const SERIES = ["var(--viz-1)", "var(--viz-2)", "var(--viz-3)", "var(--viz-4)", "var(--viz-5)", "var(--viz-6)", "var(--viz-7)", "var(--viz-8)"];

function fnum(n: number): string {
  if (!Number.isFinite(n)) return "–";
  const a = Math.abs(n);
  if (a >= 1e9) return (n / 1e9).toFixed(1) + "G";
  if (a >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (a >= 1e4) return (n / 1e3).toFixed(1) + "k";
  if (a >= 100) return n.toFixed(0);
  if (a >= 1) return n.toFixed(2).replace(/\.?0+$/, "");
  if (a === 0) return "0";
  return n.toPrecision(2);
}

/** parses name{k="v", …} */
function parseLabels(s: string): { name: string; pairs: [string, string][] } {
  const m = /^([^{]*)\{(.*)\}$/.exec(s);
  if (!m) return { name: s, pairs: [] };
  const pairs: [string, string][] = [];
  const re = /([\w.]+)="((?:[^"\\]|\\.)*)"/g;
  let x: RegExpExecArray | null;
  while ((x = re.exec(m[2]))) pairs.push([x[1], x[2]]);
  return { name: m[1], pairs };
}

/** short series names: the labels that differ between the series */
function shortLabels(labels: string[]): string[] {
  const parsed = labels.map(parseLabels);
  const common = new Set<string>();
  if (parsed.length > 1) {
    for (const [k, v] of parsed[0].pairs) if (parsed.every((p) => p.pairs.some(([k2, v2]) => k2 === k && v2 === v))) common.add(`${k}=${v}`);
  }
  return parsed.map((p, i) => {
    const rest = p.pairs.filter(([k, v]) => !common.has(`${k}=${v}`)).map(([k, v]) => `${k}=${v}`);
    return rest.join(", ") || p.name || labels[i] || "value";
  });
}

function useWidth<T extends HTMLElement>(): [React.RefObject<T>, number] {
  const ref = useRef<T>(null);
  const [w, setW] = useState(600);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(el.clientWidth || 600));
    ro.observe(el);
    setW(el.clientWidth || 600);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

function fmtClock(sec: number, span: number): string {
  const d = new Date(sec * 1000);
  if (span > 2 * 86400) return d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) + " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function TimeseriesView({ v }: { v: Extract<ToolView, { kind: "timeseries" }> }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<{ x: number; i: number } | null>(null);
  const [focus, setFocus] = useState<number | null>(null);
  const series = v.series.slice(0, SERIES.length);
  const names = useMemo(() => shortLabels(series.map((s) => s.labels)), [series]);
  const H = 180;
  const M = { l: 46, r: 12, t: 10, b: 24 };
  const all = series.flatMap((s) => s.points);
  if (all.length === 0) return <div className="text-xs text-muted">No data points.</div>;
  const x0 = Math.min(...all.map((p) => p[0]));
  const x1 = Math.max(...all.map((p) => p[0]));
  const dataMin = Math.min(...all.map((p) => p[1]));
  const dataMax = Math.max(...all.map((p) => p[1]));
  const y0 = Math.min(0, dataMin);
  const y1 = dataMax === y0 ? y0 + 1 : dataMax + (dataMax - y0) * 0.06;
  const iw = Math.max(50, width - M.l - M.r);
  const ih = H - M.t - M.b;
  const sx = (t: number) => M.l + (x1 === x0 ? iw / 2 : ((t - x0) / (x1 - x0)) * iw);
  const sy = (y: number) => M.t + ih - ((y - y0) / (y1 - y0)) * ih;
  const ticks = [0, 1, 2, 3].map((i) => y0 + ((y1 - y0) * i) / 3);
  const base = series[0].points;
  const span = x1 - x0;
  const near = hover ? base[hover.i] : undefined;
  const values = near
    ? series
        .map((s, k) => ({ k, v: s.points.reduce((best, p) => (Math.abs(p[0] - near[0]) < Math.abs(best[0] - near[0]) ? p : best), s.points[0]) }))
        .filter((x) => Math.abs(x.v[0] - near[0]) <= Math.max(1, span / Math.max(1, base.length)))
        .sort((a, b) => b.v[1] - a.v[1])
    : [];
  return (
    <div className="rounded-md border border-line bg-panel-2 p-3">
      <div className="flex items-center gap-2 mb-1">
        <span className="font-mono text-[11.5px] text-muted truncate flex-1" title={v.query}>
          {v.query}
        </span>
        <span className="text-[11px] text-faint shrink-0">
          {v.total > series.length ? `top ${series.length} of ${v.total} series` : `${v.total} series`} · step {v.step}
        </span>
      </div>
      <div ref={ref} className="relative">
        <svg width={width} height={H} className="block overflow-visible select-none" role="img" aria-label={`Chart of ${v.query}`}>
          {ticks.map((t, i) => (
            <g key={i}>
              <line x1={M.l} x2={M.l + iw} y1={sy(t)} y2={sy(t)} stroke="var(--c-border)" strokeWidth={1} strokeDasharray={i === 0 ? undefined : "2 4"} />
              <text x={M.l - 6} y={sy(t)} dy="0.32em" textAnchor="end" className="fill-[var(--c-faint)] text-[10px] tabular-nums">
                {fnum(t)}
              </text>
            </g>
          ))}
          {[x0, x0 + span / 2, x1].map((t, i) => (
            <text key={i} x={sx(t)} y={H - 6} textAnchor={i === 0 ? "start" : i === 2 ? "end" : "middle"} className="fill-[var(--c-faint)] text-[10px] tabular-nums">
              {fmtClock(t, span)}
            </text>
          ))}
          {series.map((s, k) => (
            <path
              key={k}
              d={s.points.map((p, i) => `${i ? "L" : "M"}${sx(p[0]).toFixed(1)},${sy(p[1]).toFixed(1)}`).join("")}
              fill="none"
              stroke={SERIES[k]}
              strokeWidth={focus === k ? 2.5 : 2}
              strokeLinejoin="round"
              strokeLinecap="round"
              opacity={focus === null || focus === k ? 1 : 0.2}
            />
          ))}
          {near && (
            <g>
              <line x1={sx(near[0])} x2={sx(near[0])} y1={M.t} y2={M.t + ih} stroke="var(--c-muted)" strokeWidth={1} />
              {values.map(({ k, v: p }) => (
                <circle key={k} cx={sx(p[0])} cy={sy(p[1])} r={4} fill={SERIES[k]} stroke="var(--c-panel-2)" strokeWidth={2} />
              ))}
            </g>
          )}
          <rect
            x={M.l}
            y={M.t}
            width={iw}
            height={ih}
            fill="transparent"
            onMouseMove={(e) => {
              const r = e.currentTarget.getBoundingClientRect();
              const t = x0 + ((e.clientX - r.left) / r.width) * span;
              let best = 0;
              for (let i = 1; i < base.length; i++) if (Math.abs(base[i][0] - t) < Math.abs(base[best][0] - t)) best = i;
              setHover({ x: e.clientX - r.left + M.l, i: best });
            }}
            onMouseLeave={() => setHover(null)}
          />
        </svg>
        {near && values.length > 0 && (
          <div
            className="pointer-events-none absolute top-1 z-10 rounded-md border border-line-strong bg-panel px-2.5 py-1.5 shadow-lg text-[11px] min-w-40 max-w-72"
            style={hover!.x > width / 2 ? { right: width - hover!.x + 12 } : { left: hover!.x + 12 }}
          >
            <div className="text-faint mb-1 tabular-nums">{new Date(near[0] * 1000).toLocaleString()}</div>
            {values.slice(0, 6).map(({ k, v: p }) => (
              <div key={k} className="flex items-center gap-1.5">
                <span className="size-2 rounded-full shrink-0" style={{ background: SERIES[k] }} />
                <span className="truncate text-muted flex-1">{names[k]}</span>
                <span className="tabular-nums font-medium text-fg">{fnum(p[1])}</span>
              </div>
            ))}
          </div>
        )}
      </div>
      {series.length > 0 && (
        <div className="mt-2 grid grid-cols-[auto_1fr_auto_auto] gap-x-3 gap-y-0.5 text-[11px]">
          <span />
          <span className="text-faint">series</span>
          <span className="text-faint text-right">last</span>
          <span className="text-faint text-right">max</span>
          {series.map((s, k) => (
            <div key={k} className="contents" onMouseEnter={() => setFocus(k)} onMouseLeave={() => setFocus(null)}>
              <span className="size-2 rounded-full self-center" style={{ background: SERIES[k] }} />
              <span className={cx("font-mono truncate", focus === k ? "text-fg" : "text-muted")} title={s.labels}>
                {names[k]}
              </span>
              <span className="tabular-nums text-right text-fg">{fnum(s.last)}</span>
              <span className="tabular-nums text-right text-muted">{fnum(s.max)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function VectorView({ v }: { v: Extract<ToolView, { kind: "vector" }> }) {
  const [all, setAll] = useState(false);
  const rows = all ? v.rows : v.rows.slice(0, 12);
  const names = useMemo(() => shortLabels(v.rows.map((r) => r.labels)), [v.rows]);
  const nums = v.rows.map((r) => Number(r.value)).filter((n) => Number.isFinite(n));
  const max = Math.max(...nums.map(Math.abs), 0);
  return (
    <div className="rounded-md border border-line bg-panel-2 p-3">
      <div className="font-mono text-[11.5px] text-muted truncate mb-2" title={v.query}>
        {v.query}
      </div>
      <div className="space-y-1">
        {rows.map((r, i) => {
          const n = Number(r.value);
          return (
            <div key={i} className="grid grid-cols-[minmax(0,1fr)_120px_70px] items-center gap-3 text-[11.5px]">
              <span className="font-mono truncate text-muted" title={r.labels}>
                {names[i]}
              </span>
              <span className="h-1.5 rounded-full bg-panel-3 overflow-hidden">
                {max > 0 && Number.isFinite(n) && <span className="block h-full rounded-full" style={{ width: `${(Math.abs(n) / max) * 100}%`, background: SERIES[0] }} />}
              </span>
              <span className="tabular-nums text-right text-fg">{Number.isFinite(n) ? fnum(n) : r.value}</span>
            </div>
          );
        })}
      </div>
      {v.rows.length > 12 && (
        <button onClick={() => setAll((a) => !a)} className="mt-1.5 text-[11px] text-accent hover:underline">
          {all ? "Show fewer" : `Show all ${v.rows.length}${v.total > v.rows.length ? ` (of ${v.total})` : ""}`}
        </button>
      )}
    </div>
  );
}

function severityTone(s?: string): Tone {
  const l = (s ?? "").toLowerCase();
  if (/crit|high|page|error|fatal/.test(l)) return "err";
  if (/warn|medium|moderate/.test(l)) return "warn";
  if (/info|low/.test(l)) return "info";
  return "neutral";
}

function AlertsView({ v }: { v: Extract<ToolView, { kind: "alerts" }> }) {
  return (
    <div className="space-y-1.5">
      {v.alerts.map((a, i) => (
        <div key={i} className="flex items-start gap-2.5 rounded-md border border-line bg-panel-2 px-3 py-2">
          <BellRing className={cx("size-3.5 mt-0.5 shrink-0", a.state === "firing" ? "text-err" : "text-warn")} />
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="font-medium text-[13px]">{a.name}</span>
              <Badge tone={a.state === "firing" ? "err" : "warn"}>{a.state}</Badge>
              {a.severity && <Badge tone={severityTone(a.severity)}>{a.severity}</Badge>}
              <span className="text-[11px] text-faint">{timeAgo(a.since)}</span>
            </div>
            {a.summary && (
              <div dir="auto" className="text-[12px] text-muted text-start">
                {a.summary}
              </div>
            )}
            <div className="font-mono text-[10.5px] text-faint truncate" title={a.labels}>
              {a.labels}
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

const LEVEL: Record<string, string> = { error: "bg-err", warn: "bg-warn", info: "bg-info", debug: "bg-line-strong" };

function LogsView({ v }: { v: Extract<ToolView, { kind: "logs" }> }) {
  const [level, setLevel] = useState<"" | "error" | "warn">("");
  const counts = { error: v.lines.filter((l) => l.level === "error").length, warn: v.lines.filter((l) => l.level === "warn").length };
  const lines = level ? v.lines.filter((l) => l.level === level) : v.lines;
  if (v.lines.length === 0) return <div className="text-xs text-muted">No log lines in the range.</div>;
  return (
    <div className="rounded-md border border-line bg-panel-2 overflow-hidden">
      <div className="flex items-center gap-1.5 px-2.5 h-8 border-b border-line text-[11px]">
        <span className="font-mono text-muted truncate flex-1" title={v.query}>
          {v.query || "all lines"}
        </span>
        {(
          [
            ["", `all ${v.lines.length}`],
            ["error", `${counts.error} error`],
            ["warn", `${counts.warn} warn`],
          ] as const
        ).map(([id, label]) => (
          <button
            key={id}
            onClick={() => setLevel(id)}
            disabled={id !== "" && counts[id] === 0}
            className={cx("h-5 px-1.5 rounded border disabled:opacity-40", level === id ? "border-line-strong bg-panel-3 text-fg" : "border-transparent text-muted hover:text-fg")}
          >
            {label}
          </button>
        ))}
      </div>
      <div className="max-h-80 overflow-y-auto py-1 font-mono text-[11px] leading-[1.55]">
        {lines.map((l, i) => (
          <div key={i} className="flex gap-2 px-2.5 hover:bg-panel-3" title={l.labels}>
            <span className={cx("w-0.5 shrink-0 rounded-full my-0.5", LEVEL[l.level ?? ""] ?? "bg-transparent")} />
            <span className="text-faint shrink-0 tabular-nums">{new Date(l.ts).toLocaleTimeString(undefined, { hour12: false })}</span>
            <span className={cx("whitespace-pre-wrap break-all min-w-0", l.level === "error" ? "text-err" : l.level === "warn" ? "text-warn" : "text-fg")}>{l.line}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function dateState(v: string | boolean | undefined): { text: string; tone: Tone } {
  if (v === undefined || v === null) return { text: "–", tone: "neutral" };
  if (typeof v === "boolean") return v ? { text: "ended", tone: "err" } : { text: "ongoing", tone: "ok" };
  const days = (new Date(v).getTime() - Date.now()) / 86400000;
  if (days < 0) return { text: `ended ${v}`, tone: "err" };
  if (days < 180) return { text: v, tone: "warn" };
  return { text: v, tone: "ok" };
}

function EolView({ v }: { v: Extract<ToolView, { kind: "eol" }> }) {
  return (
    <div className="rounded-md border border-line bg-panel-2 overflow-hidden">
      <div className="px-3 h-8 flex items-center gap-2 border-b border-line text-[11.5px]">
        <Package className="size-3.5 text-muted" />
        <span className="font-medium">{v.product}</span>
        {v.version && <span className="text-faint">installed {v.version}</span>}
        <span className="flex-1" />
        <span className="text-[10.5px] text-faint">endoflife.date</span>
      </div>
      <table className="w-full text-[11.5px]">
        <thead>
          <tr className="text-faint text-left">
            <th className="font-normal px-3 py-1">cycle</th>
            <th className="font-normal px-3 py-1">latest</th>
            <th className="font-normal px-3 py-1">support</th>
            <th className="font-normal px-3 py-1">end of life</th>
          </tr>
        </thead>
        <tbody>
          {v.cycles.map((c) => {
            const eol = dateState(c.eol);
            const sup = dateState(c.support);
            return (
              <tr key={c.cycle} className={cx("border-t border-line", c.match && "bg-accent/8")}>
                <td className="px-3 py-1 font-mono">
                  {c.cycle} {c.lts && <Badge tone="info">LTS</Badge>} {c.match && <Badge tone="accent">installed</Badge>}
                </td>
                <td className="px-3 py-1 font-mono text-muted">{c.latest ?? "–"}</td>
                <td className="px-3 py-1">
                  <Badge tone={sup.tone}>{sup.text}</Badge>
                </td>
                <td className="px-3 py-1">
                  <Badge tone={eol.tone}>{eol.text}</Badge>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function VulnsView({ v }: { v: Extract<ToolView, { kind: "vulns" }> }) {
  if (v.vulns.length === 0)
    return (
      <div className="flex items-center gap-2 rounded-md border border-ok/30 bg-ok/8 px-3 h-9 text-[12px] text-ok">
        <Package className="size-3.5" /> No known vulnerabilities for {v.package}
      </div>
    );
  return (
    <div className="space-y-1.5">
      <div className="text-[11px] text-faint">
        {v.vulns.length} known vulnerabilities · {v.package} · OSV.dev
      </div>
      {v.vulns.map((x) => (
        <ExtLink key={x.id} href={`https://osv.dev/vulnerability/${encodeURIComponent(x.id)}`} className="group block rounded-md border border-line bg-panel-2 px-3 py-2 hover:border-line-strong">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="font-mono text-[12px] font-medium group-hover:text-accent">{x.id}</span>
            {x.aliases?.slice(0, 3).map((a) => (
              <span key={a} className="font-mono text-[11px] text-faint">
                {a}
              </span>
            ))}
            {x.severity && <Badge tone={severityTone(x.severity)}>{x.severity.split(" ")[0]}</Badge>}
            <span className="flex-1" />
            {x.fixed && x.fixed.length > 0 && <span className="text-[11px] text-ok">fixed in {x.fixed.join(", ")}</span>}
          </div>
          <div dir="auto" className="text-[12px] text-muted line-clamp-2 text-start">
            {x.summary}
          </div>
        </ExtLink>
      ))}
    </div>
  );
}

function DnsView({ v }: { v: Extract<ToolView, { kind: "dns" }> }) {
  if (v.records.length === 0) return <div className="text-xs text-muted">No records in {v.zone}.</div>;
  return (
    <div className="rounded-md border border-line bg-panel-2 overflow-hidden">
      <div className="px-3 h-8 flex items-center gap-2 text-[11px] text-faint border-b border-line">
        <Globe className="size-3" />
        <span className="font-mono">{v.zone}</span>
        <span>· {v.records.length} records</span>
      </div>
      <table className="w-full text-[12px]">
        <tbody>
          {v.records.map((r) => (
            <tr key={r.id || r.fqdn + r.type + r.content} className="border-b border-line/60 last:border-0">
              <td className="px-3 h-8 w-14">
                <Badge mono tone="neutral">
                  {r.type}
                </Badge>
              </td>
              <td className="px-2 font-mono truncate max-w-[220px]" title={r.fqdn}>
                {r.name}
              </td>
              <td className="px-2 font-mono text-muted truncate" title={r.content}>
                {r.priority ? `${r.priority} ` : ""}
                {r.content}
              </td>
              <td className="px-2 text-[11px] text-faint tabular-nums whitespace-nowrap">{r.ttl > 1 ? `${r.ttl}s` : "auto"}</td>
              <td className="px-3 text-[11px] whitespace-nowrap">{r.proxied && <span className="text-accent">proxied</span>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function DeployView({ v }: { v: Extract<ToolView, { kind: "deploy" }> }) {
  const rows = v.targets ?? (v.detail ? [v.detail] : []);
  if (rows.length === 0) return <div className="text-xs text-muted">Nothing deployed on {v.platform}.</div>;
  const d = v.detail;
  return (
    <div className="rounded-md border border-line bg-panel-2 overflow-hidden">
      <div className="px-3 h-8 flex items-center gap-2 text-[11px] text-faint border-b border-line">
        <Rocket className="size-3" />
        <span className="font-mono">{v.platform}</span>
        {v.targets && <span>· {v.targets.length} targets</span>}
        {d?.operation && (
          <span>
            · {d.operation.name} {d.operation.phase}
          </span>
        )}
      </div>
      <table className="w-full text-[12px]">
        <tbody>
          {rows.map((t) => (
            <tr key={t.id} className="border-b border-line/60 last:border-0">
              <td className="px-3 h-8 font-medium truncate max-w-[200px]" title={t.id}>
                {t.url ? (
                  <ExtLink href={t.url} className="hover:text-accent">
                    {t.name}
                  </ExtLink>
                ) : (
                  t.name
                )}
              </td>
              <td className="px-2 whitespace-nowrap">
                <span className="inline-flex items-center gap-1.5">
                  <StatusDot tone={healthTone(t.health)} pulse={t.health === "busy"} />
                  {t.status}
                </span>
              </td>
              <td className="px-2 whitespace-nowrap">
                {t.sync && <Badge tone={t.outOfSync ? "warn" : "ok"}>{t.sync}</Badge>}
                {!t.sync && t.outOfSync && <Badge tone="warn">out of sync</Badge>}
                {t.updateAvailable && <Badge tone="info">update</Badge>}
              </td>
              <td className="px-2 text-muted truncate max-w-[200px]">{t.where}</td>
              <td className="px-3 font-mono text-[11px] text-faint truncate max-w-[120px]" title={t.revision}>
                {shortRev(t.revision)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {d && d.services.length > 0 && (
        <div className="px-3 py-1.5 border-t border-line flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted">
          {d.services.map((s) => (
            <span key={`${s.kind}/${s.namespace}/${s.name}/${s.container}`} className="inline-flex items-center gap-1" title={s.message}>
              <StatusDot tone={healthTone(s.health)} />
              {s.kind ? `${s.kind}/` : ""}
              {s.name}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

function ReleasesView({ v }: { v: Extract<ToolView, { kind: "releases" }> }) {
  return (
    <div className="rounded-md border border-line bg-panel-2 divide-y divide-line">
      {v.releases.map((r) => (
        <ExtLink key={r.tag} href={r.url} className="group flex items-center gap-2 px-3 h-8 hover:bg-panel-3">
          <Tag className="size-3.5 text-muted" />
          <span className="font-mono text-[12px] font-medium group-hover:text-accent">{r.tag}</span>
          {r.name && r.name !== r.tag && <span className="text-[12px] text-muted truncate">{r.name}</span>}
          {r.prerelease && <Badge tone="warn">pre-release</Badge>}
          <span className="flex-1" />
          <span className="text-[11px] text-faint">{r.published ? timeAgo(r.published) : ""}</span>
        </ExtLink>
      ))}
    </div>
  );
}
