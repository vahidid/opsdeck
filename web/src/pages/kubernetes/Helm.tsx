import { useMemo, useState } from "react";
import { Package } from "lucide-react";
import { api } from "../../api/client";
import type { HelmRelease } from "../../api/types";
import { Badge, CodeBlock, EmptyState, ErrorBox, Loading, SearchInput, Select, Table, Tabs, Td, Th, Tr } from "../../components/ui";
import { Drawer } from "../../components/overlay";
import { useAsync, useInterval } from "../../hooks";
import { timeAgo } from "../../lib/format";
import { kTone, listNs, useKube } from "./shared";

// Helm releases, read from the release secrets Helm 3 keeps in the cluster:
// no helm binary needed. Values, rendered manifest, notes and history.
export function Helm() {
  const { ws, cluster, ns } = useKube();
  const r = useAsync(() => api.helmReleases(ws, cluster, listNs(ns)), [ws, cluster, ns]);
  useInterval(() => void r.reload(), 15000);
  const [q, setQ] = useState("");
  const [sel, setSel] = useState<HelmRelease | null>(null);
  const rows = useMemo(() => (r.data ?? []).filter((x) => !q || `${x.namespace}/${x.name} ${x.chart}`.toLowerCase().includes(q.toLowerCase())), [r.data, q]);
  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-3">
        <SearchInput value={q} onChange={setQ} placeholder="Filter releases…" className="w-60" />
        <div className="flex-1" />
        <span className="text-xs text-faint">{rows.length} releases</span>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {r.error && <ErrorBox error={r.error} onRetry={() => void r.reload()} />}
        {!r.data && !r.error && <Loading />}
        {r.data && rows.length === 0 && (
          <EmptyState icon={<Package />} title="No Helm releases">
            Releases installed with Helm 3 show up here.
          </EmptyState>
        )}
        {rows.length > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>Release</Th>
                <Th>Namespace</Th>
                <Th>Chart</Th>
                <Th>App version</Th>
                <Th>Revision</Th>
                <Th>Status</Th>
                <Th>Updated</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((x) => (
                <Tr key={x.namespace + "/" + x.name} onClick={() => setSel(x)}>
                  <Td className="font-medium">{x.name}</Td>
                  <Td className="text-muted">{x.namespace}</Td>
                  <Td mono>
                    {x.chart}-{x.chartVersion}
                  </Td>
                  <Td mono>{x.appVersion}</Td>
                  <Td mono>{x.revision}</Td>
                  <Td>
                    <Badge tone={kTone(x.status)}>{x.status}</Badge>
                  </Td>
                  <Td className="text-muted" title={x.updated}>
                    {timeAgo(x.updated)}
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </div>
      {sel && <ReleaseDrawer rel={sel} onClose={() => setSel(null)} />}
    </div>
  );
}

type HTab = "values" | "manifest" | "notes" | "history";

function ReleaseDrawer({ rel, onClose }: { rel: HelmRelease; onClose: () => void }) {
  const { ws, cluster } = useKube();
  const [rev, setRev] = useState(0);
  const [tab, setTab] = useState<HTab>("values");
  const d = useAsync(() => api.helmRelease(ws, cluster, rel.namespace, rel.name, rev), [ws, cluster, rel.namespace, rel.name, rev]);
  const h = d.data;
  return (
    <Drawer
      open
      onClose={onClose}
      width={1000}
      title={
        <span className="inline-flex items-center gap-2">
          <span className="text-faint font-normal">Release</span> {rel.name} {h && <Badge tone={kTone(h.status)}>{h.status}</Badge>}
        </span>
      }
      subtitle={`${rel.namespace} · ${rel.chart}-${rel.chartVersion}`}
    >
      <div className="-m-4 flex flex-col h-[calc(100vh-48px)]">
        <div className="flex items-center gap-2 px-4 py-2 border-b border-line">
          <span className="text-[12px] text-muted">Revision</span>
          <Select className="!w-auto !h-7 text-xs" value={rev} onChange={(e) => setRev(Number(e.target.value))}>
            <option value={0}>latest ({rel.revision})</option>
            {(h?.history ?? [])
              .filter((x) => x.revision !== rel.revision)
              .map((x) => (
                <option key={x.revision} value={x.revision}>
                  {x.revision} · {x.status}
                </option>
              ))}
          </Select>
          {h?.description && <span className="text-[11.5px] text-faint truncate">{h.description}</span>}
        </div>
        <Tabs<HTab>
          className="px-3"
          value={tab}
          onChange={setTab}
          tabs={[
            { id: "values", label: "Values" },
            { id: "manifest", label: "Manifest" },
            { id: "notes", label: "Notes" },
            { id: "history", label: "History", count: h?.history.length },
          ]}
        />
        <div className="flex-1 min-h-0 overflow-auto p-3" dir="ltr">
          {d.error && <ErrorBox error={d.error} />}
          {!h && !d.error && <Loading />}
          {h && tab === "values" && (h.values.trim() && h.values.trim() !== "{}" ? <CodeBlock>{h.values}</CodeBlock> : <EmptyState title="Chart defaults">No values were set for this revision.</EmptyState>)}
          {h && tab === "manifest" && <CodeBlock>{h.manifest}</CodeBlock>}
          {h && tab === "notes" && (h.notes ? <CodeBlock>{h.notes}</CodeBlock> : <EmptyState title="No notes" />)}
          {h && tab === "history" && (
            <div className="rounded-lg border border-line bg-panel overflow-hidden">
              <Table>
                <thead>
                  <tr>
                    <Th>Revision</Th>
                    <Th>Status</Th>
                    <Th>Chart</Th>
                    <Th>App version</Th>
                    <Th>Updated</Th>
                    <Th>Description</Th>
                  </tr>
                </thead>
                <tbody>
                  {h.history.map((x) => (
                    <Tr key={x.revision} onClick={() => (setRev(x.revision === rel.revision ? 0 : x.revision), setTab("values"))}>
                      <Td mono>{x.revision}</Td>
                      <Td>
                        <Badge tone={kTone(x.status)}>{x.status}</Badge>
                      </Td>
                      <Td mono>
                        {x.chart}-{x.chartVersion}
                      </Td>
                      <Td mono>{x.appVersion}</Td>
                      <Td className="text-muted">{timeAgo(x.updated)}</Td>
                      <Td className="text-muted">{x.description}</Td>
                    </Tr>
                  ))}
                </tbody>
              </Table>
            </div>
          )}
        </div>
      </div>
    </Drawer>
  );
}
