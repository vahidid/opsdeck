// Orthogonal connector routing for the topology map: every connection is
// routed around the components (not through them) with as few bends as
// possible, connections on the same side of a component get their own
// port, and parallel lines that would overlap are pushed apart.
//
// The approach is the usual one for diagram editors (cf. libavoid): a sparse
// grid of candidate lines along the obstacles' edges and the channels between
// them, A* over it with a penalty per bend and per already-used segment, then
// a nudging pass for collinear overlaps.

export type Side = "top" | "right" | "bottom" | "left";
export type Pt = { x: number; y: number };
export type Rect = { x: number; y: number; w: number; h: number };
export type RouteLink = { id: string; from: string; to: string };
export type Route = { points: Pt[]; sourceSide: Side; targetSide: Side };

const PAD = 10; // clearance around components
const STUB = 18; // straight segment leaving a port
const BEND = 60; // cost of a bend, in pixels of length
const USED = 25; // cost of reusing a grid segment another route already uses
const SEP = 7; // distance between nudged parallel lines
const CROSS = 60; // cost of crossing another route

const DIR: Record<Side, Pt> = { top: { x: 0, y: -1 }, right: { x: 1, y: 0 }, bottom: { x: 0, y: 1 }, left: { x: -1, y: 0 } };

function center(r: Rect): Pt {
  return { x: r.x + r.w / 2, y: r.y + r.h / 2 };
}

const OPP: Record<Side, Side> = { top: "bottom", bottom: "top", left: "right", right: "left" };

// room tells whether a connection can leave r through side (no other
// component right in front of it).
function room(r: Rect, side: Side, others: Rect[]): boolean {
  const depth = STUB + PAD;
  const band =
    side === "top"
      ? { l: r.x, r: r.x + r.w, t: r.y - depth, b: r.y }
      : side === "bottom"
        ? { l: r.x, r: r.x + r.w, t: r.y + r.h, b: r.y + r.h + depth }
        : side === "left"
          ? { l: r.x - depth, r: r.x, t: r.y, b: r.y + r.h }
          : { l: r.x + r.w, r: r.x + r.w + depth, t: r.y, b: r.y + r.h };
  return !others.some((o) => o !== r && o.x < band.r && o.x + o.w > band.l && o.y < band.b && o.y + o.h > band.t);
}

// sidesFor picks the sides of a connection: the facing ones, unless a side
// has no room, then the best alternative towards the other end.
function sidesFor(a: Rect, b: Rect, all: Rect[]): [Side, Side] {
  const [fa, fb] = facing(a, b);
  const ca = center(a);
  const cb = center(b);
  const prefs = (from: Pt, to: Pt, first: Side): Side[] => {
    const h: Side = to.x >= from.x ? "right" : "left";
    const v: Side = to.y >= from.y ? "bottom" : "top";
    const order = [first, first === h || first === OPP[h] ? v : h, OPP[first === h || first === OPP[h] ? v : h], OPP[first]];
    return [...new Set(order)];
  };
  const sa = prefs(ca, cb, fa).find((s) => room(a, s, all)) ?? fa;
  const sb = prefs(cb, ca, fb).find((s) => room(b, s, all)) ?? fb;
  return [sa, sb];
}

// facing sides of two rectangles
function facing(a: Rect, b: Rect): [Side, Side] {
  const ca = center(a);
  const cb = center(b);
  const overlapX = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x);
  const overlapY = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y);
  const vertical = overlapX > 0 ? true : overlapY > 0 ? false : Math.abs(cb.y - ca.y) * 1.3 >= Math.abs(cb.x - ca.x);
  if (vertical) return cb.y >= ca.y ? ["bottom", "top"] : ["top", "bottom"];
  return cb.x >= ca.x ? ["right", "left"] : ["left", "right"];
}

// a min-heap on f
class Heap {
  private a: { f: number; s: number }[] = [];
  get size() {
    return this.a.length;
  }
  push(f: number, s: number) {
    const a = this.a;
    a.push({ f, s });
    let i = a.length - 1;
    while (i > 0) {
      const p = (i - 1) >> 1;
      if (a[p].f <= a[i].f) break;
      [a[p], a[i]] = [a[i], a[p]];
      i = p;
    }
  }
  pop(): number {
    const a = this.a;
    const top = a[0].s;
    const last = a.pop()!;
    if (a.length) {
      a[0] = last;
      let i = 0;
      for (;;) {
        const l = 2 * i + 1;
        const r = l + 1;
        let m = i;
        if (l < a.length && a[l].f < a[m].f) m = l;
        if (r < a.length && a[r].f < a[m].f) m = r;
        if (m === i) break;
        [a[m], a[i]] = [a[i], a[m]];
        i = m;
      }
    }
    return top;
  }
}

function uniqSorted(v: number[]): number[] {
  const s = [...new Set(v.map((x) => Math.round(x * 2) / 2))].sort((a, b) => a - b);
  // channels: the middle of every wide gap
  const out: number[] = [];
  for (let i = 0; i < s.length; i++) {
    out.push(s[i]);
    if (i + 1 < s.length && s[i + 1] - s[i] > 2 * PAD + 8) out.push((s[i] + s[i + 1]) / 2);
  }
  return out;
}

function simplify(pts: Pt[]): Pt[] {
  const out: Pt[] = [];
  for (const p of pts) {
    if (out.length && Math.abs(out[out.length - 1].x - p.x) < 0.5 && Math.abs(out[out.length - 1].y - p.y) < 0.5) continue;
    out.push({ ...p });
    while (out.length >= 3) {
      const [a, b, c] = out.slice(-3);
      const collinear = (Math.abs(a.x - b.x) < 0.5 && Math.abs(b.x - c.x) < 0.5) || (Math.abs(a.y - b.y) < 0.5 && Math.abs(b.y - c.y) < 0.5);
      if (!collinear) break;
      out.splice(out.length - 2, 1);
    }
  }
  return out;
}

export function routeAll(rects: Map<string, Rect>, links: RouteLink[]): Map<string, Route> {
  const routes = new Map<string, Route>();
  const valid = links.filter((l) => rects.has(l.from) && rects.has(l.to) && l.from !== l.to);
  if (!valid.length) return routes;

  const allRects = [...rects.values()];
  // 1. sides and ports: connections on one side are spread along it, ordered
  //    by where their other end is, so they do not cross at the component
  const sideOf = new Map<string, [Side, Side]>();
  const bySide = new Map<string, { link: string; end: 0 | 1; other: Pt }[]>();
  for (const l of valid) {
    const a = rects.get(l.from)!;
    const b = rects.get(l.to)!;
    const s = sidesFor(a, b, allRects);
    sideOf.set(l.id, s);
    for (const [end, node, side, other] of [
      [0, l.from, s[0], center(b)],
      [1, l.to, s[1], center(a)],
    ] as const) {
      const k = node + "|" + side;
      if (!bySide.has(k)) bySide.set(k, []);
      bySide.get(k)!.push({ link: l.id, end, other });
    }
  }
  const port = new Map<string, Pt>(); // linkId|end -> point on the border
  for (const [k, list] of bySide) {
    const [node, side] = k.split("|") as [string, Side];
    const r = rects.get(node)!;
    const horizontalSide = side === "top" || side === "bottom";
    list.sort((p, q) => (horizontalSide ? p.other.x - q.other.x : p.other.y - q.other.y));
    list.forEach((e, i) => {
      const t = (i + 1) / (list.length + 1);
      // on the grid's half-pixel resolution, so the lines stay exactly straight
      const snap = (v: number) => Math.round(v * 2) / 2;
      const p = horizontalSide
        ? { x: snap(r.x + r.w * t), y: side === "top" ? r.y : r.y + r.h }
        : { x: side === "left" ? r.x : r.x + r.w, y: snap(r.y + r.h * t) };
      port.set(e.link + "|" + e.end, p);
    });
  }

  // 2. the grid
  const obstacles = [...rects.values()].map((r) => ({ l: r.x - PAD, t: r.y - PAD, r: r.x + r.w + PAD, b: r.y + r.h + PAD }));
  const xsRaw: number[] = [];
  const ysRaw: number[] = [];
  for (const o of obstacles) {
    xsRaw.push(o.l, o.r);
    ysRaw.push(o.t, o.b);
  }
  const insideAny = (x: number, y: number) => obstacles.some((o) => x > o.l + 0.5 && x < o.r - 0.5 && y > o.t + 0.5 && y < o.b - 0.5);
  const stubs = new Map<string, Pt>();
  for (const l of valid) {
    const s = sideOf.get(l.id)!;
    for (const end of [0, 1] as const) {
      const p = port.get(l.id + "|" + end)!;
      const d = DIR[s[end]];
      // as long as possible without entering a neighbour's clearance
      let len = STUB;
      for (const tryLen of [STUB, 15, 13, PAD + 2]) {
        len = tryLen;
        if (!insideAny(p.x + d.x * tryLen, p.y + d.y * tryLen)) break;
      }
      const q = { x: p.x + d.x * len, y: p.y + d.y * len };
      stubs.set(l.id + "|" + end, q);
      xsRaw.push(q.x);
      ysRaw.push(q.y);
    }
  }
  const xs = uniqSorted(xsRaw);
  const ys = uniqSorted(ysRaw);
  const W = xs.length;
  const H = ys.length;
  const inside = insideAny;
  const blocked = new Uint8Array(W * H);
  for (let i = 0; i < W; i++) for (let j = 0; j < H; j++) blocked[j * W + i] = inside(xs[i], ys[j]) ? 1 : 0;
  // open[dir][cell]: can move from the cell to its neighbour in dir (0 right, 1 down, 2 left, 3 up)
  // cells a route may start or end in even when they lie in a neighbour's
  // clearance (components packed tightly)
  let exempt = new Set<number>();
  const canMove = (i: number, j: number, d: number): boolean => {
    const ni = i + (d === 0 ? 1 : d === 2 ? -1 : 0);
    const nj = j + (d === 1 ? 1 : d === 3 ? -1 : 0);
    if (ni < 0 || nj < 0 || ni >= W || nj >= H) return false;
    const a = j * W + i;
    const b = nj * W + ni;
    if (exempt.has(a) || exempt.has(b)) return !(blocked[a] && blocked[b] && !(exempt.has(a) && exempt.has(b)));
    if (blocked[b] || blocked[a]) return false;
    return !inside((xs[i] + xs[ni]) / 2, (ys[j] + ys[nj]) / 2);
  };
  const used = new Map<number, number>(); // segment key -> uses
  const through = new Uint8Array(W * H); // routes passing a cell: 1 horizontally, 2 vertically
  const segKey = (i: number, j: number, d: number) => {
    // normalise to the right/down direction from the lower cell
    if (d === 2) return ((j * W + (i - 1)) << 1) | 0;
    if (d === 3) return (((j - 1) * W + i) << 1) | 1;
    return ((j * W + i) << 1) | (d === 1 ? 1 : 0);
  };
  const xi = new Map(xs.map((x, i) => [x, i]));
  const yi = new Map(ys.map((y, i) => [y, i]));
  const cellOf = (p: Pt): [number, number] | null => {
    const i = xi.get(Math.round(p.x * 2) / 2);
    const j = yi.get(Math.round(p.y * 2) / 2);
    return i === undefined || j === undefined ? null : [i, j];
  };
  const sideDir: Record<Side, number> = { right: 0, bottom: 1, left: 2, top: 3 };

  // 3. A* per connection, shortest first so long ones go around them
  const order = [...valid].sort((a, b) => {
    const da = Math.abs(center(rects.get(a.from)!).x - center(rects.get(a.to)!).x) + Math.abs(center(rects.get(a.from)!).y - center(rects.get(a.to)!).y);
    const db = Math.abs(center(rects.get(b.from)!).x - center(rects.get(b.to)!).x) + Math.abs(center(rects.get(b.from)!).y - center(rects.get(b.to)!).y);
    return da - db;
  });
  const N = W * H * 4;
  const g = new Float64Array(N);
  const prev = new Int32Array(N);
  const closed = new Uint8Array(N);
  for (const l of order) {
    const s = sideOf.get(l.id)!;
    const p0 = port.get(l.id + "|0")!;
    const p1 = port.get(l.id + "|1")!;
    const a = stubs.get(l.id + "|0")!;
    const b = stubs.get(l.id + "|1")!;
    const ca = cellOf(a);
    const cb = cellOf(b);
    let pts: Pt[] | null = null;
    if (ca && cb) {
      exempt = new Set([ca[1] * W + ca[0], cb[1] * W + cb[0]]);
      g.fill(Infinity);
      closed.fill(0);
      prev.fill(-1);
      const heap = new Heap();
      const startDir = sideDir[s[0]];
      const endDir = (sideDir[s[1]] + 2) % 4; // arriving into the target
      const start = ((ca[1] * W + ca[0]) << 2) | startDir;
      g[start] = 0;
      const h = (i: number, j: number) => Math.abs(xs[i] - xs[cb[0]]) + Math.abs(ys[j] - ys[cb[1]]);
      heap.push(h(ca[0], ca[1]), start);
      let goal = -1;
      while (heap.size) {
        const st = heap.pop();
        if (closed[st]) continue;
        closed[st] = 1;
        const cell = st >> 2;
        const dir = st & 3;
        const i = cell % W;
        const j = (cell / W) | 0;
        if (i === cb[0] && j === cb[1]) {
          goal = st;
          break;
        }
        for (let d = 0; d < 4; d++) {
          if (d === (dir + 2) % 4 || !canMove(i, j, d)) continue;
          const ni = i + (d === 0 ? 1 : d === 2 ? -1 : 0);
          const nj = j + (d === 1 ? 1 : d === 3 ? -1 : 0);
          let cost = Math.abs(xs[ni] - xs[i]) + Math.abs(ys[nj] - ys[j]) + (d !== dir ? BEND : 0) + (used.get(segKey(i, j, d)) ?? 0) * USED;
          if (ni === cb[0] && nj === cb[1] && d !== endDir) cost += BEND;
          if (through[nj * W + ni] & (d % 2 === 0 ? 2 : 1)) cost += CROSS;
          const ns = ((nj * W + ni) << 2) | d;
          const ng = g[st] + cost;
          if (ng < g[ns]) {
            g[ns] = ng;
            prev[ns] = st;
            heap.push(ng + h(ni, nj), ns);
          }
        }
      }
      if (goal >= 0) {
        const cells: number[] = [];
        for (let st = goal; st >= 0; st = prev[st]) cells.push(st);
        cells.reverse();
        for (let k = 1; k < cells.length; k++) {
          const c0 = cells[k - 1] >> 2;
          const d = cells[k] & 3;
          used.set(segKey(c0 % W, (c0 / W) | 0, d), (used.get(segKey(c0 % W, (c0 / W) | 0, d)) ?? 0) + 1);
          const bit = d % 2 === 0 ? 1 : 2;
          through[c0] |= bit;
          through[cells[k] >> 2] |= bit;
        }
        pts = [p0, ...cells.map((st) => ({ x: xs[(st >> 2) % W], y: ys[((st >> 2) / W) | 0] })), p1];
      }
    }
    if (!pts) {
      // no free path (overlapping components): a plain elbow
      pts = [p0, a, { x: b.x, y: a.y }, b, p1];
    }
    routes.set(l.id, { points: simplify(pts), sourceSide: s[0], targetSide: s[1] });
  }

  nudge(routes, rects, new Map(valid.map((l) => [l.id, l])));
  return routes;
}

// nudge spreads collinear overlapping segments of different routes apart.
// The first and last segment (at the ports) stay put.
function nudge(routes: Map<string, Route>, rects: Map<string, Rect>, links: Map<string, RouteLink>) {
  const hits = (rid: string, a: Pt, b: Pt) => {
    const l = links.get(rid)!;
    const lo = { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y) };
    const hi = { x: Math.max(a.x, b.x), y: Math.max(a.y, b.y) };
    for (const [id, r] of rects) {
      if (id === l.from || id === l.to) continue;
      if (hi.x > r.x + 1 && lo.x < r.x + r.w - 1 && hi.y > r.y + 1 && lo.y < r.y + r.h - 1) return true;
    }
    return false;
  };
  for (const axis of ["x", "y"] as const) {
    const other = axis === "x" ? "y" : "x";
    // segments that run along `other` at a fixed `axis` coordinate
    const groups = new Map<number, { route: string; k: number; lo: number; hi: number }[]>();
    for (const [id, r] of routes) {
      const p = r.points;
      for (let k = 1; k < p.length - 2; k++) {
        if (Math.abs(p[k][axis] - p[k + 1][axis]) > 0.5) continue;
        const key = Math.round(p[k][axis]);
        const lo = Math.min(p[k][other], p[k + 1][other]);
        const hi = Math.max(p[k][other], p[k + 1][other]);
        if (!groups.has(key)) groups.set(key, []);
        groups.get(key)!.push({ route: id, k, lo, hi });
      }
    }
    for (const list of groups.values()) {
      if (list.length < 2) continue;
      list.sort((a, b) => a.lo - b.lo);
      // clusters of overlapping segments
      let cluster: typeof list = [];
      let end = -Infinity;
      const flush = () => {
        const routesIn = [...new Set(cluster.map((c) => c.route))];
        if (routesIn.length > 1) {
          routesIn.forEach((rid, n) => {
            const off = (n - (routesIn.length - 1) / 2) * SEP;
            for (const c of cluster.filter((c) => c.route === rid)) {
              const p = routes.get(rid)!.points;
              const a = { ...p[c.k], [axis]: p[c.k][axis] + off };
              const b = { ...p[c.k + 1], [axis]: p[c.k + 1][axis] + off };
              // the neighbouring segments stretch along; none may enter a component
              const before = c.k > 0 ? [p[c.k - 1], a] : null;
              const after = c.k + 2 < p.length ? [b, p[c.k + 2]] : null;
              if (hits(rid, a, b) || (before && hits(rid, before[0], before[1])) || (after && hits(rid, after[0], after[1]))) continue;
              p[c.k] = a;
              p[c.k + 1] = b;
            }
          });
        }
        cluster = [];
      };
      for (const s of list) {
        if (s.lo > end) {
          flush();
          end = -Infinity;
        }
        cluster.push(s);
        end = Math.max(end, s.hi);
      }
      flush();
    }
  }
}

// roundedPath draws a polyline with rounded corners.
export function roundedPath(pts: Pt[], radius = 8): string {
  if (pts.length < 2) return "";
  let d = `M ${pts[0].x} ${pts[0].y}`;
  for (let i = 1; i < pts.length - 1; i++) {
    const a = pts[i - 1];
    const b = pts[i];
    const c = pts[i + 1];
    const la = Math.hypot(a.x - b.x, a.y - b.y);
    const lc = Math.hypot(c.x - b.x, c.y - b.y);
    if (la < 0.01 || lc < 0.01) continue;
    const r = Math.min(radius, la / 2, lc / 2);
    const p1 = { x: b.x + ((a.x - b.x) / la) * r, y: b.y + ((a.y - b.y) / la) * r };
    const p2 = { x: b.x + ((c.x - b.x) / lc) * r, y: b.y + ((c.y - b.y) / lc) * r };
    d += ` L ${p1.x} ${p1.y} Q ${b.x} ${b.y} ${p2.x} ${p2.y}`;
  }
  const last = pts[pts.length - 1];
  return d + ` L ${last.x} ${last.y}`;
}

// labelPoint is the middle of the longest segment (spreads labels out).
export function labelPoint(pts: Pt[]): Pt {
  let best = 0;
  let at = { x: pts[0].x, y: pts[0].y };
  for (let i = 0; i + 1 < pts.length; i++) {
    const len = Math.abs(pts[i + 1].x - pts[i].x) + Math.abs(pts[i + 1].y - pts[i].y);
    if (len > best) {
      best = len;
      at = { x: (pts[i].x + pts[i + 1].x) / 2, y: (pts[i].y + pts[i + 1].y) / 2 };
    }
  }
  return at;
}
