import { useEffect, useRef, useState } from "react";
import { AskAgentButton } from "../components/AskAgent";
import { AlertTriangle, BadgeCheck, CheckCircle2, ChevronDown, ChevronRight, Clock, Pencil, Plus, RefreshCw, Server, ShieldAlert, Upload } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { CertDeploy, CertJob, CertStatus, Resource } from "../api/types";
import { useWs } from "../context/app";
import { useTabTitle } from "../context/tabs";
import { Badge, Button, CodeBlock, CopyButton, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, PageHeader, Select, Spinner, cx } from "../components/ui";
import type { Tone } from "../components/ui";
import { Drawer, Modal, useToast } from "../components/overlay";
import { useAsync, useInterval } from "../hooks";
import { timeAgo } from "../lib/format";
import { ResourceDrawer } from "./Resources";

// Certificates of the workspace (Certificate resources): issued and renewed
// from an ACME CA with dns-01 through the DNS connectors, kept in the vault,
// deployed to hosts from there. Every issuance is a job with a live log.
export function CertificatesPage() {
  const { name: ws, byKind, reloadResources } = useWs();
  const resources = byKind("Certificate");
  const list = useAsync(() => api.certificates(ws), [ws, resources.length]);
  const kinds = useAsync(() => api.kinds(), []);
  const schema = kinds.data?.find((k) => k.kind === "Certificate");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<Resource | null>(null);
  const [job, setJob] = useState<{ cert: string; id: string } | null>(null);
  const [detail, setDetail] = useState<string | null>(null);
  const [deploying, setDeploying] = useState<CertStatus | null>(null);
  useTabTitle("Certificates");
  const running = (list.data ?? []).some((c) => c.job);
  useInterval(() => void list.reload(), running ? 4000 : null);
  const toast = useToast();

  async function issue(c: CertStatus) {
    try {
      const j = await api.certIssue(ws, c.name);
      setJob({ cert: c.name, id: j.id });
      void list.reload();
    } catch (e) {
      toast.error("Could not start", errMsg(e));
    }
  }
  if (!resources.length) {
    return (
      <div className="flex flex-col h-full">
        <PageHeader icon={<BadgeCheck />} title="Certificates" />
        <EmptyState
          icon={<BadgeCheck />}
          title="No certificates in this workspace"
          action={
            <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
              Add a certificate
            </Button>
          }
        >
          Describe the certificate you need (its domains, the CA — Let&apos;s Encrypt by default); opsdeck proves the domains with DNS records through your Cloudflare or ArvanCloud connector, keeps the
          certificate and key in the vault, renews it when due and deploys it to your hosts.
        </EmptyState>
        {adding && schema && <ResourceDrawer schema={schema} resource={null} title="New certificate" onClose={() => setAdding(false)} onSaved={async () => (setAdding(false), await reloadResources())} />}
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<BadgeCheck />}
        title="Certificates"
        subtitle="TLS certificates from ACME CAs, proven over DNS through the connectors, kept in the vault"
        actions={
          <>
            <AskAgentButton prompt={"Check this workspace's TLS certificates and tell me which expire soon or fail to renew."} />
            <IconButton title="Reload" onClick={() => void list.reload()}>
              <RefreshCw className={cx("size-4", list.loading && "animate-spin")} />
            </IconButton>
            <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
              Add a certificate
            </Button>
          </>
        }
      />
      <div className="px-5 pb-8 space-y-3 overflow-y-auto">
        {list.error && <ErrorBox error={list.error} onRetry={() => void list.reload()} />}
        {list.loading && !list.data && <Loading />}
        {(list.data ?? []).map((c) => (
          <CertCard
            key={c.name}
            c={c}
            onIssue={() => void issue(c)}
            onFollow={() => c.job && setJob({ cert: c.name, id: c.job })}
            onDetail={() => setDetail(c.name)}
            onDeploy={() => setDeploying(c)}
            onEdit={() => setEditing(resources.find((r) => r.name === c.name) ?? null)}
          />
        ))}
      </div>
      {adding && schema && <ResourceDrawer schema={schema} resource={null} title="New certificate" onClose={() => setAdding(false)} onSaved={async () => (setAdding(false), await reloadResources(), void list.reload())} />}
      {editing && schema && <ResourceDrawer schema={schema} resource={editing} onClose={() => setEditing(null)} onSaved={async () => (setEditing(null), await reloadResources(), void list.reload())} />}
      {job && (
        <JobModal
          ws={ws}
          cert={job.cert}
          id={job.id}
          onClose={() => {
            setJob(null);
            void list.reload();
          }}
        />
      )}
      {detail && <DetailDrawer ws={ws} name={detail} onClose={() => setDetail(null)} />}
      {deploying && <DeployModal ws={ws} c={deploying} onClose={() => setDeploying(null)} />}
    </div>
  );
}

function statusTone(c: CertStatus): Tone {
  if (c.error) return "err";
  if (!c.issued) return "neutral";
  if (c.info && c.info.daysLeft < 0) return "err";
  if (c.due) return "warn";
  return "ok";
}

function CertCard({ c, onIssue, onFollow, onDetail, onDeploy, onEdit }: { c: CertStatus; onIssue: () => void; onFollow: () => void; onDetail: () => void; onDeploy: () => void; onEdit: () => void }) {
  const tone = statusTone(c);
  const label = c.error ? "invalid" : !c.issued ? "not issued" : c.info && c.info.daysLeft < 0 ? "expired" : c.due ? "due for renewal" : "valid";
  return (
    <div className="rounded-lg border border-line bg-panel px-4 py-3">
      <div className="flex items-start gap-3">
        <span className={cx("mt-0.5 shrink-0", tone === "ok" ? "text-ok" : tone === "warn" ? "text-warn" : tone === "err" ? "text-err" : "text-faint")}>
          {tone === "ok" ? <CheckCircle2 className="size-5" /> : tone === "err" ? <ShieldAlert className="size-5" /> : tone === "warn" ? <AlertTriangle className="size-5" /> : <Clock className="size-5" />}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-[14px] font-semibold font-mono">{c.name}</span>
            <Badge tone={tone}>{label}</Badge>
            {c.job && (
              <button type="button" onClick={onFollow} className="inline-flex items-center gap-1 text-[11.5px] text-info hover:underline">
                <Spinner className="size-3" /> issuing… follow
              </button>
            )}
            {c.environment && <Badge tone={c.protected ? "err" : "neutral"}>{c.environment + (c.protected ? " · protected" : "")}</Badge>}
            {c.staging && <Badge tone="warn">test CA</Badge>}
            {c.autoRenew && <Badge tone="info">auto-renew</Badge>}
          </div>
          <div className="mt-0.5 font-mono text-[12.5px] text-fg break-all">{c.domains.join("  ")}</div>
          <div className="text-[11.5px] text-faint mt-0.5">
            {c.caLabel}
            {c.error ? (
              <span className="text-err"> · {c.error}</span>
            ) : c.issued && c.info ? (
              <>
                {" "}
                · issued by {c.info.issuer} · expires {new Date(c.info.notAfter).toLocaleDateString()} ({c.info.daysLeft} days) · {c.info.keyType} · renew {c.renewDays} days before
                {c.due && c.reason && <span className="text-warn"> · {c.reason}</span>}
              </>
            ) : (
              <> · not issued yet · renew {c.renewDays} days before expiry</>
            )}
            {" · vault "}
            <span className="font-mono">{c.item}</span>
          </div>
        </div>
        <div className="flex items-center gap-1 shrink-0">
          {c.issued && (
            <Button size="sm" variant="ghost" onClick={onDetail}>
              Details
            </Button>
          )}
          {c.issued && (
            <Button size="sm" variant="secondary" icon={<Upload className="size-3.5" />} onClick={onDeploy}>
              Deploy
            </Button>
          )}
          <Button size="sm" variant={c.due && !c.error ? "primary" : "secondary"} disabled={!!c.job || !!c.error} loading={!!c.job} icon={<RefreshCw className="size-3.5" />} onClick={onIssue}>
            {c.issued ? "Renew" : "Issue"}
          </Button>
          <IconButton title="Edit" onClick={onEdit}>
            <Pencil className="size-4" />
          </IconButton>
        </div>
      </div>
    </div>
  );
}

// JobModal follows an issuance: its log, live, then the result.
function JobModal({ ws, cert, id, onClose }: { ws: string; cert: string; id: string; onClose: () => void }) {
  const [job, setJob] = useState<CertJob | null>(null);
  const [err, setErr] = useState("");
  const linesRef = useRef<string[]>([]);
  const boxRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let stop = false;
    linesRef.current = [];
    (async () => {
      let since = 0;
      while (!stop) {
        try {
          const j = await api.certJob(ws, id, since, true);
          if (stop) return;
          linesRef.current = [...linesRef.current, ...j.lines];
          since = j.total;
          setJob({ ...j, lines: linesRef.current });
          if (j.state !== "running") return;
        } catch (e) {
          if (!stop) setErr(errMsg(e));
          return;
        }
      }
    })();
    return () => {
      stop = true;
    };
  }, [ws, id]);
  useEffect(() => {
    boxRef.current?.scrollTo({ top: boxRef.current.scrollHeight });
  }, [job?.lines.length]);
  const state = job?.state ?? "running";
  return (
    <Modal
      open
      onClose={onClose}
      title={
        <>
          {state === "running" ? <Spinner className="size-4" /> : state === "done" ? <CheckCircle2 className="size-4 text-ok" /> : <ShieldAlert className="size-4 text-err" />}
          {state === "running" ? `Issuing ${cert}…` : state === "done" ? `${cert} issued` : `${cert}: issuance failed`}
        </>
      }
      width={720}
      footer={
        <Button variant={state === "running" ? "ghost" : "primary"} onClick={onClose}>
          {state === "running" ? "Keep running in the background" : "Close"}
        </Button>
      }
    >
      {err && <div className="text-xs text-err mb-2">{err}</div>}
      <div ref={boxRef} className="rounded-md border border-line bg-panel-2 font-mono text-[11.5px] p-3 max-h-[52vh] overflow-y-auto whitespace-pre-wrap">
        {(job?.lines ?? []).map((l, i) => (
          <div key={i} className={cx(l.includes("  error:") && "text-err")}>
            {l}
          </div>
        ))}
        {state === "running" && <div className="text-faint">…</div>}
      </div>
      {job?.state === "done" && job.result && (
        <div className="mt-3 text-[12.5px] flex items-center gap-2 text-ok">
          <CheckCircle2 className="size-4" /> Valid until {new Date(job.result.info.notAfter).toLocaleDateString()} ({job.result.info.daysLeft} days), stored as <span className="font-mono">{job.result.item}</span>. Deploy it where it is used.
        </div>
      )}
      {job?.state === "error" && <div className="mt-3 text-[12.5px] text-err whitespace-pre-wrap">{job.error}</div>}
    </Modal>
  );
}

function DetailDrawer({ ws, name, onClose }: { ws: string; name: string; onClose: () => void }) {
  const d = useAsync(() => api.certificate(ws, name), [ws, name]);
  const c = d.data;
  return (
    <Drawer open onClose={onClose} width={640} title={`Certificate ${name}`} subtitle={c?.info ? c.info.domains.join(", ") : undefined}>
      {d.error && <ErrorBox error={d.error} />}
      {!c && !d.error && <Loading />}
      {c?.info && (
        <div className="space-y-4">
          <dl className="grid grid-cols-[140px_1fr] gap-x-3 gap-y-1.5 text-[12.5px]">
            <Row k="Subject" v={c.info.subject} />
            <Row k="Names" v={c.info.domains.join(", ")} />
            <Row k="Issuer" v={c.info.issuer} />
            <Row k="Valid" v={`${new Date(c.info.notBefore).toLocaleString()} → ${new Date(c.info.notAfter).toLocaleString()} (${c.info.daysLeft} days left)`} />
            <Row k="Key" v={c.info.keyType} />
            <Row k="Serial" v={c.info.serial} mono />
            <Row k="SHA-256" v={c.info.fingerprint} mono />
            <Row k="Chain" v={`${c.info.chain} certificates`} />
            <Row k="Issued" v={c.issuedAt ? `${timeAgo(c.issuedAt)} (${new Date(c.issuedAt).toLocaleString()})` : "—"} />
            <Row k="Vault item" v={c.item} mono />
          </dl>
          <div className="text-[11.5px] text-faint">The private key stays in the vault (reveal it there if you must); Deploy copies both to a host.</div>
          {c.chain && (
            <div>
              <div className="flex items-center justify-between mb-1">
                <div className="text-[11px] uppercase tracking-wide text-faint">Certificate chain (PEM)</div>
                <CopyButton text={c.chain} />
              </div>
              <CodeBlock maxHeight={288} className="text-[10.5px]">
                {c.chain}
              </CodeBlock>
            </div>
          )}
        </div>
      )}
    </Drawer>
  );
}

function Row({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <>
      <dt className="text-faint">{k}</dt>
      <dd className={cx("break-all", mono && "font-mono text-[11.5px]")}>{v}</dd>
    </>
  );
}

// DeployModal writes the certificate to a host and optionally reloads the
// service there.
function DeployModal({ ws, c, onClose }: { ws: string; c: CertStatus; onClose: () => void }) {
  const { hosts } = useWs();
  const toast = useToast();
  const base = "/etc/ssl/" + c.domains[0].replace(/^\*\./, "");
  const [host, setHost] = useState(hosts[0]?.name ?? "");
  const [certPath, setCertPath] = useState(base + "/fullchain.pem");
  const [keyPath, setKeyPath] = useState(base + "/privkey.pem");
  const [more, setMore] = useState(false);
  const [leafPath, setLeafPath] = useState("");
  const [chainPath, setChainPath] = useState("");
  const [bundlePath, setBundlePath] = useState("");
  const [command, setCommand] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ written: string[]; exitCode?: number; stdout?: string; stderr?: string; error?: string } | null>(null);
  const presets: { label: string; command: string }[] = [
    { label: "nginx", command: "sudo -n nginx -t && sudo -n systemctl reload nginx" },
    { label: "Apache", command: "sudo -n apachectl configtest && sudo -n systemctl reload apache2" },
    { label: "HAProxy", command: "sudo -n systemctl reload haproxy" },
    { label: "Caddy", command: "sudo -n systemctl reload caddy" },
  ];
  async function run() {
    setBusy(true);
    setResult(null);
    const d: CertDeploy = { host, certPath: certPath.trim() || undefined, keyPath: keyPath.trim() || undefined, leafPath: leafPath.trim() || undefined, chainPath: chainPath.trim() || undefined, bundlePath: bundlePath.trim() || undefined, command: command.trim() || undefined };
    try {
      const r = await api.certDeploy(ws, c.name, d);
      setResult(r);
      toast.success(`Deployed ${c.name}`, `${r.written.length} files on ${host}`);
    } catch (e) {
      const msg = errMsg(e);
      setResult({ written: [], error: msg });
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={`Deploy ${c.name}`}
      icon={<Upload />}
      width={620}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {result ? "Close" : "Cancel"}
          </Button>
          <Button variant="primary" loading={busy} disabled={!host || !(certPath.trim() || keyPath.trim() || bundlePath.trim())} onClick={() => void run()}>
            Write files
          </Button>
        </>
      }
    >
      <div className="space-y-3.5">
        {hosts.length === 0 && <div className="text-xs text-warn">This workspace has no hosts yet (add one under Resources).</div>}
        <FormField label="Host">
          <Select value={host} onChange={(e) => setHost(e.target.value)}>
            {hosts.map((h) => (
              <option key={h.name} value={h.name}>
                {h.name}
                {h.environment ? ` · ${h.environment}` : ""}
              </option>
            ))}
          </Select>
        </FormField>
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Full chain" help="Leaf and intermediates — nginx ssl_certificate, Caddy, most servers">
            <Input mono value={certPath} onChange={(e) => setCertPath(e.target.value)} />
          </FormField>
          <FormField label="Private key" help="Written with mode 0600">
            <Input mono value={keyPath} onChange={(e) => setKeyPath(e.target.value)} />
          </FormField>
        </div>
        <div>
          <button type="button" onClick={() => setMore((v) => !v)} className="flex items-center gap-1 text-xs text-muted hover:text-fg">
            {more ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />} Other layouts (Apache, HAProxy)
          </button>
          {more && (
            <div className="mt-2 grid grid-cols-3 gap-3">
              <FormField label="Leaf only" help="Apache SSLCertificateFile">
                <Input mono value={leafPath} onChange={(e) => setLeafPath(e.target.value)} placeholder={base + "/cert.pem"} />
              </FormField>
              <FormField label="Intermediates" help="Apache SSLCertificateChainFile">
                <Input mono value={chainPath} onChange={(e) => setChainPath(e.target.value)} placeholder={base + "/chain.pem"} />
              </FormField>
              <FormField label="Chain + key" help="HAProxy, one file, 0600">
                <Input mono value={bundlePath} onChange={(e) => setBundlePath(e.target.value)} placeholder={"/etc/haproxy/certs/" + c.domains[0].replace(/^\*\./, "") + ".pem"} />
              </FormField>
            </div>
          )}
        </div>
        <FormField
          label="Then run"
          help={
            <span className="inline-flex items-center gap-1.5 flex-wrap">
              Reload the service after the files are written (optional).
              {presets.map((p) => (
                <button key={p.label} type="button" className="text-accent hover:underline" onClick={() => setCommand(p.command)}>
                  {p.label}
                </button>
              ))}
            </span>
          }
        >
          <Input mono value={command} onChange={(e) => setCommand(e.target.value)} placeholder="sudo -n systemctl reload nginx" />
        </FormField>
        <div className="text-[11.5px] text-faint flex items-start gap-1.5">
          <Server className="size-3.5 mt-0.5 shrink-0" />
          Files are written over SFTP as the host&apos;s SSH user (folders are created when missing). If the folder belongs to root, prepare it for the user, or let the agent write the files with sudo.
        </div>
        {result && (
          <div className={cx("rounded-md border px-3 py-2 text-[12px] space-y-1", result.error ? "border-err/40 bg-err/8" : "border-ok/40 bg-ok/8")}>
            {result.written.length > 0 && (
              <div>
                Written: <span className="font-mono">{result.written.join(", ")}</span>
              </div>
            )}
            {result.exitCode !== undefined && <div>Command exited with {result.exitCode}</div>}
            {result.stdout && <pre className="font-mono text-[11px] whitespace-pre-wrap max-h-32 overflow-auto">{result.stdout}</pre>}
            {result.stderr && <pre className="font-mono text-[11px] whitespace-pre-wrap max-h-32 overflow-auto text-err">{result.stderr}</pre>}
            {result.error && <div className="text-err whitespace-pre-wrap">{result.error}</div>}
          </div>
        )}
      </div>
    </Modal>
  );
}
