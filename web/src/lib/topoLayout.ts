import type { TopoBox, TopologyDoc } from "../api/types";

// elkLayout arranges the topology with ELK's layered algorithm (Eclipse
// Layout Kernel, loaded on demand): zones stay nested, connections flow top
// to bottom (internet → edge → apps → data), crossings are minimised.
// Returns positions relative to the enclosing zone, and zone sizes.
export async function elkLayout(doc: TopologyDoc, size: (id: string) => { w: number; h: number }): Promise<Record<string, TopoBox>> {
  const { default: ELK } = await import("elkjs/lib/elk.bundled.js");
  const elk = new ELK();
  type ElkNode = { id: string; width?: number; height?: number; x?: number; y?: number; children?: ElkNode[]; layoutOptions?: Record<string, string> };
  const build = (parent: string | undefined): ElkNode[] => {
    const out: ElkNode[] = [];
    for (const z of doc.zones.filter((z) => (z.parent || undefined) === parent)) {
      const children = build(z.id);
      out.push({
        id: z.id,
        children,
        ...(children.length ? {} : { width: 240, height: 120 }),
        layoutOptions: { "elk.padding": "[top=52,left=24,bottom=24,right=24]" },
      });
    }
    for (const n of doc.nodes.filter((n) => (n.zone || undefined) === parent)) {
      const s = size(n.id);
      out.push({ id: n.id, width: s.w, height: s.h });
    }
    return out;
  };
  const ids = new Set(doc.nodes.map((n) => n.id));
  const graph = {
    id: "root",
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": "DOWN",
      "elk.hierarchyHandling": "INCLUDE_CHILDREN",
      "elk.edgeRouting": "ORTHOGONAL",
      "elk.layered.spacing.nodeNodeBetweenLayers": "80",
      "elk.layered.spacing.edgeNodeBetweenLayers": "26",
      "elk.layered.spacing.edgeEdgeBetweenLayers": "14",
      "elk.spacing.nodeNode": "56",
      "elk.spacing.edgeNode": "30",
      "elk.spacing.edgeEdge": "14",
      "elk.spacing.componentComponent": "70",
      "elk.layered.crossingMinimization.strategy": "LAYER_SWEEP",
      "elk.layered.nodePlacement.strategy": "NETWORK_SIMPLEX",
      "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
      "elk.aspectRatio": "1.6",
    },
    children: build(undefined),
    edges: doc.links.filter((l) => ids.has(l.from) && ids.has(l.to) && l.from !== l.to).map((l) => ({ id: l.id, sources: [l.from], targets: [l.to] })),
  };
  const res = (await elk.layout(graph)) as ElkNode;
  const out: Record<string, TopoBox> = {};
  const zoneIds = new Set(doc.zones.map((z) => z.id));
  const walk = (n: ElkNode) => {
    for (const c of n.children ?? []) {
      const x = Math.round(c.x ?? 0);
      const y = Math.round(c.y ?? 0);
      out[c.id] = zoneIds.has(c.id) ? { x, y, w: Math.round(c.width ?? 240), h: Math.round(c.height ?? 120) } : { x, y };
      walk(c);
    }
  };
  walk(res);
  return out;
}
