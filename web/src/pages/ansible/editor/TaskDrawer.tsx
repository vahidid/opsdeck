import { useEffect, useMemo, useState } from "react";
import { BookOpen, ExternalLink, Plus, Trash2, X } from "lucide-react";
import type { AnsibleModuleDoc, AnsibleModuleOption } from "../../../api/types";
import { Drawer } from "../../../components/overlay";
import { Badge, Button, FormField, IconButton, Input, Segmented, Select, Textarea, cx } from "../../../components/ui";
import { useEditor } from "./context";
import { ChipsField, ValueEditor, YamlField } from "./fields";
import { Code } from "./LazyCode";
import { TASK_KEYWORDS, asList, isBlock, moduleDocsUrl, moduleOf, parseValue, yamlText } from "./model";
import type { Task } from "./model";

// keywords with their own field in the form
const FLOW = ["when", "loop", "register", "notify", "listen", "tags", "become", "become_user", "ignore_errors", "changed_when", "failed_when", "delegate_to", "run_once", "no_log"];

function Tri({ value, onChange }: { value: unknown; onChange: (v: boolean | undefined) => void }) {
  return (
    <Select value={value === undefined ? "" : value ? "true" : "false"} onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value === "true")}>
      <option value="">— inherit —</option>
      <option value="true">yes</option>
      <option value="false">no</option>
    </Select>
  );
}

function OptionInput({ opt, value, onChange }: { opt: AnsibleModuleOption; value: unknown; onChange: (v: unknown) => void }) {
  if (opt.choices?.length) {
    return (
      <Select value={value === undefined ? "" : String(value)} onChange={(e) => onChange(e.target.value === "" ? undefined : opt.choices!.find((c) => String(c) === e.target.value))}>
        <option value="">{opt.default !== undefined && opt.default !== null ? `default: ${String(opt.default)}` : "—"}</option>
        {opt.choices.map((c) => (
          <option key={String(c)} value={String(c)}>
            {String(c)}
          </option>
        ))}
      </Select>
    );
  }
  switch (opt.type) {
    case "bool":
      return <Tri value={value} onChange={onChange} />;
    case "int":
    case "float":
      return <Input mono type="number" value={value === undefined ? "" : String(value)} onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))} />;
    case "list":
      if (typeof value === "string") return <Input mono value={value} onChange={(e) => onChange(e.target.value || undefined)} dir="ltr" />;
      if ((value === undefined || (Array.isArray(value) && value.every((x) => typeof x !== "object"))) && (!opt.elements || ["str", "path"].includes(opt.elements))) {
        return (
          <Textarea
            mono
            rows={Math.max(2, asList(value).length + 1)}
            value={asList(value).join("\n")}
            placeholder="one per line"
            className="text-[12px]"
            dir="ltr"
            onChange={(e) => {
              const l = e.target.value.split("\n").filter((x) => x !== "");
              onChange(l.length ? l : undefined);
            }}
          />
        );
      }
      return <YamlField value={value} onChange={(v, err) => !err && onChange(v)} />;
    case "dict":
    case "raw":
    case "jsonarg":
      if (typeof value === "string") return <Input mono value={value} onChange={(e) => onChange(e.target.value)} dir="ltr" />;
      return <YamlField value={value} onChange={(v, err) => !err && onChange(v)} />;
  }
  const s = value === undefined || value === null ? "" : typeof value === "object" ? JSON.stringify(value) : String(value);
  if (opt.name === "content" || s.includes("\n")) {
    return <Textarea mono rows={Math.min(14, Math.max(3, s.split("\n").length + 1))} value={s} onChange={(e) => onChange(e.target.value || undefined)} className="text-[12px]" dir="ltr" />;
  }
  return <Input mono value={s} onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)} dir="ltr" placeholder={opt.default !== undefined && opt.default !== null ? `default: ${String(opt.default)}` : ""} />;
}

/** removes empty values and keeps a stable key order: name, module, the rest */
function clean(t: Task, module: string): Task {
  const out: Task = {};
  const put = (k: string) => {
    const v = t[k];
    if (v === undefined || v === "" || (Array.isArray(v) && v.length === 0)) return;
    out[k] = v;
  };
  put("name");
  if (module) out[module] = t[module] ?? null;
  for (const k of Object.keys(t)) if (k !== "name" && k !== module) put(k);
  return out;
}

/**
 * TaskDrawer edits one task (or handler, or block): its name, module and
 * the module's options (from ansible-doc), conditions, loops, tags and
 * privilege escalation — or the raw YAML.
 */
export function TaskDrawer({
  task,
  isNew,
  handler,
  onSave,
  onDelete,
  onClose,
}: {
  task: Task;
  isNew: boolean;
  handler?: boolean;
  onSave: (t: Task) => void;
  onDelete?: () => void;
  onClose: () => void;
}) {
  const { modules, moduleDoc, readOnly } = useEditor();
  const [draft, setDraft] = useState<Task>(task);
  const [tab, setTabState] = useState<"form" | "yaml">("form");
  const [yamlDraft, setYamlDraft] = useState("");
  const setTab = (t: "form" | "yaml") => {
    if (t === "yaml") setYamlDraft(yamlText(clean(draft, isBlock(draft) ? "" : moduleOf(draft))));
    setYamlErr("");
    setTabState(t);
  };
  const [yamlErr, setYamlErr] = useState("");
  const [doc, setDoc] = useState<AnsibleModuleDoc | null>(null);
  const [adding, setAdding] = useState("");
  const block = isBlock(draft);
  const module = block ? "" : moduleOf(draft);
  const args = module ? draft[module] : undefined;
  const set = (patch: Task) => setDraft((d) => ({ ...d, ...patch }));

  useEffect(() => {
    let cancel = false;
    setDoc(null);
    if (!module || /^(include|import)_(tasks|role|playbook)$|\.(include|import)_/.test(module)) return;
    const t = setTimeout(() => {
      void moduleDoc(module).then((d) => !cancel && setDoc(d));
    }, 250);
    return () => {
      cancel = true;
      clearTimeout(t);
    };
  }, [module, moduleDoc]);

  function setModule(m: string) {
    m = m.trim();
    if (m === module) return;
    const next: Task = {};
    for (const [k, v] of Object.entries(draft)) {
      if (k === module) {
        if (m) next[m] = v;
      } else next[k] = v;
    }
    if (m && !(m in next)) next[m] = null;
    setDraft(next);
  }

  const argMap = args && typeof args === "object" && !Array.isArray(args) ? (args as Record<string, unknown>) : null;
  const setArg = (k: string, v: unknown) => {
    const a = { ...(argMap ?? {}) };
    if (v === undefined) delete a[k];
    else a[k] = v;
    set({ [module]: Object.keys(a).length ? a : null });
  };
  const options = doc?.options ?? [];
  const shown = useMemo(() => {
    const names = new Set<string>([...options.filter((o) => o.required).map((o) => o.name), ...Object.keys(argMap ?? {})]);
    return Array.from(names);
  }, [options, argMap]);
  const others = Object.keys(draft).filter((k) => draft[k] !== undefined && k !== "name" && k !== module && !FLOW.includes(k) && !["block", "rescue", "always"].includes(k));

  function save() {
    if (tab === "yaml" && yamlErr) return;
    onSave(clean(draft, module));
  }

  const missing = options.filter((o) => o.required && (argMap?.[o.name] === undefined || argMap?.[o.name] === "")).map((o) => o.name);
  const what = handler ? "handler" : block ? "block" : "task";
  return (
    <Drawer
      open
      onClose={onClose}
      width={680}
      title={isNew ? `New ${what}` : String(draft.name || module || what)}
      subtitle={module ? <span className="font-mono">{module}</span> : undefined}
      footer={
        <>
          {onDelete && !readOnly && (
            <Button variant="ghost" className="mr-auto text-err" icon={<Trash2 className="size-3.5" />} onClick={onDelete}>
              Delete
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          {!readOnly && (
            <Button variant="primary" disabled={(!block && !module) || (tab === "yaml" && !!yamlErr)} onClick={save}>
              {isNew ? "Add" : "Apply"}
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-4">
        <Segmented
          value={tab}
          onChange={setTab}
          options={[
            { id: "form", label: "Form" },
            { id: "yaml", label: "YAML" },
          ]}
        />
        {tab === "yaml" ? (
          <div className="h-[60vh] rounded-md border border-line overflow-hidden">
            <Code
              language="yaml"
              value={yamlDraft}
              modules={modules}
              onChange={(text) => {
                setYamlDraft(text);
                const p = parseValue(text);
                if (p.error) setYamlErr(p.error);
                else if (p.value && typeof p.value === "object" && !Array.isArray(p.value)) {
                  setYamlErr("");
                  setDraft(p.value as Task);
                } else setYamlErr("a task is a mapping (name: …, module: …)");
              }}
              onSave={save}
            />
            {yamlErr && <div className="text-xs text-err mt-1">{yamlErr}</div>}
          </div>
        ) : (
          <>
            <FormField label="Name" help="Shown in the output of every run; describe what the task makes true.">
              <Input autoFocus={isNew} value={String(draft.name ?? "")} onChange={(e) => set({ name: e.target.value })} placeholder="Install nginx" />
            </FormField>

            {block ? (
              <div className="rounded-md border border-line bg-panel-2 px-3 py-2 text-xs text-muted">
                A block groups tasks (with <span className="font-mono">rescue</span> and <span className="font-mono">always</span>); its tasks are edited in the list.
                The settings below apply to all of them.
              </div>
            ) : (
              <div className="space-y-3 rounded-lg border border-line p-3">
                <FormField label="Module" required>
                  <Input mono list="ansible-module-list" value={module} onChange={(e) => setModule(e.target.value)} placeholder="ansible.builtin.copy" dir="ltr" />
                  <datalist id="ansible-module-list">
                    {modules.map((m) => (
                      <option key={m.name} value={m.name}>
                        {m.description}
                      </option>
                    ))}
                  </datalist>
                </FormField>
                {module && (
                  <div className="flex items-start gap-2 text-xs text-muted">
                    <BookOpen className="size-3.5 mt-0.5 shrink-0" />
                    <span className="flex-1">{doc?.short || modules.find((m) => m.name === module)?.description || "—"}</span>
                    <a href={moduleDocsUrl(module)} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-accent hover:underline shrink-0">
                      docs <ExternalLink className="size-3" />
                    </a>
                  </div>
                )}
                {module && (typeof args === "string" || (doc?.freeForm && !argMap)) ? (
                  <FormField label={doc?.freeForm ? "Command" : "Arguments"} help={doc?.freeForm ? "Free form, e.g. systemctl restart nginx" : "key=value arguments"}>
                    <Textarea mono rows={3} value={typeof args === "string" ? args : ""} onChange={(e) => set({ [module]: e.target.value })} dir="ltr" className="text-[12px]" />
                  </FormField>
                ) : (
                  module && (
                    <div className="space-y-2">
                      {shown.map((name) => {
                        const opt = options.find((o) => o.name === name || o.aliases?.includes(name)) ?? { name, type: typeof argMap?.[name] === "object" ? "raw" : undefined };
                        return (
                          <div key={name} className="grid grid-cols-[150px_1fr_auto] gap-2 items-start">
                            <div className="pt-1.5 min-w-0" title={opt.description}>
                              <div className="font-mono text-[12px] truncate">
                                {name}
                                {opt.required && <span className="text-err">*</span>}
                              </div>
                              {opt.type && <div className="text-[10.5px] text-faint">{opt.type}{opt.elements ? ` of ${opt.elements}` : ""}</div>}
                            </div>
                            <div className="min-w-0">
                              <OptionInput opt={opt} value={argMap?.[name]} onChange={(v) => setArg(name, v)} />
                              {opt.description && <div className="text-[10.5px] text-faint mt-0.5 line-clamp-2">{opt.description}</div>}
                            </div>
                            <IconButton title="Remove option" onClick={() => setArg(name, undefined)} disabled={opt.required && !(name in (argMap ?? {}))}>
                              <X className="size-3.5" />
                            </IconButton>
                          </div>
                        );
                      })}
                      <div className="flex items-center gap-2">
                        {options.length > 0 ? (
                          <Select
                            value={adding}
                            onChange={(e) => {
                              const n = e.target.value;
                              setAdding("");
                              if (n) setArg(n, options.find((o) => o.name === n)?.type === "bool" ? true : "");
                            }}
                            className="w-72"
                          >
                            <option value="">+ Add an option…</option>
                            {options
                              .filter((o) => !shown.includes(o.name))
                              .map((o) => (
                                <option key={o.name} value={o.name}>
                                  {o.name}
                                  {o.description ? ` — ${o.description.slice(0, 70)}` : ""}
                                </option>
                              ))}
                          </Select>
                        ) : (
                          <AddOption onAdd={(n) => setArg(n, "")} />
                        )}
                        {missing.length > 0 && <Badge tone="warn">required: {missing.join(", ")}</Badge>}
                      </div>
                    </div>
                  )
                )}
              </div>
            )}

            <div className="grid grid-cols-2 gap-3">
              <FormField label="When" className="col-span-2" help="A Jinja condition without {{ }}, e.g. ansible_os_family == 'Debian'">
                {Array.isArray(draft.when) ? (
                  <Textarea mono rows={3} value={asList(draft.when).join("\n")} onChange={(e) => set({ when: e.target.value.split("\n").filter(Boolean) })} dir="ltr" className="text-[12px]" />
                ) : (
                  <Input mono value={String(draft.when ?? "")} onChange={(e) => set({ when: e.target.value || undefined })} dir="ltr" />
                )}
              </FormField>
              {!block && (
                <FormField label="Loop" className="col-span-2" help="A list, or a variable: {{ packages }}">
                  <YamlField value={draft.loop} rows={2} onChange={(v, err) => !err && set({ loop: v })} />
                </FormField>
              )}
              {!block && (
                <FormField label="Register">
                  <Input mono value={String(draft.register ?? "")} onChange={(e) => set({ register: e.target.value || undefined })} placeholder="result" />
                </FormField>
              )}
              <FormField label="Delegate to">
                <Input mono value={String(draft.delegate_to ?? "")} onChange={(e) => set({ delegate_to: e.target.value || undefined })} placeholder="localhost" />
              </FormField>
              {handler ? (
                <FormField label="Listen" className="col-span-2">
                  <ChipsField value={asList(draft.listen)} onChange={(v) => set({ listen: v.length ? v : undefined })} placeholder="restart web stack" />
                </FormField>
              ) : (
                <FormField label="Notify handlers" className="col-span-2">
                  <ChipsField value={asList(draft.notify)} onChange={(v) => set({ notify: v.length ? v : undefined })} placeholder="Restart nginx" />
                </FormField>
              )}
              <FormField label="Tags" className="col-span-2">
                <ChipsField value={asList(draft.tags)} onChange={(v) => set({ tags: v.length ? v : undefined })} placeholder="config, deploy" />
              </FormField>
              <FormField label="Become (sudo)">
                <Tri value={draft.become} onChange={(v) => set({ become: v })} />
              </FormField>
              <FormField label="Become user">
                <Input mono value={String(draft.become_user ?? "")} onChange={(e) => set({ become_user: e.target.value || undefined })} placeholder="root" />
              </FormField>
              <FormField label="Ignore errors">
                <Tri value={draft.ignore_errors} onChange={(v) => set({ ignore_errors: v })} />
              </FormField>
              <FormField label="Run once">
                <Tri value={draft.run_once} onChange={(v) => set({ run_once: v })} />
              </FormField>
              <FormField label="Changed when">
                <Input mono value={typeof draft.changed_when === "object" ? JSON.stringify(draft.changed_when) : String(draft.changed_when ?? "")} onChange={(e) => set({ changed_when: e.target.value === "" ? undefined : e.target.value === "false" ? false : e.target.value })} placeholder="false" />
              </FormField>
              <FormField label="Failed when">
                <Input mono value={typeof draft.failed_when === "object" ? JSON.stringify(draft.failed_when) : String(draft.failed_when ?? "")} onChange={(e) => set({ failed_when: e.target.value === "" ? undefined : e.target.value === "false" ? false : e.target.value })} />
              </FormField>
              <FormField label="Hide output (no_log)">
                <Tri value={draft.no_log} onChange={(v) => set({ no_log: v })} />
              </FormField>
            </div>

            {others.length > 0 && (
              <div className="space-y-2">
                <div className="text-xs font-medium text-muted">Other keywords</div>
                {others.map((k) => (
                  <div key={k} className="grid grid-cols-[150px_1fr_auto] gap-2 items-start">
                    <div className={cx("pt-1.5 font-mono text-[12px]", TASK_KEYWORDS.has(k) || k.startsWith("with_") ? "text-fg" : "text-warn")} title={TASK_KEYWORDS.has(k) ? "" : "unknown keyword"}>
                      {k}
                    </div>
                    <ValueEditor value={draft[k]} kind={typeof draft[k] === "object" && draft[k] !== null ? "yaml" : typeof draft[k] === "boolean" ? "bool" : typeof draft[k] === "number" ? "number" : "string"} onChange={(v) => set({ [k]: v })} />
                    <IconButton title="Remove" onClick={() => set({ [k]: undefined })}>
                      <X className="size-3.5" />
                    </IconButton>
                  </div>
                ))}
              </div>
            )}
            <AddKeyword exclude={Object.keys(draft)} onAdd={(k) => set({ [k]: k === "vars" || k === "environment" ? {} : "" })} />
          </>
        )}
      </div>
    </Drawer>
  );
}

function AddOption({ onAdd }: { onAdd: (n: string) => void }) {
  const [n, setN] = useState("");
  return (
    <div className="flex items-center gap-1">
      <Input mono value={n} onChange={(e) => setN(e.target.value.trim())} placeholder="option" className="w-48" onKeyDown={(e) => e.key === "Enter" && n && (onAdd(n), setN(""))} />
      <IconButton title="Add option" disabled={!n} onClick={() => (onAdd(n), setN(""))}>
        <Plus className="size-4" />
      </IconButton>
    </div>
  );
}

const EXTRA = ["vars", "environment", "until", "retries", "delay", "loop_control", "async", "poll", "throttle", "timeout", "check_mode", "diff", "args", "become_method", "remote_user", "any_errors_fatal"];

function AddKeyword({ exclude, onAdd }: { exclude: string[]; onAdd: (k: string) => void }) {
  const left = EXTRA.filter((k) => !exclude.includes(k));
  if (!left.length) return null;
  return (
    <Select value="" onChange={(e) => e.target.value && onAdd(e.target.value)} className="w-72">
      <option value="">+ More keywords (vars, environment, until, retries…)</option>
      {left.map((k) => (
        <option key={k} value={k}>
          {k}
        </option>
      ))}
    </Select>
  );
}
