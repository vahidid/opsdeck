import { useEffect, useState } from "react";
import { Activity, Link2, RefreshCw, ShieldCheck, ShieldX } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { AuditEntry, AuditVerify } from "../api/types";
import { useApp, useWs } from "../context/app";
import { Badge, Button, CodeBlock, EmptyState, ErrorBox, PageHeader, SearchInput, Select, Table, Td, Th, Tr } from "../components/ui";
import { Drawer, useToast } from "../components/overlay";
import { fmtTime, str } from "../lib/format";

const PAGE = 100;

export function ActivityPage() {
  const { name: ws } = useWs();
  const { workspaces } = useApp();
  const [filterWs, setFilterWs] = useState<string>(ws);
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [done, setDone] = useState(false);
  const [err, setErr] = useState("");
  const [q, setQ] = useState("");
  const [verify, setVerify] = useState<AuditVerify | null>(null);
  const [verifying, setVerifying] = useState(false);
  const [sel, setSel] = useState<AuditEntry | null>(null);
  const toast = useToast();

  async function load(reset: boolean) {
    setLoading(true);
    setErr("");
    try {
      const before = reset ? undefined : entries[entries.length - 1]?.seq;
      const page = await api.audit({ workspace: filterWs || undefined, limit: PAGE, before });
      setEntries((e) => (reset ? page : [...e, ...page]));
      setDone(page.length < PAGE);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filterWs]);

  async function doVerify() {
    setVerifying(true);
    try {
      const v = await api.auditVerify();
      setVerify(v);
      if (v.ok) toast.success("Audit chain intact", `${v.entries} entries verified`);
      else toast.error("Audit chain broken", `First invalid entry: #${v.brokenAt}`);
    } catch (e) {
      toast.error("Verify failed", errMsg(e));
    } finally {
      setVerifying(false);
    }
  }

  const t = q.toLowerCase();
  const shown = entries.filter((e) => !t || `${e.actor} ${e.action} ${e.target} ${e.result} ${str(e.detail)}`.toLowerCase().includes(t));

  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<Activity />}
        title="Activity"
        subtitle="Append-only, hash-chained journal of every action taken by you and the agent."
        actions={
          <>
            {verify && (
              <Badge tone={verify.ok ? "ok" : "err"}>
                {verify.ok ? <ShieldCheck className="size-3" /> : <ShieldX className="size-3" />}
                {verify.ok ? `chain ok · ${verify.entries}` : `broken at #${verify.brokenAt}`}
              </Badge>
            )}
            <Button icon={<Link2 className="size-3.5" />} onClick={doVerify} loading={verifying}>
              Verify chain
            </Button>
          </>
        }
      />
      <div className="flex items-center gap-2 px-5 pb-3">
        <Select value={filterWs} onChange={(e) => setFilterWs(e.target.value)} className="w-56">
          <option value="">All workspaces</option>
          {workspaces.map((w) => (
            <option key={w.name} value={w.name}>
              {w.title || w.name}
            </option>
          ))}
        </Select>
        <SearchInput value={q} onChange={setQ} placeholder="Filter loaded entries…" className="w-72" />
        <div className="flex-1" />
        <Button size="sm" variant="ghost" icon={<RefreshCw className="size-3.5" />} onClick={() => void load(true)} loading={loading}>
          Refresh
        </Button>
      </div>
      <div className="flex-1 min-h-0 px-5 pb-5">
        <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
          {err && <ErrorBox error={err} onRetry={() => void load(true)} />}
          {!err && entries.length === 0 && !loading && <EmptyState icon={<Activity />} title="No activity recorded yet" />}
          {entries.length > 0 && (
            <Table className="flex-1">
              <thead>
                <tr>
                  <Th>#</Th>
                  <Th>Time</Th>
                  {!filterWs && <Th>Workspace</Th>}
                  <Th>Actor</Th>
                  <Th>Action</Th>
                  <Th>Target</Th>
                  <Th>Result</Th>
                </tr>
              </thead>
              <tbody>
                {shown.map((e) => (
                  <Tr key={e.seq} onClick={() => setSel(e)}>
                    <Td mono className="text-faint">
                      {e.seq}
                    </Td>
                    <Td className="text-muted whitespace-nowrap tabular-nums">{fmtTime(e.ts)}</Td>
                    {!filterWs && <Td mono>{e.workspace || "—"}</Td>}
                    <Td>
                      <Badge tone={e.actor.startsWith("agent") ? "accent" : "neutral"}>{e.actor}</Badge>
                    </Td>
                    <Td className="font-medium">{e.action}</Td>
                    <Td mono className="max-w-[420px] truncate text-muted" title={e.target}>
                      {e.target}
                    </Td>
                    <Td>
                      <Badge tone={/^(ok|success|allowed?|approved?)$/i.test(e.result) ? "ok" : /err|fail|den/i.test(e.result) ? "err" : "neutral"}>{e.result || "—"}</Badge>
                    </Td>
                  </Tr>
                ))}
                {!done && (
                  <tr>
                    <td colSpan={7} className="p-3 text-center">
                      <Button size="sm" onClick={() => void load(false)} loading={loading}>
                        Load more
                      </Button>
                    </td>
                  </tr>
                )}
              </tbody>
            </Table>
          )}
        </div>
      </div>
      <Drawer open={!!sel} onClose={() => setSel(null)} title={`Entry #${sel?.seq}`} subtitle={sel ? fmtTime(sel.ts) : undefined}>
        {sel && (
          <div className="space-y-3 text-[13px]">
            <Row k="Workspace" v={sel.workspace} />
            <Row k="Actor" v={sel.actor} />
            <Row k="Action" v={sel.action} />
            <Row k="Target" v={sel.target} mono />
            <Row k="Result" v={sel.result} />
            <div>
              <div className="text-xs text-muted mb-1">Detail</div>
              <CodeBlock maxHeight={400}>{typeof sel.detail === "string" ? sel.detail : JSON.stringify(sel.detail, null, 2)}</CodeBlock>
            </div>
            <div>
              <div className="text-xs text-muted mb-1">Hash</div>
              <div className="font-mono text-[11px] break-all text-faint">{sel.hash}</div>
            </div>
          </div>
        )}
      </Drawer>
    </div>
  );
}

function Row({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div className="flex gap-3">
      <div className="w-24 text-xs text-muted shrink-0 pt-0.5">{k}</div>
      <div className={mono ? "font-mono text-[12px] break-all" : ""}>{v || "—"}</div>
    </div>
  );
}
