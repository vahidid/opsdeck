import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  AlertTriangle,
  CheckCheck,
  ChevronDown,
  ChevronRight,
  Code2,
  Eye,
  EyeOff,
  FilePlus2,
  FileText,
  Folder,
  KeyRound,
  LayoutList,
  Lock,
  LockOpen,
  Play,
  RefreshCw,
  Save,
  Search,
  ShieldCheck,
  TextCursorInput,
  Trash2,
  X,
} from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { AnsibleFile, AnsibleFileContent, AnsibleModuleDoc } from "../../api/types";
import { Modal, useConfirm, useToast } from "../../components/overlay";
import { Badge, Button, CodeBlock, EmptyState, ErrorBox, IconButton, Loading, SearchInput, Segmented, cx } from "../../components/ui";
import { useAsync, useLocalStorage } from "../../hooks";
import { fmtBytes } from "../../lib/format";
import { CodeView } from "../Git";
import { EncryptModal, EncryptStringModal, NewFileModal, RekeyModal, RenameModal, SearchModal, SetVarModal } from "./dialogs";
import { FileIcon, TYPE_LABEL, useAnsible } from "./shared";
import { EditorContext } from "./editor/context";
import type { EditorCtx } from "./editor/context";
import type { ModuleItem } from "./editor/CodeEditor";
import { Code } from "./editor/LazyCode";
import { InventoryView } from "./editor/InventoryView";
import { PlaybookView } from "./editor/PlaybookView";
import { RequirementsView } from "./editor/RequirementsView";
import { TaskList } from "./editor/TaskList";
import { VarsView } from "./editor/VarsView";
import { docToIni, iniToDoc } from "./editor/ini";
import { VIEW_LABEL, languageOf, parseYaml, stringify, viewOf, yamlProblems } from "./editor/model";
import type { View } from "./editor/model";

type Filter = "all" | "playbook" | "inventory" | "vars" | "vault";
type Node = { name: string; path: string; dir: boolean; file?: AnsibleFile; children: Node[] };

/** an open file: its text in the editor and as last saved */
type Buffer = {
  path: string;
  text: string;
  saved: string;
  meta: AnsibleFileContent;
  /** a vault file shown decrypted (text is the plaintext) */
  decrypted?: boolean;
  mode?: "visual" | "code";
};

function buildTree(files: AnsibleFile[]): Node[] {
  const root: Node = { name: "", path: "", dir: true, children: [] };
  for (const f of files) {
    const parts = f.path.split("/");
    let node = root;
    parts.forEach((p, i) => {
      const dir = i < parts.length - 1;
      const path = parts.slice(0, i + 1).join("/");
      let child = node.children.find((c) => c.name === p && c.dir === dir);
      if (!child) {
        child = { name: p, path, dir, children: [], file: dir ? undefined : f };
        node.children.push(child);
      }
      node = child;
    });
  }
  const sort = (n: Node) => {
    n.children.sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : a.name.localeCompare(b.name)));
    n.children.forEach(sort);
  };
  sort(root);
  return root.children;
}

const base = (p: string) => p.split("/").pop() ?? p;

/** a tab's label: the file name, with its folder when another open file has the same name */
function tabLabel(path: string, tabs: string[]): string {
  const b = base(path);
  if (tabs.filter((t) => base(t) === b).length < 2) return b;
  return path.split("/").slice(-2).join("/");
}

/**
 * EditorTab is the Ansible project's editor: a file tree, open files as
 * tabs, and each file in a visual view (playbooks, task files, inventories,
 * variables, requirements) or as code, both on the same text. Vault files
 * open decrypted with the vault password and are saved encrypted again.
 */
export function EditorTab() {
  const { ws, project: p, reload: reloadProject, withPassword } = useAnsible();
  const confirm = useConfirm();
  const [params, setParams] = useSearchParams();
  const active = params.get("path") ?? "";
  const setActive = useCallback(
    (path: string) =>
      setParams(
        (prev) => {
          const n = new URLSearchParams(prev);
          if (path) n.set("path", path);
          else n.delete("path");
          return n;
        },
        { replace: true },
      ),
    [setParams],
  );
  const files = useAsync(() => api.ansibleFiles(ws, p.name), [ws, p.name]);
  const modulesQ = useAsync(() => api.ansibleModules(ws, p.name).catch(() => [] as ModuleItem[]), [ws, p.name]);
  const docs = useRef(new Map<string, Promise<AnsibleModuleDoc | null>>());
  const moduleDoc = useCallback(
    (name: string) => {
      if (!docs.current.has(name)) docs.current.set(name, api.ansibleModuleDoc(ws, p.name, name).catch(() => null));
      return docs.current.get(name)!;
    },
    [ws, p.name],
  );
  const [tabs, setTabs] = useLocalStorage<string[]>(`opsdeck.ansible.tabs.${ws}.${p.name}`, []);
  const [buffers, setBuffers] = useState<Record<string, Buffer>>({});
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [newIn, setNewIn] = useState<string | null>(null);
  const [stringOpen, setStringOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);

  const all = files.data?.files ?? [];
  const paths = useMemo(() => all.map((f) => f.path), [all]);
  const roles = useMemo(() => Array.from(new Set(all.map((f) => /^roles\/([^/]+)\//.exec(f.path)?.[1]).filter(Boolean) as string[])).sort(), [all]);

  // the active file is always among the tabs
  useEffect(() => {
    if (active && !tabs.includes(active)) setTabs([...tabs, active]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active]);
  // a file that is gone (deleted, renamed outside) closes
  useEffect(() => {
    if (!files.data) return;
    const gone = tabs.filter((t) => !paths.includes(t) && !buffers[t]);
    if (gone.length) {
      setTabs(tabs.filter((t) => !gone.includes(t)));
      if (gone.includes(active)) setActive("");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [files.data]);

  const shown = useMemo(
    () =>
      all.filter((f) => {
        if (q && !f.path.toLowerCase().includes(q.toLowerCase())) return false;
        if (filter === "vault") return f.encrypted || (f.inline ?? 0) > 0;
        if (filter !== "all") return f.type === filter;
        return true;
      }),
    [all, q, filter],
  );
  const tree = useMemo(() => buildTree(shown), [shown]);
  const expanded = (path: string) => open[path] ?? (!!q || filter !== "all" || all.length <= 60 || active.startsWith(path + "/"));
  const dirty = (path: string) => !!buffers[path] && buffers[path].text !== buffers[path].saved;

  const setBuffer = useCallback((path: string, patch: Partial<Buffer> | ((b: Buffer) => Partial<Buffer>)) => {
    setBuffers((all) => {
      const b = all[path];
      if (!b) return all;
      return { ...all, [path]: { ...b, ...(typeof patch === "function" ? patch(b) : patch) } };
    });
  }, []);

  async function closeTab(path: string) {
    if (dirty(path) && !(await confirm({ title: `Discard the changes to ${base(path)}?`, danger: true, confirmLabel: "Discard" }))) return;
    const next = tabs.filter((t) => t !== path);
    setTabs(next);
    setBuffers((b) => {
      const n = { ...b };
      delete n[path];
      return n;
    });
    if (path === active) setActive(next[Math.max(0, tabs.indexOf(path) - 1)] ?? "");
  }

  async function changed() {
    await files.reload();
    void reloadProject();
  }

  const counts = {
    playbook: all.filter((f) => f.type === "playbook").length,
    inventory: all.filter((f) => f.type === "inventory").length,
    vars: all.filter((f) => f.type === "vars").length,
    vault: all.filter((f) => f.encrypted || (f.inline ?? 0) > 0).length,
  };

  const renderNode = (n: Node, depth: number): React.ReactNode =>
    n.dir ? (
      <div key={"d:" + n.path}>
        <div className="group flex items-center pr-1">
          <button
            onClick={() => setOpen((o) => ({ ...o, [n.path]: !expanded(n.path) }))}
            className="flex flex-1 min-w-0 items-center gap-1 py-0.5 text-left text-muted hover:text-fg"
            style={{ paddingInlineStart: depth * 12 + 6 }}
          >
            {expanded(n.path) ? <ChevronDown className="size-3 shrink-0" /> : <ChevronRight className="size-3 shrink-0" />}
            <Folder className="size-3.5 shrink-0 text-faint" />
            <span className="truncate text-[12.5px]">{n.name}</span>
          </button>
          <button title={`New file in ${n.path}/`} onClick={() => setNewIn(n.path + "/")} className="opacity-0 group-hover:opacity-100 text-faint hover:text-accent px-1">
            <FilePlus2 className="size-3.5" />
          </button>
        </div>
        {expanded(n.path) && n.children.map((c) => renderNode(c, depth + 1))}
      </div>
    ) : (
      <button
        key={"f:" + n.path}
        onClick={() => setActive(n.path)}
        title={n.file ? TYPE_LABEL[n.file.type] + (n.file.encrypted ? " · encrypted" : "") : undefined}
        className={cx("flex w-full items-center gap-1.5 py-0.5 pr-2 text-left rounded", active === n.path ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2")}
        style={{ paddingInlineStart: depth * 12 + 22 }}
      >
        {n.file && <FileIcon f={n.file} className="size-3.5 shrink-0" />}
        <span className="truncate text-[12.5px] font-mono flex-1">{n.name}</span>
        {dirty(n.path) && <span className="size-1.5 rounded-full bg-warn shrink-0" title="unsaved" />}
        {n.file?.inline ? <Lock className="size-3 text-warn shrink-0" /> : null}
      </button>
    );

  const ctxBase = { modules: modulesQ.data ?? [], moduleDoc, roles, files: paths, openFile: setActive };

  return (
    <div className="flex h-full min-h-0 rounded-lg border border-line bg-panel overflow-hidden">
      <aside className="w-64 shrink-0 border-r border-line flex flex-col">
        <div className="flex items-center gap-1 p-2 border-b border-line">
          <div className="flex-1">
            <SearchInput value={q} onChange={setQ} placeholder="Filter files…" />
          </div>
          <IconButton title="Search in files" onClick={() => setSearchOpen(true)}>
            <Search className="size-4" />
          </IconButton>
          <IconButton title="Encrypt a value (encrypt_string)" onClick={() => setStringOpen(true)}>
            <TextCursorInput className="size-4" />
          </IconButton>
          <IconButton title="New file" onClick={() => setNewIn(active.includes("/") ? active.split("/").slice(0, -1).join("/") + "/" : "")}>
            <FilePlus2 className="size-4" />
          </IconButton>
        </div>
        <div className="px-2 py-1.5 border-b border-line flex flex-wrap gap-1">
          {(
            [
              ["all", "All", all.length],
              ["playbook", "Playbooks", counts.playbook],
              ["inventory", "Inventory", counts.inventory],
              ["vars", "Vars", counts.vars],
              ["vault", "Vault", counts.vault],
            ] as [Filter, string, number][]
          )
            .filter(([id, , n]) => id === "all" || n > 0)
            .map(([id, label, n]) => (
              <button
                key={id}
                onClick={() => setFilter(id)}
                className={cx("rounded px-1.5 h-5 text-[11px] border", filter === id ? "bg-accent/15 text-accent border-accent/30" : "text-muted border-line hover:text-fg")}
              >
                {label} <span className="tabular-nums opacity-70">{n}</span>
              </button>
            ))}
        </div>
        <div className="flex-1 overflow-y-auto py-1">
          {files.loading && !files.data && <Loading />}
          {files.error && <div className="p-2 text-xs text-err">{files.error}</div>}
          {tree.map((n) => renderNode(n, 0))}
          {files.data?.truncated && <div className="px-3 py-2 text-[11px] text-faint">Only the first 5 000 files are listed.</div>}
        </div>
        <div className="px-3 py-1.5 border-t border-line text-[11px] text-faint truncate" title={p.root}>
          {p.root}
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col">
        {tabs.length > 0 && (
          <div className="flex items-stretch border-b border-line overflow-x-auto shrink-0 bg-panel-2/50">
            {tabs.map((t) => {
              const f = all.find((x) => x.path === t);
              return (
                <div
                  key={t}
                  onClick={() => setActive(t)}
                  onAuxClick={(e) => e.button === 1 && void closeTab(t)}
                  title={t}
                  className={cx(
                    "group flex items-center gap-1.5 pl-3 pr-1.5 h-9 border-r border-line cursor-pointer text-[12.5px] shrink-0",
                    t === active ? "bg-panel text-fg" : "text-muted hover:text-fg",
                  )}
                >
                  {f && <FileIcon f={f} className="size-3.5" />}
                  <span className="font-mono">{tabLabel(t, tabs)}</span>
                  {dirty(t) ? (
                    <span className="size-2 rounded-full bg-warn mx-1" />
                  ) : (
                    <button
                      onClick={(e) => {
                        e.stopPropagation();
                        void closeTab(t);
                      }}
                      className="opacity-0 group-hover:opacity-100 text-faint hover:text-fg p-0.5"
                    >
                      <X className="size-3" />
                    </button>
                  )}
                  {dirty(t) && (
                    <button
                      onClick={(e) => {
                        e.stopPropagation();
                        void closeTab(t);
                      }}
                      className="hidden group-hover:block text-faint hover:text-fg p-0.5 -ml-2"
                    >
                      <X className="size-3" />
                    </button>
                  )}
                </div>
              );
            })}
          </div>
        )}
        {!active ? (
          <EmptyState icon={<FileText />} title="Open a file">
            Playbooks, task files, inventories and variables open in a visual editor; everything can also be edited as code. Vault files open decrypted with the vault
            password and are saved encrypted again.
          </EmptyState>
        ) : (
          tabs.map((t) => (
            <div key={t} className={cx("flex-1 min-h-0 flex flex-col", t !== active && "hidden")}>
              <FileEditor
                path={t}
                active={t === active}
                meta={all.find((f) => f.path === t)}
                buffer={buffers[t]}
                setBuffer={(patch) => setBuffer(t, patch)}
                load={(b) => setBuffers((all) => ({ ...all, [t]: b }))}
                ctx={{ ...ctxBase, path: t }}
                onChanged={() => void changed()}
                onDeleted={() => {
                  setBuffers((b) => {
                    const n = { ...b };
                    delete n[t];
                    return n;
                  });
                  setTabs(tabs.filter((x) => x !== t));
                  setActive("");
                  void changed();
                }}
                onRenamed={(to) => {
                  setBuffers((b) => {
                    const n = { ...b };
                    delete n[t];
                    return n;
                  });
                  setTabs(tabs.map((x) => (x === t ? to : x)));
                  setActive(to);
                  void changed();
                }}
                withPassword={withPassword}
              />
            </div>
          ))
        )}
      </section>
      <NewFileModal
        open={newIn !== null}
        dir={newIn ?? ""}
        vaultIds={p.vaultIds}
        onClose={() => setNewIn(null)}
        onCreated={async (path) => {
          setNewIn(null);
          await changed();
          setActive(path);
        }}
      />
      {stringOpen && <EncryptStringModal onClose={() => setStringOpen(false)} />}
      {searchOpen && (
        <SearchModal
          onClose={() => setSearchOpen(false)}
          onOpen={(path) => {
            setSearchOpen(false);
            setActive(path);
          }}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// One open file

function FileEditor({
  path,
  active,
  meta,
  buffer: b,
  setBuffer,
  load,
  ctx,
  onChanged,
  onDeleted,
  onRenamed,
  withPassword,
}: {
  path: string;
  active: boolean;
  meta?: AnsibleFile;
  buffer?: Buffer;
  setBuffer: (patch: Partial<Buffer> | ((b: Buffer) => Partial<Buffer>)) => void;
  load: (b: Buffer) => void;
  ctx: Omit<EditorCtx, "doc" | "edit" | "readOnly">;
  onChanged: () => void;
  onDeleted: () => void;
  onRenamed: (to: string) => void;
  withPassword: ReturnType<typeof useAnsible>["withPassword"];
}) {
  const { ws, project: p, goto } = useAnsible();
  const toast = useToast();
  const confirm = useConfirm();
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");
  const [modal, setModal] = useState<"" | "encrypt" | "rekey" | "rename" | { secret: string | null }>("");
  const [check, setCheck] = useState<{ ok: boolean; output: string } | null>(null);
  const [revealed, setRevealed] = useState<string | null>(null);
  const textRef = useRef(b?.text ?? "");
  textRef.current = b?.text ?? "";

  const fetchFile = useCallback(async () => {
    setErr("");
    try {
      const m = await api.ansibleFile(ws, p.name, path);
      load({ path, text: m.text, saved: m.text, meta: m });
    } catch (e) {
      setErr(errMsg(e));
    }
  }, [ws, p.name, path, load]);

  useEffect(() => {
    if (!b) void fetchFile();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);

  const lockedVault = !!b?.meta.encrypted && !b.decrypted;
  const language = languageOf(path);
  const type = meta?.type ?? "file";
  const isIni = type === "inventory" && language === "ini";
  const doc = useMemo(() => {
    if (!b || lockedVault) return null;
    if (isIni) return iniToDoc(b.text);
    return language === "yaml" ? parseYaml(b.text) : null;
  }, [b?.text, lockedVault, isIni, language]); // eslint-disable-line react-hooks/exhaustive-deps
  const view: View = !b || lockedVault ? "code" : isIni ? "inventory-ini" : viewOf(path, type, doc);
  const problems = doc && !isIni ? yamlProblems(doc) : [];
  const yamlBroken = language === "yaml" && problems.some((x) => x.error);
  const visualAvailable = view !== "code" || (language === "yaml" && yamlBroken && viewOf(path, type, null) !== "code");
  const mode = b?.mode ?? (view !== "code" ? "visual" : "code");
  const isDirty = !!b && b.text !== b.saved;

  const edit = useCallback(
    (fn: (d: ReturnType<typeof parseYaml>) => void) => {
      if (isIni) {
        const d = iniToDoc(textRef.current);
        fn(d);
        setBuffer({ text: docToIni(d) });
        return;
      }
      const d = parseYaml(textRef.current);
      if (d.errors.length) {
        toast.error("The file has YAML errors", "Fix them in the Code view first.");
        return;
      }
      fn(d);
      setBuffer({ text: stringify(d) });
    },
    [isIni, setBuffer, toast],
  );

  async function decrypt() {
    setBusy("view");
    try {
      const r = await withPassword((pw) => api.ansibleVault<{ text: string }>(ws, p.name, "view", { path, password: pw?.password, vaultId: pw?.vaultId }));
      if (r) setBuffer({ text: r.text, saved: r.text, decrypted: true });
    } catch (e) {
      toast.error("Could not decrypt", errMsg(e));
    } finally {
      setBusy("");
    }
  }

  const save = useCallback(async () => {
    if (!b || b.text === b.saved || lockedVault) return;
    setBusy("save");
    try {
      if (b.meta.encrypted) {
        const r = await withPassword((pw) => api.ansibleVault(ws, p.name, "save", { path, text: b.text, vaultId: b.meta.vaultId, password: pw?.password }));
        if (!r) return;
      } else {
        await api.ansibleWrite(ws, p.name, path, b.text);
      }
      setBuffer({ saved: b.text });
      onChanged();
      toast.success(b.meta.encrypted ? `${base(path)} saved and encrypted` : `${base(path)} saved`);
    } catch (e) {
      toast.error("Save failed", errMsg(e));
    } finally {
      setBusy("");
    }
  }, [b, lockedVault, withPassword, ws, p.name, path, setBuffer, onChanged, toast]);

  // Ctrl/⌘-S in the visual view too
  useEffect(() => {
    if (!active) return;
    const h = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === "s") {
        e.preventDefault();
        void save();
      }
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [active, save]);

  async function remove() {
    if (!(await confirm({ title: `Delete ${path}?`, message: p.source === "repo" ? "Removed from the working copy; commit to make it permanent." : "The file is removed from the project folder.", danger: true, confirmLabel: "Delete" })))
      return;
    try {
      await api.ansibleDelete(ws, p.name, path);
      onDeleted();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  async function decryptFile() {
    if (!(await confirm({ title: `Store ${path} decrypted?`, message: "Its secrets become readable on disk" + (p.source === "repo" ? " and in Git once committed" : "") + ".", danger: true, confirmLabel: "Decrypt file" }))) return;
    try {
      const r = await withPassword((pw) => api.ansibleVault(ws, p.name, "decrypt", { path, password: pw?.password, vaultId: pw?.vaultId ?? b?.meta.vaultId }));
      if (r) {
        await fetchFile();
        onChanged();
      }
    } catch (e) {
      toast.error("Could not decrypt", errMsg(e));
    }
  }

  async function syntaxCheck() {
    setBusy("check");
    try {
      if (isDirty) await save();
      const r = await withPassword(() => api.ansibleCheck(ws, p.name, { kind: "syntax", playbook: path }));
      if (r) setCheck({ ok: r.ok, output: r.output || r.error || "" });
    } catch (e) {
      toast.error("Syntax check failed", errMsg(e));
    } finally {
      setBusy("");
    }
  }

  async function revealInline() {
    if (revealed !== null) return setRevealed(null);
    try {
      const r = await withPassword((pw) => api.ansibleVault<{ text: string }>(ws, p.name, "view", { path, password: pw?.password, vaultId: pw?.vaultId }));
      if (r) setRevealed(r.text);
    } catch (e) {
      toast.error("Could not decrypt", errMsg(e));
    }
  }

  async function openSecret(key: string | null) {
    if (isDirty) {
      if (!(await confirm({ title: "Save your changes first?", message: "Encrypted values are written to the file directly.", confirmLabel: "Save" }))) return;
      await save();
    }
    setModal({ secret: key });
  }

  if (err) return <ErrorBox error={err} onRetry={() => void fetchFile()} />;
  if (!b) return <Loading />;
  const inline = b.meta.inline ?? [];
  const editorCtx: EditorCtx = { ...ctx, doc: doc ?? parseYaml(""), edit, readOnly: false, setSecret: !b.meta.encrypted && language === "yaml" ? (k) => void openSecret(k) : undefined };

  return (
    <>
      <div className="flex items-center gap-2 px-3 h-11 border-b border-line shrink-0">
        <span className="font-mono text-[12.5px] truncate text-muted">{path}</span>
        {view !== "code" && <Badge tone="accent">{VIEW_LABEL[view]}</Badge>}
        {b.meta.encrypted && (
          <Badge tone="warn">
            {b.decrypted ? <LockOpen className="size-3" /> : <Lock className="size-3" />} vault {b.meta.vaultId || "default"}
          </Badge>
        )}
        {inline.length > 0 && <Badge tone="warn">{inline.length} encrypted value{inline.length > 1 ? "s" : ""}</Badge>}
        {isDirty && <Badge tone="warn">unsaved</Badge>}
        {problems.length > 0 && (
          <button onClick={() => setBuffer({ mode: "code" })} className="inline-flex">
            <Badge tone={yamlBroken ? "err" : "warn"}>
              <AlertTriangle className="size-3" /> {problems.length} problem{problems.length > 1 ? "s" : ""}
            </Badge>
          </button>
        )}
        <div className="flex-1" />
        {!lockedVault && visualAvailable && (
          <Segmented
            size="xs"
            value={mode}
            onChange={(m) => setBuffer({ mode: m })}
            options={[
              { id: "visual", label: <><LayoutList className="size-3" /> Visual</>, title: "Edit it as a form" },
              { id: "code", label: <><Code2 className="size-3" /> Code</>, title: "Edit the text" },
            ]}
          />
        )}
        {type === "playbook" && (
          <>
            <Button size="sm" variant="ghost" icon={<CheckCheck className="size-3.5" />} loading={busy === "check"} onClick={() => void syntaxCheck()}>
              Check
            </Button>
            <Button size="sm" variant="ghost" icon={<Play className="size-3.5" />} onClick={() => goto("run", { playbook: path })}>
              Run
            </Button>
          </>
        )}
        {inline.length > 0 && !b.meta.encrypted && (
          <IconButton title={revealed === null ? "Show the encrypted values decrypted" : "Hide the decrypted values"} onClick={() => void revealInline()}>
            {revealed === null ? <Eye className="size-4" /> : <EyeOff className="size-4" />}
          </IconButton>
        )}
        {b.meta.encrypted ? (
          <>
            {b.decrypted && (
              <IconButton title="Lock (hide the decrypted content)" onClick={async () => {
                if (isDirty && !(await confirm({ title: "Discard unsaved changes?", danger: true, confirmLabel: "Discard" }))) return;
                setBuffer({ text: b.meta.text, saved: b.meta.text, decrypted: false });
              }}>
                <Lock className="size-4" />
              </IconButton>
            )}
            <IconButton title="Change the vault password of this file" onClick={() => setModal("rekey")}>
              <RefreshCw className="size-4" />
            </IconButton>
            <IconButton title="Store decrypted (removes the encryption)" onClick={() => void decryptFile()}>
              <LockOpen className="size-4" />
            </IconButton>
          </>
        ) : (
          !b.meta.binary && (
            <IconButton title="Encrypt this file with Ansible Vault" onClick={async () => {
              if (isDirty) await save();
              setModal("encrypt");
            }}>
              <ShieldCheck className="size-4" />
            </IconButton>
          )
        )}
        <IconButton title="Rename or move" onClick={() => setModal("rename")}>
          <TextCursorInput className="size-4" />
        </IconButton>
        <IconButton title="Delete" onClick={() => void remove()}>
          <Trash2 className="size-4" />
        </IconButton>
        <Button size="sm" variant="primary" icon={<Save className="size-3.5" />} loading={busy === "save"} disabled={!isDirty} onClick={() => void save()} title="Ctrl/⌘-S">
          Save
        </Button>
      </div>
      <div className="flex-1 min-h-0 overflow-auto">
        {b.meta.binary ? (
          <EmptyState title="Binary file">{fmtBytes(b.meta.size)}</EmptyState>
        ) : b.meta.truncated ? (
          <EmptyState title="Large file">Only files up to 2 MB can be edited here.</EmptyState>
        ) : lockedVault ? (
          <EmptyState
            icon={<ShieldCheck />}
            title="Encrypted with Ansible Vault"
            action={
              <Button variant="primary" icon={<KeyRound className="size-3.5" />} loading={busy === "view"} onClick={() => void decrypt()}>
                Open decrypted
              </Button>
            }
          >
            Vault id <span className="font-mono">{b.meta.vaultId || "default"}</span>. It opens in this window only and is saved encrypted again.
          </EmptyState>
        ) : revealed !== null ? (
          <div>
            <div className="sticky top-0 z-10 flex items-center gap-1.5 px-4 py-1 text-[11px] bg-warn/10 text-warn border-b border-warn/20">
              <LockOpen className="size-3" /> encrypted values shown decrypted (read-only)
              <div className="flex-1" />
              <button className="underline" onClick={() => setRevealed(null)}>
                back to editing
              </button>
            </div>
            <CodeView text={revealed} />
          </div>
        ) : mode === "visual" && view !== "code" && !yamlBroken ? (
          <EditorContext.Provider value={editorCtx}>
            <div className="p-4 max-w-6xl">
              {b.decrypted && (
                <div className="mb-3 text-[11.5px] text-warn flex items-center gap-1.5">
                  <LockOpen className="size-3.5" /> Decrypted in this window only; saving encrypts it again with vault id {b.meta.vaultId || "default"}.
                </div>
              )}
              {view === "playbook" ? (
                <PlaybookView />
              ) : view === "tasks" ? (
                <TaskList path={[]} handler={/(^|\/)handlers\//.test(path)} empty="No tasks yet." />
              ) : view === "inventory" || view === "inventory-ini" ? (
                <InventoryView ini={view === "inventory-ini"} />
              ) : view === "requirements" ? (
                <RequirementsView />
              ) : (
                <VarsView />
              )}
            </div>
          </EditorContext.Provider>
        ) : (
          <div className="h-full flex flex-col">
            {mode === "visual" && yamlBroken && (
              <div className="px-4 py-1.5 text-[11.5px] text-err border-b border-err/20 bg-err/5">The YAML has errors; fix them here to use the visual editor again.</div>
            )}
            <div className="flex-1 min-h-0">
              <Code language={language} value={b.text} onChange={(v) => setBuffer({ text: v })} onSave={() => void save()} modules={ctx.modules} />
            </div>
          </div>
        )}
      </div>
      {modal === "encrypt" && (
        <EncryptModal
          path={path}
          onClose={() => setModal("")}
          onDone={async () => {
            setModal("");
            await fetchFile();
            onChanged();
          }}
        />
      )}
      {modal === "rekey" && (
        <RekeyModal
          path={path}
          vaultId={b.meta.vaultId || "default"}
          onClose={() => setModal("")}
          onDone={async () => {
            setModal("");
            await fetchFile();
            onChanged();
          }}
        />
      )}
      {modal === "rename" && (
        <RenameModal
          path={path}
          onClose={() => setModal("")}
          onDone={(to) => {
            setModal("");
            onRenamed(to);
          }}
        />
      )}
      {typeof modal === "object" && (
        <SetVarModal
          path={path}
          encrypted={!!b.meta.encrypted}
          initialKey={modal.secret ?? ""}
          onClose={() => setModal("")}
          onDone={async () => {
            setModal("");
            setRevealed(null);
            await fetchFile();
            onChanged();
          }}
        />
      )}
      <Modal open={!!check} onClose={() => setCheck(null)} title="Syntax check" width={720} icon={<CheckCheck />}>
        {check && (
          <div className="space-y-2">
            <Badge tone={check.ok ? "ok" : "err"}>{check.ok ? "no errors" : "failed"}</Badge>
            <CodeBlock maxHeight={480} tone={check.ok ? undefined : "err"}>
              {check.output.trim()}
            </CodeBlock>
          </div>
        )}
      </Modal>
    </>
  );
}
