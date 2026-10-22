import { useRef, useState } from "react";
import { FileUp, Upload } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { KubeApplyResult } from "../../api/types";
import { Badge, Button, FormField, Input, Textarea } from "../../components/ui";
import { Modal } from "../../components/overlay";
import { DiffView } from "../Git";
import { useKube } from "./shared";

const ACTION_TONE = { created: "ok", configured: "accent", unchanged: "neutral", failed: "err" } as const;

/** What an apply did (or would do, for a dry run), object by object with diffs. */
export function ApplyResults({ results, dry }: { results: KubeApplyResult[]; dry?: boolean }) {
  const [open, setOpen] = useState<Record<number, boolean>>({});
  return (
    <div className="rounded-lg border border-line bg-panel divide-y divide-line">
      {results.map((r, i) => {
        const shown = open[i] ?? (!!r.diff && results.length <= 3);
        return (
          <div key={i}>
            <button className="w-full flex items-center gap-2 px-3 py-2 text-left text-[12.5px] hover:bg-panel-2" onClick={() => setOpen({ ...open, [i]: !shown })}>
              <Badge tone={ACTION_TONE[r.action] ?? "neutral"}>{dry && r.action !== "failed" && r.action !== "unchanged" ? `would be ${r.action}` : r.action}</Badge>
              <span className="text-faint">{r.kind}</span>
              <span className="font-medium font-mono">{r.namespace ? `${r.namespace}/` : ""}{r.name}</span>
              {r.note && <span className="text-faint text-[11.5px]">{r.note}</span>}
              {r.error && <span className="text-err text-[11.5px] truncate">{r.error}</span>}
              <span className="flex-1" />
              {r.diff && <span className="text-[11px] text-faint">{shown ? "hide diff" : "diff"}</span>}
            </button>
            {shown && r.diff && (
              <div className="border-t border-line max-h-96 overflow-auto bg-bg">
                <DiffView text={r.diff} />
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

/** Paste or upload manifests, review a server-side dry run, then apply. */
export function ApplyModal({ initial, namespace, onClose, onApplied }: { initial?: string; namespace: string; onClose: () => void; onApplied?: () => void }) {
  const { ws, cluster } = useKube();
  const [text, setText] = useState(initial ?? "");
  const [ns, setNs] = useState(namespace);
  const [results, setResults] = useState<KubeApplyResult[] | null>(null);
  const [dry, setDry] = useState(true);
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const file = useRef<HTMLInputElement>(null);
  async function go(dryRun: boolean) {
    setBusy(dryRun ? "dry" : "apply");
    setErr("");
    try {
      const r = await api.kubeApply(ws, cluster, text, ns, dryRun);
      setResults(r);
      setDry(dryRun);
      if (!dryRun && !r.some((x) => x.action === "failed")) onApplied?.();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy("");
    }
  }
  async function load(f: File) {
    const body = await f.text();
    setText((t) => (t.trim() ? `${t.trimEnd()}\n---\n` : "") + body);
    setResults(null);
  }
  const applied = results && !dry;
  return (
    <Modal
      open
      onClose={onClose}
      width={980}
      icon={<Upload />}
      title={`Apply to ${cluster}`}
      footer={
        applied ? (
          <Button onClick={onClose}>Close</Button>
        ) : (
          <>
            <Button loading={busy === "dry"} disabled={!text.trim() || !!busy} onClick={() => void go(true)}>
              Review changes (dry run)
            </Button>
            <Button variant="primary" loading={busy === "apply"} disabled={!text.trim() || !!busy} onClick={() => void go(false)}>
              Apply
            </Button>
          </>
        )
      }
    >
      <div className="space-y-3">
        {!applied && (
          <>
            <div className="flex items-end gap-3">
              <FormField label="Default namespace" help="For objects without one in their metadata.">
                <Input mono className="w-56" value={ns} onChange={(e) => setNs(e.target.value)} />
              </FormField>
              <div className="flex-1" />
              <input ref={file} type="file" accept=".yaml,.yml,.json" multiple hidden onChange={(e) => void Promise.all([...(e.target.files ?? [])].map(load)).then(() => (e.target.value = ""))} />
              <Button size="sm" icon={<FileUp className="size-3.5" />} onClick={() => file.current?.click()}>
                Load files
              </Button>
            </div>
            <Textarea
              mono
              dir="ltr"
              spellCheck={false}
              rows={results ? 10 : 22}
              className="!text-[12px] leading-relaxed"
              placeholder={"apiVersion: apps/v1\nkind: Deployment\n…\n---\n(several documents, or a List, are fine)"}
              value={text}
              onChange={(e) => (setText(e.target.value), setResults(null))}
            />
            <div className="text-[11.5px] text-faint">Server-side apply with field manager “opsdeck”. The dry run is done by the API server, so admission webhooks and defaults are included in the diff. Secret values never appear in it.</div>
          </>
        )}
        {err && <div className="text-err text-xs whitespace-pre-wrap">{err}</div>}
        {results && (
          <>
            <div className="text-[12.5px] font-medium">{dry ? "Dry run — nothing has changed yet" : "Applied"}</div>
            <ApplyResults results={results} dry={dry} />
          </>
        )}
      </div>
    </Modal>
  );
}
