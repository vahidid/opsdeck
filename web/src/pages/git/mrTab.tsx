import { useEffect, useState } from "react";
import {
  CircleCheck,
  CircleDot,
  CircleX,
  ExternalLink,
  GitMerge,
  GitPullRequest,
  RefreshCw,
} from "lucide-react";
import type { MergeRequest } from "../../api/types";
import { api, errMsg } from "../../api/client";
import { Badge, Button, EmptyState, ErrorBox, FormField, Input, Loading, Segmented, Textarea, Toggle, cx } from "../../components/ui";
import { Modal, useConfirm, useToast } from "../../components/overlay";
import { useAsync } from "../../hooks";
import { timeAgo } from "../../lib/format";

// ---------------------------------------------------------------------------
// Merge requests

function checksBadge(c?: string) {
  if (!c) return null;
  const icon = c === "success" ? <CircleCheck className="size-3" /> : c === "failed" ? <CircleX className="size-3" /> : <CircleDot className="size-3" />;
  return (
    <Badge tone={c === "success" ? "ok" : c === "failed" ? "err" : "warn"}>
      {icon} CI {c}
    </Badge>
  );
}

export function MRTab({ ws, repo, rev, onNew, canNew }: { ws: string; repo: string; rev: number; onNew: () => void; canNew: boolean }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [state, setState] = useState<"open" | "merged" | "closed" | "all">("open");
  const mrs = useAsync(() => api.mrs(ws, repo, state), [ws, repo, state, rev]);
  const [busy, setBusy] = useState<number | null>(null);
  const [comment, setComment] = useState<MergeRequest | null>(null);
  const provider = mrs.data?.provider;
  const noun = provider === "github" ? "pull request" : "merge request";

  async function merge(m: MergeRequest) {
    let method = "merge";
    let removeSource = true;
    const ok = await confirm({
      title: `Merge !${m.id} into ${m.target}?`,
      message: (
        <div className="space-y-3">
          <div>
            <b>{m.title}</b> — {m.source} → {m.target}. Changes on {m.target} may deploy.
          </div>
          <MergeOptions provider={provider} onChange={(mm, rs) => {
            method = mm;
            removeSource = rs;
          }} />
        </div>
      ),
      confirmLabel: "Merge",
      danger: true,
    });
    if (!ok) return;
    setBusy(m.id);
    try {
      await api.mrMerge(ws, repo, m.id, method, removeSource);
      toast.success(`Merged !${m.id}`, "Pull the target branch to update the working copy.");
      await mrs.reload();
    } catch (e) {
      toast.error(`Merge !${m.id} failed`, errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="h-full overflow-y-auto">
      <div className="flex items-center gap-2 px-5 py-3">
        <Segmented
          value={state}
          onChange={setState}
          options={[
            { id: "open", label: "Open" },
            { id: "merged", label: "Merged" },
            { id: "closed", label: "Closed" },
            { id: "all", label: "All" },
          ]}
        />
        <div className="flex-1" />
        <Button size="sm" variant="ghost" icon={<RefreshCw className="size-3.5" />} onClick={() => void mrs.reload()}>
          Refresh
        </Button>
        <Button size="sm" variant="primary" icon={<GitPullRequest className="size-3.5" />} onClick={onNew} disabled={!canNew} title={canNew ? "" : "Switch to a branch other than the default"}>
          New {noun}
        </Button>
      </div>
      <div className="px-5 pb-6 space-y-2 max-w-5xl">
        {mrs.error && <ErrorBox error={mrs.error} onRetry={() => void mrs.reload()} />}
        {mrs.loading && !mrs.data && <Loading />}
        {mrs.data && (mrs.data.items ?? []).length === 0 && <EmptyState icon={<GitPullRequest />} title={`No ${state === "all" ? "" : state + " "}${noun}s`} />}
        {(mrs.data?.items ?? []).map((m) => (
          <div key={m.id} className="rounded-lg border border-line bg-panel px-4 py-3">
            <div className="flex items-start gap-3">
              {m.state === "merged" ? <GitMerge className="size-4 text-accent mt-0.5" /> : <GitPullRequest className={cx("size-4 mt-0.5", m.state === "open" ? "text-ok" : "text-faint")} />}
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-[13.5px] font-medium">{m.title}</span>
                  <span className="text-faint text-xs">!{m.id}</span>
                  {m.draft && <Badge tone="neutral">draft</Badge>}
                  {m.state !== "open" && <Badge tone={m.state === "merged" ? "accent" : "neutral"}>{m.state}</Badge>}
                  {checksBadge(m.checks)}
                  {m.mergeable && m.state === "open" && <Badge tone={/mergeable|clean/.test(m.mergeable) ? "ok" : "neutral"}>{m.mergeable.replace(/_/g, " ")}</Badge>}
                </div>
                <div className="text-[11.5px] text-faint mt-0.5">
                  <span className="font-mono">{m.source}</span> → <span className="font-mono">{m.target}</span> · {m.author} · updated {timeAgo(m.updated)}
                </div>
              </div>
              {m.url && (
                <a href={m.url} target="_blank" rel="noreferrer">
                  <Button size="sm" variant="ghost" icon={<ExternalLink className="size-3.5" />}>
                    Open
                  </Button>
                </a>
              )}
              {m.state === "open" && (
                <>
                  <Button size="sm" variant="secondary" onClick={() => setComment(m)}>
                    Comment
                  </Button>
                  <Button size="sm" variant="primary" loading={busy === m.id} icon={<GitMerge className="size-3.5" />} onClick={() => void merge(m)}>
                    Merge
                  </Button>
                </>
              )}
            </div>
          </div>
        ))}
      </div>
      {comment && <CommentModal ws={ws} repo={repo} mr={comment} onClose={() => setComment(null)} />}
    </div>
  );
}

function MergeOptions({ provider, onChange }: { provider?: string; onChange: (method: string, removeSource: boolean) => void }) {
  const [method, setMethod] = useState("merge");
  const [rm, setRm] = useState(true);
  useEffect(() => {
    onChange(method, rm);
  }, [method, rm, onChange]);
  const methods = provider === "github" ? ["merge", "squash", "rebase"] : ["merge", "squash"];
  return (
    <div className="flex items-center gap-4 flex-wrap">
      <Segmented value={method} onChange={setMethod} options={methods.map((m) => ({ id: m, label: m }))} />
      <Toggle checked={rm} onChange={setRm} label={<span className="text-xs">Delete the source branch</span>} />
    </div>
  );
}

function CommentModal({ ws, repo, mr, onClose }: { ws: string; repo: string; mr: MergeRequest; onClose: () => void }) {
  const toast = useToast();
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <Modal
      open
      onClose={onClose}
      title={`Comment on !${mr.id}`}
      width={560}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={busy}
            disabled={!body.trim()}
            onClick={async () => {
              setBusy(true);
              try {
                await api.mrComment(ws, repo, mr.id, body);
                toast.success("Comment posted");
                onClose();
              } catch (e) {
                toast.error("Comment failed", errMsg(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Post
          </Button>
        </>
      }
    >
      <Textarea autoFocus rows={6} value={body} onChange={(e) => setBody(e.target.value)} placeholder="Markdown" />
    </Modal>
  );
}

export function NewMRModal({ ws, repo, source, target, onClose, onCreated }: { ws: string; repo: string; source: string; target: string; onClose: () => void; onCreated: () => void }) {
  const toast = useToast();
  const log = useAsync(() => api.repoLog(ws, repo, source, "", 20), [ws, repo, source]);
  const [title, setTitle] = useState("");
  const [desc, setDesc] = useState("");
  const [tgt, setTgt] = useState(target);
  const [draft, setDraft] = useState(false);
  const [rm, setRm] = useState(true);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!title && log.data?.[0]) setTitle(log.data[0].subject);
  }, [log.data]);
  return (
    <Modal
      open
      onClose={onClose}
      title="New merge request"
      icon={<GitPullRequest />}
      width={620}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={busy}
            disabled={!title.trim() || !tgt || tgt === source}
            onClick={async () => {
              setBusy(true);
              try {
                const m = await api.mrCreate(ws, repo, { title, description: desc, source, target: tgt, draft, removeSource: rm });
                toast.success(`Opened !${m.id}`, m.url);
                onCreated();
              } catch (e) {
                toast.error("Could not open the merge request", errMsg(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            Open merge request
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="flex items-center gap-2 text-xs">
          <span className="font-mono rounded bg-panel-3 px-1.5 py-0.5">{source}</span>
          <span className="text-faint">into</span>
          <Input mono value={tgt} onChange={(e) => setTgt(e.target.value)} className="w-48 h-7" />
        </div>
        <div className="text-[11px] text-faint">The branch must be pushed first.</div>
        <FormField label="Title">
          <Input autoFocus value={title} onChange={(e) => setTitle(e.target.value)} />
        </FormField>
        <FormField label="Description" help="What changes, why, how it was verified, how to roll back.">
          <Textarea rows={6} value={desc} onChange={(e) => setDesc(e.target.value)} />
        </FormField>
        <div className="flex items-center gap-5">
          <Toggle checked={draft} onChange={setDraft} label={<span className="text-xs">Draft</span>} />
          <Toggle checked={rm} onChange={setRm} label={<span className="text-xs">Delete the branch after merging (GitLab)</span>} />
        </div>
      </div>
    </Modal>
  );
}
