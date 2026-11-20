import { memo } from "react";
import {
  BaseEdge,
  EdgeLabelRenderer,
  getSmoothStepPath,
  getStraightPath,
  Handle,
  NodeResizer,
  Position,
 } from "@xyflow/react";
import type { EdgeProps, NodeProps } from "@xyflow/react";
import { Lock } from "lucide-react";
import type { TopoNode, TopoNodeType, TopoZone, TopologyDoc } from "../../api/types";
import { cx } from "../../components/ui";
import { TopoIcon } from "../../components/topoIcons";
import { labelPoint, roundedPath, type Pt, type Rect } from "../../lib/orthoRoute";
import { NODE_W, NODE_H } from "./doc";

// ---------------------------------------------------------------------------
// Canvas nodes

type CompData = { node: TopoNode; nt: TopoNodeType; dim?: boolean };

const ComponentNode = memo(function ComponentNode({ data, selected }: NodeProps) {
  const { node: n, nt, dim } = data as CompData;
  const deps = n.deployments ?? [];
  const sub = [n.variant, n.vendor].filter(Boolean).join(" · ") || nt.label;
  return (
    <div
      className={cx(
        "group flex flex-col justify-center rounded-lg border bg-panel shadow-sm px-2.5 py-2 text-left transition-opacity",
        selected ? "border-accent ring-2 ring-accent/30" : "border-line-strong",
        dim && "opacity-30",
      )}
      style={{ width: NODE_W, minHeight: NODE_H }}
    >
      {[Position.Top, Position.Right, Position.Bottom, Position.Left].map((p) => (
        <Handle key={p} id={p} type="source" position={p} className="!size-2.5 !bg-accent !border-0 opacity-0 group-hover:opacity-80 hover:!opacity-100" />
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

export const NODE_TYPES = { component: ComponentNode, zone: ZoneNode };

export type ConnData = { label: string; color: string; dash?: string; width: number; dim: boolean; hot: boolean; points?: Pt[]; straight: boolean; showLabel: boolean };

// ConnEdge draws a connection as right-angled (or straight) lines with a
// readable label pill; dimmed when something else is in focus.
const ConnEdge = memo(function ConnEdge(p: EdgeProps) {
  const d = p.data as ConnData;
  let path: string;
  let lx: number;
  let ly: number;
  if (d.points && d.points.length >= 2) {
    path = roundedPath(d.points, 9);
    ({ x: lx, y: ly } = labelPoint(d.points));
  } else if (d.straight) {
    [path, lx, ly] = getStraightPath({ sourceX: p.sourceX, sourceY: p.sourceY, targetX: p.targetX, targetY: p.targetY });
  } else {
    [path, lx, ly] = getSmoothStepPath({ sourceX: p.sourceX, sourceY: p.sourceY, sourcePosition: p.sourcePosition, targetX: p.targetX, targetY: p.targetY, targetPosition: p.targetPosition, borderRadius: 10, offset: 22 });
  }
  // nothing in focus: slightly faded so components stand out; in focus: full
  const opacity = d.dim ? 0.1 : d.hot ? 1 : 0.6;
  return (
    <>
      <BaseEdge
        id={p.id}
        path={path}
        markerEnd={p.markerEnd}
        markerStart={p.markerStart}
        interactionWidth={18}
        style={{ stroke: d.color, strokeDasharray: d.dash, strokeWidth: p.selected ? d.width + 1.5 : d.width, opacity, transition: "opacity 120ms" }}
      />
      {(d.showLabel || p.selected) && !d.dim && (
        <EdgeLabelRenderer>
          <div
            className="nodrag nopan absolute rounded-full border bg-panel px-1.5 py-px text-[10px] leading-4 whitespace-nowrap shadow-sm pointer-events-auto"
            style={{ transform: `translate(-50%, -50%) translate(${lx}px, ${ly}px)`, borderColor: d.color, color: "var(--c-fg, #e5e7eb)", zIndex: 30 }}
          >
            {d.label}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  );
});

export const EDGE_TYPES = { conn: ConnEdge };

// absRects is every component's box on the canvas (layout positions are
// relative to the enclosing zone).
export function absRects(doc: TopologyDoc, sizes: Map<string, { w: number; h: number }>): Map<string, Rect> {
  const layout = doc.layout ?? {};
  const zoneOrigin = (id?: string, depth = 0): { x: number; y: number } => {
    if (!id || depth > 20) return { x: 0, y: 0 };
    const z = doc.zones.find((x) => x.id === id);
    const o = zoneOrigin(z?.parent, depth + 1);
    const b = layout[id] ?? { x: 0, y: 0 };
    return { x: o.x + b.x, y: o.y + b.y };
  };
  const out = new Map<string, Rect>();
  for (const n of doc.nodes) {
    const o = zoneOrigin(n.zone);
    const b = layout[n.id] ?? { x: 0, y: 0 };
    const sz = sizes.get(n.id) ?? { w: NODE_W, h: NODE_H };
    out.set(n.id, { x: o.x + b.x, y: o.y + b.y, w: sz.w, h: sz.h });
  }
  return out;
}
