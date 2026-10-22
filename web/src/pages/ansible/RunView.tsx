import { Download, RotateCcw, Square } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { AnsibleRecap, AnsibleRun } from "../../api/types";
import { LogViewer } from "../../components/LogViewer";
import { useConfirm, useToast } from "../../components/overlay";
import { Badge, Button, CopyButton, ErrorBox, Loading, cx } from "../../components/ui";
import { useAsync, useInterval } from "../../hooks";
import { fmtMs, timeAgo } from "../../lib/format";
import { StatusBadge, actorLabel } from "./shared";

// RunView follows one run: its command, status and PLAY RECAP, and the log
// streamed live (it resumes after a dropped connection).
export function RunView({ ws, id, onRerun, className }: { ws: string; id: string; onRerun?: (r: AnsibleRun) => void; className?: string }) {
  const toast = useToast();
  const confirm = useConfirm();
  const run = useAsync(() => api.ansibleRunInfo(ws, id), [ws, id]);
  const r = run.data;
  const live = r?.status === "running";
  // while it runs, poll its record: the last poll brings the final status and recap
  useInterval(() => void run.reload(), live ? 1500 : null);

  async function cancel() {
    if (!(await confirm({ title: "Stop this run?", message: "Ansible is interrupted; tasks that already ran stay applied.", confirmLabel: "Stop", danger: true }))) return;
    try {
      await api.ansibleRunCancel(ws, id);
      setTimeout(() => void run.reload(), 800);
    } catch (e) {
      toast.error("Could not stop", errMsg(e));
    }
  }

  if (!r && run.loading) return <Loading />;
  if (!r) return <ErrorBox error={run.error ?? "run not found"} onRetry={() => void run.reload()} />;
  return (
    <div className={cx("flex flex-col min-h-0 gap-3", className)}>
      <div className="rounded-lg border border-line bg-panel px-3 py-2.5 space-y-2">
        <div className="flex items-center gap-2 flex-wrap">
          <StatusBadge status={r.status} />
          <span className="text-[13px] font-medium">{r.options.kind === "adhoc" ? `${r.options.module} on ${r.options.pattern}` : r.options.playbook || r.options.requirements || r.options.kind}</span>
          {r.options.check && <Badge tone="info">check</Badge>}
          {r.options.diff && <Badge>diff</Badge>}
          {r.options.limit && <Badge mono>limit {r.options.limit}</Badge>}
          <span className="text-xs text-muted">
            by {actorLabel(r.actor)} · {timeAgo(r.started)}
            {r.durationMs ? ` · ${fmtMs(r.durationMs)}` : ""}
            {r.status !== "running" && r.exitCode !== 0 && ` · exit ${r.exitCode}`}
          </span>
          <div className="flex-1" />
          {live ? (
            <Button size="sm" variant="danger" icon={<Square className="size-3.5" />} onClick={() => void cancel()}>
              Stop
            </Button>
          ) : (
            <>
              {onRerun && (
                <Button size="sm" icon={<RotateCcw className="size-3.5" />} onClick={() => onRerun(r)}>
                  Run again
                </Button>
              )}
              <a href={api.ansibleRunLogTextUrl(ws, id)} className="inline-flex">
                <Button size="sm" variant="ghost" icon={<Download className="size-3.5" />}>
                  Log
                </Button>
              </a>
            </>
          )}
        </div>
        <div className="group flex items-start gap-2 rounded-md bg-panel-2 border border-line px-2.5 py-1.5 font-mono text-[11.5px]" dir="ltr">
          <span className="text-accent select-none">$</span>
          <span className="flex-1 min-w-0 break-all">{r.command}</span>
          <span className="opacity-0 group-hover:opacity-100">
            <CopyButton text={r.command} />
          </span>
        </div>
        {r.error && <div className="text-xs text-err">{r.error}</div>}
        {r.recap && r.recap.length > 0 && <Recap recap={r.recap} />}
      </div>
      <LogViewer
        key={id}
        className="flex-1 min-h-[320px]"
        url={api.ansibleRunLogUrl(ws, id)}
        reconnect={(ts) => api.ansibleRunLogUrl(ws, id, ts)}
        downloadName={`ansible-${id}`}
      />
    </div>
  );
}

export function Recap({ recap }: { recap: AnsibleRecap[] }) {
  const cols: { k: keyof AnsibleRecap; label: string; tone: string }[] = [
    { k: "ok", label: "ok", tone: "text-ok" },
    { k: "changed", label: "changed", tone: "text-warn" },
    { k: "unreachable", label: "unreachable", tone: "text-err" },
    { k: "failed", label: "failed", tone: "text-err" },
    { k: "skipped", label: "skipped", tone: "text-info" },
    { k: "rescued", label: "rescued", tone: "text-muted" },
    { k: "ignored", label: "ignored", tone: "text-muted" },
  ];
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[12px]">
        <thead>
          <tr className="text-[11px] text-faint">
            <th className="text-left font-medium py-1 pr-3">Host</th>
            {cols.map((c) => (
              <th key={c.k} className="text-right font-medium py-1 px-2">
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {recap.map((h) => {
            const bad = h.failed > 0 || h.unreachable > 0;
            return (
              <tr key={h.host} className="border-t border-line">
                <td className={cx("py-1 pr-3 font-mono", bad ? "text-err" : h.changed > 0 ? "text-warn" : "text-ok")}>{h.host}</td>
                {cols.map((c) => {
                  const v = h[c.k] as number;
                  return (
                    <td key={c.k} className={cx("py-1 px-2 text-right tabular-nums", v > 0 ? c.tone : "text-faint")}>
                      {v}
                    </td>
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
