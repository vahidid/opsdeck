import { useEffect, useState } from "react";
import {
  ArrowUp,
  CircleCheck,
  GitCommitHorizontal,
  Undo2,
} from "lucide-react";
import type { RepoChange, RepoStatus } from "../../api/types";
import { api } from "../../api/client";
import type { Tone } from "../../components/ui";
import { Badge, Button, EmptyState, ErrorBox, Loading, Textarea, Toggle, cx } from "../../components/ui";
import { DiffView } from "../../components/DiffView";
import { useConfirm } from "../../components/overlay";
import { useAsync } from "../../hooks";

// ---------------------------------------------------------------------------
// Changes

const CHANGE_TONE: Record<RepoChange["status"], Tone> = {
  modified: "warn",
  added: "ok",
  untracked: "ok",
  deleted: "err",
  renamed: "info",
  conflict: "err",
  typechange: "warn",
};
const CHANGE_LETTER: Record<RepoChange["status"], string> = { modified: "M", added: "A", untracked: "U", deleted: "D", renamed: "R", conflict: "!", typechange: "T" };

export function ChangesTab({
  ws,
  repo,
  st,
  rev,
  op,
  onPush,
}: {
  ws: string;
  repo: string;
  st: RepoStatus;
  rev: number;
  op: (label: string, fn: () => Promise<unknown>, quiet?: boolean) => Promise<boolean>;
  onPush: () => void;
}) {
  const confirm = useConfirm();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [focus, setFocus] = useState<string>("");
  const [msg, setMsg] = useState("");
  const [pushAfter, setPushAfter] = useState(true);
  const paths = st.changes.map((c) => c.path).join("\n");
  useEffect(() => {
    setSelected(new Set(st.changes.map((c) => c.path)));
  }, [paths]);
  const diff = useAsync(() => api.repoDiff(ws, repo, focus), [ws, repo, focus, rev, paths]);

  if (st.changes.length === 0) {
    return (
      <EmptyState
        icon={<CircleCheck />}
        title="No uncommitted changes"
        action={
          st.ahead > 0 ? (
            <Button variant="primary" icon={<ArrowUp className="size-4" />} onClick={onPush}>
              Push {st.ahead} commit{st.ahead > 1 ? "s" : ""}
            </Button>
          ) : undefined
        }
      >
        Edit files in the Files tab (or let the agent do it); changes show up here for review before you commit.
      </EmptyState>
    );
  }
  const chosen = st.changes.filter((c) => selected.has(c.path)).map((c) => c.path);
  const all = chosen.length === st.changes.length;

  async function commit() {
    const ok = await op("Commit", () => api.repoCommit(ws, repo, msg, all ? [] : chosen));
    if (ok) {
      setMsg("");
      if (pushAfter) onPush();
    }
  }

  async function discard(list: string[]) {
    const ok = await confirm({
      title: `Discard ${list.length === st.changes.length ? "all changes" : list.length === 1 ? list[0] : `${list.length} changes`}?`,
      message: "Uncommitted edits are lost; new files are deleted.",
      confirmLabel: "Discard",
      danger: true,
    });
    if (ok) await op("Discard", () => api.repoDiscard(ws, repo, list.length === st.changes.length ? [] : list));
  }

  return (
    <div className="flex h-full min-h-0">
      <aside className="w-80 shrink-0 border-r border-line bg-panel flex flex-col">
        <div className="flex items-center gap-2 px-3 h-10 border-b border-line text-xs">
          <input
            type="checkbox"
            className="accent-[var(--color-accent)]"
            checked={all}
            onChange={() => setSelected(all ? new Set() : new Set(st.changes.map((c) => c.path)))}
          />
          <span className="flex-1 text-muted">
            {chosen.length} of {st.changes.length} selected
          </span>
          <Button size="xs" variant="ghost" icon={<Undo2 className="size-3" />} disabled={chosen.length === 0} onClick={() => void discard(chosen)}>
            Discard
          </Button>
        </div>
        <div className="flex-1 overflow-y-auto py-1">
          <button
            onClick={() => setFocus("")}
            className={cx("w-full px-3 py-1 text-left text-[12px]", focus === "" ? "text-fg bg-panel-3" : "text-muted hover:bg-panel-2")}
          >
            All changes
          </button>
          {st.changes.map((c) => (
            <div key={c.path} className={cx("flex items-center gap-2 px-3 py-1", focus === c.path ? "bg-panel-3" : "hover:bg-panel-2")}>
              <input
                type="checkbox"
                className="accent-[var(--color-accent)]"
                checked={selected.has(c.path)}
                onChange={() =>
                  setSelected((s) => {
                    const n = new Set(s);
                    if (n.has(c.path)) n.delete(c.path);
                    else n.add(c.path);
                    return n;
                  })
                }
              />
              <button onClick={() => setFocus(c.path)} className="min-w-0 flex-1 text-left">
                <span className="block truncate font-mono text-[12px]" title={c.orig ? `${c.orig} → ${c.path}` : c.path}>
                  {c.path.split("/").pop()}
                </span>
                <span className="block truncate text-[10.5px] text-faint">{c.path.split("/").slice(0, -1).join("/") || "/"}</span>
              </button>
              <Badge tone={CHANGE_TONE[c.status]} title={c.status} mono>
                {CHANGE_LETTER[c.status]}
              </Badge>
            </div>
          ))}
        </div>
        <div className="border-t border-line p-3 space-y-2">
          <Textarea
            value={msg}
            onChange={(e) => setMsg(e.target.value)}
            rows={4}
            placeholder={"Commit message\n\nWhat changes and why"}
            className="text-[12.5px]"
            onKeyDown={(e) => {
              if ((e.ctrlKey || e.metaKey) && e.key === "Enter" && msg.trim() && chosen.length) {
                e.preventDefault();
                void commit();
              }
            }}
          />
          <Toggle checked={pushAfter} onChange={setPushAfter} label={<span className="text-xs">Push after committing</span>} />
          <Button
            variant="primary"
            className="w-full"
            icon={<GitCommitHorizontal className="size-4" />}
            disabled={!msg.trim() || chosen.length === 0}
            onClick={() => void commit()}
          >
            Commit {all ? "all" : chosen.length} to {st.branch || "HEAD"}
          </Button>
          {st.branch && st.branch === st.default && (
            <div className="text-[11px] text-warn">You are on the default branch. Create a branch first to propose the change in a merge request.</div>
          )}
        </div>
      </aside>
      <section className="flex-1 min-w-0 overflow-auto">
        {diff.error && <ErrorBox error={diff.error} />}
        {diff.loading && !diff.data && <Loading />}
        {diff.data && <DiffView text={diff.data.diff} />}
        {diff.data?.truncated && <div className="px-4 py-2 text-xs text-faint">Diff truncated.</div>}
      </section>
    </div>
  );
}

