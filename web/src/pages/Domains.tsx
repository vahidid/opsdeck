import { useEffect, useMemo, useState } from "react";
import { AskAgentButton } from "../components/AskAgent";
import { useSearchParams } from "react-router-dom";
import { AlertTriangle, Cloud, CloudOff, ExternalLink, Globe, Lock, Pencil, Plug, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { DNSConnection, DNSRecord, DNSRecordInput, DNSZone, DomainInfo, Resource } from "../api/types";
import { DNS_RECORD_TYPES } from "../api/types";
import { useWs } from "../context/app";
import { useTabTitle } from "../context/tabs";
import { Badge, Button, CopyButton, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, PageHeader, SearchInput, Select, Spinner, Table, Td, Textarea, Th, Toggle, Tr, cx } from "../components/ui";
import { Modal, useConfirm, useToast } from "../components/overlay";
import { ProviderMark } from "../components/brand";
import { useAsync, useLocalStorage } from "../hooks";
import { slugify, timeAgo } from "../lib/format";
import { DNSConnectModal } from "./Connectors";

// Domains of the workspace (Domain resources: zones at Cloudflare or
// ArvanCloud, through the DNS connectors of this computer): the zone's
// status and name servers, and its records — read, add, change, delete.
// Every change is recorded in the activity journal.
export function DomainsPage() {
  const { name: ws, byKind, reloadResources } = useWs();
  const all = byKind("Domain");
  const [params, setParams] = useSearchParams();
  const [saved, setSaved] = useLocalStorage<string>(`opsdeck.domain.${ws}`, "");
  const current = params.get("domain") || (all.some((d) => d.name === saved) ? saved : all[0]?.name) || "";
  const res = all.find((d) => d.name === current);
  useEffect(() => {
    if (current) setSaved(current);
  }, [current, setSaved]);
  const [adding, setAdding] = useState(false);
  const info = useAsync(() => (res ? api.domain(ws, res.name) : Promise.resolve(undefined)), [ws, res?.name]);
  useTabTitle(res ? `${zoneOf(res)} · DNS` : "Domains");

  if (!all.length) {
    return (
      <div className="flex flex-col h-full">
        <PageHeader icon={<Globe />} title="Domains" />
        <EmptyState
          icon={<Globe />}
          title="No domains in this workspace"
          action={
            <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
              Add a domain
            </Button>
          }
        >
          Connect your Cloudflare or ArvanCloud account once under Connectors, then pick the zones that belong to this workspace. Their DNS records are managed here and by the agent&apos;s{" "}
          <span className="font-mono">dns</span> tool, with every change recorded.
        </EmptyState>
        {adding && <AddDomainModal onClose={() => setAdding(false)} onAdded={() => (setAdding(false), void reloadResources())} />}
      </div>
    );
  }

  const d = info.data;
  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<Globe />}
        title="Domains"
        subtitle={
          info.error ? (
            <span className="text-err">{info.error}</span>
          ) : d?.error ? (
            <span className="text-err">{d.error}</span>
          ) : d?.info ? (
            <ZoneSummary d={d} />
          ) : (
            "DNS zones of this workspace and their records"
          )
        }
        actions={
          <>
            <AskAgentButton prompt={"Review the DNS records of this workspace's domains and point out anything broken or risky."} />
            <Select value={current} onChange={(e) => setParams({ domain: e.target.value }, { replace: true })} className="w-56" title="Domain">
              {all.map((r) => (
                <option key={r.name} value={r.name}>
                  {zoneOf(r)}
                  {r.environment ? ` · ${r.environment}` : ""}
                </option>
              ))}
            </Select>
            <Button variant="secondary" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
              Add a domain
            </Button>
          </>
        }
      />
      {res && d && !d.error && <Records ws={ws} res={res} d={d} />}
      {res && d?.error && (
        <div className="px-5">
          <ErrorBox error={d.error} onRetry={() => void info.reload()} />
          <div className="mt-2 text-xs text-muted">
            The domain&apos;s connector is set under Connectors; the resource itself (zone {zoneOf(res)}) is under Resources.
          </div>
        </div>
      )}
      {res && !d && !info.error && <Loading />}
      {adding && <AddDomainModal onClose={() => setAdding(false)} onAdded={() => (setAdding(false), void reloadResources())} />}
    </div>
  );
}

function zoneOf(r: Resource): string {
  return String(r.spec.zone ?? r.name).toLowerCase();
}

function ZoneSummary({ d }: { d: DomainInfo }) {
  const z = d.info!;
  const ns = z.nameServers ?? [];
  const cur = z.currentNameServers ?? [];
  const mismatch = cur.length > 0 && ns.length > 0 && !ns.every((n) => cur.map(norm).includes(norm(n)));
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <ProviderMark provider={d.provider ?? ""} className="size-3.5" />
      <span>{d.provider === "cloudflare" ? "Cloudflare" : d.provider === "arvancloud" ? "ArvanCloud" : d.provider}</span>
      {d.account && <span className="text-faint">· {d.account}</span>}
      <Badge tone={z.status === "active" ? "ok" : z.status === "pending" ? "warn" : "neutral"}>{z.status}</Badge>
      {z.paused && <Badge tone="warn">paused</Badge>}
      {z.plan && <span className="text-faint">· {z.plan}</span>}
      {d.environment && <Badge tone={d.protected ? "err" : "neutral"}>{d.environment + (d.protected ? " · protected" : "")}</Badge>}
      {ns.length > 0 && (
        <span className="text-faint inline-flex items-center gap-1">
          · NS <span className="font-mono text-muted">{ns.join(" ")}</span>
          <CopyButton text={ns.join("\n")} />
        </span>
      )}
      {mismatch && (
        <span className="text-warn inline-flex items-center gap-1" title={`The registrar points at ${cur.join(", ")}`}>
          <AlertTriangle className="size-3.5" /> registrar still points at {cur.join(", ")}
        </span>
      )}
      {z.url && (
        <a href={z.url} target="_blank" rel="noreferrer" className="text-accent hover:underline inline-flex items-center gap-1">
          dashboard <ExternalLink className="size-3" />
        </a>
      )}
    </span>
  );
}

const norm = (s: string) => s.toLowerCase().replace(/\.$/, "");

// ---------------------------------------------------------------------------
// Records of a zone

function Records({ ws, res, d }: { ws: string; res: Resource; d: DomainInfo }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [q, setQ] = useState("");
  const [type, setType] = useState("");
  const recs = useAsync(() => api.dnsRecords(ws, res.name), [ws, res.name]);
  const [editing, setEditing] = useState<DNSRecord | true | null>(null);
  const list = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (recs.data ?? []).filter((r) => (!type || r.type === type) && (!needle || r.fqdn.includes(needle) || r.content.toLowerCase().includes(needle) || (r.comment ?? "").toLowerCase().includes(needle)));
  }, [recs.data, q, type]);
  const types = useMemo(() => Array.from(new Set((recs.data ?? []).map((r) => r.type))).sort(), [recs.data]);

  async function remove(r: DNSRecord) {
    const ok = await confirm({
      title: `Delete the ${r.type} record ${r.fqdn}?`,
      message: (
        <>
          <div className="font-mono text-xs break-all">
            {r.fqdn} {r.ttl > 1 ? r.ttl : "auto"} {r.type} {r.priority ? `${r.priority} ` : ""}
            {r.content}
          </div>
          <div className="mt-2">Resolvers keep the old answer for up to its TTL. {d.protected && "This zone is in a protected environment."}</div>
        </>
      ),
      confirmLabel: "Delete record",
      danger: true,
      typeToConfirm: d.protected ? r.fqdn : undefined,
    });
    if (!ok) return;
    try {
      await api.dnsRecordDelete(ws, res.name, r.id);
      toast.success("Record deleted", `${r.type} ${r.fqdn}`);
      void recs.reload();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  return (
    <>
      <div className="flex items-center gap-2 px-5 py-2.5">
        <SearchInput value={q} onChange={setQ} placeholder="Filter by name, content, comment…" className="w-72" />
        <Select value={type} onChange={(e) => setType(e.target.value)} className="w-32">
          <option value="">All types</option>
          {types.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </Select>
        <span className="text-xs text-faint">
          {recs.data ? `${list.length}${list.length !== recs.data.length ? ` of ${recs.data.length}` : ""} records` : ""}
        </span>
        <div className="flex-1" />
        <IconButton title="Reload" onClick={() => void recs.reload()}>
          <RefreshCw className={cx("size-4", recs.loading && "animate-spin")} />
        </IconButton>
        <Button variant="primary" size="sm" icon={<Plus className="size-4" />} onClick={() => setEditing(true)}>
          Add record
        </Button>
      </div>
      {recs.error && (
        <div className="px-5">
          <ErrorBox error={recs.error} onRetry={() => void recs.reload()} />
        </div>
      )}
      {recs.loading && !recs.data && <Loading />}
      {recs.data && (
        <Table className="flex-1 min-h-0">
          <thead>
            <tr>
              <Th className="w-20">Type</Th>
              <Th>Name</Th>
              <Th>Content</Th>
              <Th className="w-20">TTL</Th>
              <Th className="w-24">CDN</Th>
              {d.comments && <Th>Comment</Th>}
              <Th className="w-24" />
            </tr>
          </thead>
          <tbody>
            {list.length === 0 && (
              <tr>
                <Td colSpan={7} className="text-center text-faint py-8">
                  {recs.data.length === 0 ? "This zone has no records yet." : "No records match."}
                </Td>
              </tr>
            )}
            {list.map((r) => (
              <Tr key={r.id} onClick={() => !r.locked && setEditing(r)}>
                <Td>
                  <Badge mono tone={r.type === "A" || r.type === "AAAA" ? "info" : r.type === "CNAME" ? "accent" : "neutral"}>
                    {r.type}
                  </Badge>
                </Td>
                <Td mono title={r.fqdn}>
                  <span className={cx(r.name === "@" && "text-muted")}>{r.name}</span>
                  {r.locked && <Lock className="inline size-3 ms-1.5 text-faint" aria-label="managed by the provider" />}
                </Td>
                <Td mono className="max-w-[420px]">
                  <span className="block truncate" title={r.content}>
                    {r.priority ? <span className="text-faint">{r.priority} </span> : null}
                    {r.content}
                  </span>
                </Td>
                <Td mono className="text-muted">
                  {r.ttl > 1 ? `${r.ttl}s` : "auto"}
                </Td>
                <Td>
                  {r.proxiable ? (
                    r.proxied ? (
                      <span className="inline-flex items-center gap-1 text-accent text-[11.5px]" title={d.proxyLabel}>
                        <Cloud className="size-3.5" /> proxied
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1 text-faint text-[11.5px]" title="DNS only">
                        <CloudOff className="size-3.5" /> DNS only
                      </span>
                    )
                  ) : (
                    <span className="text-faint">—</span>
                  )}
                </Td>
                {d.comments && (
                  <Td className="text-muted max-w-[220px]">
                    <span className="block truncate" title={r.comment}>
                      {r.comment}
                    </span>
                  </Td>
                )}
                <Td>
                  <div className="flex items-center justify-end gap-0.5 opacity-0 group-hover:opacity-100">
                    {!r.locked && (
                      <IconButton
                        title="Edit"
                        onClick={(e) => {
                          e.stopPropagation();
                          setEditing(r);
                        }}
                      >
                        <Pencil className="size-3.5" />
                      </IconButton>
                    )}
                    {!r.locked && (
                      <IconButton
                        title="Delete"
                        onClick={(e) => {
                          e.stopPropagation();
                          void remove(r);
                        }}
                      >
                        <Trash2 className="size-3.5" />
                      </IconButton>
                    )}
                  </div>
                </Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      )}
      {editing && (
        <RecordModal
          ws={ws}
          res={res}
          d={d}
          record={editing === true ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void recs.reload();
          }}
        />
      )}
    </>
  );
}

// RecordModal adds a record or changes one.
function RecordModal({ ws, res, d, record, onClose, onSaved }: { ws: string; res: Resource; d: DomainInfo; record?: DNSRecord; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const zone = d.zone;
  const [type, setType] = useState(record?.type ?? "A");
  const [name, setName] = useState(record?.name ?? "");
  const [content, setContent] = useState(record?.content ?? "");
  const [ttl, setTtl] = useState<number>(record ? record.ttl : d.ttls?.length ? d.ttls[0] : 1);
  const [priority, setPriority] = useState<number>(record?.priority ?? 10);
  const [proxied, setProxied] = useState(record?.proxied ?? false);
  const [comment, setComment] = useState(record?.comment ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const proxiable = ["A", "AAAA", "CNAME"].includes(type);
  const hasPriority = type === "MX" || type === "SRV";
  const hint: Record<string, string> = {
    A: d.provider === "arvancloud" ? "An IPv4 address; several, comma-separated, are balanced" : "An IPv4 address, e.g. 203.0.113.10",
    AAAA: "An IPv6 address",
    CNAME: "The host name this name is an alias of, e.g. shop.example.net",
    MX: "The mail server's host name; priority below (lower wins)",
    TXT: "The text, e.g. v=spf1 include:_spf.example.net -all",
    NS: "A name server's host name",
    SRV: "weight port target, e.g. 5 5060 sip.example.com — the name is like _sip._tcp",
    CAA: 'flags tag "value", e.g. 0 issue "letsencrypt.org"',
    PTR: "The host name the address maps to",
  };
  const ttlOptions = d.ttls?.length ? d.ttls : [1, 60, 120, 300, 600, 1800, 3600, 7200, 14400, 43200, 86400];

  async function save() {
    setBusy(true);
    setErr("");
    const body: DNSRecordInput = { type, name: name.trim() || "@", content: content.trim(), ttl, priority: hasPriority ? priority : 0, proxied: proxiable ? proxied : undefined, comment: d.comments ? comment : undefined };
    try {
      if (record) await api.dnsRecordUpdate(ws, res.name, record.id, body);
      else await api.dnsRecordCreate(ws, res.name, body);
      toast.success(record ? "Record saved" : "Record added", `${type} ${fqdn(body.name, zone)}`);
      onSaved();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={record ? `Edit ${record.type} ${record.fqdn}` : `Add a record to ${zone}`}
      icon={<Globe />}
      width={560}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!content.trim()} onClick={() => void save()}>
            {record ? "Save" : "Add"}
          </Button>
        </>
      }
    >
      <div className="space-y-3.5">
        <div className="grid grid-cols-[120px_1fr] gap-3">
          <FormField label="Type" required>
            <Select value={type} onChange={(e) => setType(e.target.value)} disabled={!!record && !DNS_RECORD_TYPES.includes(record.type)}>
              {DNS_RECORD_TYPES.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
              {record && !DNS_RECORD_TYPES.includes(record.type) && <option value={record.type}>{record.type}</option>}
            </Select>
          </FormField>
          <FormField label="Name" required help={`Relative to ${zone}: @ for the zone itself, www, _dmarc. Full names are accepted too.`}>
            <div className="flex items-center gap-1.5">
              <Input mono autoFocus={!record} value={name} onChange={(e) => setName(e.target.value)} placeholder="@" className="flex-1" />
              <span className="text-xs text-faint font-mono shrink-0">.{zone}</span>
            </div>
          </FormField>
        </div>
        <FormField label="Content" required help={hint[type]}>
          {type === "TXT" ? (
            <Textarea mono rows={3} value={content} onChange={(e) => setContent(e.target.value)} placeholder={'v=spf1 -all'} />
          ) : (
            <Input mono value={content} onChange={(e) => setContent(e.target.value)} placeholder={type === "A" ? "203.0.113.10" : type === "CNAME" ? "target.example.net" : ""} />
          )}
        </FormField>
        <div className="grid grid-cols-2 gap-3">
          <FormField label="TTL" help={d.ttls?.length ? "Values the provider accepts" : "auto: the provider decides (Cloudflare: 300s, or 1s when proxied)"}>
            <Select value={String(ttlOptions.includes(ttl) ? ttl : "custom")} onChange={(e) => e.target.value !== "custom" && setTtl(Number(e.target.value))}>
              {ttlOptions.map((t) => (
                <option key={t} value={t}>
                  {t <= 1 ? "auto" : t >= 3600 ? `${t / 3600} h` : t >= 60 ? `${t / 60} min` : `${t} s`}
                </option>
              ))}
              {!ttlOptions.includes(ttl) && <option value="custom">{ttl} s</option>}
            </Select>
          </FormField>
          {hasPriority && (
            <FormField label="Priority" help="Lower is preferred">
              <Input type="number" mono value={priority} onChange={(e) => setPriority(Number(e.target.value))} />
            </FormField>
          )}
        </div>
        {proxiable && (
          <div>
            <Toggle checked={proxied} onChange={setProxied} label={d.proxyLabel || "Proxied through the CDN"} />
            <div className="mt-1 text-[11px] text-faint">On: visitors reach the provider&apos;s edge and the origin address stays hidden. Off: DNS only, the address is published as it is.</div>
          </div>
        )}
        {d.comments && (
          <FormField label="Comment" help="A note kept with the record at the provider">
            <Input value={comment} onChange={(e) => setComment(e.target.value)} placeholder="optional" />
          </FormField>
        )}
        {d.protected && (
          <div className="flex items-center gap-2 rounded-md border border-warn/40 bg-warn/8 px-3 py-2 text-xs text-warn">
            <AlertTriangle className="size-3.5 shrink-0" /> This zone belongs to a protected environment: check the value twice; resolvers keep the old answer for up to its TTL.
          </div>
        )}
        {err && <div className="text-xs text-err whitespace-pre-wrap">{err}</div>}
      </div>
    </Modal>
  );
}

function fqdn(name: string, zone: string): string {
  const n = name.toLowerCase().replace(/\.$/, "");
  if (!n || n === "@" || n === zone) return zone;
  if (n.endsWith("." + zone)) return n;
  return `${n}.${zone}`;
}

// ---------------------------------------------------------------------------
// Adding a domain: pick a zone from a connected account.

function AddDomainModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const { name: ws, workspace, resources } = useWs();
  const toast = useToast();
  const conns = useAsync(() => api.dnsConnections(), []);
  const [connId, setConnId] = useState("");
  const [connecting, setConnecting] = useState(false);
  const [q, setQ] = useState("");
  const [pick, setPick] = useState<DNSZone | null>(null);
  const [name, setName] = useState("");
  const [env, setEnv] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!connId && conns.data?.length) setConnId(conns.data[0].id);
  }, [conns.data, connId]);
  const zones = useAsync(() => (connId ? api.dnsAccountZones(connId) : Promise.resolve([] as DNSZone[])), [connId]);
  const existing = resources.filter((r) => r.kind === "Domain");
  const added = (z: DNSZone) => existing.some((x) => zoneOf(x) === z.name);
  const list = (zones.data ?? []).filter((z) => !q.trim() || z.name.includes(q.trim().toLowerCase()));
  const conn = conns.data?.find((c) => c.id === connId);

  function choose(z: DNSZone) {
    setPick(z);
    let n = slugify(z.name) || "domain";
    const taken = new Set(existing.map((x) => x.name));
    for (let i = 2; taken.has(n); i++) n = `${slugify(z.name)}-${i}`;
    setName(n);
  }

  async function add() {
    if (!pick || !conn) return;
    setBusy(true);
    try {
      await api.createResource(ws, { kind: "Domain", name, environment: env || undefined, spec: { zone: pick.name, connection: conn.id } } as Resource);
      toast.success(`Added ${pick.name}`, `as ${name}`);
      onAdded();
    } catch (e) {
      toast.error("Could not add the domain", errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  const onConnected = (c: DNSConnection) => {
    setConnecting(false);
    setConnId(c.id);
    void conns.reload();
  };
  if (connecting) return <DNSConnectModal onClose={() => setConnecting(false)} onConnected={onConnected} />;
  if (conns.data && conns.data.length === 0) {
    return (
      <Modal open onClose={onClose} title="Add a domain" icon={<Globe />} width={520}>
        <div className="text-center py-4 space-y-3">
          <div className="flex justify-center gap-3 text-muted">
            <ProviderMark provider="cloudflare" className="size-7" />
            <ProviderMark provider="arvancloud" className="size-7" />
          </div>
          <div className="text-[13px]">Connect your Cloudflare or ArvanCloud account first: you then pick the zones from a list.</div>
          <Button variant="primary" icon={<Plug className="size-4" />} onClick={() => setConnecting(true)}>
            Connect an account
          </Button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal
      open
      onClose={onClose}
      title="Add a domain"
      icon={<Globe />}
      width={640}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!pick || !/^[a-z0-9][a-z0-9-]{0,62}$/.test(name)} loading={busy} onClick={() => void add()}>
            Add
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="flex items-end gap-2">
          <FormField label="Account" className="flex-1">
            <Select
              value={connId}
              onChange={(e) => {
                if (e.target.value === "__new") setConnecting(true);
                else {
                  setConnId(e.target.value);
                  setPick(null);
                }
              }}
            >
              {(conns.data ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.label} · {c.account || `${c.zones} zones`} · {c.id}
                </option>
              ))}
              <option value="__new">+ Connect another account…</option>
            </Select>
          </FormField>
          <SearchInput value={q} onChange={setQ} placeholder="Filter zones…" className="w-56" />
        </div>
        <div className="rounded-md border border-line bg-panel-2 max-h-72 overflow-y-auto">
          {zones.loading && !zones.data && (
            <div className="flex items-center gap-2 p-3 text-xs text-muted">
              <Spinner className="size-3.5" /> Loading zones…
            </div>
          )}
          {zones.error && <div className="p-3 text-xs text-err">{zones.error}</div>}
          {zones.data && list.length === 0 && <div className="p-3 text-xs text-faint">No zones{q ? " match" : " in this account"}.</div>}
          {list.map((z) => {
            const dup = added(z);
            return (
              <button
                key={z.id}
                type="button"
                disabled={dup}
                onClick={() => choose(z)}
                className={cx(
                  "w-full flex items-center gap-3 px-3 h-10 text-left border-b border-line/60 last:border-0",
                  pick?.id === z.id ? "bg-accent/10" : "hover:bg-panel-3",
                  dup && "opacity-50 cursor-default",
                )}
              >
                <Globe className="size-4 text-muted shrink-0" />
                <span className="font-mono text-[13px] flex-1 min-w-0 truncate">{z.name}</span>
                {z.account && <span className="text-[11px] text-faint truncate max-w-[160px]">{z.account}</span>}
                <Badge tone={z.status === "active" ? "ok" : "warn"}>{z.status}</Badge>
                {dup && <Badge tone="neutral">added</Badge>}
                {z.modified && <span className="text-[11px] text-faint tabular-nums">{timeAgo(z.modified)}</span>}
              </button>
            );
          })}
        </div>
        {pick && (
          <div className="grid grid-cols-2 gap-3 pt-1">
            <FormField label="Resource name" help="Lowercase letters, digits and dashes">
              <Input mono value={name} onChange={(e) => setName(e.target.value)} />
            </FormField>
            <FormField label="Environment">
              <Select value={env} onChange={(e) => setEnv(e.target.value)}>
                <option value="">— none —</option>
                {workspace?.environments.map((e) => (
                  <option key={e.name} value={e.name}>
                    {e.name}
                    {e.protected ? " (protected)" : ""}
                  </option>
                ))}
              </Select>
            </FormField>
          </div>
        )}
      </div>
    </Modal>
  );
}
