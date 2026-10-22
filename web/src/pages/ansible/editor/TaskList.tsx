import { useState } from "react";
import { ArrowDown, ArrowUp, ChevronDown, ChevronRight, Copy, ExternalLink, GripVertical, Layers, Pencil, Plus, Trash2 } from "lucide-react";
import { Button, IconButton, cx } from "../../../components/ui";
import { useEditor } from "./context";
import { Pill } from "./fields";
import { TaskDrawer } from "./TaskDrawer";
import { argsSummary, asList, ensureSeq, getMap, getSeq, isBlock, moduleOf, moveItem, patchMap, shortModule, toNode } from "./model";
import type { Path, Task } from "./model";

/** the file an include/import points at, if it is in the project */
function includeTarget(t: Task, module: string, here: string, files: string[]): string | null {
  const m = shortModule(module).replace(/^ansible\.builtin\./, "");
  const arg = t[module];
  const file = typeof arg === "string" ? arg.split(/\s/)[0] : arg && typeof arg === "object" ? String((arg as Record<string, unknown>).file ?? (arg as Record<string, unknown>).name ?? "") : "";
  if (!file || file.includes("{{")) return null;
  if (m === "include_role" || m === "import_role") {
    const p = `roles/${file}/tasks/main.yml`;
    return files.includes(p) ? p : files.find((f) => f.startsWith(`roles/${file}/tasks/main.`)) ?? null;
  }
  if (!["include_tasks", "import_tasks", "import_playbook", "include_vars"].includes(m)) return null;
  const dir = here.split("/").slice(0, -1).join("/");
  const roleTasks = here.match(/^(.*roles\/[^/]+)\//)?.[1];
  for (const c of [dir ? `${dir}/${file}` : file, file, roleTasks ? `${roleTasks}/tasks/${file}` : ""]) {
    const norm = c.replace(/\/\.\//g, "/");
    if (norm && files.includes(norm)) return norm;
  }
  return null;
}

/**
 * TaskList shows the tasks (or handlers) of the list at path and edits
 * them: open one to change it, add, duplicate, delete, reorder (drag or
 * arrows). Blocks show their block/rescue/always lists inside.
 */
export function TaskList({ path, handler, empty }: { path: Path; handler?: boolean; empty?: string }) {
  const { doc, edit, readOnly } = useEditor();
  const seq = getSeq(doc, path);
  const items = ((seq?.toJSON() ?? []) as unknown[]).map((x) => (x && typeof x === "object" && !Array.isArray(x) ? (x as Task) : { name: String(x) }));
  const [open, setOpen] = useState<{ index: number | null; task: Task } | null>(null);
  const [drag, setDrag] = useState<number | null>(null);
  const [over, setOver] = useState<number | null>(null);

  const save = (t: Task) => {
    const index = open?.index ?? null;
    edit((d) => {
      if (index === null) ensureSeq(d, path).items.push(toNode(d, t));
      else {
        const m = getMap(d, [...path, index]);
        if (m) patchMap(d, m, t);
      }
    });
    setOpen(null);
  };
  const remove = (i: number) => {
    edit((d) => getSeq(d, path)?.items.splice(i, 1));
    setOpen(null);
  };

  return (
    <div className="space-y-1">
      {items.length === 0 && <div className="text-xs text-faint px-1 py-1.5">{empty ?? "No tasks yet."}</div>}
      {items.map((t, i) => (
        <div
          key={i}
          draggable={!readOnly}
          onDragStart={(e) => {
            e.stopPropagation();
            setDrag(i);
          }}
          onDragOver={(e) => {
            if (drag === null) return;
            e.preventDefault();
            e.stopPropagation();
            setOver(i);
          }}
          onDragEnd={() => {
            setDrag(null);
            setOver(null);
          }}
          onDrop={(e) => {
            e.preventDefault();
            e.stopPropagation();
            if (drag !== null && drag !== i) edit((d) => moveItem(getSeq(d, path)!, drag, i));
            setDrag(null);
            setOver(null);
          }}
          className={cx(over === i && drag !== null && drag !== i && "ring-1 ring-accent rounded-md")}
        >
          <TaskRow
            task={t}
            index={i}
            path={[...path, i]}
            handler={handler}
            first={i === 0}
            last={i === items.length - 1}
            onOpen={() => setOpen({ index: i, task: t })}
            onMove={(to) => edit((d) => moveItem(getSeq(d, path)!, i, to))}
            onDuplicate={() =>
              edit((d) => {
                const s = getSeq(d, path)!;
                s.items.splice(i + 1, 0, (s.items[i] as { clone: () => unknown }).clone() as never);
              })
            }
            onDelete={() => remove(i)}
          />
        </div>
      ))}
      {!readOnly && (
        <div className="flex gap-1.5 pt-1">
          <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setOpen({ index: null, task: { name: "" } })}>
            Add {handler ? "handler" : "task"}
          </Button>
          {!handler && (
            <Button size="sm" variant="ghost" icon={<Layers className="size-3.5" />} onClick={() => edit((d) => ensureSeq(d, path).items.push(toNode(d, { name: "New block", block: [] })))}>
              Add block
            </Button>
          )}
        </div>
      )}
      {open && (
        <TaskDrawer
          task={open.task}
          isNew={open.index === null}
          handler={handler}
          onSave={save}
          onDelete={open.index !== null ? () => remove(open.index!) : undefined}
          onClose={() => setOpen(null)}
        />
      )}
    </div>
  );
}

function TaskRow({
  task: t,
  index,
  path,
  handler,
  first,
  last,
  onOpen,
  onMove,
  onDuplicate,
  onDelete,
}: {
  task: Task;
  index: number;
  path: Path;
  handler?: boolean;
  first: boolean;
  last: boolean;
  onOpen: () => void;
  onMove: (to: number) => void;
  onDuplicate: () => void;
  onDelete: () => void;
}) {
  const { readOnly, files, openFile, path: here } = useEditor();
  const [expanded, setExpanded] = useState(true);
  const block = isBlock(t);
  const module = block ? "" : moduleOf(t);
  const target = module ? includeTarget(t, module, here, files) : null;
  const tags = asList(t.tags);
  const notify = asList(t.notify);
  return (
    <div className={cx("group rounded-md border bg-panel", block ? "border-line-strong" : "border-line")}>
      <div className="flex items-start gap-2 px-2 py-1.5 cursor-pointer hover:bg-panel-2 rounded-md" onClick={onOpen}>
        {!readOnly && <GripVertical className="size-3.5 mt-0.5 text-faint opacity-0 group-hover:opacity-100 cursor-grab shrink-0" />}
        {block ? (
          <button
            className="mt-0.5 text-faint hover:text-fg"
            onClick={(e) => {
              e.stopPropagation();
              setExpanded(!expanded);
            }}
          >
            {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
          </button>
        ) : (
          <span className="w-5 text-right text-[11px] text-faint tabular-nums mt-0.5 shrink-0">{index + 1}</span>
        )}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 min-w-0">
            <span className={cx("text-[13px] truncate", t.name ? "text-fg" : "text-faint italic")}>{String(t.name ?? (block ? "block" : "(no name)"))}</span>
            {block ? <Pill tone="accent">block</Pill> : module && <Pill tone="info" title={module}>{shortModule(module)}</Pill>}
            {target && (
              <button
                className="inline-flex items-center gap-0.5 text-[11px] text-accent hover:underline"
                onClick={(e) => {
                  e.stopPropagation();
                  openFile(target);
                }}
              >
                open <ExternalLink className="size-3" />
              </button>
            )}
          </div>
          {!block && argsSummary(t) && <div className="font-mono text-[11.5px] text-muted truncate mt-0.5" dir="ltr">{argsSummary(t)}</div>}
          {(t.when !== undefined || t.loop !== undefined || tags.length > 0 || notify.length > 0 || t.become !== undefined || t.register || t.delegate_to || Object.keys(t).some((k) => k.startsWith("with_"))) && (
            <div className="flex flex-wrap gap-1 mt-1">
              {t.when !== undefined && <Pill tone="warn" title={asList(t.when).join(" and ")}>when {asList(t.when).join(" and ")}</Pill>}
              {(t.loop !== undefined || Object.keys(t).some((k) => k.startsWith("with_"))) && <Pill>loop</Pill>}
              {t.register ? <Pill>→ {String(t.register)}</Pill> : null}
              {t.become !== undefined && <Pill tone={t.become ? "accent" : "neutral"}>{t.become ? `become${t.become_user ? " " + String(t.become_user) : ""}` : "no become"}</Pill>}
              {t.delegate_to ? <Pill>on {String(t.delegate_to)}</Pill> : null}
              {notify.map((n) => (
                <Pill key={"n" + n} tone="ok">⚡ {n}</Pill>
              ))}
              {tags.map((g) => (
                <Pill key={"t" + g}>#{g}</Pill>
              ))}
            </div>
          )}
        </div>
        {!readOnly && (
          <div className="flex items-center opacity-0 group-hover:opacity-100 shrink-0" onClick={(e) => e.stopPropagation()}>
            <IconButton title="Edit" onClick={onOpen}>
              <Pencil className="size-3.5" />
            </IconButton>
            <IconButton title="Move up" disabled={first} onClick={() => onMove(index - 1)}>
              <ArrowUp className="size-3.5" />
            </IconButton>
            <IconButton title="Move down" disabled={last} onClick={() => onMove(index + 1)}>
              <ArrowDown className="size-3.5" />
            </IconButton>
            <IconButton title="Duplicate" onClick={onDuplicate}>
              <Copy className="size-3.5" />
            </IconButton>
            <IconButton title="Delete" onClick={onDelete}>
              <Trash2 className="size-3.5" />
            </IconButton>
          </div>
        )}
      </div>
      {block && expanded && (
        <div className="pl-7 pr-2 pb-2 space-y-2" onClick={(e) => e.stopPropagation()}>
          {(["block", "rescue", "always"] as const)
            .filter((k) => k === "block" || Array.isArray(t[k]))
            .map((k) => (
              <div key={k}>
                <div className="text-[10.5px] uppercase tracking-wide text-faint mb-1">{k}</div>
                <TaskList path={[...path, k]} handler={handler} empty="Empty." />
              </div>
            ))}
          {!readOnly && !Array.isArray(t.rescue) && <AddSection path={path} k="rescue" />}
        </div>
      )}
    </div>
  );
}

function AddSection({ path, k }: { path: Path; k: string }) {
  const { edit } = useEditor();
  return (
    <div className="flex gap-2">
      <button className="text-[11px] text-muted hover:text-accent" onClick={() => edit((d) => ensureSeq(d, [...path, k]))}>
        + rescue
      </button>
      <button className="text-[11px] text-muted hover:text-accent" onClick={() => edit((d) => ensureSeq(d, [...path, "always"]))}>
        + always
      </button>
    </div>
  );
}
