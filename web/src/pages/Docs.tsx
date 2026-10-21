import { useEffect, useMemo, useState } from "react";
import { BookOpen, Brain, ClipboardList, FilePlus2, FileText, Pencil, Save, Sparkles, Trash2, X } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { DocInfo } from "../api/types";
import { useWs } from "../context/app";
import { Badge, Button, EmptyState, ErrorBox, FormField, Input, Loading, Textarea, cx } from "../components/ui";
import { Modal, useConfirm, useToast } from "../components/overlay";
import { Markdown } from "../components/Markdown";
import { useAsync } from "../hooks";
import { timeAgo } from "../lib/format";

const MEMORY = "__memory__";

export function DocsPage() {
  const { name: ws } = useWs();
  const list = useAsync(() => api.docs(ws), [ws]);
  const [sel, setSel] = useState<string>("OPSDECK.md");
  const [content, setContent] = useState<string | null>(null);
  const [edit, setEdit] = useState<string | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [saving, setSaving] = useState(false);
  const [newOpen, setNewOpen] = useState(false);
  const toast = useToast();
  const confirm = useConfirm();
  const editing = edit !== null;
  const dirty = editing && edit !== content;

  useEffect(() => {
    let cancel = false;
    setContent(null);
    setEdit(null);
    setLoadErr("");
    const p = sel === MEMORY ? api.memory(ws) : api.doc(ws, sel);
    p.then((d) => !cancel && setContent(d.content ?? "")).catch((e) => {
      if (cancel) return;
      if (sel === "OPSDECK.md") setContent("");
      else setLoadErr(errMsg(e));
    });
    return () => {
      cancel = true;
    };
  }, [ws, sel]);

  const groups = useMemo(() => {
    const docs = list.data ?? [];
    const g: Record<string, DocInfo[]> = { root: [], docs: [], skills: [], reports: [] };
    for (const d of docs) {
      if (d.path.startsWith("skills/")) g.skills.push(d);
      else if (d.path.startsWith("reports/")) g.reports.push(d);
      else if (d.path.startsWith("docs/")) g.docs.push(d);
      else g.root.push(d);
    }
    Object.values(g).forEach((l) => l.sort((a, b) => a.path.localeCompare(b.path)));
    g.reports.reverse(); // newest first (paths start with the date)
    if (!g.root.some((d) => d.path === "OPSDECK.md")) g.root.unshift({ path: "OPSDECK.md", title: "Agent instructions", size: 0, updated: "" });
    return g;
  }, [list.data]);

  async function choose(p: string) {
    if (dirty && !(await confirm({ title: "Discard unsaved changes?", danger: true, confirmLabel: "Discard" }))) return;
    setSel(p);
  }

  async function save() {
    if (edit === null) return;
    setSaving(true);
    try {
      if (sel === MEMORY) await api.saveMemory(ws, edit);
      else await api.saveDoc(ws, sel, edit);
      setContent(edit);
      setEdit(null);
      toast.success("Saved", sel === MEMORY ? "Agent memory" : sel);
      void list.reload();
    } catch (e) {
      toast.error("Save failed", errMsg(e));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!(await confirm({ title: `Delete ${sel}?`, danger: true, confirmLabel: "Delete" }))) return;
    try {
      await api.deleteDoc(ws, sel);
      toast.success("Deleted", sel);
      setSel("OPSDECK.md");
      void list.reload();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  const title = sel === MEMORY ? "Agent memory" : sel;
  const placeholderFor = (p: string) =>
    p === MEMORY
      ? "Facts the agent should remember about this workspace (it can also write here)."
      : p === "OPSDECK.md"
        ? "# Instructions for the agent\n\nDescribe this project, conventions, what is safe to do, who to contact…"
        : "";

  return (
    <div className="flex h-full min-h-0">
      <aside className="w-64 shrink-0 border-r border-line bg-panel flex flex-col">
        <div className="flex items-center justify-between px-3 h-12 border-b border-line">
          <span className="font-semibold flex items-center gap-2">
            <BookOpen className="size-4 text-muted" /> Docs
          </span>
          <Button size="xs" variant="ghost" icon={<FilePlus2 className="size-3.5" />} onClick={() => setNewOpen(true)}>
            New
          </Button>
        </div>
        <div className="flex-1 overflow-y-auto p-2 space-y-3">
          {list.loading && !list.data && <Loading />}
          <Group label="Workspace">
            {groups.root.map((d) => (
              <DocLink key={d.path} active={sel === d.path} onClick={() => void choose(d.path)} icon={<FileText />} label={d.path} sub={d.path === "OPSDECK.md" ? "agent instructions" : undefined} />
            ))}
            <DocLink active={sel === MEMORY} onClick={() => void choose(MEMORY)} icon={<Brain />} label="Memory" sub="what the agent learned" />
          </Group>
          <Group label="Documentation">
            {groups.docs.length === 0 && <div className="text-[11px] text-faint px-2">No docs yet</div>}
            {groups.docs.map((d) => (
              <DocLink key={d.path} active={sel === d.path} onClick={() => void choose(d.path)} icon={<FileText />} label={d.path.replace(/^docs\//, "")} />
            ))}
          </Group>
          <Group label="Reports">
            {groups.reports.length === 0 && <div className="text-[11px] text-faint px-2">Analyses and results the agent saves</div>}
            {groups.reports.map((d) => (
              <DocLink
                key={d.path}
                active={sel === d.path}
                onClick={() => void choose(d.path)}
                icon={<ClipboardList />}
                label={d.title}
                sub={d.path.match(/(\d{4}-\d{2}-\d{2})-(\d{2})(\d{2})/)?.slice(1).join(" ").replace(/ (\d{2}) (\d{2})$/, " $1:$2") ?? d.path}
              />
            ))}
          </Group>
          <Group label="Skills">
            {groups.skills.length === 0 && <div className="text-[11px] text-faint px-2">Reusable procedures for the agent</div>}
            {groups.skills.map((d) => (
              <DocLink key={d.path} active={sel === d.path} onClick={() => void choose(d.path)} icon={<Sparkles />} label={d.path.replace(/^skills\//, "")} sub={d.title} />
            ))}
          </Group>
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col">
        <div className="flex items-center gap-2 px-5 h-12 border-b border-line shrink-0">
          <span className="font-mono text-[13px] truncate">{title}</span>
          {dirty && <Badge tone="warn">unsaved</Badge>}
          {list.data?.find((d) => d.path === sel)?.updated && (
            <span className="text-[11px] text-faint">updated {timeAgo(list.data.find((d) => d.path === sel)!.updated)}</span>
          )}
          <div className="flex-1" />
          {editing ? (
            <>
              <Button size="sm" variant="ghost" icon={<X className="size-3.5" />} onClick={() => setEdit(null)}>
                Cancel
              </Button>
              <Button size="sm" variant="primary" icon={<Save className="size-3.5" />} onClick={save} loading={saving} disabled={!dirty}>
                Save
              </Button>
            </>
          ) : (
            <>
              {sel !== MEMORY && sel !== "OPSDECK.md" && (
                <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} onClick={remove}>
                  Delete
                </Button>
              )}
              <Button size="sm" icon={<Pencil className="size-3.5" />} onClick={() => setEdit(content ?? "")} disabled={content === null}>
                Edit
              </Button>
            </>
          )}
        </div>
        <div className="flex-1 min-h-0 overflow-y-auto">
          {loadErr && <ErrorBox error={loadErr} />}
          {content === null && !loadErr && <Loading />}
          {content !== null &&
            (editing ? (
              <div className="h-full p-4">
                <Textarea
                  mono
                  autoFocus
                  value={edit ?? ""}
                  onChange={(e) => setEdit(e.target.value)}
                  placeholder={placeholderFor(sel)}
                  className="h-full resize-none text-[12.5px] leading-relaxed"
                  onKeyDown={(e) => {
                    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
                      e.preventDefault();
                      void save();
                    }
                  }}
                />
              </div>
            ) : content.trim() ? (
              <div className="max-w-3xl px-8 py-6">
                <Markdown>{stripFrontMatter(content)}</Markdown>
              </div>
            ) : (
              <EmptyState icon={<FileText />} title="Empty" action={<Button onClick={() => setEdit("")}>Start writing</Button>}>
                {placeholderFor(sel).split("\n").pop() || "This document is empty."}
              </EmptyState>
            ))}
        </div>
      </section>
      <NewDocModal
        open={newOpen}
        onClose={() => setNewOpen(false)}
        onCreate={async (path, body) => {
          try {
            await api.saveDoc(ws, path, body);
            await list.reload();
            setSel(path);
            setNewOpen(false);
          } catch (e) {
            toast.error("Cannot create", errMsg(e));
          }
        }}
      />
    </div>
  );
}

function Group({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="px-2 mb-1 text-[10.5px] uppercase tracking-wider text-faint">{label}</div>
      <div className="space-y-px">{children}</div>
    </div>
  );
}

function DocLink({ active, onClick, icon, label, sub }: { active: boolean; onClick: () => void; icon: React.ReactNode; label: string; sub?: string }) {
  return (
    <button
      onClick={onClick}
      className={cx(
        "w-full flex items-center gap-2 px-2 py-1 rounded-md text-left [&_svg]:size-3.5 [&_svg]:shrink-0",
        active ? "bg-panel-3 text-fg [&_svg]:text-accent" : "text-muted hover:bg-panel-2 hover:text-fg",
      )}
    >
      {icon}
      <span className="min-w-0">
        <span className="block truncate text-[12.5px] font-mono">{label}</span>
        {sub && <span className="block truncate text-[10.5px] text-faint">{sub}</span>}
      </span>
    </button>
  );
}

function NewDocModal({ open, onClose, onCreate }: { open: boolean; onClose: () => void; onCreate: (p: string, body: string) => Promise<void> }) {
  const [kind, setKind] = useState<"docs" | "skills">("docs");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) setName("");
  }, [open]);
  const clean = name.trim().replace(/^\/+/, "").replace(/\.md$/i, "");
  const path = `${kind}/${clean}.md`;
  const body =
    kind === "skills"
      ? `---\nname: ${clean.split("/").pop()}\ndescription: When to use this skill\n---\n\n# ${clean}\n\n1. Step one\n2. Step two\n`
      : `# ${clean.split("/").pop()}\n\n`;
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New document"
      width={460}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={busy}
            disabled={!/^[A-Za-z0-9][A-Za-z0-9/_.-]*$/.test(clean)}
            onClick={async () => {
              setBusy(true);
              await onCreate(path, body);
              setBusy(false);
            }}
          >
            Create
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-2">
          {(["docs", "skills"] as const).map((k) => (
            <button
              key={k}
              onClick={() => setKind(k)}
              className={cx("rounded-md border p-2.5 text-left", kind === k ? "border-accent bg-accent/8" : "border-line hover:border-line-strong")}
            >
              <div className="font-medium text-[13px]">{k === "docs" ? "Document" : "Skill"}</div>
              <div className="text-[11px] text-muted">{k === "docs" ? "Runbooks, topology notes, contacts" : "A procedure the agent can load and follow"}</div>
            </button>
          ))}
        </div>
        <FormField label="Name" help={<span className="font-mono">{path}</span>}>
          <Input mono autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder={kind === "docs" ? "runbooks/deploy" : "deploy-docker-app"} />
        </FormField>
      </div>
    </Modal>
  );
}

/** Reports start with YAML front matter (title, session, model); hide it when rendering. */
function stripFrontMatter(md: string): string {
  return md.replace(/^---\n[\s\S]*?\n---\n+/, "");
}
