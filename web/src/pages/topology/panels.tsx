import { useMemo, useState } from "react";
import { ArrowLeftRight, Bot, Eye, EyeOff, Plus, SquareDashed, Trash2, X } from "lucide-react";
import type { TopoCatalog, TopoDeployment, TopoLink, TopoLinkKind, TopoNode, TopoNodeType, TopoZone, TopologyDoc } from "../../api/types";
import { Button, ChipInput, FormField, IconButton, Input, SearchInput, Select, Textarea, Toggle, cx } from "../../components/ui";
import { Popover, MenuItem } from "../../components/overlay";
import { TopoIcon } from "../../components/topoIcons";
import { useLocalStorage } from "../../hooks";
import { LINK_STYLE, ZONE_COLOR, isInside } from "./doc";

const CATEGORY_LABEL: Record<string, string> = { network: "Network", tunnel: "Tunnels / VPN", routing: "Routing", app: "Application", data: "Data", mgmt: "Management" };

export function countByCategory(doc: TopologyDoc, kindOf: (k?: string) => TopoLinkKind): Record<string, number> {
  const c: Record<string, number> = {};
  for (const l of doc.links) {
    const cat = kindOf(l.kind).category;
    c[cat] = (c[cat] ?? 0) + 1;
  }
  return c;
}

export function Legend({
  hidden,
  onToggle,
  counts,
  labelMode,
  setLabelMode,
  lineMode,
  setLineMode,
  showLines,
  setShowLines,
}: {
  hidden: string[];
  onToggle: (c: string) => void;
  counts: Record<string, number>;
  labelMode: "auto" | "all" | "none";
  setLabelMode: (v: "auto" | "all" | "none") => void;
  lineMode: "routed" | "straight";
  setLineMode: (v: "routed" | "straight") => void;
  showLines: boolean;
  setShowLines: (v: boolean) => void;
}) {
  const [open, setOpen] = useLocalStorage("opsdeck.topology.legend", false);
  return (
    <div className="rounded-lg border border-line bg-panel/95 shadow-sm text-[11px] w-48">
      <div className="flex items-center h-7">
        <button
          onClick={() => setShowLines(!showLines)}
          className={cx("h-full ps-2.5 pe-1.5 flex items-center", showLines ? "text-muted hover:text-fg" : "text-accent")}
          title={showLines ? "Hide the lines (hovered or selected components still show theirs)" : "Show the lines"}
          aria-label={showLines ? "Hide connections" : "Show connections"}
          aria-pressed={!showLines}
        >
          {showLines ? <Eye className="size-3.5" /> : <EyeOff className="size-3.5" />}
        </button>
        <button onClick={() => setOpen(!open)} className="flex-1 h-full flex items-center justify-between pe-2.5 text-muted hover:text-fg">
          <span className={cx("font-medium", !showLines && "text-faint")}>Connections{!showLines && " (hidden)"}</span>
          <span className="text-faint">{open ? "hide" : "show"}</span>
        </button>
      </div>
      {open && (
        <div className="px-2.5 pb-2 space-y-1">
          {Object.entries(LINK_STYLE).map(([cat, st]) => {
            const off = hidden.includes(cat);
            return (
              <button key={cat} onClick={() => onToggle(cat)} className={cx("w-full flex items-center gap-2 text-left", off && "opacity-40")} title={off ? "show" : "hide"}>
                <svg width="26" height="8" className="shrink-0">
                  <line x1="1" y1="4" x2="25" y2="4" stroke={st.stroke} strokeWidth={2} strokeDasharray={st.dash} />
                </svg>
                <span className={cx("flex-1", off && "line-through")}>{CATEGORY_LABEL[cat]}</span>
                <span className="text-faint tabular-nums">{counts[cat] ?? 0}</span>
              </button>
            );
          })}
          <div className="pt-1.5 border-t border-line space-y-1">
            <div className="flex items-center justify-between">
              <span className="text-faint">Labels</span>
              <Segmented3 value={labelMode} onChange={setLabelMode} options={["auto", "all", "none"]} />
            </div>
            <div className="flex items-center justify-between">
              <span className="text-faint">Lines</span>
              <Segmented3 value={lineMode} onChange={setLineMode} options={["routed", "straight"]} />
            </div>
            <div className="text-faint leading-snug pt-0.5">Hover or select a component to highlight its connections.</div>
          </div>
        </div>
      )}
    </div>
  );
}

export function Segmented3<T extends string>({ value, onChange, options }: { value: T; onChange: (v: T) => void; options: T[] }) {
  return (
    <div className="inline-flex rounded border border-line">
      {options.map((o) => (
        <button key={o} onClick={() => onChange(o)} className={cx("px-1.5 h-5", value === o ? "bg-panel-3 text-fg" : "text-muted hover:text-fg")}>
          {o}
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Palette

export function Palette({ catalog, onAdd, onZone }: { catalog: TopoCatalog; onAdd: (nt: TopoNodeType) => void; onZone: (kind: string, label: string) => void }) {
  const [q, setQ] = useState("");
  const [zoneMenu, setZoneMenu] = useState(false);
  const groups = useMemo(() => {
    const g: Record<string, TopoNodeType[]> = {};
    const t = q.toLowerCase();
    for (const n of catalog.nodeTypes) {
      if (t && !(n.label + " " + n.type + " " + (n.variants ?? []).join(" ") + " " + (n.tech ?? []).join(" ")).toLowerCase().includes(t)) continue;
      (g[n.category] ??= []).push(n);
    }
    return g;
  }, [catalog, q]);
  return (
    <aside className="w-56 shrink-0 border-r border-line bg-panel max-md:hidden flex flex-col">
      <div className="p-2 space-y-2 border-b border-line">
        <SearchInput value={q} onChange={setQ} placeholder="Router, WAF, nginx…" />
        <div className="relative">
          <Button size="sm" variant="secondary" className="w-full" icon={<SquareDashed className="size-3.5" />} onClick={() => setZoneMenu((v) => !v)}>
            Add zone
          </Button>
          <Popover open={zoneMenu} onClose={() => setZoneMenu(false)} className="left-0 mt-1 w-52 max-h-80 overflow-y-auto">
            {catalog.zoneKinds.map((z) => (
              <MenuItem
                key={z.id}
                icon={<SquareDashed style={{ color: ZONE_COLOR[z.id] }} />}
                onClick={() => {
                  setZoneMenu(false);
                  onZone(z.id, z.label);
                }}
              >
                {z.label}
              </MenuItem>
            ))}
          </Popover>
        </div>
      </div>
      <div className="flex-1 overflow-y-auto p-2 space-y-3">
        {Object.entries(groups).map(([cat, list]) => (
          <div key={cat}>
            <div className="px-1 mb-1 text-[10.5px] uppercase tracking-wider text-faint">{cat}</div>
            <div className="grid grid-cols-2 gap-1">
              {list.map((nt) => (
                <button
                  key={nt.type}
                  draggable
                  onDragStart={(e) => {
                    e.dataTransfer.setData("application/opsdeck-node", nt.type);
                    e.dataTransfer.effectAllowed = "move";
                  }}
                  onClick={() => onAdd(nt)}
                  title={[nt.label, nt.variants?.join(", "), nt.tech?.slice(0, 6).join(", ")].filter(Boolean).join("\n")}
                  className="flex flex-col items-center gap-1 rounded-md border border-line bg-panel-2 hover:border-accent/60 px-1 py-1.5 cursor-grab active:cursor-grabbing"
                >
                  <TopoIcon name={nt.icon} className="size-4 text-accent" />
                  <span className="text-[10.5px] leading-tight text-center">{nt.label}</span>
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
      <div className="p-2 border-t border-line text-[10.5px] text-faint leading-snug">Drag onto the canvas or click. Connect by dragging from a handle to another component.</div>
    </aside>
  );
}

// ---------------------------------------------------------------------------
// Inspectors

export function Head({ icon, title, onDelete }: { icon: React.ReactNode; title: string; onDelete: () => void }) {
  return (
    <div className="flex items-center gap-2 px-3 h-11 border-b border-line sticky top-0 bg-panel z-10">
      <span className="text-accent">{icon}</span>
      <span className="font-semibold text-[13px] truncate flex-1">{title}</span>
      <IconButton title="Delete" onClick={onDelete}>
        <Trash2 className="size-4" />
      </IconButton>
    </div>
  );
}

export function Suggest({ options, onPick }: { options: string[]; onPick: (v: string) => void }) {
  if (!options.length) return null;
  return (
    <div className="flex flex-wrap gap-1 mt-1">
      {options.slice(0, 12).map((o) => (
        <button key={o} type="button" onClick={() => onPick(o)} className="rounded border border-line px-1.5 text-[10.5px] text-muted hover:text-fg hover:border-accent/60">
          + {o}
        </button>
      ))}
    </div>
  );
}

export function NodeInspector({
  n,
  doc,
  catalog,
  resources,
  repos,
  environments,
  onChange,
  onZone,
  onDelete,
  onSelectLink,
}: {
  n: TopoNode;
  doc: TopologyDoc;
  catalog: TopoCatalog;
  resources: string[];
  repos: string[];
  environments: string[];
  onChange: (fn: (n: TopoNode) => void) => void;
  onZone: (zone: string) => void;
  onDelete: () => void;
  onSelectLink: (id: string) => void;
}) {
  const nt = catalog.nodeTypes.find((t) => t.type === n.type);
  const set = <K extends keyof TopoNode>(k: K, v: TopoNode[K]) => onChange((x) => void (x[k] = v));
  const deps = n.deployments ?? [];
  const setDep = (i: number, fn: (d: TopoDeployment) => void) => onChange((x) => fn(x.deployments![i]));
  return (
    <div>
      <Head icon={<TopoIcon name={nt?.icon} />} title={n.label || n.id} onDelete={onDelete} />
      <div className="p-3 space-y-3">
        <div className="grid grid-cols-2 gap-2">
          <FormField label="Type">
            <Select value={n.type} onChange={(e) => set("type", e.target.value)}>
              {catalog.nodeTypes.map((t) => (
                <option key={t.type} value={t.type}>
                  {t.label}
                </option>
              ))}
            </Select>
          </FormField>
          <FormField label="Kind">
            <Select value={n.variant ?? ""} onChange={(e) => set("variant", e.target.value || undefined)}>
              <option value="">—</option>
              {(nt?.variants ?? []).map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
              {n.variant && !(nt?.variants ?? []).includes(n.variant) && <option value={n.variant}>{n.variant}</option>}
            </Select>
          </FormField>
        </div>
        <FormField label="Name" help={<span className="font-mono">id: {n.id}</span>}>
          <Input value={n.label ?? ""} onChange={(e) => set("label", e.target.value)} />
        </FormField>
        <FormField label="Zone">
          <Select value={n.zone ?? ""} onChange={(e) => onZone(e.target.value)}>
            <option value="">— none —</option>
            {doc.zones.map((z) => (
              <option key={z.id} value={z.id}>
                {z.label || z.id}
              </option>
            ))}
          </Select>
        </FormField>
        <FormField label="Product / vendor">
          <Input value={n.vendor ?? ""} onChange={(e) => set("vendor", e.target.value || undefined)} placeholder={nt?.tech?.[0] ?? "e.g. MikroTik CCR2004"} />
          {!n.vendor && <Suggest options={nt?.tech ?? []} onPick={(v) => set("vendor", v)} />}
        </FormField>
        <FormField label="Technologies" help="Software, versions, protocols: nginx 1.25, BGP, Ubuntu 24.04…">
          <ChipInput value={n.tech ?? []} onChange={(v) => set("tech", v.length ? v : undefined)} placeholder="add…" />
          <Suggest options={(nt?.tech ?? []).filter((t) => !(n.tech ?? []).includes(t) && t !== n.vendor)} onPick={(v) => set("tech", [...(n.tech ?? []), v])} />
        </FormField>
        <FormField label="Addresses">
          <ChipInput mono value={n.addresses ?? []} onChange={(v) => set("addresses", v.length ? v : undefined)} placeholder="10.0.1.10, app.example.com" />
        </FormField>
        <div className="grid grid-cols-2 gap-2">
          <FormField label="Inventory">
            <Select value={n.resource ?? ""} onChange={(e) => set("resource", e.target.value || undefined)}>
              <option value="">—</option>
              {resources.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
              {n.resource && !resources.includes(n.resource) && <option value={n.resource}>{n.resource} (not in inventory)</option>}
            </Select>
          </FormField>
          <FormField label="Environment">
            <Select value={n.environment ?? ""} onChange={(e) => set("environment", e.target.value || undefined)}>
              <option value="">—</option>
              {environments.map((e) => (
                <option key={e} value={e}>
                  {e}
                </option>
              ))}
            </Select>
          </FormField>
        </div>

        <div className="border-t border-line pt-3">
          <div className="flex items-center justify-between mb-2">
            <span className="text-[12px] font-medium">Deployments</span>
            <Button size="xs" variant="ghost" icon={<Plus className="size-3" />} onClick={() => onChange((x) => void (x.deployments = [...(x.deployments ?? []), { name: "", kind: "docker-compose" }]))}>
              Add
            </Button>
          </div>
          {deps.length === 0 && <div className="text-[11px] text-faint">What runs here: compose stacks, Kubernetes workloads, systemd services, packages…</div>}
          <div className="space-y-2">
            {deps.map((d, i) => (
              <div key={i} className="rounded-md border border-line bg-panel-2 p-2 space-y-1.5">
                <div className="flex gap-1.5">
                  <Input value={d.name} onChange={(e) => setDep(i, (x) => void (x.name = e.target.value))} placeholder="name (e.g. shop-api)" className="h-7 text-xs" />
                  <IconButton title="Remove" onClick={() => onChange((x) => void x.deployments!.splice(i, 1))}>
                    <X className="size-3.5" />
                  </IconButton>
                </div>
                <div className="grid grid-cols-2 gap-1.5">
                  <Select value={d.kind ?? ""} onChange={(e) => setDep(i, (x) => void (x.kind = e.target.value || undefined))} className="h-7 text-xs">
                    <option value="">kind…</option>
                    {catalog.deploymentKinds.map((k) => (
                      <option key={k.id} value={k.id}>
                        {k.label}
                      </option>
                    ))}
                  </Select>
                  <Input
                    value={d.replicas ?? ""}
                    type="number"
                    onChange={(e) => setDep(i, (x) => void (x.replicas = e.target.value ? Number(e.target.value) : undefined))}
                    placeholder="replicas"
                    className="h-7 text-xs"
                  />
                </div>
                <Input mono value={d.image ?? ""} onChange={(e) => setDep(i, (x) => void (x.image = e.target.value || undefined))} placeholder="image / package / version" className="h-7 text-xs" />
                <ChipInput mono value={d.ports ?? []} onChange={(v) => setDep(i, (x) => void (x.ports = v.length ? v : undefined))} placeholder="ports: 8080, 443/tcp" />
                <div className="grid grid-cols-2 gap-1.5">
                  <Input mono list="topo-repos" value={d.repo ?? ""} onChange={(e) => setDep(i, (x) => void (x.repo = e.target.value || undefined))} placeholder="repo (git/…)" className="h-7 text-xs" />
                  <Input mono value={d.path ?? ""} onChange={(e) => setDep(i, (x) => void (x.path = e.target.value || undefined))} placeholder="path on host" className="h-7 text-xs" />
                </div>
              </div>
            ))}
          </div>
          <datalist id="topo-repos">
            {repos.map((r) => (
              <option key={r} value={r} />
            ))}
          </datalist>
        </div>
        <FormField label="Tags">
          <ChipInput value={n.tags ?? []} onChange={(v) => set("tags", v.length ? v : undefined)} placeholder="add…" />
        </FormField>
        <FormField label="Notes">
          <Textarea value={n.notes ?? ""} onChange={(e) => set("notes", e.target.value || undefined)} className="min-h-16" />
        </FormField>
        <Connections doc={doc} id={n.id} onSelect={onSelectLink} />
      </div>
    </div>
  );
}

function Connections({ doc, id, onSelect }: { doc: TopologyDoc; id: string; onSelect: (link: string) => void }) {
  const links = doc.links.filter((l) => l.from === id || l.to === id);
  if (!links.length) return null;
  const name = (x: string) => doc.nodes.find((n) => n.id === x)?.label || x;
  return (
    <div className="border-t border-line pt-3">
      <div className="text-[12px] font-medium mb-1">Connections</div>
      {links.map((l) => (
        <button key={l.id} onClick={() => onSelect(l.id)} className="block w-full text-left text-[11.5px] text-muted hover:text-fg">
          {l.from === id ? `→ ${name(l.to)}` : `← ${name(l.from)}`} <span className="text-faint">{[l.kind, l.port].filter(Boolean).join(" :")}</span>
        </button>
      ))}
    </div>
  );
}

export function ZoneInspector({ z, doc, catalog, onChange, onDelete }: { z: TopoZone; doc: TopologyDoc; catalog: TopoCatalog; onChange: (fn: (z: TopoZone) => void) => void; onDelete: () => void }) {
  const set = <K extends keyof TopoZone>(k: K, v: TopoZone[K]) => onChange((x) => void (x[k] = v));
  return (
    <div>
      <Head icon={<SquareDashed className="size-4" />} title={z.label || z.id} onDelete={onDelete} />
      <div className="p-3 space-y-3">
        <FormField label="Name" help={<span className="font-mono">id: {z.id}</span>}>
          <Input value={z.label ?? ""} onChange={(e) => set("label", e.target.value)} />
        </FormField>
        <FormField label="Kind">
          <Select value={z.kind ?? ""} onChange={(e) => set("kind", e.target.value || undefined)}>
            <option value="">—</option>
            {catalog.zoneKinds.map((k) => (
              <option key={k.id} value={k.id}>
                {k.label}
              </option>
            ))}
          </Select>
        </FormField>
        <FormField label="Inside" help="Or drag the zone into another zone.">
          <Select value={z.parent ?? ""} onChange={(e) => set("parent", e.target.value || undefined)}>
            <option value="">— top level —</option>
            {doc.zones
              .filter((x) => x.id !== z.id && !isInside(doc, x.id, z.id))
              .map((x) => (
                <option key={x.id} value={x.id}>
                  {x.label || x.id}
                </option>
              ))}
          </Select>
        </FormField>
        <div className="grid grid-cols-2 gap-2">
          <FormField label="CIDR">
            <Input mono value={z.cidr ?? ""} onChange={(e) => set("cidr", e.target.value || undefined)} placeholder="10.0.1.0/24" />
          </FormField>
          <FormField label="Provider">
            <Input list="topo-providers" value={z.provider ?? ""} onChange={(e) => set("provider", e.target.value || undefined)} placeholder="ArvanCloud, AWS, on-prem" />
            <datalist id="topo-providers">
              {(catalog.providers ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.label}
                </option>
              ))}
            </datalist>
          </FormField>
        </div>
        <FormField label="Notes">
          <Textarea value={z.notes ?? ""} onChange={(e) => set("notes", e.target.value || undefined)} className="min-h-16" />
        </FormField>
        <div className="text-[11px] text-faint">Resize the zone by dragging its corners while it is selected.</div>
      </div>
    </div>
  );
}

export function LinkInspector({ l, doc, catalog, onChange, onDelete }: { l: TopoLink; doc: TopologyDoc; catalog: TopoCatalog; onChange: (fn: (l: TopoLink) => void) => void; onDelete: () => void }) {
  const set = <K extends keyof TopoLink>(k: K, v: TopoLink[K]) => onChange((x) => void (x[k] = v));
  const name = (x: string) => doc.nodes.find((n) => n.id === x)?.label || x;
  const cats = useMemo(() => {
    const g: Record<string, TopoLinkKind[]> = {};
    for (const k of catalog.linkKinds) (g[k.category] ??= []).push(k);
    return g;
  }, [catalog]);
  return (
    <div>
      <Head icon={<ArrowLeftRight className="size-4" />} title={`${name(l.from)} → ${name(l.to)}`} onDelete={onDelete} />
      <div className="p-3 space-y-3">
        <div className="flex items-center gap-2 text-xs">
          <span className="font-mono truncate">{name(l.from)}</span>
          <span className="text-faint">{l.bidirectional ? "↔" : "→"}</span>
          <span className="font-mono truncate">{name(l.to)}</span>
          <Button size="xs" variant="ghost" className="ms-auto" onClick={() => onChange((x) => void ([x.from, x.to] = [x.to, x.from]))}>
            Swap
          </Button>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <FormField label="Connection">
            <Select
              value={l.kind ?? ""}
              onChange={(e) =>
                onChange((x) => {
                  x.kind = e.target.value || undefined;
                  const p = catalog.linkKinds.find((k) => k.kind === x.kind)?.port;
                  if (p && !x.port) x.port = p;
                })
              }
            >
              <option value="">—</option>
              {Object.entries(cats).map(([c, list]) => (
                <optgroup key={c} label={c}>
                  {list.map((k) => (
                    <option key={k.kind} value={k.kind}>
                      {k.label}
                    </option>
                  ))}
                </optgroup>
              ))}
            </Select>
          </FormField>
          <FormField label="Port">
            <Input mono value={l.port ?? ""} onChange={(e) => set("port", e.target.value || undefined)} placeholder="443" />
          </FormField>
        </div>
        <FormField label="Label">
          <Input value={l.label ?? ""} onChange={(e) => set("label", e.target.value || undefined)} placeholder="e.g. public traffic, primary → replica" />
        </FormField>
        <div className="flex flex-col gap-2">
          <Toggle checked={!!l.encrypted} onChange={(v) => set("encrypted", v || undefined)} label={<span className="text-xs">Encrypted (TLS / tunnel)</span>} />
          <Toggle checked={!!l.bidirectional} onChange={(v) => set("bidirectional", v || undefined)} label={<span className="text-xs">Both directions</span>} />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <FormField label="Bandwidth">
            <Input value={l.bandwidth ?? ""} onChange={(e) => set("bandwidth", e.target.value || undefined)} placeholder="1 Gbps" />
          </FormField>
          <FormField label="VLAN">
            <Input mono value={l.vlan ?? ""} onChange={(e) => set("vlan", e.target.value || undefined)} placeholder="100" />
          </FormField>
        </div>
        <FormField label="Notes">
          <Textarea value={l.notes ?? ""} onChange={(e) => set("notes", e.target.value || undefined)} className="min-h-16" />
        </FormField>
      </div>
    </div>
  );
}

export function AiView({ text, dirty }: { text: string; dirty: boolean }) {
  return (
    <div className="p-3 space-y-2">
      <div className="flex items-center gap-2 text-[12px] font-medium">
        <Bot className="size-4 text-muted" /> What the agent reads
      </div>
      <div className="text-[11px] text-faint">
        Select a component, zone or connection to edit it. The agent gets this description in every session and can propose changes (it asks you first).
      </div>
      {dirty ? (
        <div className="text-[11px] text-warn">Save to update the agent's view.</div>
      ) : (
        <pre className="text-[10.5px] leading-relaxed whitespace-pre-wrap font-mono text-muted bg-panel-2 rounded-md p-2 border border-line" dir="ltr">
          {text}
        </pre>
      )}
    </div>
  );
}
