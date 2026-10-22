import { useState } from "react";
import type { ReactNode } from "react";
import { ArrowDown, ArrowUp, BookOpen, ChevronDown, ChevronRight, Copy, ExternalLink, FileInput, ListChecks, Pencil, Play, Plus, Settings2, Sparkles, Trash2, Variable, X } from "lucide-react";
import { Drawer } from "../../../components/overlay";
import { Button, FormField, IconButton, Input, Select, cx } from "../../../components/ui";
import { useAnsible } from "../shared";
import { useEditor } from "./context";
import { ChipsField, Pill, SectionTitle, VarsTable, YamlField } from "./fields";
import { TaskList } from "./TaskList";
import { applyRows, asList, ensureMap, ensureSeq, getMap, getSeq, moveItem, patchMap, rowsOf, toNode } from "./model";
import type { Path, Task } from "./model";

type PlayObj = Record<string, unknown>;

const SECTIONS: { key: string; label: string; icon: ReactNode }[] = [
  { key: "pre_tasks", label: "Pre-tasks", icon: <ListChecks /> },
  { key: "roles", label: "Roles", icon: <BookOpen /> },
  { key: "tasks", label: "Tasks", icon: <ListChecks /> },
  { key: "post_tasks", label: "Post-tasks", icon: <ListChecks /> },
  { key: "handlers", label: "Handlers", icon: <Sparkles /> },
];

/** PlaybookView: the plays of a playbook, each with its settings, variables, roles, tasks and handlers. */
export function PlaybookView() {
  const { doc, edit, readOnly } = useEditor();
  const plays = ((getSeq(doc, [])?.toJSON() ?? []) as unknown[]).map((p) => (p && typeof p === "object" ? (p as PlayObj) : {}));
  return (
    <div className="space-y-4">
      {plays.map((p, i) =>
        "import_playbook" in p || "ansible.builtin.import_playbook" in p ? (
          <ImportCard key={i} index={i} play={p} count={plays.length} />
        ) : (
          <PlayCard key={i} index={i} play={p} count={plays.length} />
        ),
      )}
      {!readOnly && (
        <div className="flex gap-2">
          <Button icon={<Plus className="size-3.5" />} onClick={() => edit((d) => ensureSeq(d, []).items.push(toNode(d, { name: "New play", hosts: "all", become: true, tasks: [] })))}>
            Add play
          </Button>
          <Button variant="ghost" icon={<FileInput className="size-3.5" />} onClick={() => edit((d) => ensureSeq(d, []).items.push(toNode(d, { import_playbook: "other.yml" })))}>
            Import a playbook
          </Button>
        </div>
      )}
    </div>
  );
}

function PlayActions({ index, count }: { index: number; count: number }) {
  const { edit, readOnly } = useEditor();
  if (readOnly) return null;
  return (
    <div className="flex items-center" onClick={(e) => e.stopPropagation()}>
      <IconButton title="Move up" disabled={index === 0} onClick={() => edit((d) => moveItem(getSeq(d, [])!, index, index - 1))}>
        <ArrowUp className="size-3.5" />
      </IconButton>
      <IconButton title="Move down" disabled={index === count - 1} onClick={() => edit((d) => moveItem(getSeq(d, [])!, index, index + 1))}>
        <ArrowDown className="size-3.5" />
      </IconButton>
      <IconButton
        title="Duplicate"
        onClick={() =>
          edit((d) => {
            const s = getSeq(d, [])!;
            s.items.splice(index + 1, 0, (s.items[index] as { clone: () => unknown }).clone() as never);
          })
        }
      >
        <Copy className="size-3.5" />
      </IconButton>
      <IconButton title="Delete play" onClick={() => edit((d) => getSeq(d, [])?.items.splice(index, 1))}>
        <Trash2 className="size-3.5" />
      </IconButton>
    </div>
  );
}

function ImportCard({ index, play, count }: { index: number; play: PlayObj; count: number }) {
  const { edit, files, openFile, path, readOnly } = useEditor();
  const key = "import_playbook" in play ? "import_playbook" : "ansible.builtin.import_playbook";
  const file = String(play[key] ?? "");
  const dir = path.split("/").slice(0, -1).join("/");
  const target = [dir ? `${dir}/${file}` : file, file].find((f) => files.includes(f));
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed border-line-strong bg-panel px-3 py-2">
      <FileInput className="size-4 text-faint" />
      <span className="text-[13px] text-muted">Import playbook</span>
      <Input mono value={file} disabled={readOnly} onChange={(e) => edit((d) => d.setIn([index, key], e.target.value))} className="w-72" />
      {target && (
        <button className="inline-flex items-center gap-1 text-xs text-accent hover:underline" onClick={() => openFile(target)}>
          open <ExternalLink className="size-3" />
        </button>
      )}
      <div className="flex-1" />
      <PlayActions index={index} count={count} />
    </div>
  );
}

function PlayCard({ index, play: p, count }: { index: number; play: PlayObj; count: number }) {
  const { doc, edit, readOnly, path } = useEditor();
  const { goto } = useAnsible();
  const [open, setOpen] = useState(true);
  const [settings, setSettings] = useState(false);
  const [shown, setShown] = useState<Record<string, boolean>>({});
  const base: Path = [index];
  const has = (k: string) => k in p;
  const varsRows = rowsOf(getMap(doc, [...base, "vars"]));
  const missing = SECTIONS.filter((s) => !has(s.key));
  return (
    <div className="rounded-lg border border-line bg-panel">
      <div className="flex items-center gap-2 px-3 py-2 border-b border-line cursor-pointer" onClick={() => setOpen(!open)}>
        {open ? <ChevronDown className="size-4 text-faint" /> : <ChevronRight className="size-4 text-faint" />}
        <Play className="size-4 text-accent" />
        <span className="text-[14px] font-medium truncate">{String(p.name ?? "(play without a name)")}</span>
        <Pill tone="info" title="hosts">
          hosts: {asList(p.hosts).join(", ") || "—"}
        </Pill>
        {p.become !== undefined && <Pill tone={p.become ? "accent" : "neutral"}>{p.become ? `become${p.become_user ? " " + String(p.become_user) : ""}` : "no become"}</Pill>}
        {p.gather_facts === false && <Pill>no facts</Pill>}
        {p.serial !== undefined && <Pill>serial {String(p.serial)}</Pill>}
        {p.strategy !== undefined && <Pill>{String(p.strategy)}</Pill>}
        {asList(p.tags).map((t) => (
          <Pill key={t}>#{t}</Pill>
        ))}
        <div className="flex-1" />
        <div className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
          <Button size="sm" variant="ghost" icon={<Play className="size-3.5" />} onClick={() => goto("run", { playbook: path })} title="Run this playbook">
            Run
          </Button>
          <Button size="sm" variant="ghost" icon={<Settings2 className="size-3.5" />} onClick={() => setSettings(true)}>
            Settings
          </Button>
          <PlayActions index={index} count={count} />
        </div>
      </div>
      {open && (
        <div className="p-3 space-y-4">
          {(has("vars") || has("vars_files") || shown.vars) && (
            <div>
              <SectionTitle icon={<Variable />} count={varsRows.length}>
                Variables
              </SectionTitle>
              <VarsTable rows={varsRows} onChange={(rows) => edit((d) => applyRows(d, ensureMap(d, [...base, "vars"]), rows))} />
              {(has("vars_files") || !readOnly) && (
                <div className="mt-2">
                  <div className="text-[11px] text-faint mb-1">Variable files (vars_files)</div>
                  <ChipsField
                    value={asList(p.vars_files)}
                    onChange={(v) => edit((d) => (v.length ? d.setIn([...base, "vars_files"], toNode(d, v)) : d.deleteIn([...base, "vars_files"])))}
                    placeholder="vars/main.yml"
                  />
                </div>
              )}
            </div>
          )}
          {SECTIONS.filter((s) => has(s.key) || shown[s.key]).map((s) => (
            <div key={s.key}>
              <SectionTitle icon={s.icon} count={Array.isArray(p[s.key]) ? (p[s.key] as unknown[]).length : 0}>
                {s.label}
              </SectionTitle>
              {s.key === "roles" ? <Roles path={[...base, "roles"]} /> : <TaskList path={[...base, s.key]} handler={s.key === "handlers"} />}
            </div>
          ))}
          {!readOnly && (missing.length > 0 || !has("vars")) && (
            <div className="flex flex-wrap gap-1.5 pt-1 border-t border-line">
              {!has("vars") && !shown.vars && (
                <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setShown({ ...shown, vars: true })}>
                  Variables
                </Button>
              )}
              {missing
                .filter((s) => !shown[s.key])
                .map((s) => (
                  <Button key={s.key} size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setShown({ ...shown, [s.key]: true })}>
                    {s.label}
                  </Button>
                ))}
            </div>
          )}
        </div>
      )}
      {settings && (
        <PlayDrawer
          play={p}
          onClose={() => setSettings(false)}
          onSave={(next) => {
            edit((d) => {
              const m = getMap(d, base);
              if (m) patchMap(d, m, next);
            });
            setSettings(false);
          }}
        />
      )}
    </div>
  );
}

function Roles({ path }: { path: Path }) {
  const { doc, edit, roles, files, openFile, readOnly } = useEditor();
  const list = ((getSeq(doc, path)?.toJSON() ?? []) as unknown[]).map((r) => (typeof r === "string" ? { role: r } : ((r ?? {}) as Record<string, unknown>)));
  const [add, setAdd] = useState("");
  const [expanded, setExpanded] = useState<number | null>(null);
  const nameOf = (r: Record<string, unknown>) => String(r.role ?? r.name ?? "");
  return (
    <div className="space-y-1">
      {list.length === 0 && <div className="text-xs text-faint px-1 py-1.5">No roles.</div>}
      {list.map((r, i) => {
        const name = nameOf(r);
        const main = files.find((f) => f.startsWith(`roles/${name}/tasks/main.`));
        const extra = Object.fromEntries(Object.entries(r).filter(([k]) => k !== "role" && k !== "name"));
        return (
          <div key={i} className="rounded-md border border-line">
            <div className="flex items-center gap-2 px-2 py-1.5">
              <BookOpen className="size-3.5 text-faint" />
              <span className="font-mono text-[12.5px]">{name}</span>
              {Object.keys(extra).map((k) => (
                <Pill key={k}>{k}</Pill>
              ))}
              {main && (
                <button className="inline-flex items-center gap-0.5 text-[11px] text-accent hover:underline" onClick={() => openFile(main)}>
                  open <ExternalLink className="size-3" />
                </button>
              )}
              <div className="flex-1" />
              {!readOnly && (
                <>
                  <IconButton title="Parameters (vars, tags, when…)" onClick={() => setExpanded(expanded === i ? null : i)}>
                    <Pencil className="size-3.5" />
                  </IconButton>
                  <IconButton title="Remove role" onClick={() => edit((d) => getSeq(d, path)?.items.splice(i, 1))}>
                    <X className="size-3.5" />
                  </IconButton>
                </>
              )}
            </div>
            {expanded === i && (
              <div className="px-2 pb-2">
                <YamlField
                  value={extra}
                  rows={4}
                  placeholder={"vars:\n  nginx_port: 8080\ntags: [web]"}
                  onChange={(v, err) => {
                    if (err) return;
                    const params = (v && typeof v === "object" ? v : {}) as Record<string, unknown>;
                    edit((d) => {
                      const s = getSeq(d, path);
                      if (!s) return;
                      s.items[i] = (Object.keys(params).length ? toNode(d, { role: name, ...params }) : toNode(d, name)) as never;
                    });
                  }}
                />
              </div>
            )}
          </div>
        );
      })}
      {!readOnly && (
        <div className="flex items-center gap-2 pt-1">
          <Input mono list="ansible-roles" value={add} onChange={(e) => setAdd(e.target.value.trim())} placeholder="role name" className="w-64"
            onKeyDown={(e) => {
              if (e.key === "Enter" && add) {
                edit((d) => ensureSeq(d, path).items.push(toNode(d, add)));
                setAdd("");
              }
            }}
          />
          <datalist id="ansible-roles">
            {roles.map((r) => (
              <option key={r} value={r} />
            ))}
          </datalist>
          <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} disabled={!add} onClick={() => { edit((d) => ensureSeq(d, path).items.push(toNode(d, add))); setAdd(""); }}>
            Add role
          </Button>
        </div>
      )}
    </div>
  );
}

function Tri({ value, onChange }: { value: unknown; onChange: (v: boolean | undefined) => void }) {
  return (
    <Select value={value === undefined ? "" : value ? "true" : "false"} onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value === "true")}>
      <option value="">— default —</option>
      <option value="true">yes</option>
      <option value="false">no</option>
    </Select>
  );
}

const HEADER = ["name", "hosts", "become", "become_user", "gather_facts", "serial", "strategy", "remote_user", "connection", "tags", "any_errors_fatal", "max_fail_percentage", "order"];

function PlayDrawer({ play, onSave, onClose }: { play: PlayObj; onSave: (p: PlayObj) => void; onClose: () => void }) {
  const [d, setD] = useState<Task>(() => Object.fromEntries(HEADER.filter((k) => k in play).map((k) => [k, play[k]])));
  const set = (patch: Task) => setD({ ...d, ...patch });
  function save() {
    const next: PlayObj = { ...play };
    for (const k of HEADER) {
      const v = d[k];
      if (v === undefined || v === "" || (Array.isArray(v) && v.length === 0)) delete next[k];
      else next[k] = v;
    }
    onSave(next);
  }
  return (
    <Drawer
      open
      onClose={onClose}
      width={560}
      title="Play settings"
      subtitle={String(play.name ?? "")}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={save}>
            Apply
          </Button>
        </>
      }
    >
      <div className="grid grid-cols-2 gap-3">
        <FormField label="Name" className="col-span-2">
          <Input autoFocus value={String(d.name ?? "")} onChange={(e) => set({ name: e.target.value })} />
        </FormField>
        <FormField label="Hosts" className="col-span-2" help="A host pattern: all, web, web:&prod, web-1…">
          <Input mono value={asList(d.hosts).join(",")} onChange={(e) => set({ hosts: e.target.value })} />
        </FormField>
        <FormField label="Become (sudo)">
          <Tri value={d.become} onChange={(v) => set({ become: v })} />
        </FormField>
        <FormField label="Become user">
          <Input mono value={String(d.become_user ?? "")} onChange={(e) => set({ become_user: e.target.value || undefined })} placeholder="root" />
        </FormField>
        <FormField label="Gather facts">
          <Tri value={d.gather_facts} onChange={(v) => set({ gather_facts: v })} />
        </FormField>
        <FormField label="Strategy">
          <Select value={String(d.strategy ?? "")} onChange={(e) => set({ strategy: e.target.value || undefined })}>
            <option value="">linear (default)</option>
            <option value="free">free</option>
            <option value="host_pinned">host_pinned</option>
            <option value="debug">debug</option>
          </Select>
        </FormField>
        <FormField label="Serial" help="Hosts per batch: 1, 30%, [1, 5, 50%]">
          <Input mono value={typeof d.serial === "object" ? JSON.stringify(d.serial) : String(d.serial ?? "")} onChange={(e) => set({ serial: e.target.value === "" ? undefined : /^\d+$/.test(e.target.value) ? Number(e.target.value) : e.target.value })} />
        </FormField>
        <FormField label="Max fail %">
          <Input mono type="number" value={String(d.max_fail_percentage ?? "")} onChange={(e) => set({ max_fail_percentage: e.target.value === "" ? undefined : Number(e.target.value) })} />
        </FormField>
        <FormField label="Remote user">
          <Input mono value={String(d.remote_user ?? "")} onChange={(e) => set({ remote_user: e.target.value || undefined })} />
        </FormField>
        <FormField label="Connection">
          <Input mono value={String(d.connection ?? "")} onChange={(e) => set({ connection: e.target.value || undefined })} placeholder="ssh" />
        </FormField>
        <FormField label="Any errors fatal">
          <Tri value={d.any_errors_fatal} onChange={(v) => set({ any_errors_fatal: v })} />
        </FormField>
        <FormField label="Order">
          <Select value={String(d.order ?? "")} onChange={(e) => set({ order: e.target.value || undefined })}>
            <option value="">inventory (default)</option>
            {["reverse_inventory", "sorted", "reverse_sorted", "shuffle"].map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </Select>
        </FormField>
        <FormField label="Tags" className="col-span-2">
          <ChipsField value={asList(d.tags)} onChange={(v) => set({ tags: v.length ? v : undefined })} />
        </FormField>
      </div>
      <p className={cx("text-[11px] text-faint mt-4")}>Variables, roles, tasks and handlers are edited on the play itself; anything else in the Code view.</p>
    </Drawer>
  );
}
