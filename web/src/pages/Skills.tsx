import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { BookCopy, FileCode2, FileText, Library, Sparkles } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { Skill } from "../api/types";
import { useWs } from "../context/app";
import { useTabTitle } from "../context/tabs";
import { Badge, Button, EmptyState, ErrorBox, Loading, SearchInput, Toggle, cx } from "../components/ui";
import { Modal, useToast } from "../components/overlay";
import { Markdown } from "../components/Markdown";
import { useAsync } from "../hooks";

// Skills: the workspace's own procedures and opsdeck's built-in library of
// infrastructure / networking / DevOps skills. The agent sees all of them
// and loads one (load_skill) when a task matches.
export function SkillsPage() {
  const { name: ws, workspace, reload } = useWs();
  const toast = useToast();
  const list = useAsync(() => api.skills(ws), [ws, workspace?.settings.noBuiltinSkills]);
  const lib = useAsync(() => api.skillLibrary(), []);
  const [params, setParams] = useSearchParams();
  const sel = params.get("skill") ?? "";
  const file = params.get("file") ?? "";
  const select = (name: string, f = "") => setParams(f ? { skill: name, file: f } : { skill: name }, { replace: true });
  const [q, setQ] = useState("");
  const [content, setContent] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const [about, setAbout] = useState(false);
  useTabTitle(sel || "Skills");

  const builtinOff = !!workspace?.settings.noBuiltinSkills;
  // the library is shown even when it is off for the agent
  const all = useMemo(() => {
    const own = (list.data ?? []).filter((s) => !s.builtin);
    const names = new Set(own.map((s) => s.name));
    const builtin = (lib.data?.skills ?? []).map((s) => ({ ...s, overridden: names.has(s.name) }));
    return { own, builtin };
  }, [list.data, lib.data]);
  const current: Skill | undefined = all.own.find((s) => s.name === sel) ?? all.builtin.find((s) => s.name === sel);
  const match = (s: Skill) => !q || (s.name + " " + s.description + " " + (s.category ?? "")).toLowerCase().includes(q.toLowerCase());
  const groups = useMemo(() => {
    const g: Record<string, (Skill & { overridden?: boolean })[]> = {};
    for (const s of all.builtin.filter(match)) (g[s.category || "Other"] ??= []).push(s);
    return g;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [all.builtin, q]);

  useEffect(() => {
    if (!current) {
      setContent(null);
      return;
    }
    let cancel = false;
    setContent(null);
    setErr("");
    const p = current.builtin ? api.skillLibraryFile(current.name, file).then((r) => r.content) : file ? Promise.reject(new Error("open supporting files of workspace skills in Docs")) : api.doc(ws, current.path).then((r) => r.content);
    p.then((c) => !cancel && setContent(c)).catch((e) => !cancel && setErr(errMsg(e)));
    return () => {
      cancel = true;
    };
  }, [ws, current?.name, current?.builtin, current?.path, file]); // eslint-disable-line react-hooks/exhaustive-deps

  async function toggleLibrary(on: boolean) {
    if (!workspace) return;
    try {
      await api.saveWorkspace(ws, { ...workspace, settings: { ...workspace.settings, noBuiltinSkills: !on } });
      await reload();
    } catch (e) {
      toast.error("Could not save", errMsg(e));
    }
  }

  async function customize(s: Skill) {
    try {
      await api.skillCopy(ws, s.name);
      toast.success(`Copied ${s.name} into the workspace`, "Edit it under Docs → skills/; your copy replaces the built-in one.");
      await list.reload();
    } catch (e) {
      toast.error("Copy failed", errMsg(e));
    }
  }

  const body = content?.replace(/^---\n[\s\S]*?\n---\n+/, "") ?? "";
  const isMd = !file || file.endsWith(".md");
  return (
    <div className="flex max-md:flex-col h-full min-h-0">
      <aside className="w-80 max-md:w-full max-md:max-h-[42%] max-md:border-r-0 max-md:border-b shrink-0 border-r border-line bg-panel flex flex-col min-h-0">
        <div className="px-3 pt-3 pb-2 space-y-2 border-b border-line">
          <div className="font-semibold flex items-center gap-2">
            <Sparkles className="size-4 text-muted" /> Skills
          </div>
          <SearchInput value={q} onChange={setQ} placeholder="Search skills…" />
        </div>
        <div className="flex-1 overflow-y-auto p-2 space-y-3">
          {(list.loading || lib.loading) && !lib.data && <Loading />}
          <Group label="This workspace">
            {all.own.filter(match).length === 0 && <div className="text-[11px] text-faint px-2">Your own procedures: Docs → New → Skill, or customize a built-in one.</div>}
            {all.own.filter(match).map((s) => (
              <Item key={s.name} s={s} active={sel === s.name} onClick={() => select(s.name)} />
            ))}
          </Group>
          <div className="px-2 pt-1 flex items-center justify-between">
            <span className="text-[10.5px] uppercase tracking-wider text-faint flex items-center gap-1">
              <Library className="size-3" /> Built-in library
            </span>
            <button className="text-[10.5px] text-accent hover:underline" onClick={() => setAbout(true)}>
              sources
            </button>
          </div>
          <div className="px-2">
            <Toggle checked={!builtinOff} onChange={(v) => void toggleLibrary(v)} label={<span className="text-[11.5px]">The agent uses the library in this workspace</span>} />
          </div>
          {Object.entries(groups).map(([cat, items]) => (
            <Group key={cat} label={cat}>
              {items.map((s) => (
                <Item key={s.name} s={s} active={sel === s.name} onClick={() => select(s.name)} dim={builtinOff || s.overridden} note={s.overridden ? "customized" : undefined} />
              ))}
            </Group>
          ))}
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col">
        {!current ? (
          <EmptyState icon={<Sparkles />} title="Skills are procedures the agent loads when a task matches">
            The agent sees each skill's name and description and loads the full instructions (and its references and templates) when needed. Pick one to read it.
          </EmptyState>
        ) : (
          <>
            <div className="flex items-center gap-2 px-5 h-12 border-b border-line shrink-0">
              <span className="font-mono text-[13px] truncate">{current.name}</span>
              {current.builtin ? <Badge tone="accent">built-in</Badge> : <Badge tone="neutral">workspace</Badge>}
              {current.category && <Badge tone="neutral">{current.category}</Badge>}
              {file && <span className="font-mono text-[12px] text-muted truncate">/ {file}</span>}
              <div className="flex-1" />
              {current.builtin && !all.own.some((s) => s.name === current.name) && (
                <Button size="sm" icon={<BookCopy className="size-3.5" />} onClick={() => void customize(current)}>
                  Customize in this workspace
                </Button>
              )}
            </div>
            {(current.files ?? []).length > 0 && (
              <div className="flex items-center gap-1 px-5 py-1.5 border-b border-line overflow-x-auto text-[11.5px]">
                <FileTab active={!file} onClick={() => select(current.name)} icon={<FileText className="size-3" />}>
                  SKILL.md
                </FileTab>
                {current.builtin &&
                  current.files!.map((f) => (
                    <FileTab key={f} active={file === f} onClick={() => select(current.name, f)} icon={f.endsWith(".md") ? <FileText className="size-3" /> : <FileCode2 className="size-3" />}>
                      {f}
                    </FileTab>
                  ))}
              </div>
            )}
            <div className="flex-1 min-h-0 overflow-y-auto">
              {err && <ErrorBox error={err} />}
              {content === null && !err && <Loading />}
              {content !== null &&
                (isMd ? (
                  <div className="max-w-3xl px-8 py-6">
                    <div className="text-[12px] text-muted mb-4 border-s-2 border-line ps-3">{current.description}</div>
                    <Markdown>{body}</Markdown>
                  </div>
                ) : (
                  <pre className="px-6 py-5 font-mono text-[12px] leading-relaxed whitespace-pre-wrap break-all" dir="ltr">
                    {content}
                  </pre>
                ))}
            </div>
          </>
        )}
      </section>
      <Modal open={about} onClose={() => setAbout(false)} title="Built-in skill library" width={640}>
        <div className="text-[12.5px] whitespace-pre-wrap font-mono leading-relaxed">{lib.data?.attribution}</div>
      </Modal>
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

function Item({ s, active, onClick, dim, note }: { s: Skill; active: boolean; onClick: () => void; dim?: boolean; note?: string }) {
  return (
    <button
      onClick={onClick}
      title={s.description}
      className={cx("w-full text-left px-2 py-1 rounded-md", active ? "bg-panel-3" : "hover:bg-panel-2", dim && !active && "opacity-55")}
    >
      <span className={cx("block truncate font-mono text-[12.5px]", active ? "text-fg" : "text-muted")}>
        {s.name} {note && <span className="text-[10px] text-warn font-sans">{note}</span>}
      </span>
      <span className="block truncate text-[10.5px] text-faint">{s.description}</span>
    </button>
  );
}

function FileTab({ active, onClick, icon, children }: { active: boolean; onClick: () => void; icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      className={cx("inline-flex items-center gap-1 rounded px-2 h-6 font-mono whitespace-nowrap", active ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2")}
    >
      {icon}
      {children}
    </button>
  );
}
