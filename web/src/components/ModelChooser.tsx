import { useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangle, RefreshCw } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { ProviderConfig, ProviderModels } from "../api/types";
import { isAgentKind } from "../api/types";
import { Button, ChipInput, FormField, SearchInput, cx } from "./ui";

/** Lists over this size start with only "default" selected. */
const BIG_LIST = 30;
const PRESELECT = 5;

/**
 * Model selection for one provider: asks the agent CLI (or the API) which
 * models it offers and lets the user tick the ones that show up in the model
 * picker. Other ids can still be typed in.
 */
export function ModelChooser({ p, onChange, autoLoad }: { p: ProviderConfig; onChange: (v: Partial<ProviderConfig>) => void; autoLoad?: boolean }) {
  const [list, setList] = useState<ProviderModels | null>(null);
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState("");
  const agent = isAgentKind(p.type);
  // the latest props, for loads that finish after more edits
  const cur = useRef(p);
  cur.current = p;
  // what the list depends on; a change (or another provider taking this
  // slot) drops the list and any load still running
  const sig = JSON.stringify([p.type, p.command, p.args, p.env, p.baseUrl]);
  const seq = useRef(0);
  const sigRef = useRef(sig);
  useEffect(() => {
    if (sig === sigRef.current) return;
    sigRef.current = sig;
    seq.current++;
    setList(null);
    setLoading(false);
  }, [sig]);

  async function load(preselect: boolean) {
    const n = ++seq.current;
    setLoading(true);
    try {
      const l = await api.providerModels(cur.current);
      if (n !== seq.current) return;
      setList(l);
      const names = { ...(cur.current.modelNames ?? {}) };
      for (const m of l.models) if (m.name && m.name !== m.id) names[m.id] = m.name;
      let models = cur.current.models;
      if (preselect && l.source !== "builtin" && l.models.length > 0) {
        const ids = l.models.map((m) => m.id);
        models = l.models.length > BIG_LIST ? ids.filter((id) => id === "default") : ids.slice(0, PRESELECT);
        if (models.length === 0) models = ids.slice(0, 1);
      }
      onChange({ models, modelNames: pick(names, models) });
    } catch (e) {
      if (n === seq.current) setList({ models: [], source: "builtin", error: errMsg(e) });
    } finally {
      if (n === seq.current) setLoading(false);
    }
  }

  useEffect(() => {
    if (autoLoad) void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  // an API key was just chosen: ask the API
  const keyRef = useRef(p.apiKeyRef);
  useEffect(() => {
    if (!agent && p.apiKeyRef && p.apiKeyRef !== keyRef.current && !loading) void load(p.models.length === 0);
    keyRef.current = p.apiKeyRef;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.apiKeyRef]);

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const all = list?.models ?? [];
    return q ? all.filter((m) => `${m.id} ${m.name} ${m.description ?? ""}`.toLowerCase().includes(q)) : all;
  }, [list, filter]);

  const names = p.modelNames ?? {};
  const toggle = (id: string, name: string, on: boolean) => {
    const models = on ? [...p.models.filter((m) => m !== id), id] : p.models.filter((m) => m !== id);
    onChange({ models, modelNames: pick({ ...names, ...(name && name !== id ? { [id]: name } : {}) }, models) });
  };
  const listed = new Set(list?.models.map((m) => m.id));
  const source = list?.source === "cli" ? "reported by the CLI" : list?.source === "api" ? "listed by the API" : "built-in list";

  return (
    <FormField
      label="Models"
      help={
        agent
          ? "Tick the models to offer in the model picker. “default” uses the agent's own default; other ids the agent accepts can be typed below."
          : "Tick the models to offer in the model picker, or type model ids below (Enter after each)."
      }
    >
      <div className="rounded-md border border-line bg-panel">
        <div className="flex items-center gap-2 px-2.5 py-1.5 border-b border-line">
          <Button size="xs" icon={<RefreshCw className={cx("size-3", loading && "animate-spin")} />} onClick={() => void load(false)} disabled={loading}>
            {list ? "Reload" : agent ? "Load models from the CLI" : "Load models from the API"}
          </Button>
          <span className="text-[11.5px] text-faint truncate">
            {loading
              ? agent
                ? "Asking the agent… (a few seconds, no tokens used)"
                : "Asking the API…"
              : list
                ? `${list.models.length} model${list.models.length === 1 ? "" : "s"} · ${source} · ${p.models.length} selected`
                : agent
                  ? "Ask the installed CLI which models your login can use"
                  : "Ask the API which models this key can use"}
          </span>
          <div className="flex-1" />
          {list && list.models.length > 8 && <SearchInput value={filter} onChange={setFilter} placeholder="Filter models…" className="w-48" />}
        </div>
        {list?.error && (
          <div className="flex gap-1.5 px-2.5 py-1.5 text-[11.5px] text-warn border-b border-line whitespace-pre-wrap break-words">
            <AlertTriangle className="size-3.5 shrink-0 mt-px" />
            <span className="min-w-0">
              {list.source === "builtin" ? "Could not ask the agent, showing the built-in list: " : ""}
              {list.error}
            </span>
          </div>
        )}
        {list && list.models.length > 0 && (
          <div className="max-h-64 overflow-y-auto py-1">
            {shown.map((m) => {
              const on = p.models.includes(m.id);
              return (
                <label key={m.id} className={cx("flex items-start gap-2 px-2.5 py-1 cursor-pointer hover:bg-panel-2", on && "bg-accent/5")}>
                  <input type="checkbox" checked={on} onChange={(e) => toggle(m.id, m.name, e.target.checked)} className="mt-0.5 accent-[var(--c-accent)]" />
                  <span className="min-w-0 flex-1">
                    <span className="text-[12.5px] font-medium">{m.name || m.id}</span>
                    {m.name && m.name !== m.id && <span className="ml-1.5 font-mono text-[11px] text-faint">{m.id}</span>}
                    {m.description && <span className="block text-[11.5px] text-muted truncate">{m.description}</span>}
                  </span>
                </label>
              );
            })}
            {shown.length === 0 && <div className="px-2.5 py-1.5 text-[12px] text-faint">No model matches “{filter}”.</div>}
          </div>
        )}
        <div className={cx("p-2", list && list.models.length > 0 && "border-t border-line")}>
          <ChipInput
            mono
            value={p.models}
            onChange={(m) => onChange({ models: m, modelNames: pick(names, m) })}
            placeholder={list ? "other model id…" : "model id…"}
          />
          {list && p.models.some((m) => !listed.has(m)) && (
            <div className="mt-1 text-[11px] text-faint">Models not in the list above are used as typed.</div>
          )}
        </div>
      </div>
    </FormField>
  );
}

function pick(names: Record<string, string>, models: string[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of models) if (names[m]) out[m] = names[m];
  return out;
}
