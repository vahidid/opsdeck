import { Document, isMap, isScalar, isSeq, parseDocument } from "yaml";
import type { Node, YAMLMap, YAMLSeq } from "yaml";

// The visual editors work on the file's text: they parse it into a YAML
// document (comments and formatting kept), change nodes and write the text
// back, so the code view and the visual view are always the same file.

export type Path = (string | number)[];

// Ansible reads YAML 1.1: yes/no/on/off are booleans and 0644 is octal, so
// strings like "on" or "0644" must stay quoted when written.
const YAML_OPTS = { version: "1.1" as const };

export function parseYaml(text: string): Document {
  return parseDocument(text, { ...YAML_OPTS, prettyErrors: true, strict: false, uniqueKeys: false });
}

export function newDocument(value: unknown): Document {
  return new Document(value, YAML_OPTS);
}

/** errors, plus warnings other than unknown tags such as !vault */
export function yamlProblems(doc: Document): { message: string; line: number; from: number; to: number; error: boolean }[] {
  const out: { message: string; line: number; from: number; to: number; error: boolean }[] = [];
  for (const e of doc.errors) out.push({ message: e.message.split("\n")[0], line: e.linePos?.[0]?.line ?? 1, from: e.pos[0], to: e.pos[1], error: true });
  for (const w of doc.warnings)
    if (!/Unresolved tag/.test(w.message)) out.push({ message: w.message.split("\n")[0], line: w.linePos?.[0]?.line ?? 1, from: w.pos[0], to: w.pos[1], error: false });
  return out;
}

export function stringify(doc: Document): string {
  return doc.toString({ lineWidth: 0, minContentWidth: 0, flowCollectionPadding: false });
}

/** a node for a JS value, multi-line strings as | blocks */
export function toNode(doc: Document, value: unknown): Node {
  const n = doc.createNode(value) as Node;
  const visit = (x: unknown) => {
    if (isScalar(x)) {
      if (typeof x.value === "string" && x.value.includes("\n")) x.type = "BLOCK_LITERAL";
    } else if (isMap(x)) x.items.forEach((p) => visit(p.value));
    else if (isSeq(x)) x.items.forEach(visit);
  };
  visit(n);
  return n;
}

function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/**
 * patchMap makes the map node hold exactly obj, changing only what
 * differs: unchanged entries keep their comments and style, removed keys
 * go, new keys are added at the end (name first).
 */
export function patchMap(doc: Document, map: YAMLMap, obj: Record<string, unknown>) {
  const current = (map.toJSON() ?? {}) as Record<string, unknown>;
  for (const key of Object.keys(current)) if (!(key in obj)) map.delete(key);
  const added: string[] = [];
  for (const [k, v] of Object.entries(obj)) {
    if (!(k in current)) added.push(k);
    else if (!same(current[k], v)) map.set(k, toNode(doc, v));
  }
  added.sort((a, b) => (a === "name" ? -1 : b === "name" ? 1 : 0));
  for (const k of added) {
    if (k === "name" && map.items.length > 0) {
      map.items.unshift(doc.createPair(k, toNode(doc, obj[k])) as never);
    } else map.set(k, toNode(doc, obj[k]));
  }
}

export function getMap(doc: Document, path: Path): YAMLMap | null {
  const n = path.length ? doc.getIn(path, true) : doc.contents;
  return isMap(n) ? (n as YAMLMap) : null;
}

export function getSeq(doc: Document, path: Path): YAMLSeq | null {
  const n = path.length ? doc.getIn(path, true) : doc.contents;
  return isSeq(n) ? (n as YAMLSeq) : null;
}

/** the list at path, created when missing */
export function ensureSeq(doc: Document, path: Path): YAMLSeq {
  let s = getSeq(doc, path);
  if (!s) {
    doc.setIn(path, doc.createNode([]));
    s = getSeq(doc, path)!;
  }
  return s;
}

export function ensureMap(doc: Document, path: Path): YAMLMap {
  let m = getMap(doc, path);
  if (!m) {
    doc.setIn(path, doc.createNode({}));
    m = getMap(doc, path)!;
  }
  return m;
}

export function moveItem(seq: YAMLSeq, from: number, to: number) {
  if (to < 0 || to >= seq.items.length) return;
  const [it] = seq.items.splice(from, 1);
  seq.items.splice(to, 0, it);
}

/** the value of a node as YAML text (for editing complex values) */
export function yamlText(value: unknown): string {
  if (value === undefined || value === null) return "";
  const d = newDocument(value);
  return stringify(d).replace(/\n$/, "");
}

/** parse YAML text typed into a field (empty = undefined) */
export function parseValue(text: string): { value?: unknown; error?: string } {
  if (!text.trim()) return { value: undefined };
  const d = parseYaml(text);
  if (d.errors.length) return { error: d.errors[0].message.split("\n")[0] };
  return { value: d.toJS() };
}

// ---------------------------------------------------------------------------
// Variables

/** one variable of a mapping; orig is its key in the file (for renames) */
export type VarRow = { key: string; value: unknown; vault?: boolean; orig?: string };

export function rowsOf(map: YAMLMap | null): VarRow[] {
  if (!map) return [];
  return map.items.map((p) => {
    const key = String(isScalar(p.key) ? p.key.value : p.key);
    const v = p.value as Node | null;
    const vault = isScalar(v) && v.tag === "!vault";
    return { key, orig: key, vault, value: vault ? "" : v && "toJSON" in v ? (v as { toJSON: () => unknown }).toJSON() : v };
  });
}

/** applyRows makes map hold rows, keeping the nodes (comments, !vault values) of unchanged and renamed entries */
export function applyRows(doc: Document, map: YAMLMap, rows: VarRow[]) {
  const byKey = new Map(map.items.map((p) => [String(isScalar(p.key) ? p.key.value : p.key), p]));
  const before = rowsOf(map);
  map.items = rows.map((r) => {
    const pair = r.orig !== undefined ? byKey.get(r.orig) : undefined;
    if (!pair) return doc.createPair(r.key, toNode(doc, r.value)) as never;
    if (r.key !== r.orig) pair.key = doc.createNode(r.key);
    const old = before.find((b) => b.orig === r.orig);
    if (!r.vault && JSON.stringify(old?.value) !== JSON.stringify(r.value)) pair.value = toNode(doc, r.value);
    return pair;
  });
}

// ---------------------------------------------------------------------------
// Ansible

export const TASK_KEYWORDS = new Set([
  "name", "action", "local_action", "args", "async", "poll", "become", "become_exe", "become_flags", "become_method", "become_user",
  "changed_when", "check_mode", "collections", "connection", "debugger", "delay", "delegate_facts", "delegate_to", "diff", "environment",
  "failed_when", "ignore_errors", "ignore_unreachable", "loop", "loop_control", "module_defaults", "no_log", "notify", "port", "register",
  "remote_user", "retries", "run_once", "tags", "throttle", "timeout", "until", "vars", "when", "listen", "block", "rescue", "always",
  "any_errors_fatal",
]);

export const PLAY_KEYWORDS = [
  "name", "hosts", "become", "become_user", "become_method", "gather_facts", "serial", "strategy", "remote_user", "connection", "tags",
  "vars", "vars_files", "vars_prompt", "roles", "pre_tasks", "tasks", "post_tasks", "handlers", "environment", "collections",
  "max_fail_percentage", "any_errors_fatal", "order", "module_defaults", "force_handlers", "ignore_unreachable", "check_mode", "diff",
  "run_once", "throttle", "timeout", "import_playbook",
];

export type Task = Record<string, unknown>;

/** the module key of a task (its first key that is not a keyword) */
export function moduleOf(t: Task): string {
  for (const k of Object.keys(t)) if (!TASK_KEYWORDS.has(k) && !k.startsWith("with_")) return k;
  if (typeof t.action === "string") return t.action.split(/\s/)[0];
  return "";
}

export function shortModule(m: string): string {
  return m.replace(/^ansible\.(builtin|legacy)\./, "");
}

export function isBlock(t: Task): boolean {
  return Array.isArray(t.block);
}

/** a one-line summary of a task's arguments */
export function argsSummary(t: Task): string {
  const m = moduleOf(t);
  const a = m ? t[m] : undefined;
  if (typeof a === "string") return a;
  if (a && typeof a === "object" && !Array.isArray(a)) {
    return Object.entries(a as Record<string, unknown>)
      .filter(([, v]) => v !== null && typeof v !== "object")
      .slice(0, 4)
      .map(([k, v]) => `${k}=${String(v).split("\n")[0]}`)
      .join("  ");
  }
  if (a !== undefined && a !== null) return String(a);
  return "";
}

export function asList(v: unknown): string[] {
  if (v === undefined || v === null || v === "") return [];
  if (Array.isArray(v)) return v.map((x) => (typeof x === "string" ? x : JSON.stringify(x)));
  return [String(v)];
}

/** documentation link of a module on docs.ansible.com */
export function moduleDocsUrl(m: string): string {
  const parts = m.split(".");
  if (parts.length < 3) return `https://docs.ansible.com/ansible/latest/collections/ansible/builtin/${m}_module.html`;
  return `https://docs.ansible.com/ansible/latest/collections/${parts[0]}/${parts[1]}/${parts.slice(2).join(".")}_module.html`;
}

// ---------------------------------------------------------------------------
// What a file is, for the editor

export type View = "playbook" | "tasks" | "inventory" | "inventory-ini" | "vars" | "requirements" | "code";

export function languageOf(path: string): "yaml" | "jinja" | "ini" | "shell" | "json" | "python" | "text" {
  const p = path.toLowerCase();
  if (/\.(ya?ml)$/.test(p)) return "yaml";
  if (/\.j2$/.test(p) || p.includes("/templates/")) return "jinja";
  if (/\.(cfg|ini|conf)$/.test(p) || /(^|\/)(hosts|inventory)$/.test(p)) return "ini";
  if (/\.(sh|bash)$/.test(p)) return "shell";
  if (/\.json$/.test(p)) return "json";
  if (/\.py$/.test(p)) return "python";
  return "text";
}

/** the visual view a file opens in, from where it is and what it holds */
export function viewOf(path: string, type: string, doc: Document | null): View {
  const p = path.toLowerCase();
  if (type === "inventory" && !/\.(ya?ml|json)$/.test(p)) return "inventory-ini";
  if (!doc || doc.errors.length) return "code";
  const c = doc.contents;
  if (type === "requirements" && isMap(c)) return "requirements";
  if (type === "inventory" && (isMap(c) || c === null)) return "inventory";
  if (isSeq(c)) {
    const items = (c.toJSON() ?? []) as unknown[];
    const maps = items.filter((x) => x && typeof x === "object" && !Array.isArray(x)) as Task[];
    if (maps.some((x) => "hosts" in x || "import_playbook" in x || "ansible.builtin.import_playbook" in x)) return "playbook";
    if (maps.length > 0 && maps.length === items.length) return "tasks";
    return "code";
  }
  if ((isMap(c) || c === null) && (type === "vars" || /(^|\/)(group_vars|host_vars|vars|defaults)\//.test(p) || /vault/.test(p))) return "vars";
  if (isMap(c) && /\.ya?ml$/.test(p) && type !== "config") return "vars";
  return "code";
}

export const VIEW_LABEL: Record<View, string> = {
  playbook: "Playbook",
  tasks: "Tasks",
  inventory: "Inventory",
  "inventory-ini": "Inventory",
  vars: "Variables",
  requirements: "Requirements",
  code: "Code",
};

// keywords offered by autocompletion
export const COMPLETE_TASK = [
  "name", "become", "become_user", "when", "loop", "register", "notify", "tags", "ignore_errors", "changed_when", "failed_when",
  "delegate_to", "run_once", "no_log", "until", "retries", "delay", "vars", "environment", "block", "rescue", "always", "listen",
  "check_mode", "loop_control", "args", "async", "poll", "throttle",
];
