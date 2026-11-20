import { useState } from "react";
import {
  GitCommitHorizontal,
  History,
} from "lucide-react";
import { api } from "../../api/client";
import { DiffView } from "../../components/DiffView";
import { EmptyState, ErrorBox, Loading, cx } from "../../components/ui";
import { useAsync } from "../../hooks";
import { timeAgo } from "../../lib/format";

// ---------------------------------------------------------------------------
// History

export function HistoryTab({ ws, repo, branch, rev }: { ws: string; repo: string; branch: string; rev: number }) {
  const log = useAsync(() => api.repoLog(ws, repo, "", "", 200), [ws, repo, branch, rev]);
  const [open, setOpen] = useState<string>("");
  const show = useAsync(() => (open ? api.repoShow(ws, repo, open) : Promise.resolve(null)), [ws, repo, open]);
  return (
    <div className="flex h-full min-h-0">
      <aside className="w-[28rem] shrink-0 border-r border-line overflow-y-auto">
        {log.loading && !log.data && <Loading />}
        {log.error && <ErrorBox error={log.error} />}
        {log.data?.length === 0 && <EmptyState icon={<History />} title="No commits yet" />}
        {(log.data ?? []).map((c) => (
          <button
            key={c.hash}
            onClick={() => setOpen(c.hash)}
            className={cx("w-full text-left px-4 py-2 border-b border-line", open === c.hash ? "bg-panel-3" : "hover:bg-panel-2")}
          >
            <div className="text-[13px] truncate">{c.subject}</div>
            <div className="text-[11px] text-faint flex items-center gap-2">
              <span className="font-mono">{c.short}</span>
              <span className="truncate">{c.author}</span>
              <span>{timeAgo(c.date)}</span>
              {c.refs && (
                <span className="truncate text-accent font-mono" title={c.refs}>
                  {c.refs.replace(/HEAD -> /, "")}
                </span>
              )}
            </div>
          </button>
        ))}
      </aside>
      <section className="flex-1 min-w-0 overflow-auto">
        {!open && <EmptyState icon={<GitCommitHorizontal />} title="Pick a commit" />}
        {open && show.loading && <Loading />}
        {show.data && <DiffView text={show.data.diff} />}
      </section>
    </div>
  );
}
