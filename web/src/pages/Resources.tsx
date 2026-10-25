import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  Activity,
  BarChart3,
  ChevronDown,
  ChevronRight,
  ExternalLink,
  FolderOpen,
  MoreHorizontal,
  Pencil,
  Plus,
  Server,
  SquareTerminal,
  TerminalSquare,
  Trash2,
} from "lucide-react";
import { api, errMsg } from "../api/client";
import type { KindField, KindSchema, PathHop, Resource } from "../api/types";
import { NETWORK_KINDS } from "../api/types";
import { hostAddress, useApp, useWs } from "../context/app";
import { useTerminals } from "../context/terminals";
import { useAsync } from "../hooks";
import { KindIcon } from "../components/icons";
import {
  Badge,
  Button,
  ChipInput,
  EmptyState,
  FormField,
  IconButton,
  Input,
  PageHeader,
  SearchInput,
  Select,
  Table,
  Tabs,
  Td,
  Textarea,
  Th,
  Toggle,
  Tr,
} from "../components/ui";
import { Drawer, MenuItem, Popover, useConfirm, useToast } from "../components/overlay";
import { SecretRefPicker } from "../components/VaultItemForm";
import { ExecModal, FilesModal, PathView, StatsDrawer, useHostCheck } from "./HostTools";
import { str } from "../lib/format";

export function resourceSummary(r: Resource): string {
  const s = r.spec || {};
  switch (r.kind) {
    case "Host":
      return hostAddress(r);
    case "HostGroup":
      return Array.isArray(s.hosts) ? (s.hosts as unknown[]).map(String).join(", ") : str(s.selector);
    case "WireGuard":
      return str(s.config);
    case "SocksProxy":
      return str(s.address);
    case "Panel":
    case "Prometheus":
    case "Loki":
    case "Elasticsearch":
    case "GitRemote":
      return str(s.url);
    case "KubernetesCluster":
      return [s.context, s.namespace].filter(Boolean).map(String).join(" · ") || str(s.kubeconfig);
    default: {
      const first = Object.values(s).find((v) => typeof v === "string" && v);
      return str(first);
    }
  }
}

export function envTone(ws: { environments: { name: string; protected: boolean }[] } | null, env?: string) {
  if (!env) return "neutral" as const;
  const e = ws?.environments.find((x) => x.name === env);
  if (e?.protected) return "err" as const;
  if (/prod/i.test(env)) return "err" as const;
  if (/stag/i.test(env)) return "warn" as const;
  return "info" as const;
}

export function ResourcesPage() {
  const { kinds, kindMap } = useApp();
  const { name: ws, workspace, resources, reloadResources } = useWs();
  const terms = useTerminals();
  const navigate = useNavigate();
  const toast = useToast();
  const confirm = useConfirm();
  const [params, setParams] = useSearchParams();
  const [kind, setKind] = useState<string>("all");
  const [env, setEnv] = useState("");
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<{ res: Resource | null; kind: string } | null>(null);
  const [addOpen, setAddOpen] = useState(false);
  const [statsHost, setStatsHost] = useState<string | null>(null);
  const [filesHost, setFilesHost] = useState<string | null>(null);
  const [execHost, setExecHost] = useState<string | null>(null);
  const [menu, setMenu] = useState<string | null>(null);
  const check = useHostCheck(ws);

  // deep link: ?edit=Kind/name or ?new=Kind
  useEffect(() => {
    const ed = params.get("edit");
    const nw = params.get("new");
    if (ed) {
      const [k, ...rest] = ed.split("/");
      const r = resources.find((x) => x.kind === k && x.name === rest.join("/"));
      if (r) {
        setEditing({ res: r, kind: r.kind });
        params.delete("edit");
        setParams(params, { replace: true });
      }
    } else if (nw) {
      setEditing({ res: null, kind: nw });
      params.delete("new");
      setParams(params, { replace: true });
    }
  }, [params, resources, setParams]);

  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    resources.forEach((r) => (c[r.kind] = (c[r.kind] ?? 0) + 1));
    return c;
  }, [resources]);

  const filtered = useMemo(() => {
    const t = q.trim().toLowerCase();
    return resources
      .filter((r) => kind === "all" || r.kind === kind)
      .filter((r) => !env || r.environment === env)
      .filter(
        (r) =>
          !t ||
          r.name.toLowerCase().includes(t) ||
          resourceSummary(r).toLowerCase().includes(t) ||
          (r.tags ?? []).some((x) => x.toLowerCase().includes(t)) ||
          (r.description ?? "").toLowerCase().includes(t),
      )
      .sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name));
  }, [resources, kind, env, q]);

  const kindTabs = [
    { id: "all", label: "All", count: resources.length },
    ...kinds.map((k) => ({ id: k.kind, label: k.label, count: counts[k.kind] ?? 0 })),
  ];

  async function remove(r: Resource) {
    const ok = await confirm({
      title: `Delete ${r.kind} "${r.name}"?`,
      message: "This removes it from the workspace files. Vault items it references are not deleted.",
      danger: true,
      confirmLabel: "Delete",
    });
    if (!ok) return;
    try {
      await api.deleteResource(ws, r.kind, r.name);
      toast.success("Resource deleted", r.name);
      await reloadResources();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  async function openPanel(r: Resource) {
    try {
      const { url } = await api.openPanel(ws, r.name);
      window.open(url, "_blank", "noopener");
    } catch (e) {
      toast.error("Cannot open panel", errMsg(e));
    }
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<Server />}
        title="Resources"
        subtitle="Servers, networks, panels and clusters of this workspace. Stored as YAML — no secrets inside."
        actions={
          <div className="relative">
            <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAddOpen((o) => !o)}>
              Add resource
            </Button>
            <Popover open={addOpen} onClose={() => setAddOpen(false)} className="right-0 mt-1 w-56">
              {kinds.map((k) => (
                <MenuItem
                  key={k.kind}
                  icon={<KindIcon kind={k.kind} icon={k.icon} />}
                  onClick={() => {
                    setAddOpen(false);
                    setEditing({ res: null, kind: k.kind });
                  }}
                  hint={k.category}
                >
                  {k.label}
                </MenuItem>
              ))}
            </Popover>
          </div>
        }
      />
      <Tabs className="px-3" tabs={kindTabs} value={kind} onChange={setKind} />
      <div className="flex items-center gap-2 px-5 py-2.5">
        <SearchInput value={q} onChange={setQ} placeholder="Filter by name, address, tag…" className="w-72" />
        <Select value={env} onChange={(e) => setEnv(e.target.value)} className="w-40">
          <option value="">All environments</option>
          {workspace?.environments.map((e) => (
            <option key={e.name} value={e.name}>
              {e.name}
            </option>
          ))}
        </Select>
        <div className="flex-1" />
        <span className="text-xs text-faint">{filtered.length} shown</span>
      </div>
      <div className="flex-1 min-h-0 px-5 pb-5">
        <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
          {filtered.length === 0 ? (
            <EmptyState
              icon={<Server />}
              title={resources.length ? "No matching resources" : "No resources yet"}
              action={
                !resources.length && (
                  <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setEditing({ res: null, kind: "Host" })}>
                    Add your first host
                  </Button>
                )
              }
            >
              {!resources.length && "Start with a network (e.g. WireGuard) and the hosts behind it, then add panels and clusters."}
            </EmptyState>
          ) : (
            <Table className="flex-1">
              <thead>
                <tr>
                  <Th>Name</Th>
                  {kind === "all" && <Th>Kind</Th>}
                  <Th>Environment</Th>
                  <Th>Endpoint</Th>
                  <Th>Via</Th>
                  <Th>Tags</Th>
                  <Th className="w-[1%]" />
                </tr>
              </thead>
              <tbody>
                {filtered.map((r) => {
                  const key = `${r.kind}/${r.name}`;
                  return (
                    <Tr key={key} onClick={() => setEditing({ res: r, kind: r.kind })}>
                      <Td>
                        <div className="flex items-center gap-2">
                          <span className="text-muted">
                            <KindIcon kind={r.kind} icon={kindMap[r.kind]?.icon} />
                          </span>
                          <span className="font-medium">{r.name}</span>
                        </div>
                        {r.description && <div className="text-[11px] text-faint truncate max-w-72">{r.description}</div>}
                      </Td>
                      {kind === "all" && <Td className="text-muted">{kindMap[r.kind]?.label ?? r.kind}</Td>}
                      <Td>{r.environment ? <Badge tone={envTone(workspace, r.environment)}>{r.environment}</Badge> : <span className="text-faint">—</span>}</Td>
                      <Td mono className="max-w-80 truncate" title={resourceSummary(r)}>
                        {resourceSummary(r) || <span className="text-faint">—</span>}
                      </Td>
                      <Td mono className="text-muted">
                        {r.via || <span className="text-faint">direct</span>}
                      </Td>
                      <Td>
                        <div className="flex flex-wrap gap-1">
                          {[...((r.spec?.roles as string[] | undefined) ?? []), ...(r.tags ?? [])].slice(0, 5).map((t) => (
                            <Badge key={t}>{t}</Badge>
                          ))}
                        </div>
                      </Td>
                      <Td>
                        <div className="flex items-center gap-0.5 justify-end" onClick={(e) => e.stopPropagation()}>
                          {r.kind === "Host" && (
                            <>
                              <IconButton
                                title="Open terminal"
                                onClick={() => {
                                  terms.open(ws, r.name);
                                  navigate(`/w/${encodeURIComponent(ws)}/terminal`);
                                }}
                              >
                                <SquareTerminal className="size-4" />
                              </IconButton>
                              <IconButton title="Check reachability" onClick={() => void check(r.name)}>
                                <Activity className="size-4" />
                              </IconButton>
                            </>
                          )}
                          {(r.kind === "Panel" || r.kind === "Prometheus" || r.kind === "Loki" || r.kind === "Elasticsearch") && (
                            <IconButton title="Open in the browser" onClick={() => void openPanel(r)}>
                              <ExternalLink className="size-4" />
                            </IconButton>
                          )}
                          <div className="relative">
                            <IconButton title="More" onClick={() => setMenu(menu === key ? null : key)}>
                              <MoreHorizontal className="size-4" />
                            </IconButton>
                            <Popover open={menu === key} onClose={() => setMenu(null)} className="right-0 mt-1 w-48">
                              {r.kind === "Host" && (
                                <>
                                  <MenuItem icon={<BarChart3 />} onClick={() => (setMenu(null), setStatsHost(r.name))}>
                                    Stats
                                  </MenuItem>
                                  <MenuItem icon={<FolderOpen />} onClick={() => (setMenu(null), setFilesHost(r.name))}>
                                    Files
                                  </MenuItem>
                                  <MenuItem icon={<TerminalSquare />} onClick={() => (setMenu(null), setExecHost(r.name))}>
                                    Run command
                                  </MenuItem>
                                </>
                              )}
                              <MenuItem icon={<Pencil />} onClick={() => (setMenu(null), setEditing({ res: r, kind: r.kind }))}>
                                Edit
                              </MenuItem>
                              <MenuItem icon={<Trash2 />} danger onClick={() => (setMenu(null), void remove(r))}>
                                Delete
                              </MenuItem>
                            </Popover>
                          </div>
                        </div>
                      </Td>
                    </Tr>
                  );
                })}
              </tbody>
            </Table>
          )}
        </div>
      </div>
      {editing && kindMap[editing.kind] && (
        <ResourceDrawer
          schema={kindMap[editing.kind]}
          resource={editing.res}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await reloadResources();
          }}
          onDelete={editing.res ? () => void remove(editing.res!).then(() => setEditing(null)) : undefined}
        />
      )}
      <StatsDrawer ws={ws} host={statsHost} onClose={() => setStatsHost(null)} />
      <FilesModal ws={ws} host={filesHost} onClose={() => setFilesHost(null)} />
      <ExecModal ws={ws} host={execHost} onClose={() => setExecHost(null)} />
    </div>
  );
}

function emptyResource(kind: string): Resource {
  return { kind, name: "", environment: "", tags: [], via: "", description: "", docs: "", spec: {} };
}

export function ResourceDrawer({
  schema,
  resource,
  initial,
  title,
  onClose,
  onSaved,
  onDelete,
}: {
  schema: KindSchema;
  resource: Resource | null;
  initial?: Partial<Resource>; // preset for a new resource
  title?: string;
  onClose: () => void;
  onSaved: () => void | Promise<void>;
  onDelete?: () => void;
}) {
  const { name: ws, workspace, resources } = useWs();
  const toast = useToast();
  const [r, setR] = useState<Resource>(() =>
    resource ? structuredClone(resource) : { ...emptyResource(schema.kind), ...initial, spec: { ...(initial?.spec ?? {}) } },
  );
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [path, setPath] = useState<PathHop[] | null>(null);
  const [docs, setDocs] = useState<string[]>([]);
  const shown = schema.fields.filter((f) => !f.showIf || r.spec[f.showIf] === true || r.spec[f.showIf] === "true");
  const advancedSet = shown.filter((f) => f.advanced && r.spec[f.key] !== undefined && r.spec[f.key] !== "" && r.spec[f.key] !== f.default).length;
  const [advanced, setAdvanced] = useState<boolean | null>(null);
  const advancedOpen = advanced ?? advancedSet > 0;

  useEffect(() => {
    api
      .docs(ws)
      .then((d) => setDocs(d.map((x) => x.path)))
      .catch(() => {});
    if (resource) {
      api
        .resolve(ws, resource.kind, resource.name)
        .then((p) => setPath(p.path ?? []))
        .catch(() => setPath(null));
    }
  }, [ws, resource]);

  const setSpec = (k: string, v: unknown) => setR((x) => ({ ...x, spec: { ...x.spec, [k]: v } }));
  const viaOptions = resources.filter(
    (x) => (NETWORK_KINDS.includes(x.kind) || x.kind === "Host") && !(x.kind === r.kind && x.name === resource?.name),
  );
  const missing = schema.fields.filter((f) => f.required && (r.spec[f.key] === undefined || r.spec[f.key] === "" || (Array.isArray(r.spec[f.key]) && !(r.spec[f.key] as unknown[]).length)));
  const nameOk = /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(r.name);

  async function save() {
    setBusy(true);
    setErr("");
    try {
      const body: Resource = {
        ...r,
        name: r.name.trim(),
        environment: r.environment || undefined,
        via: r.via || undefined,
        docs: r.docs || undefined,
        description: r.description || undefined,
        tags: r.tags?.length ? r.tags : undefined,
        spec: Object.fromEntries(Object.entries(r.spec).filter(([, v]) => v !== "" && v !== undefined && v !== null && !(Array.isArray(v) && v.length === 0))),
      };
      if (resource) await api.updateResource(ws, resource.kind, resource.name, body);
      else await api.createResource(ws, body);
      toast.success(resource ? "Resource saved" : "Resource created", body.name);
      await onSaved();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Drawer
      open
      onClose={onClose}
      width={560}
      title={resource ? `${schema.label}: ${resource.name}` : title ?? `New ${schema.label}`}
      subtitle={resource ? "Saved to the workspace YAML files" : undefined}
      footer={
        <>
          {onDelete && (
            <Button variant="danger" icon={<Trash2 className="size-3.5" />} onClick={onDelete} className="mr-auto">
              Delete
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={save} loading={busy} disabled={!nameOk || missing.length > 0}>
            {resource ? "Save" : "Create"}
          </Button>
        </>
      }
    >
      <div className="space-y-3.5">
        {resource && (
          <div className="rounded-md border border-line bg-panel-2 p-2.5">
            <div className="text-[11px] uppercase tracking-wide text-faint mb-1.5">Access path</div>
            {path ? <PathView path={path} /> : <span className="text-xs text-faint">—</span>}
          </div>
        )}
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Name" required help={!nameOk && r.name ? "Letters, digits, . _ -" : undefined}>
            <Input mono autoFocus={!resource} value={r.name} onChange={(e) => setR({ ...r, name: e.target.value })} placeholder={schema.kind === "Host" ? "app-1" : "name"} />
          </FormField>
          <FormField label="Environment">
            <Select value={r.environment ?? ""} onChange={(e) => setR({ ...r, environment: e.target.value })}>
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
        {schema.canVia && (
          <FormField label="Reached via" help="Network (VPN/proxy) or jump host used to reach this resource. Chains are resolved automatically.">
            <Select value={r.via ?? ""} onChange={(e) => setR({ ...r, via: e.target.value })} className="font-mono text-xs">
              <option value="">direct (from this machine)</option>
              {[...NETWORK_KINDS, "Host"].map((k) => {
                const list = viaOptions.filter((x) => x.kind === k);
                if (!list.length) return null;
                const prefix = k === "Host" ? "host" : "network";
                return (
                  <optgroup key={k} label={k === "Host" ? "Jump hosts" : `VPN · ${k}`}>
                    {list.map((x) => (
                      <option key={x.name} value={`${prefix}/${x.name}`}>
                        {prefix}/{x.name}
                      </option>
                    ))}
                  </optgroup>
                );
              })}
            </Select>
          </FormField>
        )}
        <div className="border-t border-line pt-3.5 space-y-3.5">
          {shown
            .filter((f) => !f.advanced)
            .map((f) => (
              <SpecField key={f.key} f={f} value={r.spec[f.key]} onChange={(v) => setSpec(f.key, v)} ws={ws} resources={resources} />
            ))}
          {shown.some((f) => f.advanced) && (
            <div>
              <button
                type="button"
                onClick={() => setAdvanced((v) => !v)}
                className="flex items-center gap-1 text-xs text-muted hover:text-fg"
              >
                {advancedOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />} Advanced
                {!advancedOpen && advancedSet > 0 && <span className="text-faint">({advancedSet} set)</span>}
              </button>
              {advancedOpen && (
                <div className="mt-3 space-y-3.5">
                  {shown
                    .filter((f) => f.advanced)
                    .map((f) => (
                      <SpecField key={f.key} f={f} value={r.spec[f.key]} onChange={(v) => setSpec(f.key, v)} ws={ws} resources={resources} />
                    ))}
                </div>
              )}
            </div>
          )}
        </div>
        <div className="border-t border-line pt-3.5 space-y-3.5">
          <FormField label="Description">
            <Textarea value={r.description ?? ""} onChange={(e) => setR({ ...r, description: e.target.value })} className="min-h-14" />
          </FormField>
          <FormField label="Tags">
            <ChipInput value={r.tags ?? []} onChange={(t) => setR({ ...r, tags: t })} placeholder="add tag…" />
          </FormField>
          <FormField label="Linked documentation">
            <Input mono list="od-docs" value={r.docs ?? ""} onChange={(e) => setR({ ...r, docs: e.target.value })} placeholder="docs/runbooks/app.md" />
            <datalist id="od-docs">
              {docs.map((d) => (
                <option key={d} value={d} />
              ))}
            </datalist>
          </FormField>
        </div>
        {err && <div className="text-err text-xs whitespace-pre-wrap">{err}</div>}
      </div>
    </Drawer>
  );
}

function SpecField({
  f,
  value,
  onChange,
  ws,
  resources,
}: {
  f: KindField;
  value: unknown;
  onChange: (v: unknown) => void;
  ws: string;
  resources: Resource[];
}) {
  const label = f.label || f.key;
  switch (f.type) {
    case "bool": {
      const on = value === undefined || value === null || value === "" ? f.default === "true" : value === true || value === "true";
      return (
        <div>
          <Toggle checked={on} onChange={(v) => onChange(v)} label={label} />
          {f.help && <div className="mt-1 text-[11px] text-faint">{f.help}</div>}
        </div>
      );
    }
    case "number":
      return (
        <FormField label={label} required={f.required} help={f.help}>
          <Input
            type="number"
            mono
            value={value === undefined || value === null ? "" : String(value)}
            onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))}
            placeholder={f.placeholder}
          />
        </FormField>
      );
    case "text":
      return (
        <FormField label={label} required={f.required} help={f.help}>
          <Textarea mono value={str(value)} onChange={(e) => onChange(e.target.value)} placeholder={f.placeholder} />
        </FormField>
      );
    case "select":
      return (
        <FormField label={label} required={f.required} help={f.help}>
          <Select value={str(value) || f.default || ""} onChange={(e) => onChange(e.target.value)}>
            {!f.default && <option value="">—</option>}
            {(f.options ?? []).map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </Select>
        </FormField>
      );
    case "secretRef":
      return (
        <div>
          <FormField label={label} required={f.required} help={f.help ?? "Secrets stay in the vault — only the reference is stored here."}>
            <span />
          </FormField>
          <SecretRefPicker value={str(value)} onChange={onChange} ws={ws} itemTypes={f.itemTypes} />
        </div>
      );
    case "gitConnection":
      return <ConnectionField label={label} help={f.help} value={str(value)} onChange={onChange} />;
    case "dnsConnection":
      return <DNSConnectionField label={label} help={f.help} value={str(value)} onChange={onChange} />;
    case "resourceRef": {
      const list = resources.filter((r) => !f.refKinds?.length || f.refKinds.includes(r.kind));
      return (
        <FormField label={label} required={f.required} help={f.help}>
          <Select value={str(value)} onChange={(e) => onChange(e.target.value)} className="font-mono text-xs">
            <option value="">—</option>
            {list.map((r) => (
              <option key={`${r.kind}/${r.name}`} value={r.name}>
                {r.name} ({r.kind})
              </option>
            ))}
          </Select>
        </FormField>
      );
    }
    case "list": {
      const arr = Array.isArray(value) ? value.map(String) : value ? [String(value)] : [];
      if (f.refKinds?.length) {
        const list = resources.filter((r) => f.refKinds!.includes(r.kind));
        return (
          <FormField label={label} required={f.required} help={f.help}>
            <div className="rounded-md border border-line bg-panel-2 max-h-44 overflow-y-auto p-1">
              {list.length === 0 && <div className="text-xs text-faint p-2">No {f.refKinds.join("/")} resources yet</div>}
              {list.map((r) => (
                <label key={r.name} className="flex items-center gap-2 px-2 h-7 rounded hover:bg-panel-3 cursor-pointer text-xs">
                  <input
                    type="checkbox"
                    checked={arr.includes(r.name)}
                    onChange={(e) => onChange(e.target.checked ? [...arr, r.name] : arr.filter((x) => x !== r.name))}
                    className="accent-[var(--c-accent)]"
                  />
                  <span className="font-mono">{r.name}</span>
                  {r.environment && <span className="text-faint">{r.environment}</span>}
                </label>
              ))}
            </div>
          </FormField>
        );
      }
      return (
        <FormField label={label} required={f.required} help={f.help}>
          <ChipInput mono value={arr} onChange={onChange} placeholder={f.placeholder ?? "type and press Enter"} />
        </FormField>
      );
    }
    default:
      return (
        <FormField label={label} required={f.required} help={f.help}>
          <Input mono value={str(value)} onChange={(e) => onChange(e.target.value)} placeholder={f.placeholder} />
        </FormField>
      );
  }
}

function DNSConnectionField({ label, help, value, onChange }: { label: string; help?: string; value: string; onChange: (v: unknown) => void }) {
  const conns = useAsync(() => api.dnsConnections(), []);
  return (
    <FormField
      label={label}
      help={
        <>
          {help}{" "}
          <Link to="/connectors" className="text-accent hover:underline">
            Manage connectors
          </Link>
        </>
      }
    >
      <Select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Automatic (the account that has the zone)</option>
        {(conns.data ?? []).map((c) => (
          <option key={c.id} value={c.id}>
            {c.label} · {c.account || `${c.zones} zones`} · {c.id}
          </option>
        ))}
        {value && conns.data && !conns.data.some((c) => c.id === value) && <option value={value}>{value} (not set up here)</option>}
      </Select>
    </FormField>
  );
}

function ConnectionField({ label, help, value, onChange }: { label: string; help?: string; value: string; onChange: (v: unknown) => void }) {
  const conns = useAsync(() => api.gitConnections(), []);
  return (
    <FormField
      label={label}
      help={
        <>
          {help}{" "}
          <Link to="/connectors" className="text-accent hover:underline">
            Manage connectors
          </Link>
        </>
      }
    >
      <Select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Automatic (by server)</option>
        {(conns.data ?? []).map((c) => (
          <option key={c.id} value={c.id}>
            {c.provider === "github" ? "GitHub" : "GitLab"} · {c.host} · {c.login}
          </option>
        ))}
        {value && conns.data && !conns.data.some((c) => c.id === value) && <option value={value}>{value} (not set up here)</option>}
      </Select>
    </FormField>
  );
}
