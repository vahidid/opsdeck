import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Background,
  ConnectionMode,
  Panel,
  Controls,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  applyEdgeChanges,
  applyNodeChanges,
  useReactFlow,
 } from "@xyflow/react";
import type { Connection, Edge, EdgeChange, Node, NodeChange } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Bot, Download, FileText, Import, LayoutGrid, Network, Save } from "lucide-react";
import { api, errMsg, ApiError } from "../../api/client";
import type { TopoBox, TopoLinkKind, TopoNodeType, TopologyDoc } from "../../api/types";
import { useApp, useWs } from "../../context/app";
import { useTabActive } from "../../context/tabs";
import { Badge, Button, Loading } from "../../components/ui";
import { Popover, MenuItem, useConfirm, useToast } from "../../components/overlay";
import { useAsync, useInterval, useLocalStorage } from "../../hooks";
import { routeAll, type Rect, type Route, type RouteLink } from "../../lib/orthoRoute";
import { elkLayout } from "../../lib/topoLayout";
import { type Sel, LINK_STYLE, ZONE_COLOR, NODE_W, NODE_H, FIT, depthOf, freeId, defaultKind, autoLayout, clean, fillLayout } from "./doc";
import { isInside, refOf } from "./doc";
import type { ConnData } from "./canvas";
import { countByCategory, ZoneInspector } from "./panels";
import { NODE_TYPES, EDGE_TYPES, absRects } from "./canvas";
import { Legend, Palette, NodeInspector, LinkInspector, AiView } from "./panels";

export function Editor() {
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
  const [hover, setHover] = useState<string | null>(null);
  const [dragging, setDragging] = useState<string | null>(null);
  const [arranging, setArranging] = useState(false);
  const [labelMode, setLabelMode] = useLocalStorage<"auto" | "all" | "none">("opsdeck.topology.labels", "auto");
  const [lineMode, setLineMode] = useLocalStorage<"routed" | "straight">("opsdeck.topology.lines2", "routed");
  const [hiddenCats, setHiddenCats] = useLocalStorage<string[]>("opsdeck.topology.hidden", []);
  // view only (this browser): hide the lines; a hovered or selected
  // component still shows its own connections
  const [showLines, setShowLines] = useLocalStorage("opsdeck.topology.showLines", true);
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
      let d = fillLayout(clean(r.topology));
      // a map nobody arranged yet (e.g. built by the agent): lay it out by its connections
      if (Object.keys(r.topology.layout ?? {}).length === 0 && d.nodes.length > 0) {
        try {
          d = { ...d, layout: await elkLayout(d, () => ({ w: NODE_W, h: NODE_H })) };
        } catch {
          /* keep the grid */
        }
      }
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

  // What is highlighted: the hovered or selected component and its
  // connections, or the selected connection and its two ends.
  const focus = useMemo(() => {
    const none = { nodes: null as Set<string> | null, links: null as Set<string> | null };
    if (!doc) return none;
    const id = hover ?? (sel?.kind === "node" ? sel.id : null);
    if (id) {
      const links = doc.links.filter((l) => l.from === id || l.to === id);
      return { nodes: new Set([id, ...links.flatMap((l) => [l.from, l.to])]), links: new Set(links.map((l) => l.id)) };
    }
    if (sel?.kind === "link") {
      const l = doc.links.find((x) => x.id === sel.id);
      if (l) return { nodes: new Set([l.from, l.to]), links: new Set([l.id]) };
    }
    return none;
  }, [doc, hover, sel]);

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
          data: { node: n, nt: typeOf(n.type), dim: focus.nodes !== null && !focus.nodes.has(n.id) },
          selected: sel?.kind === "node" && sel.id === n.id,
          zIndex: 10,
        });
      }
      return out;
    });
  }, [doc, sel, typeOf, linkKindOf, focus]);

  // Real sizes of the components on the canvas (they grow with their details).
  const sizesKey = nodes
    .filter((n) => n.type === "component")
    .map((n) => `${n.id}:${Math.round(n.measured?.width ?? NODE_W)}x${Math.round(n.measured?.height ?? NODE_H)}`)
    .join(",");
  const sizes = useMemo(() => {
    const m = new Map<string, { w: number; h: number }>();
    for (const part of sizesKey.split(",")) {
      const [id, wh] = part.split(":");
      if (!id || !wh) continue;
      const [w, h] = wh.split("x").map(Number);
      m.set(id, { w, h });
    }
    return m;
  }, [sizesKey]);

  // Routes: every visible connection routed around the components. Only
  // the geometry counts, so editing a name or a port does not route again.
  const geometry = useMemo(() => {
    if (!doc) return "";
    const rects = absRects(doc, sizes);
    const visible = doc.links.filter((l) => !hiddenCats.includes(linkKindOf(l.kind).category)).map((l) => ({ id: l.id, from: l.from, to: l.to }));
    return JSON.stringify({ rects: [...rects], visible });
  }, [doc, sizes, hiddenCats, linkKindOf]);
  const routes = useMemo(() => {
    if (!geometry) return new Map<string, Route>();
    const g = JSON.parse(geometry) as { rects: [string, Rect][]; visible: RouteLink[] };
    return routeAll(new Map(g.rects), g.visible);
  }, [geometry]);

  useEffect(() => {
    if (!doc) return;
    // connections of the component (or zone) being dragged follow it with a
    // simple path until it is dropped and they are routed again
    const moving = new Set<string>();
    if (dragging) {
      moving.add(dragging);
      for (const n of doc.nodes) if (n.zone && (n.zone === dragging || isInside(doc, n.zone, dragging))) moving.add(n.id);
    }
    setEdges(
      doc.links.map((l) => {
        const lk = linkKindOf(l.kind);
        const st = LINK_STYLE[lk.category] ?? LINK_STYLE.app;
        const route = routes.get(l.id);
        const hot = focus.links !== null && focus.links.has(l.id);
        const dim = focus.links !== null && !hot;
        const label = [l.kind || lk.label, l.port ? `:${l.port}` : "", l.encrypted ? " 🔒" : ""].join("") + (l.label ? ` · ${l.label}` : "");
        const marker = { type: MarkerType.ArrowClosed, color: st.stroke, width: 14, height: 14 };
        const live = moving.has(l.from) || moving.has(l.to);
        return {
          id: l.id,
          type: "conn",
          source: l.from,
          target: l.to,
          sourceHandle: route?.sourceSide ?? Position.Bottom,
          targetHandle: route?.targetSide ?? Position.Top,
          hidden: hiddenCats.includes(lk.category) || (!showLines && !hot),
          selected: sel?.kind === "link" && sel.id === l.id,
          data: {
            label,
            color: st.stroke,
            dash: st.dash,
            width: (st.width ?? 1.5) + (hot ? 1 : 0),
            dim,
            hot,
            points: live || lineMode === "straight" ? undefined : route?.points,
            straight: lineMode === "straight",
            showLabel: labelMode === "all" || (labelMode === "auto" && hot),
          } satisfies ConnData,
          markerEnd: marker,
          markerStart: l.bidirectional ? marker : undefined,
          zIndex: hot ? 20 : 5,
        };
      }),
    );
  }, [doc, sel, linkKindOf, focus, labelMode, lineMode, hiddenCats, showLines, routes, dragging]);

  // fit the view once the first diagram is on screen
  const fitted = useRef(false);
  useEffect(() => {
    if (!fitted.current && nodes.length > 0) {
      fitted.current = true;
      setTimeout(() => rf.fitView(FIT), 60);
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
          loading={arranging}
          onClick={async () => {
            if (!doc) return;
            setArranging(true);
            try {
              const layout = await elkLayout(doc, (id) => sizes.get(id) ?? { w: NODE_W, h: NODE_H });
              setDoc((d) => (d ? { ...d, layout } : d));
            } catch (e) {
              toast.error("Could not arrange", errMsg(e));
              setDoc((d) => (d ? { ...d, layout: autoLayout(d) } : d));
            } finally {
              setArranging(false);
              setTimeout(() => rf.fitView({ ...FIT, duration: 300 }), 80);
            }
          }}
          title="Arrange everything by its connections (positions you set are reset)"
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
            edgeTypes={EDGE_TYPES}
            onNodeMouseEnter={(_, n) => n.type === "component" && setHover(n.id)}
            onNodeMouseLeave={() => setHover(null)}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeDragStart={(_, n) => setDragging(n.id)}
            onNodeDragStop={(e, n) => {
              setDragging(null);
              onNodeDragStop(e, n);
            }}
            onNodeClick={(_, n) => setSel({ kind: n.type === "zone" ? "zone" : "node", id: n.id })}
            onEdgeClick={(_, e) => setSel({ kind: "link", id: e.id })}
            onPaneClick={() => setSel(null)}
            connectionMode={ConnectionMode.Loose}
            colorMode={theme === "light" ? "light" : "dark"}
            deleteKeyCode={null}
            fitView
            fitViewOptions={FIT}
            minZoom={0.15}
            proOptions={{ hideAttribution: true }}
          >
            <Background gap={20} size={1} />
            <Controls showInteractive={false} />
            <Panel position="top-right">
              <Legend
                hidden={hiddenCats}
                onToggle={(c) => setHiddenCats(hiddenCats.includes(c) ? hiddenCats.filter((x) => x !== c) : [...hiddenCats, c])}
                counts={countByCategory(doc, linkKindOf)}
                labelMode={labelMode}
                setLabelMode={setLabelMode}
                lineMode={lineMode}
                setLineMode={setLineMode}
                showLines={showLines}
                setShowLines={setShowLines}
              />
            </Panel>
            <MiniMap pannable zoomable className="!bg-panel" style={{ width: 150, height: 100 }} nodeColor={(n) => (n.type === "zone" ? "transparent" : "#4b5563")} nodeStrokeColor={(n) => (n.type === "zone" ? String((n.data as { color?: string }).color ?? "#666") : "#9ca3af")} />
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
              onSelectLink={(id) => setSel({ kind: "link", id })}
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

