import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Background,
  ConnectionMode,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  NodeResizer,
  Position,
  ReactFlow,
  ReactFlowProvider,
  applyEdgeChanges,
  applyNodeChanges,
  useReactFlow,
} from "@xyflow/react";
import type { Connection, Edge, EdgeChange, Node, NodeChange, NodeProps } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { ArrowLeftRight, Bot, Download, FileText, Import, LayoutGrid, Lock, Network, Plus, Save, SquareDashed, Trash2, X } from "lucide-react";
import { api, errMsg, ApiError } from "../api/client";
import type { TopoBox, TopoCatalog, TopoDeployment, TopoLink, TopoLinkKind, TopoNode, TopoNodeType, TopoZone, TopologyDoc } from "../api/types";
import { useApp, useWs } from "../context/app";
import { useTabActive } from "../context/tabs";
import { Badge, Button, ChipInput, FormField, IconButton, Input, Loading, SearchInput, Select, Textarea, Toggle, cx } from "../components/ui";
import { Popover, MenuItem, useConfirm, useToast } from "../components/overlay";
import { TopoIcon } from "../components/topoIcons";
import { useAsync, useInterval } from "../hooks";

// The topology editor: zones (group boxes), components (nodes) and
// connections (edges) of the workspace's infrastructure, saved as
// topology.yaml — the same file the agent reads and edits.

type Sel = { kind: "node" | "zone" | "link"; id: string } | null;


const LINK_STYLE: Record<string, { stroke: string; dash?: string; width?: number }> = {
  network: { stroke: "var(--c-muted, #8b93a1)", width: 2 },
  tunnel: { stroke: "#a78bfa", dash: "6 4", width: 2 },
  routing: { stroke: "#f59e0b", dash: "2 4", width: 2 },
  app: { stroke: "#38bdf8", width: 1.6 },
  data: { stroke: "#34d399", width: 1.6 },
  mgmt: { stroke: "#94a3b8", dash: "4 4", width: 1.2 },
};

const ZONE_COLOR: Record<string, string> = {
  region: "#64748b",
  datacenter: "#60a5fa",
  cloud: "#38bdf8",
  vpc: "#22d3ee",
  subnet: "#2dd4bf",
  vlan: "#a3e635",
  dmz: "#f87171",
  office: "#f59e0b",
  cluster: "#a78bfa",
  namespace: "#c084fc",
  environment: "#94a3b8",
  zone: "#94a3b8",
};

const NODE_W = 190;
const NODE_H = 76;

export function TopologyPage() {
  return (
    <ReactFlowProvider>
      <Editor />
    </ReactFlowProvider>
  );
}

function depthOf(zones: TopoZone[], id: string): number {
  let d = 0;
  let cur = zones.find((z) => z.id === id)?.parent;
  while (cur && d < 20) {
    d++;
    cur = zones.find((z) => z.id === cur)?.parent;
  }
  return d;
}

function slug(s: string): string {
  return (
    s
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 40) || "node"
  );
}

function freeId(doc: TopologyDoc, base: string): string {
  const taken = new Set([...doc.nodes.map((n) => n.id), ...doc.zones.map((z) => z.id), ...doc.links.map((l) => l.id)]);
  let id = slug(base);
  for (let i = 2; taken.has(id); i++) id = `${slug(base)}-${i}`;
  return id;
}

// default link kind for a connection to a target type
function defaultKind(target?: TopoNode): string {
  switch (target?.type) {
    case "database":
      return "sql";
    case "cache":
      return "redis";
    case "queue":
      return "amqp";
    case "object-storage":
      return "s3";
    case "file-storage":
      return "nfs";
    case "dns":
      return "dns";
    case "router":
    case "switch":
      return "lan";
    case "vpn-gateway":
      return "ipsec";
  }
  return "https";
}

const GAP_X = 24;
const GAP_Y = 30;
const PAD = 20;
const HEAD = 34;

// autoLayout places everything bottom-up: a zone holds its components in a
// grid (up to 3 columns) and its sub-zones in a row below; top level puts
// components without a zone in a row above the zones.
function autoLayout(doc: TopologyDoc): Record<string, TopoBox> {
  const out: Record<string, TopoBox> = {};
  const size = (parent: string | undefined): { w: number; h: number } => {
    const nodes = doc.nodes.filter((n) => (n.zone || undefined) === parent);
    const zones = doc.zones.filter((z) => (z.parent || undefined) === parent);
    const cols = parent ? Math.min(3, Math.max(1, nodes.length)) : Math.min(6, Math.max(1, nodes.length));
    const top = parent ? HEAD : 0;
    const left = parent ? PAD : 0;
    nodes.forEach((n, i) => {
      out[n.id] = { x: left + (i % cols) * (NODE_W + GAP_X), y: top + Math.floor(i / cols) * (NODE_H + GAP_Y) };
    });
    const rows = Math.ceil(nodes.length / cols);
    const gridW = nodes.length ? cols * (NODE_W + GAP_X) - GAP_X : 0;
    let y = top + (rows ? rows * (NODE_H + GAP_Y) + (parent ? 0 : 40) : 0);
    let x = left;
    let rowH = 0;
    let maxW = gridW;
    const wrapAt = parent ? 900 : 1700;
    for (const z of zones) {
      const s = size(z.id);
      if (x > left && x + s.w > left + wrapAt) {
        x = left;
        y += rowH + GAP_Y;
        rowH = 0;
      }
      out[z.id] = { x, y, w: s.w, h: s.h };
      x += s.w + GAP_X;
      rowH = Math.max(rowH, s.h);
      maxW = Math.max(maxW, x - GAP_X - left);
    }
    const h = y + rowH + (parent ? PAD : 0);
    return { w: Math.max(maxW + 2 * (parent ? PAD : 0), 240), h: Math.max(h, 140) };
  };
  size(undefined);
  return out;
}

// clean fills in what an older, empty or hand-edited file may lack.
function clean(t: Partial<TopologyDoc> | null | undefined): TopologyDoc {
  const arr = <T,>(v: T[] | null | undefined): T[] => (Array.isArray(v) ? v : []);
  return {
    version: t?.version || 1,
    zones: arr(t?.zones).filter((z) => z && z.id),
    nodes: arr(t?.nodes)
      .filter((n) => n && n.id)
      .map((n) => ({ ...n, type: n.type || "generic", deployments: n.deployments ? arr(n.deployments) : undefined })),
    links: arr(t?.links).filter((l) => l && l.id && l.from && l.to),
    layout: t?.layout && typeof t.layout === "object" ? t.layout : {},
  };
}

// fillLayout gives every zone and component a position: everything
// auto-arranged when nothing has one yet, otherwise new things (e.g. added
// by the agent) go below their siblings.
function fillLayout(doc: TopologyDoc): TopologyDoc {
  const layout = { ...(doc.layout ?? {}) };
  const auto = autoLayout(doc);
  if (Object.keys(layout).length === 0) return { ...doc, layout: auto };
  const bottom = (parent: string | undefined) => {
    let y = parent ? HEAD - GAP_Y : -GAP_Y;
    for (const n of doc.nodes) if ((n.zone || undefined) === parent && layout[n.id]) y = Math.max(y, layout[n.id].y + NODE_H);
    for (const z of doc.zones) if ((z.parent || undefined) === parent && layout[z.id]) y = Math.max(y, layout[z.id].y + (layout[z.id].h ?? 200));
    return y + GAP_Y;
  };
  const zones = [...doc.zones].sort((a, b) => depthOf(doc.zones, a.id) - depthOf(doc.zones, b.id));
  for (const z of zones) if (!layout[z.id]) layout[z.id] = { x: z.parent ? PAD : 0, y: bottom(z.parent || undefined), w: auto[z.id]?.w ?? 400, h: auto[z.id]?.h ?? 240 };
  for (const n of doc.nodes) {
    if (layout[n.id]) continue;
    const parent = n.zone || undefined;
    const row = doc.nodes.filter((m) => (m.zone || undefined) === parent && !layout[m.id]).indexOf(n);
    layout[n.id] = { x: (parent ? PAD : 0) + row * (NODE_W + GAP_X), y: bottom(parent) };
  }
  return { ...doc, layout };
}

function Editor() {
  const { name: ws, resources, workspace } = useWs();
  const { theme } = useApp();
  const toast = useToast();
  const confirm = useConfirm();
  const active = useTabActive();
  const rf = useReactFlow();
  const catalog = useAsync(() => api.topologyCatalog(), []);
  const [doc, setDoc] = useState<TopologyDoc | null>(null);
  const [rev, setRev] = useState("");
  const [saved, setSaved] = useState("");
  const [loadErr, setLoadErr] = useState("");
  const [sel, setSel] = useState<Sel>(null);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [saving, setSaving] = useState(false);
  const [remote, setRemote] = useState<string | null>(null); // a newer revision exists
  const [aiView, setAiView] = useState<string>("");
  const [exportMenu, setExportMenu] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  const dirty = !!doc && JSON.stringify(doc) !== saved;

  const typeOf = useCallback(
    (t: string): TopoNodeType => catalog.data?.nodeTypes.find((x) => x.type === t) ?? { type: t, label: t, category: "Other", icon: "box" },
    [catalog.data],
  );
  const linkKindOf = useCallback(
    (k?: string): TopoLinkKind => catalog.data?.linkKinds.find((x) => x.kind === k) ?? { kind: k ?? "", label: k || "connection", category: "app" },
    [catalog.data],
  );

  const load = useCallback(async () => {
    try {
      const r = await api.topologyDoc(ws);
      const d = fillLayout(clean(r.topology));
      setDoc(d);
      setSaved(JSON.stringify(d));
      setRev(r.rev);
      setAiView(r.text);
      setRemote(null);
    } catch (e) {
      setLoadErr(errMsg(e));
    }
  }, [ws]);
  useEffect(() => {
    void load();
  }, [load]);

  // The agent may change topology.yaml: follow it while nothing is unsaved.
  useInterval(async () => {
    try {
      const r = await api.topologyDoc(ws);
      if (r.rev === rev) return;
      if (!dirty) {
        await load();
        toast.info("Topology updated", "Changed outside this page (e.g. by the agent).");
      } else setRemote(r.rev);
    } catch {
      /* offline: ignore */
    }
  }, active ? 4000 : null);

  const update = (fn: (d: TopologyDoc) => void) =>
    setDoc((prev) => {
      if (!prev) return prev;
      const next: TopologyDoc = structuredClone(prev);
      next.layout ??= {};
      fn(next);
      return next;
    });

  // ---- doc → React Flow

  useEffect(() => {
    if (!doc) return;
    const auto = autoLayout(doc);
    const layout = { ...auto, ...(doc.layout ?? {}) };
    const zones = [...doc.zones].sort((a, b) => depthOf(doc.zones, a.id) - depthOf(doc.zones, b.id));
    setNodes((prev) => {
      const old = new Map(prev.map((n) => [n.id, n]));
      const out: Node[] = [];
      for (const z of zones) {
        const box: TopoBox = { ...auto[z.id], ...layout[z.id] };
        const prevNode = old.get(z.id);
        out.push({
          ...prevNode,
          id: z.id,
          type: "zone",
          position: { x: box.x, y: box.y },
          style: { width: box.w ?? 400, height: box.h ?? 260 },
          width: box.w ?? 400,
          height: box.h ?? 260,
          parentId: z.parent || undefined,
          data: { zone: z, color: ZONE_COLOR[z.kind ?? "zone"] ?? "#94a3b8" },
          selected: sel?.kind === "zone" && sel.id === z.id,
          zIndex: -10 + depthOf(doc.zones, z.id),
        });
      }
      for (const n of doc.nodes) {
        const box = layout[n.id] ?? { x: 0, y: 0 };
        const prevNode = old.get(n.id);
        out.push({
          ...prevNode,
          id: n.id,
          type: "component",
          position: { x: box.x, y: box.y },
          parentId: n.zone || undefined,
          data: { node: n, nt: typeOf(n.type) },
          selected: sel?.kind === "node" && sel.id === n.id,
          zIndex: 10,
        });
      }
      return out;
    });
    setEdges(
      doc.links.map((l) => {
        const lk = linkKindOf(l.kind);
        const st = LINK_STYLE[lk.category] ?? LINK_STYLE.app;
        const label = [l.kind || lk.label, l.port ? `:${l.port}` : "", l.encrypted ? " 🔒" : ""].join("");
        return {
          id: l.id,
          source: l.from,
          target: l.to,
          label,
          selected: sel?.kind === "link" && sel.id === l.id,
          style: { stroke: st.stroke, strokeDasharray: st.dash, strokeWidth: sel?.id === l.id ? (st.width ?? 1.5) + 1.2 : st.width },
          labelStyle: { fontSize: 10.5, fill: "var(--c-fg, #e5e7eb)" },
          labelBgStyle: { fill: "var(--c-panel, #111318)", fillOpacity: 0.9 },
          labelBgPadding: [4, 2] as [number, number],
          markerEnd: { type: MarkerType.ArrowClosed, color: st.stroke, width: 16, height: 16 },
          markerStart: l.bidirectional ? { type: MarkerType.ArrowClosed, color: st.stroke, width: 16, height: 16 } : undefined,
          zIndex: 5,
        };
      }),
    );
  }, [doc, sel, typeOf, linkKindOf]);

  // fit the view once the first diagram is on screen
  const fitted = useRef(false);
  useEffect(() => {
    if (!fitted.current && nodes.length > 0) {
      fitted.current = true;
      setTimeout(() => rf.fitView({ padding: 0.12 }), 60);
    }
  }, [nodes.length, rf]);

  // ---- React Flow → doc

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      setNodes((ns) => applyNodeChanges(changes, ns));
      const moves: Record<string, Partial<TopoBox>> = {};
      for (const c of changes) {
        if (c.type === "position" && c.position && c.dragging === false) moves[c.id] = { ...moves[c.id], x: c.position.x, y: c.position.y };
        if (c.type === "dimensions" && c.dimensions && c.resizing !== undefined && c.setAttributes) {
          moves[c.id] = { ...moves[c.id], w: c.dimensions.width, h: c.dimensions.height };
        }
      }
      if (Object.keys(moves).length)
        update((d) => {
          for (const [id, m] of Object.entries(moves)) {
            const cur = rf.getNode(id);
            d.layout![id] = { x: m.x ?? cur?.position.x ?? 0, y: m.y ?? cur?.position.y ?? 0, w: m.w ?? d.layout![id]?.w, h: m.h ?? d.layout![id]?.h };
            if (d.layout![id].w === undefined) delete d.layout![id].w;
            if (d.layout![id].h === undefined) delete d.layout![id].h;
          }
        });
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rf],
  );

  const onEdgesChange = useCallback((changes: EdgeChange[]) => setEdges((es) => applyEdgeChanges(changes, es)), []);

  const absolute = (id: string) => rf.getInternalNode(id)?.internals.positionAbsolute ?? { x: 0, y: 0 };

  // innermost zone containing a point (excluding a zone itself and its descendants)
  const zoneAt = (p: { x: number; y: number }, exclude?: string): string | undefined => {
    if (!doc) return undefined;
    let best: string | undefined;
    let bestDepth = -1;
    for (const z of doc.zones) {
      if (exclude && (z.id === exclude || isInside(doc, z.id, exclude))) continue;
      const a = absolute(z.id);
      const n = rf.getNode(z.id);
      const w = n?.measured?.width ?? n?.width ?? 0;
      const h = n?.measured?.height ?? n?.height ?? 0;
      if (p.x >= a.x && p.x <= a.x + w && p.y >= a.y && p.y <= a.y + h) {
        const dz = depthOf(doc.zones, z.id);
        if (dz > bestDepth) {
          best = z.id;
          bestDepth = dz;
        }
      }
    }
    return best;
  };

  // Dropping a component or zone into (or out of) a zone moves it there.
  const onNodeDragStop = (_: unknown, node: Node) => {
    if (!doc) return;
    const isZone = node.type === "zone";
    const a = absolute(node.id);
    const w = node.measured?.width ?? NODE_W;
    const h = node.measured?.height ?? NODE_H;
    const center = isZone ? { x: a.x + 10, y: a.y + 10 } : { x: a.x + w / 2, y: a.y + h / 2 };
    const target = zoneAt(center, isZone ? node.id : undefined);
    const current = isZone ? doc.zones.find((z) => z.id === node.id)?.parent : doc.nodes.find((n) => n.id === node.id)?.zone;
    if ((target ?? "") === (current ?? "")) return;
    const pa = target ? absolute(target) : { x: 0, y: 0 };
    update((d) => {
      if (isZone) d.zones.find((z) => z.id === node.id)!.parent = target || undefined;
      else d.nodes.find((n) => n.id === node.id)!.zone = target || undefined;
      d.layout![node.id] = { ...d.layout![node.id], x: a.x - pa.x, y: a.y - pa.y };
    });
  };

  const onConnect = (c: Connection) => {
    if (!doc || !c.source || !c.target || c.source === c.target) return;
    const target = doc.nodes.find((n) => n.id === c.target);
    const kind = defaultKind(target);
    const id = freeId(doc, `${c.source}-${c.target}`);
    update((d) => d.links.push({ id, from: c.source!, to: c.target!, kind, port: linkKindOf(kind).port, encrypted: kind === "https" || ["tunnel"].includes(linkKindOf(kind).category) }));
    setSel({ kind: "link", id });
  };

  // ---- adding

  function addNode(nt: TopoNodeType, at?: { x: number; y: number }) {
    if (!doc) return;
    const id = freeId(doc, nt.type);
    const pos = at ?? rf.screenToFlowPosition({ x: (wrap.current?.clientWidth ?? 800) / 2 + (wrap.current?.getBoundingClientRect().left ?? 0), y: (wrap.current?.clientHeight ?? 600) / 2 + (wrap.current?.getBoundingClientRect().top ?? 0) });
    const zone = zoneAt({ x: pos.x + NODE_W / 2, y: pos.y + NODE_H / 2 });
    const pa = zone ? absolute(zone) : { x: 0, y: 0 };
    update((d) => {
      d.nodes.push({ id, type: nt.type, label: nt.label, zone });
      d.layout![id] = { x: pos.x - pa.x, y: pos.y - pa.y };
    });
    setSel({ kind: "node", id });
  }

  function addZone(kind: string, label: string) {
    if (!doc) return;
    const id = freeId(doc, kind);
    const c = rf.screenToFlowPosition({ x: (wrap.current?.getBoundingClientRect().left ?? 0) + 80, y: (wrap.current?.getBoundingClientRect().top ?? 0) + 80 });
    update((d) => {
      d.zones.push({ id, kind, label });
      d.layout![id] = { x: c.x, y: c.y, w: 460, h: 280 };
    });
    setSel({ kind: "zone", id });
  }

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault();
    const t = e.dataTransfer.getData("application/opsdeck-node");
    const nt = catalog.data?.nodeTypes.find((x) => x.type === t);
    if (!nt) return;
    const p = rf.screenToFlowPosition({ x: e.clientX, y: e.clientY });
    addNode(nt, { x: p.x - NODE_W / 2, y: p.y - NODE_H / 2 });
  };

  // ---- deleting

  function removeSelected() {
    if (!sel) return;
    update((d) => {
      if (sel.kind === "node") {
        d.nodes = d.nodes.filter((n) => n.id !== sel.id);
        d.links = d.links.filter((l) => l.from !== sel.id && l.to !== sel.id);
      } else if (sel.kind === "link") {
        d.links = d.links.filter((l) => l.id !== sel.id);
      } else {
        const z = d.zones.find((x) => x.id === sel.id);
        // contents move to the parent zone, keeping their place on screen
        const za = absolute(sel.id);
        const pa = z?.parent ? absolute(z.parent) : { x: 0, y: 0 };
        for (const n of d.nodes.filter((n) => n.zone === sel.id)) {
          n.zone = z?.parent;
          const a = absolute(n.id);
          d.layout![n.id] = { x: a.x - pa.x, y: a.y - pa.y };
        }
        for (const c of d.zones.filter((c) => c.parent === sel.id)) {
          c.parent = z?.parent;
          const a = absolute(c.id);
          d.layout![c.id] = { ...d.layout![c.id], x: a.x - pa.x, y: a.y - pa.y };
        }
        void za;
        d.zones = d.zones.filter((x) => x.id !== sel.id);
      }
      delete d.layout![sel.id];
    });
    setSel(null);
  }

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (!active) return;
      const t = e.target as HTMLElement;
      const typing = t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable;
      if ((e.ctrlKey || e.metaKey) && e.key === "s") {
        e.preventDefault();
        void save();
      } else if ((e.key === "Delete" || e.key === "Backspace") && !typing && sel) {
        e.preventDefault();
        removeSelected();
      } else if (e.key === "Escape" && !typing) setSel(null);
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  });

  // ---- saving

  async function save(force = false) {
    if (!doc) return;
    setSaving(true);
    try {
      const r = await api.saveTopology(ws, doc, rev, force);
      setRev(r.rev);
      setSaved(JSON.stringify(doc));
      setAiView(r.text);
      setRemote(null);
      toast.success("Topology saved", "topology.yaml");
    } catch (e) {
      if (e instanceof ApiError && e.code === "conflict") setRemote("?");
      else toast.error("Save failed", errMsg(e));
    } finally {
      setSaving(false);
    }
  }

  async function importInventory() {
    if (!doc) return;
    try {
      const r = await api.topologyImport(ws, doc);
      if (r.added.length === 0) {
        toast.info("Nothing to import", "Every inventory resource is already on the diagram.");
        return;
      }
      setDoc(fillLayout(clean({ ...r.topology, layout: r.topology.layout ?? doc.layout ?? {} })));
      toast.success(`Added ${r.added.length} components from the inventory`, "Arrange them, add what runs on them, then save.");
    } catch (e) {
      toast.error("Import failed", errMsg(e));
    }
  }

  if (loadErr) return <div className="p-6 text-err">{loadErr}</div>;
  if (!doc || !catalog.data) return <Loading />;

  const selNode = sel?.kind === "node" ? doc.nodes.find((n) => n.id === sel.id) : undefined;
  const selZone = sel?.kind === "zone" ? doc.zones.find((z) => z.id === sel.id) : undefined;
  const selLink = sel?.kind === "link" ? doc.links.find((l) => l.id === sel.id) : undefined;

  return (
    <div className="flex flex-col h-full min-h-0">
      <div className="flex items-center gap-2 px-4 h-12 border-b border-line shrink-0">
        <Network className="size-4 text-muted" />
        <span className="font-semibold">Topology</span>
        <span className="text-[11px] text-faint">
          {doc.zones.length} zones · {doc.nodes.length} components · {doc.links.length} connections
        </span>
        {dirty && <Badge tone="warn">unsaved</Badge>}
        <div className="flex-1" />
        <Button
          size="sm"
          variant="ghost"
          icon={<LayoutGrid className="size-3.5" />}
          onClick={() => {
            setDoc((d) => (d ? { ...d, layout: autoLayout(d) } : d));
            setTimeout(() => rf.fitView({ duration: 300 }), 50);
          }}
          title="Arrange everything automatically (positions you set are reset)"
        >
          Auto-arrange
        </Button>
        <Button size="sm" variant="ghost" icon={<Import className="size-3.5" />} onClick={() => void importInventory()} title="Add hosts, VPNs, clusters, databases… of the workspace that are not on the diagram">
          Import inventory
        </Button>
        <div className="relative">
          <Button size="sm" variant="ghost" icon={<Download className="size-3.5" />} onClick={() => setExportMenu((v) => !v)}>
            Export
          </Button>
          <Popover open={exportMenu} onClose={() => setExportMenu(false)} className="right-0 mt-1 w-64">
            <a href={api.topologyExportUrl(ws, "md")} onClick={() => setExportMenu(false)}>
              <MenuItem icon={<FileText />} hint="for an AI" onClick={() => {}}>
                Markdown description
              </MenuItem>
            </a>
            <a href={api.topologyExportUrl(ws, "yaml")} onClick={() => setExportMenu(false)}>
              <MenuItem icon={<Download />} onClick={() => {}}>
                topology.yaml
              </MenuItem>
            </a>
          </Popover>
        </div>
        <Button size="sm" variant="primary" icon={<Save className="size-3.5" />} loading={saving} disabled={!dirty} onClick={() => void save()}>
          Save
        </Button>
      </div>
      {remote && (
        <div className="flex items-center gap-2 px-4 py-1.5 text-xs bg-warn/10 border-b border-warn/30">
          <Bot className="size-3.5 text-warn" />
          <span className="flex-1">topology.yaml was changed elsewhere (the agent or another tab) while you have unsaved changes.</span>
          <Button size="xs" variant="secondary" onClick={() => void load()}>
            Load theirs
          </Button>
          <Button size="xs" variant="danger" onClick={() => void save(true)}>
            Keep mine
          </Button>
        </div>
      )}
      <div className="flex flex-1 min-h-0">
        <Palette catalog={catalog.data} onAdd={(nt) => addNode(nt)} onZone={addZone} />
        <div className="flex-1 min-w-0 relative" ref={wrap} onDragOver={(e) => e.preventDefault()} onDrop={onDrop}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={NODE_TYPES}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeDragStop={onNodeDragStop}
            onNodeClick={(_, n) => setSel({ kind: n.type === "zone" ? "zone" : "node", id: n.id })}
            onEdgeClick={(_, e) => setSel({ kind: "link", id: e.id })}
            onPaneClick={() => setSel(null)}
            connectionMode={ConnectionMode.Loose}
            colorMode={theme === "light" ? "light" : "dark"}
            deleteKeyCode={null}
            fitView
            minZoom={0.15}
            proOptions={{ hideAttribution: true }}
          >
            <Background gap={20} size={1} />
            <Controls showInteractive={false} />
            <MiniMap pannable zoomable className="!bg-panel" nodeColor={(n) => (n.type === "zone" ? "transparent" : "#4b5563")} nodeStrokeColor={(n) => (n.type === "zone" ? String((n.data as { color?: string }).color ?? "#666") : "#9ca3af")} />
          </ReactFlow>
          {doc.nodes.length === 0 && doc.zones.length === 0 && (
            <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
              <div className="text-center max-w-md pointer-events-auto bg-panel/90 border border-line rounded-lg p-5">
                <Network className="size-8 text-faint mx-auto mb-2" />
                <div className="font-medium mb-1">Draw how your infrastructure fits together</div>
                <div className="text-xs text-muted mb-3">
                  Drag components from the left (routers, firewalls, CDN, WAF, load balancers, servers, clusters, databases…), group them in zones, connect them
                  by dragging between their handles, and note what runs on each. The agent reads this map — and can extend it, with your approval.
                </div>
                <Button size="sm" icon={<Import className="size-3.5" />} onClick={() => void importInventory()}>
                  Start from the inventory
                </Button>
              </div>
            </div>
          )}
        </div>
        <aside className="w-80 shrink-0 border-l border-line bg-panel overflow-y-auto">
          {selNode && (
            <NodeInspector
              key={selNode.id}
              n={selNode}
              doc={doc}
              catalog={catalog.data}
              resources={resources.map((r) => refOf(r.kind, r.name))}
              repos={resources.filter((r) => r.kind === "GitRemote").map((r) => `git/${r.name}`)}
              environments={(workspace?.environments ?? []).map((e) => e.name)}
              onChange={(fn) => update((d) => fn(d.nodes.find((n) => n.id === selNode.id)!))}
              onZone={(zone) =>
                update((d) => {
                  d.nodes.find((n) => n.id === selNode.id)!.zone = zone || undefined;
                  d.layout![selNode.id] = { x: 20, y: 44 };
                })
              }
              onDelete={removeSelected}
            />
          )}
          {selZone && (
            <ZoneInspector
              key={selZone.id}
              z={selZone}
              doc={doc}
              catalog={catalog.data}
              onChange={(fn) => update((d) => fn(d.zones.find((z) => z.id === selZone.id)!))}
              onDelete={async () => {
                if (await confirm({ title: `Remove zone ${selZone.label || selZone.id}?`, message: "Its components stay and move to the enclosing zone.", confirmLabel: "Remove" })) removeSelected();
              }}
            />
          )}
          {selLink && (
            <LinkInspector
              key={selLink.id}
              l={selLink}
              doc={doc}
              catalog={catalog.data}
              onChange={(fn) => update((d) => fn(d.links.find((l) => l.id === selLink.id)!))}
              onDelete={removeSelected}
            />
          )}
          {!sel && <AiView text={dirty ? "" : aiView} dirty={dirty} />}
        </aside>
      </div>
    </div>
  );
}

function isInside(doc: TopologyDoc, zone: string, ancestor: string): boolean {
  let cur = doc.zones.find((z) => z.id === zone)?.parent;
  for (let i = 0; cur && i < 20; i++) {
    if (cur === ancestor) return true;
    cur = doc.zones.find((z) => z.id === cur)?.parent;
  }
  return false;
}

function refOf(kind: string, name: string): string {
  if (kind === "Host") return `host/${name}`;
  if (["WireGuard", "Tailscale", "NetBird", "OpenVPN", "OpenConnect", "OpenFortiVPN", "SocksProxy"].includes(kind)) return `network/${name}`;
  if (kind === "KubernetesCluster") return `cluster/${name}`;
  if (kind === "GitRemote") return `git/${name}`;
  return `${kind.toLowerCase()}/${name}`;
}

// ---------------------------------------------------------------------------
// Canvas nodes

type CompData = { node: TopoNode; nt: TopoNodeType };

const ComponentNode = memo(function ComponentNode({ data, selected }: NodeProps) {
  const { node: n, nt } = data as CompData;
  const deps = n.deployments ?? [];
  const sub = [n.variant, n.vendor].filter(Boolean).join(" · ") || nt.label;
  return (
    <div
      className={cx("rounded-lg border bg-panel shadow-sm px-2.5 py-2 text-left", selected ? "border-accent ring-2 ring-accent/30" : "border-line-strong")}
      style={{ width: NODE_W, minHeight: NODE_H }}
    >
      {[Position.Top, Position.Right, Position.Bottom, Position.Left].map((p) => (
        <Handle key={p} id={p} type="source" position={p} className="!size-2 !bg-accent !border-0 opacity-60 hover:opacity-100" />
      ))}
      <div className="flex items-center gap-2">
        <span className="shrink-0 text-accent">
          <TopoIcon name={nt.icon} className="size-5" />
        </span>
        <div className="min-w-0">
          <div className="text-[12.5px] font-semibold truncate leading-tight">{n.label || n.id}</div>
          <div className="text-[10.5px] text-muted truncate leading-tight">{sub}</div>
        </div>
        {n.resource && <Lock className="size-3 text-faint shrink-0 ms-auto" aria-label="linked to the inventory" />}
      </div>
      {(n.tech ?? []).length > 0 && <div className="mt-1 text-[10px] text-faint truncate">{n.tech!.join(" · ")}</div>}
      {deps.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-1">
          {deps.slice(0, 3).map((d, i) => (
            <span key={i} className="rounded bg-panel-3 px-1 text-[10px] text-muted truncate max-w-full">
              {d.name}
            </span>
          ))}
          {deps.length > 3 && <span className="text-[10px] text-faint">+{deps.length - 3}</span>}
        </div>
      )}
    </div>
  );
});

const ZoneNode = memo(function ZoneNode({ data, selected }: NodeProps) {
  const { zone: z, color } = data as { zone: TopoZone; color: string };
  return (
    <>
      <NodeResizer isVisible={!!selected} minWidth={220} minHeight={140} color={color} />
      <div className="w-full h-full rounded-xl" style={{ border: `1.5px dashed ${color}`, background: `${color}10` }}>
        <div className="px-3 pt-1.5 flex items-baseline gap-2">
          <span className="text-[12px] font-semibold" style={{ color }}>
            {z.label || z.id}
          </span>
          <span className="text-[10.5px] text-faint">{[z.kind, z.cidr, z.provider].filter(Boolean).join(" · ")}</span>
        </div>
      </div>
    </>
  );
});

const NODE_TYPES = { component: ComponentNode, zone: ZoneNode };

// ---------------------------------------------------------------------------
// Palette

function Palette({ catalog, onAdd, onZone }: { catalog: TopoCatalog; onAdd: (nt: TopoNodeType) => void; onZone: (kind: string, label: string) => void }) {
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
    <aside className="w-56 shrink-0 border-r border-line bg-panel flex flex-col">
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

function Head({ icon, title, onDelete }: { icon: React.ReactNode; title: string; onDelete: () => void }) {
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

function Suggest({ options, onPick }: { options: string[]; onPick: (v: string) => void }) {
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

function NodeInspector({
  n,
  doc,
  catalog,
  resources,
  repos,
  environments,
  onChange,
  onZone,
  onDelete,
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
        <Connections doc={doc} id={n.id} />
      </div>
    </div>
  );
}

function Connections({ doc, id }: { doc: TopologyDoc; id: string }) {
  const links = doc.links.filter((l) => l.from === id || l.to === id);
  if (!links.length) return null;
  const name = (x: string) => doc.nodes.find((n) => n.id === x)?.label || x;
  return (
    <div className="border-t border-line pt-3">
      <div className="text-[12px] font-medium mb-1">Connections</div>
      {links.map((l) => (
        <div key={l.id} className="text-[11.5px] text-muted">
          {l.from === id ? `→ ${name(l.to)}` : `← ${name(l.from)}`} <span className="text-faint">{[l.kind, l.port].filter(Boolean).join(" :")}</span>
        </div>
      ))}
    </div>
  );
}

function ZoneInspector({ z, doc, catalog, onChange, onDelete }: { z: TopoZone; doc: TopologyDoc; catalog: TopoCatalog; onChange: (fn: (z: TopoZone) => void) => void; onDelete: () => void }) {
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

function LinkInspector({ l, doc, catalog, onChange, onDelete }: { l: TopoLink; doc: TopologyDoc; catalog: TopoCatalog; onChange: (fn: (l: TopoLink) => void) => void; onDelete: () => void }) {
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

function AiView({ text, dirty }: { text: string; dirty: boolean }) {
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
