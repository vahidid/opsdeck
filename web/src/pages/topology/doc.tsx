import {
 } from "@xyflow/react";
import type { FitViewOptions } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import type { TopoBox, TopoNode, TopoZone, TopologyDoc } from "../../api/types";

// The topology editor: zones (group boxes), components (nodes) and
// connections (edges) of the workspace's infrastructure, saved as
// topology.yaml — the same file the agent reads and edits.

export type Sel = { kind: "node" | "zone" | "link"; id: string } | null;


export const LINK_STYLE: Record<string, { stroke: string; dash?: string; width?: number }> = {
  network: { stroke: "var(--c-muted, #8b93a1)", width: 2 },
  tunnel: { stroke: "#a78bfa", dash: "6 4", width: 2 },
  routing: { stroke: "#f59e0b", dash: "2 4", width: 2 },
  app: { stroke: "#38bdf8", width: 1.6 },
  data: { stroke: "#34d399", width: 1.6 },
  mgmt: { stroke: "#94a3b8", dash: "4 4", width: 1.2 },
};

export const ZONE_COLOR: Record<string, string> = {
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

export const NODE_W = 190;
// fitting the map leaves room for the legend (top right) and the minimap
// (bottom right), so they do not cover components
export const FIT: FitViewOptions = { padding: { top: "64px", right: "170px", bottom: "32px", left: "40px" } };
export const NODE_H = 56;

export function depthOf(zones: TopoZone[], id: string): number {
  let d = 0;
  let cur = zones.find((z) => z.id === id)?.parent;
  while (cur && d < 20) {
    d++;
    cur = zones.find((z) => z.id === cur)?.parent;
  }
  return d;
}

export function slug(s: string): string {
  return (
    s
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 40) || "node"
  );
}

export function freeId(doc: TopologyDoc, base: string): string {
  const taken = new Set([...doc.nodes.map((n) => n.id), ...doc.zones.map((z) => z.id), ...doc.links.map((l) => l.id)]);
  let id = slug(base);
  for (let i = 2; taken.has(id); i++) id = `${slug(base)}-${i}`;
  return id;
}

// default link kind for a connection to a target type
export function defaultKind(target?: TopoNode): string {
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

export const GAP_X = 24;
export const GAP_Y = 30;
export const PAD = 20;
export const HEAD = 34;

// autoLayout places everything bottom-up: a zone holds its components in a
// grid (up to 3 columns) and its sub-zones in a row below; top level puts
// components without a zone in a row above the zones.
export function autoLayout(doc: TopologyDoc): Record<string, TopoBox> {
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
export function clean(t: Partial<TopologyDoc> | null | undefined): TopologyDoc {
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
export function fillLayout(doc: TopologyDoc): TopologyDoc {
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

export function isInside(doc: TopologyDoc, zone: string, ancestor: string): boolean {
  let cur = doc.zones.find((z) => z.id === zone)?.parent;
  for (let i = 0; cur && i < 20; i++) {
    if (cur === ancestor) return true;
    cur = doc.zones.find((z) => z.id === cur)?.parent;
  }
  return false;
}

export function refOf(kind: string, name: string): string {
  if (kind === "Host") return `host/${name}`;
  if (["WireGuard", "Tailscale", "NetBird", "OpenVPN", "OpenConnect", "OpenFortiVPN", "SocksProxy"].includes(kind)) return `network/${name}`;
  if (kind === "KubernetesCluster") return `cluster/${name}`;
  if (kind === "GitRemote") return `git/${name}`;
  return `${kind.toLowerCase()}/${name}`;
}
