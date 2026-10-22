import { useMemo, useState } from "react";
import { FolderTree, Layers, Plus, Server, Trash2, Variable } from "lucide-react";
import type { Document } from "yaml";
import { Drawer } from "../../../components/overlay";
import { Button, FormField, IconButton, Input, cx } from "../../../components/ui";
import { useEditor } from "./context";
import { ChipsField, Pill, SectionTitle, VarsTable } from "./fields";
import { applyRows, ensureMap, getMap, rowsOf } from "./model";
import type { Path } from "./model";

type Group = { name: string; path: Path; paths: Path[]; hosts: string[]; children: string[]; parents: string[] };

/** the groups of a YAML-shaped inventory; a group defined in several places is merged */
function groupsOf(doc: Document): Map<string, Group> {
  const out = new Map<string, Group>();
  const root = getMap(doc, []);
  if (!root) return out;
  const visit = (name: string, path: Path, parent: string | null) => {
    let g = out.get(name);
    if (!g) {
      g = { name, path, paths: [], hosts: [], children: [], parents: [] };
      out.set(name, g);
    }
    g.paths.push(path);
    if (parent && !g.parents.includes(parent)) g.parents.push(parent);
    const node = getMap(doc, path);
    if (!node) return;
    const content = ["hosts", "vars", "children"].some((k) => node.has(k));
    if (content && g.paths.length > 1 && !["hosts", "vars", "children"].some((k) => getMap(doc, g!.path)?.has(k))) g.path = path;
    for (const h of rowsOf(getMap(doc, [...path, "hosts"]))) if (!g.hosts.includes(h.key)) g.hosts.push(h.key);
    for (const c of rowsOf(getMap(doc, [...path, "children"]))) {
      if (!g.children.includes(c.key)) g.children.push(c.key);
      if ((out.get(c.key)?.paths ?? []).some((p) => p.join("\0") === [...path, "children", c.key].join("\0"))) continue;
      visit(c.key, [...path, "children", c.key], name);
    }
  };
  for (const r of rowsOf(root)) visit(r.key, [r.key], null);
  return out;
}

function hostPaths(groups: Map<string, Group>, host: string): Path[] {
  const out: Path[] = [];
  for (const g of groups.values()) for (const p of g.paths) out.push([...p, "hosts", host]);
  return out;
}

/** InventoryView: groups as a tree, each group's hosts, child groups and variables, and each host's variables. */
export function InventoryView({ ini }: { ini?: boolean }) {
  const { doc, edit, readOnly } = useEditor();
  const groups = useMemo(() => groupsOf(doc), [doc]);
  const roots = Array.from(groups.values()).filter((g) => g.parents.length === 0);
  const [sel, setSel] = useState<string>(roots[0]?.name ?? "all");
  const [host, setHost] = useState<string | null>(null);
  const [newGroup, setNewGroup] = useState("");
  const [newHost, setNewHost] = useState("");
  const g = groups.get(sel) ?? roots[0];
  const allHosts = useMemo(() => Array.from(new Set(Array.from(groups.values()).flatMap((x) => x.hosts))).sort(), [groups]);

  const tree = (name: string, depth: number, seen: Set<string>): React.ReactNode => {
    const x = groups.get(name);
    if (!x || seen.has(name)) return null;
    const next = new Set(seen).add(name);
    return (
      <div key={name + depth}>
        <button
          onClick={() => setSel(name)}
          className={cx("flex w-full items-center gap-1.5 rounded px-1.5 py-1 text-left", g?.name === name ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2")}
          style={{ paddingInlineStart: depth * 14 + 6 }}
        >
          <Layers className="size-3.5 shrink-0 text-faint" />
          <span className="font-mono text-[12.5px] flex-1 truncate">{name}</span>
          {x.hosts.length > 0 && <span className="text-[11px] text-faint tabular-nums">{x.hosts.length}</span>}
        </button>
        {x.children.map((c) => tree(c, depth + 1, next))}
      </div>
    );
  };

  function addGroup() {
    const name = newGroup.trim();
    if (!name || !g) return;
    edit((d) => {
      const cur = groupsOf(d).get(g.name);
      if (cur) ensureMap(d, [...cur.path, "children"]).set(name, d.createNode({}));
    });
    setNewGroup("");
    setSel(name);
  }

  function addHost(name: string) {
    if (!name || !g) return;
    edit((d) => {
      const cur = groupsOf(d).get(g.name);
      if (cur) ensureMap(d, [...cur.path, "hosts"]).set(name, d.createNode({}));
    });
    setNewHost("");
  }

  function removeGroup(name: string) {
    edit((d) => {
      const gs = groupsOf(d);
      const x = gs.get(name);
      if (!x) return;
      for (const p of [...x.paths].sort((a, b) => b.length - a.length)) d.deleteIn(p);
    });
    setSel(roots[0]?.name ?? "all");
  }

  if (!g) {
    return (
      <div className="space-y-3">
        <p className="text-sm text-muted">The inventory is empty.</p>
        {!readOnly && (
          <Button icon={<Plus className="size-3.5" />} onClick={() => edit((d) => ensureMap(d, ["all"]).set("hosts", d.createNode({})))}>
            Start with the group “all”
          </Button>
        )}
      </div>
    );
  }
  const gVars = rowsOf(getMap(doc, [...g.path, "vars"]));
  // hosts of the group's child groups, with the group they are in
  const descendants: [string, string][] = [];
  const seenG = new Set<string>([g.name]);
  const walk = (name: string) => {
    for (const c of groups.get(name)?.children ?? []) {
      if (seenG.has(c)) continue;
      seenG.add(c);
      for (const h of groups.get(c)?.hosts ?? []) if (!g.hosts.includes(h) && !descendants.some(([x]) => x === h)) descendants.push([h, c]);
      walk(c);
    }
  };
  walk(g.name);
  return (
    <div className="flex gap-4 min-h-[420px]">
      <aside className="w-60 shrink-0 space-y-2">
        <SectionTitle icon={<FolderTree />}>Groups</SectionTitle>
        <div className="rounded-md border border-line p-1">{roots.map((r) => tree(r.name, 0, new Set()))}</div>
        {!readOnly && (
          <div className="flex gap-1">
            <Input mono value={newGroup} onChange={(e) => setNewGroup(e.target.value.replace(/[^\w-]/g, ""))} placeholder={`new group in ${g.name}`} onKeyDown={(e) => e.key === "Enter" && addGroup()} />
            <IconButton title="Add group" disabled={!newGroup} onClick={addGroup}>
              <Plus className="size-4" />
            </IconButton>
          </div>
        )}
        {ini && <p className="text-[11px] text-faint">INI inventory: comments are not kept when you edit it here.</p>}
      </aside>
      <section className="flex-1 min-w-0 space-y-5">
        <div className="flex items-center gap-2">
          <Layers className="size-4 text-faint" />
          <span className="font-mono text-[15px] font-medium">{g.name}</span>
          {g.parents.map((p) => (
            <Pill key={p}>in {p}</Pill>
          ))}
          <div className="flex-1" />
          {!readOnly && g.name !== "all" && (
            <Button size="sm" variant="ghost" className="text-err" icon={<Trash2 className="size-3.5" />} onClick={() => removeGroup(g.name)}>
              Delete group
            </Button>
          )}
        </div>
        <div>
          <SectionTitle icon={<Server />} count={g.hosts.length}>
            Hosts
          </SectionTitle>
          <div className="rounded-md border border-line divide-y divide-line">
            {g.hosts.length === 0 && <div className="px-3 py-2 text-xs text-faint">No hosts directly in this group.</div>}
            {g.hosts.map((h) => {
              const vars = (getMap(doc, [...g.path, "hosts", h])?.toJSON() ?? {}) as Record<string, unknown>;
              const groupsOfHost = Array.from(groups.values()).filter((x) => x.hosts.includes(h)).map((x) => x.name);
              return (
                <button key={h} onClick={() => setHost(h)} className="w-full flex items-center gap-2 px-3 py-1.5 text-left hover:bg-panel-2">
                  <Server className="size-3.5 text-faint" />
                  <span className="font-mono text-[12.5px] w-48 truncate">{h}</span>
                  <span className="font-mono text-[11.5px] text-muted flex-1 truncate" dir="ltr">
                    {[vars.ansible_host && `${String(vars.ansible_user ?? "") ? String(vars.ansible_user) + "@" : ""}${String(vars.ansible_host)}${vars.ansible_port ? ":" + String(vars.ansible_port) : ""}`]
                      .filter(Boolean)
                      .join("")}
                  </span>
                  {Object.keys(vars).filter((k) => !["ansible_host", "ansible_user", "ansible_port"].includes(k)).length > 0 && (
                    <Pill>{Object.keys(vars).length} vars</Pill>
                  )}
                  {groupsOfHost
                    .filter((x) => x !== g.name)
                    .map((x) => (
                      <Pill key={x} tone="info">
                        {x}
                      </Pill>
                    ))}
                </button>
              );
            })}
          </div>
          {descendants.length > 0 && (
            <div className="mt-3">
              <div className="text-[11px] text-faint mb-1">In child groups</div>
              <div className="flex flex-wrap gap-1">
                {descendants.map(([h, grp]) => (
                  <button key={h} onClick={() => setHost(h)} className="inline-flex items-center gap-1 rounded border border-line bg-panel-2 px-1.5 h-6 text-[12px] font-mono hover:border-accent">
                    <Server className="size-3 text-faint" />
                    {h}
                    <span className="text-faint text-[10.5px]">{grp}</span>
                  </button>
                ))}
              </div>
            </div>
          )}
          {!readOnly && (
            <div className="flex gap-1 mt-2 max-w-md">
              <Input mono list="inv-hosts" value={newHost} onChange={(e) => setNewHost(e.target.value.trim())} placeholder="host name (new or existing)" onKeyDown={(e) => e.key === "Enter" && addHost(newHost)} />
              <datalist id="inv-hosts">
                {allHosts.filter((h) => !g.hosts.includes(h)).map((h) => <option key={h} value={h} />)}
              </datalist>
              <Button size="md" icon={<Plus className="size-3.5" />} disabled={!newHost} onClick={() => addHost(newHost)}>
                Add host
              </Button>
            </div>
          )}
        </div>
        <div>
          <SectionTitle icon={<Variable />} count={gVars.length}>
            Group variables
          </SectionTitle>
          <VarsTable
            rows={gVars}
            onChange={(rows) =>
              edit((d) => {
                const cur = groupsOf(d).get(g.name);
                if (cur) applyRows(d, ensureMap(d, [...cur.path, "vars"]), rows);
              })
            }
            empty="No variables on this group (group_vars/ files are separate files)."
          />
        </div>
      </section>
      {host && (
        <HostDrawer
          host={host}
          groups={groups}
          onClose={() => setHost(null)}
        />
      )}
    </div>
  );
}

function HostDrawer({ host, groups, onClose }: { host: string; groups: Map<string, Group>; onClose: () => void }) {
  const { doc, edit, readOnly } = useEditor();
  const memberOf = Array.from(groups.values()).filter((g) => g.hosts.includes(host)).map((g) => g.name);
  // variables live on the first entry that has some
  const entry = hostPaths(groups, host).find((p) => (getMap(doc, p)?.items.length ?? 0) > 0) ?? (() => {
    const g = groups.get(memberOf[0]);
    return g ? [...g.path, "hosts", host] : null;
  })();
  const rows = entry ? rowsOf(getMap(doc, entry)) : [];
  const [name, setName] = useState(host);

  function setGroups(next: string[]) {
    edit((d) => {
      const gs = groupsOf(d);
      for (const g of gs.values()) {
        const inIt = g.hosts.includes(host);
        if (inIt && !next.includes(g.name)) for (const p of g.paths) d.deleteIn([...p, "hosts", host]);
        if (!inIt && next.includes(g.name)) ensureMap(d, [...g.path, "hosts"]).set(host, d.createNode({}));
      }
    });
  }

  function rename() {
    const to = name.trim();
    if (!to || to === host) return;
    edit((d) => {
      for (const p of hostPaths(groupsOf(d), host)) {
        const parent = getMap(d, p.slice(0, -1));
        const pair = parent?.items.find((it) => String((it.key as { value?: unknown })?.value ?? it.key) === host);
        if (pair) pair.key = d.createNode(to);
      }
    });
    onClose();
  }

  return (
    <Drawer open onClose={onClose} width={620} title={host} subtitle={`in ${memberOf.join(", ")}`}>
      <div className="space-y-4">
        {!readOnly && (
          <FormField label="Name">
            <div className="flex gap-2">
              <Input mono value={name} onChange={(e) => setName(e.target.value)} />
              <Button disabled={!name.trim() || name.trim() === host} onClick={rename}>
                Rename
              </Button>
            </div>
          </FormField>
        )}
        <FormField label="Groups">
          <ChipsField value={memberOf} onChange={setGroups} suggestions={Array.from(groups.keys())} placeholder="add to a group" />
        </FormField>
        <div>
          <SectionTitle icon={<Variable />} count={rows.length}>
            Host variables
          </SectionTitle>
          <VarsTable
            rows={rows}
            onChange={(r) => entry && edit((d) => applyRows(d, ensureMap(d, entry), r))}
            empty="No variables. Common ones: ansible_host, ansible_user, ansible_port, ansible_become."
          />
          {!readOnly && rows.length === 0 && entry && (
            <Button
              size="sm"
              variant="ghost"
              className="mt-2"
              icon={<Plus className="size-3.5" />}
              onClick={() => edit((d) => applyRows(d, ensureMap(d, entry), [{ key: "ansible_host", value: "" }, { key: "ansible_user", value: "" }]))}
            >
              ansible_host and ansible_user
            </Button>
          )}
        </div>
      </div>
    </Drawer>
  );
}
