import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  Activity,
  BarChart3,
  Bot,
  ChevronDown,
  ChevronLeft,
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
  X,
} from "lucide-react";
import { createPortal } from "react-dom";
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
  cx,
  ChipInput,
  EmptyState,
  FormField,
  IconButton,
  Input,
  PageHeader,
  SearchInput,
  Segmented,
  Select,
  Table,
  Td,
  Textarea,
  Th,
  Toggle,
  Tr,
} from "../components/ui";
import { MenuItem, Popover, useConfirm, useEscape, useToast } from "../components/overlay";
import { BrandLogo } from "../components/brand";
import { AddResourcePicker, catalogFor, shortLabel } from "../components/AddResource";
import { productFor } from "../lib/catalog";
import type { Product } from "../lib/catalog";
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
    case "Database":
      return str(s.address);
    case "OpenConnect":
    case "OpenFortiVPN":
      return str(s.server);
    case "Tailscale":
      return str(s.controlUrl) || "tailscale.com";
    case "NetBird":
      return str(s.managementUrl) || "netbird.io";
    case "Panel":
    case "Prometheus":
    case "Loki":
    case "Elasticsearch":
    case "GitRemote":
      return str(s.url);
    case "DeployPlatform":
      return [s.platform, s.url].filter(Boolean).map(String).join(" · ");
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
  const [editing, setEditing] = useState<{ res: Resource | null; kind: string; product?: Product } | null>(null);
  const [addOpen, setAddOpen] = useState(false);
  const [statsHost, setStatsHost] = useState<string | null>(null);
  const [filesHost, setFilesHost] = useState<string | null>(null);
  const [execHost, setExecHost] = useState<string | null>(null);
  const [menu, setMenu] = useState<string | null>(null);
  const check = useHostCheck(ws);

  // deep link: ?edit=Kind/name, ?new=Kind, ?add=1 (the picker) or ?kind=Kind (a filter)
  useEffect(() => {
    const ed = params.get("edit");
    const nw = params.get("new");
    if (params.get("add")) {
      setAddOpen(true);
      params.delete("add");
      setParams(params, { replace: true });
    } else if (params.get("kind")) {
      setKind(params.get("kind")!);
      params.delete("kind");
      setParams(params, { replace: true });
    } else if (ed) {
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

  const usedKinds = kinds.filter((k) => counts[k.kind] || k.kind === kind);

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
        title="Resources"
        subtitle="Servers, networks, panels and clusters of this workspace. Stored as YAML, no secrets inside."
        actions={
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAddOpen(true)}>
            Add resource
          </Button>
        }
      />
      <div role="group" aria-label="Filter by kind" className="flex flex-wrap items-center gap-1.5 px-4 sm:px-5 shrink-0">
        {[{ kind: "all", label: "All", icon: undefined as string | undefined }, ...usedKinds].map((k) => {
          const on = kind === k.kind;
          const n = k.kind === "all" ? resources.length : (counts[k.kind] ?? 0);
          return (
            <button
              key={k.kind}
              onClick={() => setKind(k.kind)}
              aria-pressed={on}
              className={cx(
                "inline-flex items-center gap-1.5 h-7 px-3 rounded-full border text-[12.5px] font-medium transition-colors [&_svg]:size-3.5",
                on ? "bg-fg text-bg border-fg" : "border-line text-muted hover:text-fg hover:border-line-strong",
              )}
            >
              {k.kind !== "all" && <KindIcon kind={k.kind} icon={k.icon} />}
              {shortLabel(k.label)}
              <span className="tabular-nums opacity-60">{n}</span>
            </button>
          );
        })}
        <button
          onClick={() => setAddOpen(true)}
          className="inline-flex items-center gap-1.5 h-7 px-3 rounded-full border border-dashed border-line-strong text-[12.5px] text-muted hover:text-fg"
        >
          <Plus className="size-3.5" /> Add another type
        </button>
      </div>
      <div className="flex flex-wrap items-center gap-2 px-4 sm:px-5 py-3 shrink-0">
        <SearchInput value={q} onChange={setQ} placeholder="Filter by name, address, tag…" className="w-full sm:w-72" />
        {(workspace?.environments.length ?? 0) > 0 && (workspace?.environments.length ?? 0) <= 5 ? (
          <Segmented value={env} onChange={setEnv} options={[{ id: "", label: "All envs" }, ...workspace!.environments.map((e) => ({ id: e.name, label: e.name }))]} />
        ) : (
          <Select value={env} onChange={(e) => setEnv(e.target.value)} className="w-40">
            <option value="">All environments</option>
            {workspace?.environments.map((e) => (
              <option key={e.name} value={e.name}>
                {e.name}
              </option>
            ))}
          </Select>
        )}
        <div className="flex-1" />
        <span className="text-xs text-faint">{filtered.length} shown</span>
      </div>
      <div className="flex-1 min-h-0 px-4 sm:px-5 pb-6">
        <div className="h-full rounded-xl border border-line bg-panel overflow-hidden flex flex-col">
          {filtered.length === 0 ? (
            <EmptyState
              icon={<Server />}
              title={resources.length ? "No matching resources" : "No resources yet"}
              action={
                !resources.length && (
                  <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAddOpen(true)}>
                    Add your first resource
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
                  const prod = productFor(r);
                  return (
                    <Tr key={key} onClick={() => setEditing({ res: r, kind: r.kind })}>
                      <Td>
                        <div className="flex items-center gap-2.5">
                          {prod ? (
                            <BrandLogo product={prod} size={24} />
                          ) : (
                            <span className="text-muted">
                              <KindIcon kind={r.kind} icon={kindMap[r.kind]?.icon} />
                            </span>
                          )}
                          <span className="min-w-0">
                            <span className="block font-medium whitespace-nowrap">{r.name}</span>
                            {r.description && <span className="block text-[11px] text-faint truncate max-w-72">{r.description}</span>}
                          </span>
                        </div>
                      </Td>
                      {kind === "all" && <Td className="text-muted whitespace-nowrap">{prod?.name ?? shortLabel(kindMap[r.kind]?.label ?? r.kind)}</Td>}
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
                        <div className="flex items-center gap-0.5 justify-end opacity-60 group-hover:opacity-100 focus-within:opacity-100 transition-opacity" onClick={(e) => e.stopPropagation()}>
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
      <AddResourcePicker
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onPick={(p) => {
          setAddOpen(false);
          setEditing({ res: null, kind: p.kind, product: p });
        }}
      />
      {editing && kindMap[editing.kind] && (
        <ResourceDrawer
          key={`${editing.kind}/${editing.res?.name ?? ""}/${editing.product?.id ?? ""}`}
          schema={kindMap[editing.kind]}
          resource={editing.res}
          product={editing.product}
          initial={editing.product?.spec ? { spec: editing.product.spec } : undefined}
          onBack={editing.product ? () => (setEditing(null), setAddOpen(true)) : undefined}
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
  product: chosen,
  onBack,
  onClose,
  onSaved,
  onDelete,
}: {
  schema: KindSchema;
  resource: Resource | null;
  initial?: Partial<Resource>; // preset for a new resource
  title?: string;
  product?: Product; // the catalog product picked in the add flow
  onBack?: () => void;
  onClose: () => void;
  onSaved: () => void | Promise<void>;
  onDelete?: () => void;
}) {
  const { name: ws, workspace, resources } = useWs();
  const navigate = useNavigate();
  const toast = useToast();
  const [r, setR] = useState<Resource>(() =>
    resource ? structuredClone(resource) : { ...emptyResource(schema.kind), ...initial, spec: { ...(initial?.spec ?? {}) } },
  );
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [path, setPath] = useState<PathHop[] | null>(null);
  const [docs, setDocs] = useState<string[]>([]);
  // a field the chosen product already sets (protocol, engine…) moves to Advanced
  const presetKeys = new Set(Object.keys(chosen?.spec ?? {}));
  const shown = schema.fields
    .filter((f) => !f.showIf || r.spec[f.showIf] === true || r.spec[f.showIf] === "true")
    .map((f) => (presetKeys.has(f.key) ? { ...f, advanced: true } : f));
  const advancedSet = shown.filter((f) => f.advanced && !presetKeys.has(f.key) && r.spec[f.key] !== undefined && r.spec[f.key] !== "" && r.spec[f.key] !== f.default).length;
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
  const product = chosen ?? (resource ? productFor(resource) : undefined) ?? catalogFor([schema])[0];
  const preset = Object.entries(chosen?.spec ?? {});
  const envs = workspace?.environments ?? [];
  useEscape(onClose);

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

  const heading = resource ? resource.name : title ?? (chosen ? chosen.name : `New ${shortLabel(schema.label)}`);
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-3 sm:p-6 backdrop-blur-[2px]" onMouseDown={onClose}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label={heading}
        className="w-full max-w-[1000px] h-[min(760px,calc(100vh-24px))] rounded-xl border border-line-strong bg-panel shadow-2xl shadow-black/40 flex flex-col overflow-hidden"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header className="flex items-center gap-3 px-4 sm:px-5 py-4 border-b border-line shrink-0">
          {onBack && (
            <IconButton title="Back to all types" onClick={onBack}>
              <ChevronLeft className="size-4" />
            </IconButton>
          )}
          {product && <BrandLogo product={product} size={44} />}
          <div className="min-w-0 flex-1">
            <h2 className="text-[17px] font-semibold tracking-tight truncate">{heading}</h2>
            <p className="text-[12px] text-muted truncate">
              {resource ? (
                <>
                  {product?.name ?? shortLabel(schema.label)} · saved in the workspace YAML files
                </>
              ) : (
                <>
                  saved as <span className="font-mono">{schema.kind}</span>
                  {preset.length > 0 && (
                    <>
                      {" "}with <span className="font-mono">{preset.map(([k, v]) => `${k}: ${String(v)}`).join(", ")}</span>
                    </>
                  )}
                </>
              )}
            </p>
          </div>
          <IconButton title="Close" onClick={onClose}>
            <X className="size-4" />
          </IconButton>
        </header>
        <div className="flex-1 min-h-0 flex">
          <div className="flex-1 min-w-0 overflow-y-auto px-4 sm:px-5 py-5">
      <div className="space-y-3.5">
        {resource && (
          <div className="rounded-md border border-line bg-panel-2 p-2.5">
            <div className="text-[11px] uppercase tracking-wide text-faint mb-1.5">Access path</div>
            {path ? <PathView path={path} /> : <span className="text-xs text-faint">—</span>}
          </div>
        )}
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Name" required help={!nameOk && r.name ? "Letters, digits, . _ -" : undefined}>
            <Input mono autoFocus={!resource} value={r.name} onChange={(e) => setR({ ...r, name: e.target.value })} placeholder={product?.defaultName ?? NAME_HINT[product?.category ?? ""] ?? "name"} />
          </FormField>
          <FormField label="Environment" help={envs.find((e) => e.name === r.environment)?.protected ? "Protected: the agent asks before every change" : undefined}>
            {envs.length > 0 && envs.length <= 4 ? (
              <div>
                <Segmented
                  value={r.environment ?? ""}
                  onChange={(v) => setR({ ...r, environment: v })}
                  options={[{ id: "", label: "none" }, ...envs.map((e) => ({ id: e.name, label: e.name }))]}
                />
              </div>
            ) : (
            <Select value={r.environment ?? ""} onChange={(e) => setR({ ...r, environment: e.target.value })}>
              <option value="">— none —</option>
              {workspace?.environments.map((e) => (
                <option key={e.name} value={e.name}>
                  {e.name}
                  {e.protected ? " (protected)" : ""}
                </option>
              ))}
            </Select>
            )}
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
          </div>
          <aside aria-label="Summary" className="hidden md:flex w-[300px] shrink-0 flex-col gap-5 overflow-y-auto border-l border-line bg-bg px-5 py-5">
            <div>
              <div className="text-[10.5px] font-semibold uppercase tracking-wider text-faint mb-2">Saved as</div>
              <pre className="rounded-lg border border-line bg-panel px-3 py-2.5 font-mono text-[11.5px] leading-relaxed text-muted overflow-x-auto">{toYaml(r)}</pre>
              <p className="mt-2 text-[11.5px] text-faint">Plain YAML in the workspace. Passwords and keys stay in the vault; only references are stored.</p>
            </div>
            <div>
              <div className="text-[10.5px] font-semibold uppercase tracking-wider text-faint mb-2">Shows up in</div>
              <div className="flex flex-wrap gap-1.5">
                {(SHOWS_IN[schema.kind] ?? []).concat("Agent tools").map((x) => (
                  <Badge key={x}>{x}</Badge>
                ))}
              </div>
            </div>
            <div>
              <div className="text-[10.5px] font-semibold uppercase tracking-wider text-faint mb-2">Need help?</div>
              <Button
                size="sm"
                variant="agentSoft"
                icon={<Bot className="size-3.5" />}
                onClick={() => {
                  onClose();
                  navigate(
                    `/w/${encodeURIComponent(ws)}/agent?new=1&draft=${encodeURIComponent(
                      resource ? `Check the ${schema.kind} "${resource.name}" and tell me what is wrong or missing.` : `Help me add a ${product?.name ?? schema.label} resource to this workspace.`,
                    )}`,
                  );
                }}
              >
                Ask the agent
              </Button>
            </div>
          </aside>
        </div>
        <footer className="flex items-center justify-end gap-2 px-4 sm:px-5 py-3 border-t border-line shrink-0">
          {onDelete && (
            <Button variant="danger" icon={<Trash2 className="size-3.5" />} onClick={onDelete} className="mr-auto">
              Delete
            </Button>
          )}
          {!onDelete && missing.length > 0 && (
            <span className="mr-auto text-[11.5px] text-faint truncate">Required: {missing.map((f) => f.label || f.key).join(", ")}</span>
          )}
          <Button variant="ghost" onClick={onBack ?? onClose}>
            {onBack ? "Back" : "Cancel"}
          </Button>
          <Button variant="primary" onClick={save} loading={busy} disabled={!nameOk || missing.length > 0}>
            {resource ? "Save" : `Add ${product?.name ?? "resource"}`}
          </Button>
        </footer>
      </div>
    </div>,
    document.body,
  );
}

const NAME_HINT: Record<string, string> = { servers: "app-1", vpn: "corp-vpn", db: "shop-db", obs: "monitoring", code: "shop", k8s: "prod-cluster", web: "acme-shop" };

const SHOWS_IN: Record<string, string[]> = {
  Host: ["Terminal", "Logs", "Docker", "Topology"],
  HostGroup: ["Ansible", "Topology"],
  WireGuard: ["VPN", "Topology"],
  Tailscale: ["VPN", "Topology"],
  NetBird: ["VPN", "Topology"],
  OpenVPN: ["VPN", "Topology"],
  OpenConnect: ["VPN", "Topology"],
  OpenFortiVPN: ["VPN", "Topology"],
  SocksProxy: ["VPN", "Topology"],
  Database: ["Databases", "Topology"],
  Prometheus: ["Logs"],
  Loki: ["Logs"],
  Elasticsearch: ["Logs"],
  Panel: ["Topology"],
  GitRemote: ["Repositories", "Docs"],
  AnsibleProject: ["Ansible"],
  DeployPlatform: ["Deployments"],
  KubernetesCluster: ["Kubernetes", "Topology"],
  Domain: ["Domains"],
  Certificate: ["Certificates"],
};

// toYaml previews the resource file (a sketch; the server writes the real one).
function toYaml(r: Resource): string {
  const v = (x: unknown): string => (Array.isArray(x) ? `[${x.map(String).join(", ")}]` : typeof x === "string" && /[:#{}[\],&*?|<>=!%@`]|^\s|\s$/.test(x) ? JSON.stringify(x) : String(x));
  const lines = [`kind: ${r.kind}`, `name: ${r.name || "…"}`];
  if (r.environment) lines.push(`environment: ${r.environment}`);
  if (r.via) lines.push(`via: ${r.via}`);
  if (r.tags?.length) lines.push(`tags: ${v(r.tags)}`);
  const spec = Object.entries(r.spec).filter(([, x]) => x !== "" && x !== undefined && x !== null && !(Array.isArray(x) && !x.length));
  if (spec.length) {
    lines.push("spec:");
    for (const [k, x] of spec) lines.push(`  ${k}: ${typeof x === "string" && x.includes("\n") ? "|" : v(x)}`);
  }
  return lines.join("\n");
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
