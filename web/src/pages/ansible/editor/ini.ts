import type { Document } from "yaml";
import { newDocument } from "./model";

// INI inventories, read into the same shape as YAML inventories
// (all → hosts, children → group → hosts/vars/children) so one view edits
// both, and written back as INI.

type G = { hosts: Record<string, Record<string, string> | null>; vars: Record<string, string>; children: Record<string, Record<string, never>> };

function splitVars(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  const re = /([^\s=]+)=("[^"]*"|'[^']*'|\S+)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s))) out[m[1]] = m[2].replace(/^(["'])(.*)\1$/, "$2");
  return out;
}

export function iniToDoc(text: string): Document {
  const groups = new Map<string, G>();
  const group = (n: string) => {
    if (!groups.has(n)) groups.set(n, { hosts: {}, vars: {}, children: {} });
    return groups.get(n)!;
  };
  const ungrouped: Record<string, Record<string, string> | null> = {};
  let sec = "";
  let kind = "hosts";
  for (const raw of text.split("\n")) {
    const line = raw.replace(/\s[;#].*$/, "").trim();
    if (!line || line.startsWith("#") || line.startsWith(";")) continue;
    const h = /^\[([^\]:]+)(?::(vars|children))?\]$/.exec(line);
    if (h) {
      sec = h[1];
      kind = h[2] ?? "hosts";
      group(sec);
      continue;
    }
    if (!sec) {
      const [name, ...rest] = line.split(/\s+/);
      const v = splitVars(rest.join(" "));
      ungrouped[name] = Object.keys(v).length ? v : null;
    } else if (kind === "vars") {
      const i = line.indexOf("=");
      if (i > 0) group(sec).vars[line.slice(0, i).trim()] = line.slice(i + 1).trim().replace(/^(["'])(.*)\1$/, "$2");
    } else if (kind === "children") {
      group(sec).children[line] = {};
      group(line);
    } else {
      const [name, ...rest] = line.split(/\s+/);
      const v = splitVars(rest.join(" "));
      group(sec).hosts[name] = Object.keys(v).length ? v : null;
    }
  }
  const all: Record<string, unknown> = {};
  const allG = groups.get("all");
  const hosts = { ...ungrouped, ...(allG?.hosts ?? {}) };
  if (Object.keys(hosts).length) all.hosts = Object.fromEntries(Object.entries(hosts).map(([k, v]) => [k, v ?? {}]));
  if (allG && Object.keys(allG.vars).length) all.vars = allG.vars;
  const children: Record<string, unknown> = {};
  for (const [name, g] of groups) {
    if (name === "all") continue;
    const node: Record<string, unknown> = {};
    if (Object.keys(g.hosts).length) node.hosts = Object.fromEntries(Object.entries(g.hosts).map(([k, v]) => [k, v ?? {}]));
    if (Object.keys(g.vars).length) node.vars = g.vars;
    if (Object.keys(g.children).length) node.children = g.children;
    children[name] = node;
  }
  if (Object.keys(children).length) all.children = children;
  return newDocument({ all });
}

function q(v: unknown): string {
  const s = typeof v === "object" ? JSON.stringify(v) : String(v ?? "");
  return /[\s"'#;]/.test(s) || s === "" ? JSON.stringify(s) : s;
}

const vars = (o: unknown) =>
  o && typeof o === "object"
    ? Object.entries(o as Record<string, unknown>)
        .map(([k, v]) => `${k}=${q(v)}`)
        .join(" ")
    : "";

export function docToIni(doc: Document): string {
  const root = (doc.toJS() ?? {}) as Record<string, Record<string, unknown>>;
  const all = root.all ?? {};
  const out: string[] = [];
  const hosts = (all.hosts ?? {}) as Record<string, unknown>;
  for (const [h, v] of Object.entries(hosts)) out.push([h, vars(v)].filter(Boolean).join(" "));
  const seen = new Set<string>();
  const sections: string[] = [];
  const visit = (name: string, g: Record<string, unknown>) => {
    const children = (g.children ?? {}) as Record<string, Record<string, unknown> | null>;
    if (!seen.has(name) && (g.hosts || g.vars || g.children || !Object.keys(g).length)) {
      seen.add(name);
      const hs = Object.entries((g.hosts ?? {}) as Record<string, unknown>);
      if (hs.length || (!g.vars && !g.children)) sections.push(`\n[${name}]`, ...hs.map(([h, v]) => [h, vars(v)].filter(Boolean).join(" ")));
      const vs = Object.entries((g.vars ?? {}) as Record<string, unknown>);
      if (vs.length) sections.push(`\n[${name}:vars]`, ...vs.map(([k, v]) => `${k}=${q(v)}`));
      if (Object.keys(children).length) sections.push(`\n[${name}:children]`, ...Object.keys(children));
    }
    for (const [c, cg] of Object.entries(children)) if (cg && Object.keys(cg).length) visit(c, cg);
  };
  for (const [name, g] of Object.entries((all.children ?? {}) as Record<string, Record<string, unknown>>)) visit(name, g ?? {});
  const av = Object.entries((all.vars ?? {}) as Record<string, unknown>);
  if (av.length) sections.push(`\n[all:vars]`, ...av.map(([k, v]) => `${k}=${q(v)}`));
  return (out.join("\n") + sections.join("\n")).replace(/^\n+/, "") + "\n";
}
