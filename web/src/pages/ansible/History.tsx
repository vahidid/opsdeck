import { useSearchParams } from "react-router-dom";
import { History as HistoryIcon, RefreshCw } from "lucide-react";
import { api } from "../../api/client";
import type { AnsibleRun } from "../../api/types";
import { EmptyState, IconButton, Loading, cx } from "../../components/ui";
import { useAsync, useInterval } from "../../hooks";
import { fmtMs, timeAgo } from "../../lib/format";
import { RunView } from "./RunView";
import { StatusBadge, actorLabel, useAnsible } from "./shared";

function what(r: AnsibleRun): string {
  const o = r.options;
  if (o.kind === "adhoc") return `${o.module} ${o.args ?? ""}`.trim();
  if (o.kind === "galaxy") return `galaxy ${o.requirements ?? ""}`;
  if (o.kind === "lint") return `lint ${o.playbook ?? ""}`.trim();
  return o.playbook ?? "";
}

// HistoryTab lists the project's runs (the user's and the agent's) and
// shows the selected one with its log.
export function HistoryTab() {
  const { ws, project: p, goto } = useAnsible();
  const [params, setParams] = useSearchParams();
  const runs = useAsync(() => api.ansibleRuns(ws, p.name, 200), [ws, p.name]);
  const list = runs.data ?? [];
  const sel = params.get("run") ?? list[0]?.id ?? "";
  useInterval(() => void runs.reload(), list.some((r) => r.status === "running") ? 2000 : 10000);
  const pick = (id: string) =>
    setParams(
      (prev) => {
        const n = new URLSearchParams(prev);
        n.set("run", id);
        return n;
      },
      { replace: true },
    );

  if (!runs.data && runs.loading) return <Loading />;
  if (list.length === 0)
    return (
      <EmptyState icon={<HistoryIcon />} title="No runs yet">
        Runs started here or by the agent are listed with their full log.
      </EmptyState>
    );
  return (
    <div className="flex h-full min-h-0 gap-4">
      <aside className="w-80 shrink-0 flex flex-col rounded-lg border border-line bg-panel overflow-hidden">
        <div className="flex items-center gap-2 px-3 h-10 border-b border-line">
          <span className="text-[13px] font-medium flex-1">{list.length} runs</span>
          <IconButton title="Refresh" onClick={() => void runs.reload()}>
            <RefreshCw className={cx("size-4", runs.loading && "animate-spin")} />
          </IconButton>
        </div>
        <div className="flex-1 overflow-y-auto divide-y divide-line">
          {list.map((r) => {
            const changed = r.recap?.reduce((n, h) => n + h.changed, 0) ?? 0;
            const failed = r.recap?.filter((h) => h.failed > 0 || h.unreachable > 0).length ?? 0;
            return (
              <button key={r.id} onClick={() => pick(r.id)} className={cx("w-full text-left px-3 py-2 hover:bg-panel-2", sel === r.id && "bg-panel-3")}>
                <div className="flex items-center gap-2">
                  <StatusBadge status={r.status} />
                  <span className="font-mono text-[12px] truncate flex-1">{what(r)}</span>
                  {r.options.check && <span className="text-[10.5px] text-info">check</span>}
                </div>
                <div className="mt-0.5 text-[11px] text-faint">
                  {[
                    timeAgo(r.started),
                    actorLabel(r.actor),
                    r.durationMs ? fmtMs(r.durationMs) : "",
                    r.recap?.length ? `${r.recap.length} host${r.recap.length > 1 ? "s" : ""}` : "",
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                  {changed > 0 && <span className="text-warn"> · {changed} changed</span>}
                  {failed > 0 && <span className="text-err"> · {failed} failed</span>}
                </div>
              </button>
            );
          })}
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col min-h-0">
        {sel && (
          <RunView
            key={sel}
            ws={ws}
            id={sel}
            className="flex-1"
            onRerun={(r) => {
              try {
                localStorage.setItem(`opsdeck.ansible.run.${ws}.${p.name}`, JSON.stringify({ ...r.options }));
              } catch {
                /* ignore */
              }
              goto("run", r.options.playbook ? { playbook: r.options.playbook } : {});
            }}
          />
        )}
      </section>
    </div>
  );
}
