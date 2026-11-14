import { useEffect, useState } from "react";
import { Gauge, Loader2 } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { ContextUse, ProviderLimit, ProviderLimits } from "../../api/types";
import { cx } from "../../components/ui";
import { Popover } from "../../components/overlay";
import { fmtTokens, timeAgo } from "../../lib/format";

const NOT_REPORTED = "not reported";

/** "200k", "1M", "42.1k": token counts for limits and windows */
function fmtCount(n: number): string {
  return fmtTokens(n).replace(/\.0+([kM])$/, "$1");
}

/** usedShare is the share of the window in use, or undefined when either is not reported */
export function usedShare(c?: ContextUse): number | undefined {
  if (!c?.used || !c.window) return undefined;
  return Math.min(1, c.used / c.window);
}

/**
 * ContextMeter shows how full the model's context window is: the tokens of
 * the latest request against the window the provider reports. Nothing is
 * estimated: without usage or window it says so.
 */
export function ContextMeter({ context, model, className }: { context?: ContextUse; model?: string; className?: string }) {
  // numbers of another model (the session switched) say nothing about this one
  const c = context && (!model || context.model === model) ? context : undefined;
  const share = usedShare(c);
  let text: string;
  let title: string;
  if (!c?.used) {
    text = `context ${NOT_REPORTED}`;
    title = "The model has not reported token usage for this session yet";
  } else if (!c.window) {
    text = `context ${fmtCount(c.used)} · window ${NOT_REPORTED}`;
    title = `${c.used.toLocaleString()} tokens at the latest request; the provider does not report this model's context window`;
  } else {
    text = `context ${fmtCount(c.used)} / ${fmtCount(c.window)} · ${Math.round((share ?? 0) * 100)}%`;
    title = `${c.used.toLocaleString()} of ${c.window.toLocaleString()} tokens of the context window at the latest request`;
  }
  const tone = share === undefined ? "bg-faint/50" : share >= 0.95 ? "bg-err" : share >= 0.8 ? "bg-warn" : "bg-info";
  return (
    <span className={cx("inline-flex items-center gap-1.5 text-[11px] text-faint tabular-nums", className)} title={title} data-testid="context-meter">
      {share !== undefined && (
        <span className="relative h-1.5 w-12 rounded-full bg-panel-3 overflow-hidden" aria-hidden>
          <span className={cx("absolute inset-y-0 left-0 rounded-full", tone)} style={{ width: `${Math.max(2, share * 100)}%` }} />
        </span>
      )}
      {text}
    </span>
  );
}

function fmtReset(reset?: string): string {
  if (!reset) return "";
  const t = new Date(reset).getTime();
  if (Number.isNaN(t)) return "";
  const s = Math.round((t - Date.now()) / 1000);
  if (s <= 0) return "reset";
  if (s < 90) return `resets in ${s}s`;
  if (s < 90 * 60) return `resets in ${Math.round(s / 60)}m`;
  return `resets ${new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}

function money(n: number): string {
  return n.toLocaleString(undefined, { maximumFractionDigits: 2, minimumFractionDigits: n % 1 ? 2 : 0 });
}

/** limitText describes one reported limit with only the numbers the provider sent */
export function limitText(l: ProviderLimit): string {
  const credits = l.name === "credits";
  const f = credits ? money : fmtCount;
  const parts: string[] = [];
  if (l.remaining !== undefined && l.limit !== undefined) parts.push(`${f(l.remaining)} / ${f(l.limit)} left`);
  else if (l.remaining !== undefined) parts.push(`${f(l.remaining)} left`);
  else if (l.limit !== undefined) parts.push(`limit ${f(l.limit)}`);
  else if (credits) parts.push("no limit");
  if (l.used !== undefined) parts.push(`${f(l.used)} used`);
  if (l.period) parts.push(l.period);
  const reset = fmtReset(l.reset);
  if (reset) parts.push(reset);
  return parts.join(" · ");
}

/**
 * LimitsButton lists every connected provider's limits as the provider
 * reported them (rate limit headers of its latest response, or its quota
 * API); a provider that reports none shows "not reported".
 */
export function LimitsButton({ provider, tick }: { provider?: string; tick: number }) {
  const [open, setOpen] = useState(false);
  const [list, setList] = useState<ProviderLimits[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    let live = true;
    api
      .providerLimits()
      .then((l) => live && (setList(l), setError("")))
      .catch((e) => live && setError(errMsg(e)));
    return () => {
      live = false;
    };
  }, [open, tick]);
  // the session's provider first
  const sorted = [...(list ?? [])].sort((a, b) => Number(b.id === provider) - Number(a.id === provider));
  return (
    <div className="relative">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-1.5 h-7 px-2 rounded-md border border-line bg-panel-2 text-[11.5px] text-muted hover:text-fg"
        title="Limits of the connected model providers"
      >
        <Gauge className="size-3.5" /> Limits
      </button>
      <Popover open={open} onClose={() => setOpen(false)} className="right-0 mt-1 w-[380px] p-1">
        <div className="px-2.5 py-1.5 text-[11px] text-faint">Only what each provider reports: rate limits from its latest response, or its quota API. Nothing is estimated.</div>
        {error && <div className="px-2.5 py-1.5 text-[12px] text-err">{error}</div>}
        {!list && !error && (
          <div className="px-2.5 py-2 text-muted">
            <Loader2 className="size-3.5 animate-spin" />
          </div>
        )}
        {list && list.length === 0 && <div className="px-2.5 py-2 text-[12px] text-muted">No model providers are configured.</div>}
        {sorted.map((p) => (
          <div key={p.id} className="rounded-md px-2.5 py-2" data-testid={`provider-limits-${p.id}`}>
            <div className="flex items-center gap-1.5">
              <span className="text-[12.5px] font-medium">{p.name || p.id}</span>
              {p.id === provider && <span className="text-[10.5px] text-info">this session</span>}
              <span className="flex-1" />
              {p.observed && <span className="text-[10.5px] text-faint">{timeAgo(p.observed)}</span>}
            </div>
            {p.limits.length === 0 ? (
              <div className="text-[11.5px] text-muted mt-0.5">{NOT_REPORTED}</div>
            ) : (
              <div className="mt-1 space-y-0.5">
                {p.limits.map((l, i) => (
                  <div key={i} className="flex items-baseline gap-2 text-[11.5px]">
                    <span className="w-24 shrink-0 text-muted">{l.name}</span>
                    <span className="tabular-nums">{limitText(l)}</span>
                  </div>
                ))}
              </div>
            )}
            {p.error && <div className="text-[11px] text-err mt-0.5">quota: {p.error}</div>}
          </div>
        ))}
      </Popover>
    </div>
  );
}
