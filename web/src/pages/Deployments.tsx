import { useEffect, useMemo, useState } from "react";
import { AskAgentButton } from "../components/AskAgent";
import { useSearchParams } from "react-router-dom";
import { AlertTriangle, ExternalLink, GitBranch, History, Pencil, Play, Plus, RefreshCw, Rocket, RotateCcw, Square, Undo2 } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { DeployActionInput, DeployDetail, DeployPlatformInfo, DeployPlatformType, DeployTarget, Resource } from "../api/types";
import { useWs } from "../context/app";
import { useTabTitle } from "../context/tabs";
import { Badge, Button, CodeBlock, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, PageHeader, SearchInput, Select, Spinner, StatusDot, Table, Tabs, Td, Textarea, Th, Toggle, Tr, cx } from "../components/ui";
import { Drawer, Modal, useToast } from "../components/overlay";
import { useAsync, useInterval, useLocalStorage } from "../hooks";
import { healthTone, shortRev } from "../lib/deploy";
import { timeAgo } from "../lib/format";
import { DiffView } from "./Git";
import { ResourceDrawer } from "./Resources";

// Deployments of the workspace: its deployment platforms (DeployPlatform
// resources — Portainer, Argo CD, Komodo, self-hosted, reached through
// their via like any resource) and what they deploy: status and health,
// services, history, logs, what a deploy would change, and the actions
// (deploy/sync, restart, rollback…). Every action is recorded in the
// activity journal.
export function DeploymentsPage() {
  const { name: ws, byKind, reloadResources } = useWs();
  const all = byKind("DeployPlatform");
  const [params, setParams] = useSearchParams();
  const [saved, setSaved] = useLocalStorage<string>(`opsdeck.deploy.${ws}`, "");
  const current = params.get("platform") || (all.some((d) => d.name === saved) ? saved : all[0]?.name) || "";
  const res = all.find((d) => d.name === current);
  useEffect(() => {
    if (current) setSaved(current);
  }, [current, setSaved]);
  const kinds = useAsync(() => api.kinds(), []);
  const schema = kinds.data?.find((k) => k.kind === "DeployPlatform");
  const types = useAsync(() => api.deployPlatformTypes(), []);
  const platforms = useAsync(() => api.deployPlatforms(ws), [ws, all.length, JSON.stringify(res?.spec ?? {}), res?.via]);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState(false);
  useTabTitle(res ? `${res.name} · Deployments` : "Deployments");

  const add = adding && schema && (
    <ResourceDrawer schema={schema} resource={null} title="New deployment platform" onClose={() => setAdding(false)} onSaved={async () => (setAdding(false), await reloadResources())} />
  );
  if (!all.length) {
    return (
      <div className="flex flex-col h-full">
        <PageHeader icon={<Rocket />} title="Deployments" />
        <EmptyState
          icon={<Rocket />}
          title="No deployment platforms in this workspace"
          action={
            <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
              Add a platform
            </Button>
          }
        >
          Add the Portainer, Argo CD or Komodo of this workspace — self-hosted, behind a VPN or a jump host — with a token from the vault. Its stacks and applications are shown here with their
          health, logs and history, deployed from here or by the agent&apos;s <span className="font-mono">deploy</span> tool, with every action recorded.
        </EmptyState>
        {add}
      </div>
    );
  }

  const info = platforms.data?.find((p) => p.name === current);
  const type = types.data?.find((t) => t.id === info?.platform);
  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<Rocket />}
        title="Deployments"
        subtitle={platforms.error ? <span className="text-err">{platforms.error}</span> : info ? <PlatformSummary p={info} /> : "Stacks and applications of this workspace's deployment platforms"}
        actions={
          <>
            <AskAgentButton prompt={"Summarise the state of this workspace's deployments and tell me if anything failed recently."} />
            <Select value={current} onChange={(e) => setParams({ platform: e.target.value }, { replace: true })} className="w-56" title="Platform">
              {all.map((r) => (
                <option key={r.name} value={r.name}>
                  {r.name} · {platformLabel(String(r.spec.platform ?? ""))}
                  {r.environment ? ` · ${r.environment}` : ""}
                </option>
              ))}
            </Select>
            {res && (
              <IconButton title="Edit the platform" onClick={() => setEditing(true)}>
                <Pencil className="size-4" />
              </IconButton>
            )}
            <Button variant="secondary" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
              Add a platform
            </Button>
          </>
        }
      />
      {res && info?.error && (
        <div className="px-5">
          <ErrorBox error={info.error} onRetry={() => void platforms.reload()} />
          <div className="mt-2 text-xs text-muted">
            Check the URL, the credential ({type?.credential === "login" ? "a login item: API key as username, secret as password" : "a token item"}) and how opsdeck reaches the server (via) under{" "}
            <button className="text-accent hover:underline" onClick={() => setEditing(true)}>
              the platform&apos;s settings
            </button>
            .
          </div>
        </div>
      )}
      {res && info && !info.error && type && <Targets ws={ws} res={res} info={info} type={type} />}
      {res && !info && !platforms.error && <Loading />}
      {add}
      {editing && schema && res && <ResourceDrawer schema={schema} resource={res} onClose={() => setEditing(false)} onSaved={async () => (setEditing(false), await reloadResources())} />}
    </div>
  );
}

function platformLabel(id: string): string {
  return id === "argocd" ? "Argo CD" : id === "portainer" ? "Portainer" : id === "komodo" ? "Komodo" : id;
}

function PlatformSummary({ p }: { p: DeployPlatformInfo }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <span>{p.label || platformLabel(p.platform)}</span>
      {p.server?.version && <span className="text-faint">{p.server.version}</span>}
      {p.server?.user && <span className="text-faint">· {p.server.user}</span>}
      {p.environment && <Badge tone={p.protected ? "err" : "neutral"}>{p.environment + (p.protected ? " · protected" : "")}</Badge>}
      {p.via && <span className="text-faint">· via {p.via}</span>}
      {p.targets?.length ? <span className="text-faint">· targets {p.targets.join(", ")}</span> : null}
      {p.error && <span className="text-err">· not reachable</span>}
      <a href={p.url} target="_blank" rel="noreferrer" className="text-accent hover:underline inline-flex items-center gap-1">
        open <ExternalLink className="size-3" />
      </a>
    </span>
  );
}

// ---------------------------------------------------------------------------
// Targets of a platform

function Targets({ ws, res, info, type }: { ws: string; res: Resource; info: DeployPlatformInfo; type: DeployPlatformType }) {
  const [q, setQ] = useState("");
  const targets = useAsync(() => api.deployTargets(ws, res.name), [ws, res.name]);
  const [open, setOpen] = useState<string | null>(null);
  const [acting, setActing] = useState<{ target: DeployTarget | DeployDetail; input: DeployActionInput } | null>(null);
  const busy = (targets.data ?? []).some((t) => t.health === "busy");
  useInterval(() => void targets.reload(), busy ? 5000 : 30000);
  const list = useMemo(() => {
    const n = q.trim().toLowerCase();
    return (targets.data ?? []).filter((t) => !n || t.name.toLowerCase().includes(n) || (t.where ?? "").toLowerCase().includes(n) || (t.source ?? "").toLowerCase().includes(n));
  }, [targets.data, q]);
  const attention = (targets.data ?? []).filter((t) => t.health === "err" || t.health === "warn" || t.outOfSync).length;
  const deployLabel = type.actions.find((a) => a.id === "deploy")?.label ?? "Deploy";

  return (
    <>
      <div className="flex items-center gap-2 px-5 py-2.5">
        <SearchInput value={q} onChange={setQ} placeholder="Filter by name, where, source…" className="w-72" />
        <span className="text-xs text-faint">
          {targets.data ? `${list.length}${list.length !== targets.data.length ? ` of ${targets.data.length}` : ""} targets` : ""}
          {attention > 0 && <span className="text-warn"> · {attention} need attention</span>}
        </span>
        <div className="flex-1" />
        <IconButton title="Reload" onClick={() => void targets.reload()}>
          <RefreshCw className={cx("size-4", targets.loading && "animate-spin")} />
        </IconButton>
      </div>
      {targets.error && (
        <div className="px-5">
          <ErrorBox error={targets.error} onRetry={() => void targets.reload()} />
        </div>
      )}
      {targets.loading && !targets.data && <Loading />}
      {targets.data && (
        <Table className="flex-1 min-h-0">
          <thead>
            <tr>
              <Th>Name</Th>
              <Th className="w-28">Type</Th>
              <Th>Where</Th>
              <Th className="w-40">Status</Th>
              <Th className="w-28">Revision</Th>
              <Th className="w-28">Updated</Th>
              <Th className="w-28" />
            </tr>
          </thead>
          <tbody>
            {list.length === 0 && (
              <tr>
                <Td colSpan={7} className="text-center text-faint py-8">
                  {targets.data.length === 0 ? (info.targets?.length ? `Nothing matches the targets of this workspace (${info.targets.join(", ")}).` : "This platform deploys nothing yet.") : "No targets match."}
                </Td>
              </tr>
            )}
            {list.map((t) => (
              <Tr key={t.id} onClick={() => setOpen(t.id)}>
                <Td>
                  <div className="font-medium">{t.name}</div>
                  {t.source && (
                    <div className="text-[11px] text-faint truncate max-w-[360px]" title={t.source}>
                      {t.source}
                    </div>
                  )}
                </Td>
                <Td>
                  <Badge>{t.type}</Badge>
                </Td>
                <Td className="text-muted">{t.where}</Td>
                <Td>
                  <TargetStatus t={t} />
                </Td>
                <Td mono className="text-muted" title={t.revision}>
                  {shortRev(t.revision)}
                </Td>
                <Td className="text-muted tabular-nums">{t.updated ? timeAgo(t.updated) : ""}</Td>
                <Td>
                  <div className="flex items-center justify-end gap-0.5 opacity-0 group-hover:opacity-100">
                    {t.actions.includes("deploy") && (
                      <Button
                        size="sm"
                        variant="secondary"
                        icon={<Rocket className="size-3.5" />}
                        onClick={(e) => {
                          e.stopPropagation();
                          setActing({ target: t, input: { id: t.id, action: "deploy" } });
                        }}
                      >
                        {deployLabel}
                      </Button>
                    )}
                    {t.url && (
                      <a href={t.url} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()} title={`Open in ${type.label}`} className="p-1 text-muted hover:text-fg">
                        <ExternalLink className="size-3.5" />
                      </a>
                    )}
                  </div>
                </Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      )}
      {open && (
        <TargetDrawer
          ws={ws}
          res={res}
          info={info}
          type={type}
          id={open}
          onClose={() => setOpen(null)}
          onAction={(target, input) => setActing({ target, input })}
          reloadKey={acting ? 1 : 0}
        />
      )}
      {acting && (
        <ActionModal
          ws={ws}
          res={res}
          info={info}
          type={type}
          target={acting.target}
          input={acting.input}
          onClose={() => setActing(null)}
          onDone={() => {
            setActing(null);
            void targets.reload();
          }}
        />
      )}
    </>
  );
}

function TargetStatus({ t }: { t: DeployTarget }) {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <StatusDot tone={healthTone(t.health)} pulse={t.health === "busy"} />
      <span title={t.message}>{t.status}</span>
      {t.sync && <Badge tone={t.outOfSync ? "warn" : "ok"}>{t.sync}</Badge>}
      {!t.sync && t.outOfSync && <Badge tone="warn">out of sync</Badge>}
      {t.updateAvailable && <Badge tone="info">update</Badge>}
    </div>
  );
}

// ---------------------------------------------------------------------------
// One target

type TabId = "overview" | "logs" | "diff" | "file";

function TargetDrawer({
  ws,
  res,
  info,
  type,
  id,
  onClose,
  onAction,
  reloadKey,
}: {
  ws: string;
  res: Resource;
  info: DeployPlatformInfo;
  type: DeployPlatformType;
  id: string;
  onClose: () => void;
  onAction: (t: DeployDetail, input: DeployActionInput) => void;
  reloadKey: number;
}) {
  const d = useAsync(() => api.deployTarget(ws, res.name, id), [ws, res.name, id, reloadKey]);
  const [tab, setTab] = useState<TabId>("overview");
  const t = d.data;
  useInterval(() => void d.reload(), t && (t.health === "busy" || t.operation?.phase === "Running") ? 4000 : null);
  // update and rollback have their own places (the file, the history);
  // terminate only while a sync runs
  const running = t?.operation?.phase === "Running" || t?.operation?.phase === "Terminating";
  const up = t && /^(active|running|deploying|restarting|unhealthy)$/i.test(t.status);
  const down = t && /^(inactive|stopped|exited|down|not_deployed|created|dead)$/i.test(t.status);
  const offered = (id: string) => id !== "update" && id !== "rollback" && (id !== "terminate" || running) && !(id === "start" && up) && !(id === "stop" && down);
  const actions = t ? type.actions.filter((a) => t.actions.includes(a.id) && offered(a.id)) : [];
  const icon: Record<string, JSX.Element> = {
    deploy: <Rocket className="size-3.5" />,
    restart: <RotateCcw className="size-3.5" />,
    start: <Play className="size-3.5" />,
    stop: <Square className="size-3.5" />,
    refresh: <RefreshCw className="size-3.5" />,
  };

  return (
    <Drawer
      open
      onClose={onClose}
      width={880}
      title={t ? t.name : "Loading…"}
      subtitle={t ? `${t.type}${t.where ? ` on ${t.where}` : ""} · ${type.label} ${res.name}` : undefined}
      footer={
        t && (
          <>
            {t.url && (
              <a href={t.url} target="_blank" rel="noreferrer" className="text-xs text-accent hover:underline inline-flex items-center gap-1 me-auto">
                Open in {type.label} <ExternalLink className="size-3" />
              </a>
            )}
            {actions.map((a) => (
              <Button key={a.id} size="sm" variant={a.id === "deploy" ? "primary" : "secondary"} icon={icon[a.id]} title={a.help} onClick={() => onAction(t, { id: t.id, action: a.id })}>
                {a.label}
              </Button>
            ))}
          </>
        )
      }
    >
      {d.error && <ErrorBox error={d.error} onRetry={() => void d.reload()} />}
      {!t && !d.error && <Loading />}
      {t && (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
            <TargetStatus t={t} />
            {t.revision && (
              <span className="text-muted">
                rev <span className="font-mono">{shortRev(t.revision)}</span>
              </span>
            )}
            {t.git && (
              <Badge tone="accent">
                <GitBranch className="size-3" /> Git{t.autoSync ? " · auto-sync" : ""}
              </Badge>
            )}
            {!t.git && t.autoSync && <Badge tone="accent">auto-update</Badge>}
            {t.updated && <span className="text-faint">updated {timeAgo(t.updated)}</span>}
          </div>
          {t.source && <div className="text-xs text-muted font-mono break-all">{t.source}</div>}
          {t.message && <div className={cx("text-xs whitespace-pre-wrap", t.health === "ok" ? "text-muted" : "text-warn")}>{t.message}</div>}
          {info.protected && (
            <div className="flex items-center gap-2 rounded-md border border-warn/40 bg-warn/8 px-3 py-2 text-xs text-warn">
              <AlertTriangle className="size-3.5 shrink-0" /> This platform belongs to the protected environment {info.environment}.
            </div>
          )}
          <Tabs<TabId>
            tabs={[
              { id: "overview", label: "Overview" },
              { id: "logs", label: "Logs" },
              { id: "diff", label: "Diff" },
              ...(t.file || t.actions.includes("update") ? [{ id: "file" as TabId, label: "Stack file" }] : []),
            ]}
            value={tab}
            onChange={setTab}
          />
          {tab === "overview" && <Overview t={t} type={type} onRollback={(h) => onAction(t, { id: t.id, action: "rollback", history: h })} />}
          {tab === "logs" && <Logs ws={ws} res={res} t={t} />}
          {tab === "diff" && <Diff ws={ws} res={res} t={t} />}
          {tab === "file" && <StackFile t={t} onUpdate={(file) => onAction(t, { id: t.id, action: "update", file })} />}
        </div>
      )}
    </Drawer>
  );
}

function Overview({ t, type, onRollback }: { t: DeployDetail; type: DeployPlatformType; onRollback: (history: string) => void }) {
  const canRollback = t.actions.includes("rollback") && !t.autoSync;
  return (
    <div className="space-y-4">
      {t.operation && (
        <div className="rounded-md border border-line bg-panel-2 px-3 py-2 text-xs space-y-1">
          <div className="flex items-center gap-2">
            {t.operation.phase === "Running" ? <Spinner className="size-3.5" /> : <StatusDot tone={/fail|error/i.test(t.operation.phase) ? "err" : "ok"} />}
            <span className="font-medium">{t.operation.name || "Operation"}</span>
            <span className="text-muted">{t.operation.phase}</span>
            {t.operation.started && <span className="text-faint">started {timeAgo(t.operation.started)}</span>}
          </div>
          {t.operation.message && <div className="text-muted whitespace-pre-wrap break-words">{t.operation.message}</div>}
        </div>
      )}
      <section>
        <h3 className="text-xs font-semibold text-muted mb-1.5">{t.type === "application" ? "Resources" : "Services"}</h3>
        {t.services.length === 0 ? (
          <div className="text-xs text-faint">None reported.</div>
        ) : (
          <Table>
            <thead>
              <tr>
                <Th>Name</Th>
                <Th>Status</Th>
                <Th>{t.type === "application" ? "Namespace" : "Image"}</Th>
              </tr>
            </thead>
            <tbody>
              {t.services.map((s) => (
                <Tr key={`${s.kind}/${s.namespace}/${s.name}/${s.container}`}>
                  <Td>
                    {s.kind && <span className="text-faint">{s.kind} </span>}
                    <span className="font-medium">{s.name}</span>
                  </Td>
                  <Td>
                    <div className="flex items-center gap-1.5" title={s.message}>
                      <StatusDot tone={healthTone(s.health)} />
                      {s.status}
                      {s.outOfSync && <Badge tone="warn">out of sync</Badge>}
                      {s.updateAvailable && <Badge tone="info">update</Badge>}
                    </div>
                    {s.message && <div className="text-[11px] text-faint truncate max-w-[340px]">{s.message}</div>}
                  </Td>
                  <Td mono className="text-muted text-[11.5px] break-all">
                    {t.type === "application" ? s.namespace : s.image}
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </section>
      <section>
        <h3 className="text-xs font-semibold text-muted mb-1.5 flex items-center gap-1.5">
          <History className="size-3.5" /> History
        </h3>
        {t.history.length === 0 ? (
          <div className="text-xs text-faint">{type.id === "portainer" ? "Portainer keeps no deployment history; the activity journal records the deploys made from opsdeck." : "No deployments recorded."}</div>
        ) : (
          <Table>
            <tbody>
              {t.history.map((h, i) => (
                <Tr key={h.id}>
                  <Td mono className="text-faint w-16">
                    #{h.id.length > 10 ? h.id.slice(-6) : h.id}
                  </Td>
                  <Td>{h.operation}</Td>
                  <Td mono className="text-muted" title={h.revision}>
                    {shortRev(h.revision)}
                  </Td>
                  <Td className="text-muted">{h.time ? timeAgo(h.time) : ""}</Td>
                  <Td className="text-muted">{h.by}</Td>
                  <Td>{h.result && <Badge tone={/fail|error/i.test(h.result) ? "err" : /run|progress|pending/i.test(h.result) ? "info" : "ok"}>{h.result}</Badge>}</Td>
                  <Td className="w-24">
                    {canRollback && i > 0 && (
                      <Button size="sm" variant="ghost" icon={<Undo2 className="size-3.5" />} onClick={() => onRollback(h.id)}>
                        Roll back
                      </Button>
                    )}
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
        {t.actions.includes("rollback") && t.autoSync && <div className="mt-1 text-[11px] text-faint">Auto-sync is on: revert in Git (or turn auto-sync off) to go back.</div>}
      </section>
    </div>
  );
}

function Logs({ ws, res, t }: { ws: string; res: Resource; t: DeployDetail }) {
  const [service, setService] = useState("");
  const [tail, setTail] = useState(300);
  const logs = useAsync(() => api.deployLogs(ws, res.name, t.id, service, tail), [ws, res.name, t.id, service, tail]);
  const names = Array.from(new Set(t.services.filter((s) => t.type !== "application" || /^(Deployment|StatefulSet|DaemonSet|Pod|Job|CronJob|Rollout)$/.test(s.kind ?? "")).map((s) => s.name)));
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <Select value={service} onChange={(e) => setService(e.target.value)} className="w-56">
          <option value="">All {t.type === "application" ? "workloads" : "services"}</option>
          {names.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </Select>
        <Select value={String(tail)} onChange={(e) => setTail(Number(e.target.value))} className="w-32">
          {[100, 300, 1000, 3000].map((n) => (
            <option key={n} value={n}>
              last {n}
            </option>
          ))}
        </Select>
        <IconButton title="Reload" onClick={() => void logs.reload()}>
          <RefreshCw className={cx("size-4", logs.loading && "animate-spin")} />
        </IconButton>
      </div>
      {logs.error && <ErrorBox error={logs.error} onRetry={() => void logs.reload()} />}
      {logs.loading && !logs.data && <Loading />}
      {logs.data && <CodeBlock maxHeight={560}>{logs.data.text}</CodeBlock>}
    </div>
  );
}

function Diff({ ws, res, t }: { ws: string; res: Resource; t: DeployDetail }) {
  const diff = useAsync(() => api.deployDiff(ws, res.name, t.id), [ws, res.name, t.id]);
  if (diff.error) return <ErrorBox error={diff.error} onRetry={() => void diff.reload()} />;
  if (!diff.data) return <Loading />;
  if (!diff.data.supported) return <div className="text-xs text-muted">This platform does not tell what a deploy would change{t.git ? "; compare the commits in the repository." : "."}</div>;
  return (
    <div className="rounded-md border border-line overflow-auto max-h-[600px]">
      {diff.data.diff ? <DiffView text={diff.data.diff} /> : <div className="p-4 text-xs text-muted">Nothing to deploy: what runs matches what is configured.</div>}
    </div>
  );
}

function StackFile({ t, onUpdate }: { t: DeployDetail; onUpdate: (file: string) => void }) {
  const editable = t.actions.includes("update") && !t.git;
  const [text, setText] = useState(t.file ?? "");
  useEffect(() => setText(t.file ?? ""), [t.file]);
  if (!editable) return <CodeBlock maxHeight={600}>{t.file || (t.git ? "This stack deploys from Git: its file is in the repository." : "")}</CodeBlock>;
  return (
    <div className="space-y-2">
      <Textarea mono rows={22} value={text} onChange={(e) => setText(e.target.value)} spellCheck={false} />
      <div className="flex items-center justify-end gap-2">
        <Button variant="ghost" size="sm" disabled={text === (t.file ?? "")} onClick={() => setText(t.file ?? "")}>
          Discard
        </Button>
        <Button variant="primary" size="sm" disabled={!text.trim() || text === (t.file ?? "")} onClick={() => onUpdate(text)}>
          Save and redeploy…
        </Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Running an action

function ActionModal({
  ws,
  res,
  info,
  type,
  target,
  input,
  onClose,
  onDone,
}: {
  ws: string;
  res: Resource;
  info: DeployPlatformInfo;
  type: DeployPlatformType;
  target: DeployTarget | DeployDetail;
  input: DeployActionInput;
  onClose: () => void;
  onDone: () => void;
}) {
  const toast = useToast();
  const a = type.actions.find((x) => x.id === input.action);
  const [revision, setRevision] = useState("");
  const [prune, setPrune] = useState(false);
  const [pull, setPull] = useState(false);
  const [hard, setHard] = useState(false);
  const [service, setService] = useState("");
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; message: string; output?: string } | null>(null);
  const isArgo = type.id === "argocd";
  const services = "services" in target ? target.services : [];
  const diff = useAsync(
    () => (input.action === "deploy" ? api.deployDiff(ws, res.name, target.id) : Promise.resolve(undefined)),
    [ws, res.name, target.id, input.action],
  );
  const current = "file" in target ? (target.file ?? "") : "";
  const fileDiff = input.action === "update" && input.file !== undefined ? unified(current, input.file) : "";
  const destructive = prune && (input.action === "deploy" || input.action === "rollback" || input.action === "update");
  const needType = (info.protected && a?.risk !== "read") || destructive;

  async function run() {
    setBusy(true);
    try {
      const body: DeployActionInput = { ...input, revision: revision.trim() || undefined, prune: prune || undefined, pull: pull || undefined, hard: hard || undefined, service: service || undefined };
      const r = await api.deployAction(ws, res.name, body);
      setResult(r);
      if (r.ok) toast.success(`${a?.label ?? input.action}: ${target.name}`, r.message);
      else toast.error(`${a?.label ?? input.action} failed`, r.message);
    } catch (e) {
      setResult({ ok: false, message: errMsg(e) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open
      onClose={result ? onDone : onClose}
      title={`${a?.label ?? input.action} ${target.name}`}
      icon={<Rocket />}
      width={720}
      footer={
        result ? (
          <Button variant="primary" onClick={onDone}>
            Close
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant={destructive ? "danger" : "primary"} loading={busy} disabled={needType && typed !== target.name} onClick={() => void run()}>
              {a?.label ?? input.action}
            </Button>
          </>
        )
      }
    >
      {result ? (
        <div className="space-y-2">
          <div className={cx("text-[13px]", result.ok ? "text-ok" : "text-err")}>{result.message}</div>
          {result.output && <CodeBlock maxHeight={400}>{result.output}</CodeBlock>}
          {result.ok && <div className="text-xs text-muted">The platform carries on by itself; the list refreshes while the target is busy.</div>}
        </div>
      ) : (
        <div className="space-y-3">
          <div className="text-[13px] text-muted">{a?.help}</div>
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <TargetStatus t={target} />
            {target.where && <span className="text-faint">on {target.where}</span>}
            {target.revision && <span className="text-faint font-mono">rev {shortRev(target.revision)}</span>}
          </div>
          {input.action === "rollback" && input.history && (
            <div className="text-xs">
              Back to history entry <span className="font-mono">#{input.history}</span>
              {"history" in target && target.history.find((h) => h.id === input.history)?.revision && (
                <>
                  {" "}
                  (rev <span className="font-mono">{shortRev(target.history.find((h) => h.id === input.history)?.revision)}</span>)
                </>
              )}
            </div>
          )}
          {input.action === "deploy" && isArgo && (
            <FormField label="Revision" help="Empty: the application's target revision. A branch, tag or commit syncs that instead, for this sync only">
              <Input mono value={revision} onChange={(e) => setRevision(e.target.value)} placeholder="target revision" />
            </FormField>
          )}
          {(input.action === "deploy" || input.action === "rollback") && isArgo && (
            <Toggle checked={prune} onChange={setPrune} label="Prune: delete resources no longer in Git" />
          )}
          {input.action === "update" && <Toggle checked={prune} onChange={setPrune} label="Prune: remove services gone from the file" />}
          {input.action === "deploy" && type.id === "portainer" && <Toggle checked={pull} onChange={setPull} label="Pull the images first" />}
          {input.action === "refresh" && <Toggle checked={hard} onChange={setHard} label="Hard refresh (clear the manifest cache)" />}
          {(input.action === "restart" || (input.action === "deploy" && type.id === "komodo" && target.type === "stack")) && services.length > 1 && (
            <FormField label="Only" help="Empty: all of them">
              <Select value={service} onChange={(e) => setService(e.target.value)}>
                <option value="">— all —</option>
                {Array.from(new Set(services.map((s) => s.name))).map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </Select>
            </FormField>
          )}
          {input.action === "deploy" && !revision && (
            <div>
              <div className="text-xs font-semibold text-muted mb-1">What changes</div>
              {diff.loading && !diff.data && (
                <div className="flex items-center gap-2 text-xs text-muted">
                  <Spinner className="size-3.5" /> Comparing…
                </div>
              )}
              {diff.error && <div className="text-xs text-err">{diff.error}</div>}
              {diff.data && !diff.data.supported && <div className="text-xs text-faint">{type.label} does not tell what a deploy changes{target.source ? ` (source: ${target.source})` : ""}.</div>}
              {diff.data?.supported && (
                <div className="rounded-md border border-line overflow-auto max-h-72">
                  {diff.data.diff ? <DiffView text={diff.data.diff} /> : <div className="p-3 text-xs text-muted">Nothing differs: the deploy reapplies the same state.</div>}
                </div>
              )}
            </div>
          )}
          {fileDiff && (
            <div className="rounded-md border border-line overflow-auto max-h-72">
              <DiffView text={fileDiff} />
            </div>
          )}
          {destructive && (
            <div className="flex items-center gap-2 rounded-md border border-err/40 bg-err/8 px-3 py-2 text-xs text-err">
              <AlertTriangle className="size-3.5 shrink-0" /> Pruning deletes what is no longer in the source.
            </div>
          )}
          {needType && (
            <FormField label={`Type ${target.name} to confirm`} help={info.protected ? `${info.environment} is a protected environment` : undefined}>
              <Input mono value={typed} onChange={(e) => setTyped(e.target.value)} onKeyDown={(e) => e.key === "Enter" && typed === target.name && void run()} />
            </FormField>
          )}
        </div>
      )}
    </Modal>
  );
}

// unified is a small line diff of the stack file for the confirmation (the
// server shows the agent the same change).
function unified(a: string, b: string): string {
  const x = a.split("\n");
  const y = b.split("\n");
  const n = x.length;
  const m = y.length;
  if (n * m > 4_000_000) return `--- current\n+++ new\n@@ file too large to compare here @@`;
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) lcs[i][j] = x[i] === y[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
  const out: string[] = ["--- current", "+++ new"];
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && x[i] === y[j]) {
      out.push(" " + x[i]);
      i++;
      j++;
    } else if (j < m && (i === n || lcs[i][j + 1] >= lcs[i + 1][j])) out.push("+" + y[j++]);
    else out.push("-" + x[i++]);
  }
  return out.join("\n");
}
