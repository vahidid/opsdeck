import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ChevronDown,
  ChevronRight,
  Eye,
  EyeOff,
  FilePlus2,
  FileText,
  Folder,
  KeyRound,
  Lock,
  LockOpen,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Save,
  Search,
  ShieldCheck,
  TextCursorInput,
  Trash2,
  Unlock,
  X,
} from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { AnsibleFile } from "../../api/types";
import { Modal, useConfirm, useToast } from "../../components/overlay";
import { Badge, Button, CodeBlock, CopyButton, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, SearchInput, Segmented, Select, Textarea, Toggle, cx } from "../../components/ui";
import { useAsync } from "../../hooks";
import { fmtBytes } from "../../lib/format";
import { CodeView } from "../Git";
import { FileIcon, TYPE_LABEL, useAnsible } from "./shared";

type Filter = "all" | "playbook" | "inventory" | "vars" | "vault";
type Node = { name: string; path: string; dir: boolean; file?: AnsibleFile; children: Node[] };

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

const isYaml = (p: string) => /\.ya?ml$/i.test(p);

export function FilesTab() {
  const { ws, project: p, reload: reloadProject } = useAnsible();
  const confirm = useConfirm();
  const [params, setParams] = useSearchParams();
  const sel = params.get("path") ?? "";
  const setSel = (path: string) =>
    setParams(
      (prev) => {
        const n = new URLSearchParams(prev);
        if (path) n.set("path", path);
        else n.delete("path");
        return n;
      },
      { replace: true },
    );
  const files = useAsync(() => api.ansibleFiles(ws, p.name), [ws, p.name]);
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [newOpen, setNewOpen] = useState(false);
  const [stringOpen, setStringOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [dirty, setDirty] = useState(false);

  const all = files.data?.files ?? [];
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
  // small projects open fully; big ones open where the selected file is
  const expanded = (path: string) => open[path] ?? (!!q || filter !== "all" || all.length <= 40 || sel.startsWith(path + "/"));
  const current = all.find((f) => f.path === sel);

  async function choose(path: string) {
    if (dirty && !(await confirm({ title: "Discard unsaved changes?", danger: true, confirmLabel: "Discard" }))) return;
    setDirty(false);
    setSel(path);
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
        <button
          onClick={() => setOpen((o) => ({ ...o, [n.path]: !expanded(n.path) }))}
          className="flex w-full items-center gap-1 py-0.5 pr-2 text-left text-muted hover:text-fg"
          style={{ paddingInlineStart: depth * 12 + 6 }}
        >
          {expanded(n.path) ? <ChevronDown className="size-3 shrink-0" /> : <ChevronRight className="size-3 shrink-0" />}
          <Folder className="size-3.5 shrink-0 text-faint" />
          <span className="truncate text-[12.5px]">{n.name}</span>
        </button>
        {expanded(n.path) && n.children.map((c) => renderNode(c, depth + 1))}
      </div>
    ) : (
      <button
        key={"f:" + n.path}
        onClick={() => void choose(n.path)}
        title={n.file ? TYPE_LABEL[n.file.type] + (n.file.encrypted ? " · encrypted" : "") : undefined}
        className={cx("flex w-full items-center gap-1.5 py-0.5 pr-2 text-left rounded", sel === n.path ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2")}
        style={{ paddingInlineStart: depth * 12 + 22 }}
      >
        {n.file && <FileIcon f={n.file} className="size-3.5 shrink-0" />}
        <span className="truncate text-[12.5px] font-mono flex-1">{n.name}</span>
        {n.file?.inline ? <Lock className="size-3 text-warn shrink-0" /> : null}
      </button>
    );

  return (
    <div className="flex h-full min-h-0 rounded-lg border border-line bg-panel overflow-hidden">
      <aside className="w-72 shrink-0 border-r border-line flex flex-col">
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
          <IconButton title="New file" onClick={() => setNewOpen(true)}>
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
        {!sel ? (
          <EmptyState icon={<FileText />} title="Pick a file">
            Playbooks have a <Play className="inline size-3 text-accent" /> and vault files a <Lock className="inline size-3 text-warn" />. Encrypted files open with the vault password.
          </EmptyState>
        ) : (
          <FilePanel key={sel} path={sel} meta={current} onChanged={() => void changed()} onDeleted={() => { setSel(""); void changed(); }} onDirty={setDirty} onRenamed={(to) => { setSel(to); void changed(); }} />
        )}
      </section>
      <NewFileModal
        open={newOpen}
        dir={sel.includes("/") ? sel.split("/").slice(0, -1).join("/") + "/" : ""}
        vaultIds={p.vaultIds}
        onClose={() => setNewOpen(false)}
        onCreated={async (path) => {
          setNewOpen(false);
          await changed();
          setSel(path);
        }}
      />
      {stringOpen && <EncryptStringModal onClose={() => setStringOpen(false)} />}
      {searchOpen && (
        <SearchModal
          onClose={() => setSearchOpen(false)}
          onOpen={(path) => {
            setSearchOpen(false);
            void choose(path);
          }}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// One file

function FilePanel({
  path,
  meta,
  onChanged,
  onDeleted,
  onRenamed,
  onDirty,
}: {
  path: string;
  meta?: AnsibleFile;
  onChanged: () => void;
  onDeleted: () => void;
  onRenamed: (to: string) => void;
  onDirty: (d: boolean) => void;
}) {
  const { ws, project: p, goto, withPassword } = useAnsible();
  const toast = useToast();
  const confirm = useConfirm();
  const file = useAsync(() => api.ansibleFile(ws, p.name, path), [ws, p.name, path]);
  const f = file.data;
  // decrypted content: only in this component's memory, gone when the file changes
  const [plain, setPlain] = useState<string | null>(null);
  const [edit, setEdit] = useState<string | null>(null);
  const [busy, setBusy] = useState("");
  const [modal, setModal] = useState<"" | "encrypt" | "rekey" | "set" | "rename">("");
  const [raw, setRaw] = useState(false);
  const base = f?.encrypted ? plain : f?.text;
  const dirty = edit !== null && edit !== base;
  useEffect(() => onDirty(dirty), [dirty, onDirty]);

  async function act<T>(name: string, fn: () => Promise<T>): Promise<T | undefined> {
    setBusy(name);
    try {
      return await fn();
    } catch (e) {
      toast.error("Failed", errMsg(e));
    } finally {
      setBusy("");
    }
  }

  const decrypt = () =>
    act("view", async () => {
      const r = await withPassword((pw) => api.ansibleVault<{ text: string }>(ws, p.name, "view", { path, password: pw?.password, vaultId: pw?.vaultId }));
      if (r) setPlain(r.text);
      return r;
    });

  async function save() {
    if (edit === null) return;
    const ok = await act("save", async () => {
      if (f?.encrypted) {
        const r = await withPassword((pw) => api.ansibleVault(ws, p.name, "save", { path, text: edit, vaultId: f.vaultId, password: pw?.password }));
        if (!r) return false;
        setPlain(edit);
      } else {
        await api.ansibleWrite(ws, p.name, path, edit);
        await file.reload();
      }
      return true;
    });
    if (ok) {
      setEdit(null);
      onChanged();
      toast.success(f?.encrypted ? "Saved and encrypted" : "Saved");
    }
  }

  async function remove() {
    if (!(await confirm({ title: `Delete ${path}?`, message: p.source === "repo" ? "Removed from the working copy; commit to make it permanent." : "The file is removed from the project folder.", danger: true, confirmLabel: "Delete" })))
      return;
    if (await act("delete", () => api.ansibleDelete(ws, p.name, path))) onDeleted();
  }

  async function decryptFile() {
    if (
      !(await confirm({
        title: `Store ${path} decrypted?`,
        message: "Its secrets become readable on disk" + (p.source === "repo" ? " and in Git once committed" : "") + ". Usually you want View or Edit instead.",
        danger: true,
        confirmLabel: "Decrypt file",
      }))
    )
      return;
    const r = await act("decrypt", () => withPassword((pw) => api.ansibleVault(ws, p.name, "decrypt", { path, password: pw?.password, vaultId: pw?.vaultId ?? f?.vaultId })));
    if (r) {
      setPlain(null);
      await file.reload();
      onChanged();
    }
  }

  async function revealInline() {
    if (plain !== null) {
      setPlain(null);
      return;
    }
    await decrypt();
  }

  if (!f && file.loading) return <Loading />;
  if (!f) return <ErrorBox error={file.error ?? "not found"} onRetry={() => void file.reload()} />;
  const type = meta?.type ?? "file";
  const inline = f.inline ?? [];
  const editing = edit !== null;

  return (
    <>
      <div className="flex items-center gap-2 px-4 h-11 border-b border-line shrink-0">
        <FileIcon f={{ type, encrypted: f.encrypted, path }} />
        <span className="font-mono text-[12.5px] truncate">{path}</span>
        <Badge>{TYPE_LABEL[type]}</Badge>
        {f.encrypted && (
          <Badge tone="warn">
            <Lock className="size-3" /> vault {f.vaultId || "default"}
          </Badge>
        )}
        {inline.length > 0 && <Badge tone="warn">{inline.length} encrypted value{inline.length > 1 ? "s" : ""}</Badge>}
        {dirty && <Badge tone="warn">unsaved</Badge>}
        <span className="text-[11px] text-faint">{fmtBytes(f.size)}</span>
        <div className="flex-1" />
        {editing ? (
          <>
            <Button size="sm" variant="ghost" icon={<X className="size-3.5" />} onClick={() => setEdit(null)}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" icon={<Save className="size-3.5" />} loading={busy === "save"} disabled={!dirty} onClick={() => void save()}>
              {f.encrypted ? "Save encrypted" : "Save"}
            </Button>
          </>
        ) : (
          <>
            {type === "playbook" && (
              <Button size="sm" variant="primary" icon={<Play className="size-3.5" />} onClick={() => goto("run", { playbook: path })}>
                Run
              </Button>
            )}
            {f.encrypted ? (
              <>
                {plain === null ? (
                  <Button size="sm" icon={<Unlock className="size-3.5" />} loading={busy === "view"} onClick={() => void decrypt()}>
                    View
                  </Button>
                ) : (
                  <Button size="sm" variant="ghost" icon={<EyeOff className="size-3.5" />} onClick={() => setPlain(null)}>
                    Hide
                  </Button>
                )}
                <Button
                  size="sm"
                  icon={<Pencil className="size-3.5" />}
                  loading={busy === "view" && plain === null}
                  onClick={async () => {
                    const text = plain ?? (await decrypt())?.text;
                    if (text !== undefined) setEdit(text);
                  }}
                >
                  Edit
                </Button>
                {isYaml(path) && (
                  <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setModal("set")}>
                    Variable
                  </Button>
                )}
              </>
            ) : (
              <>
                {inline.length > 0 && (
                  <Button size="sm" icon={plain === null ? <Eye className="size-3.5" /> : <EyeOff className="size-3.5" />} loading={busy === "view"} onClick={() => void revealInline()}>
                    {plain === null ? "Show values" : "Hide values"}
                  </Button>
                )}
                <Button size="sm" icon={<Pencil className="size-3.5" />} onClick={() => setEdit(f.text)} disabled={f.binary || f.truncated}>
                  Edit
                </Button>
                {isYaml(path) && (type === "vars" || inline.length > 0) && (
                  <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setModal("set")} title="Add or change an encrypted variable (inline !vault)">
                    Secret
                  </Button>
                )}
              </>
            )}
            <FileMenu
              encrypted={!!f.encrypted}
              busy={busy}
              onEncrypt={() => setModal("encrypt")}
              onDecrypt={() => void decryptFile()}
              onRekey={() => setModal("rekey")}
              onRename={() => setModal("rename")}
              onDelete={() => void remove()}
              onRaw={f.encrypted ? () => setRaw(!raw) : undefined}
              raw={raw}
            />
          </>
        )}
      </div>
      <div className="flex-1 min-h-0 overflow-auto">
        {editing ? (
          <div className="h-full p-3 flex flex-col gap-2">
            {f.encrypted && (
              <div className="text-[11.5px] text-warn flex items-center gap-1.5">
                <LockOpen className="size-3.5" /> Decrypted in this window only; saving encrypts it again with vault id {f.vaultId || "default"}.
              </div>
            )}
            <Textarea
              mono
              autoFocus
              spellCheck={false}
              value={edit ?? ""}
              onChange={(e) => setEdit(e.target.value)}
              className="flex-1 resize-none text-[12.5px] leading-relaxed"
              dir="ltr"
              onKeyDown={(e) => {
                if ((e.ctrlKey || e.metaKey) && e.key === "s") {
                  e.preventDefault();
                  void save();
                }
              }}
            />
          </div>
        ) : f.binary ? (
          <EmptyState title="Binary file">{fmtBytes(f.size)}</EmptyState>
        ) : f.encrypted && !raw ? (
          plain === null ? (
            <EmptyState icon={<ShieldCheck />} title="Encrypted with Ansible Vault" action={
              <Button variant="primary" icon={<KeyRound className="size-3.5" />} loading={busy === "view"} onClick={() => void decrypt()}>
                View decrypted
              </Button>
            }>
              Vault id <span className="font-mono">{f.vaultId || "default"}</span>. The decrypted content is shown here only, never stored.
            </EmptyState>
          ) : (
            <div className="relative">
              <div className="sticky top-0 z-10 flex items-center gap-1.5 px-4 py-1 text-[11px] bg-warn/10 text-warn border-b border-warn/20">
                <LockOpen className="size-3" /> decrypted view
                <div className="flex-1" />
                <CopyButton text={plain} />
              </div>
              <CodeView text={plain} />
            </div>
          )
        ) : plain !== null && inline.length > 0 ? (
          <div className="relative">
            <div className="sticky top-0 z-10 flex items-center gap-1.5 px-4 py-1 text-[11px] bg-warn/10 text-warn border-b border-warn/20">
              <LockOpen className="size-3" /> encrypted values shown decrypted (read-only)
            </div>
            <CodeView text={plain} />
          </div>
        ) : (
          <>
            {f.truncated && <div className="px-4 py-1 text-[11px] text-warn border-b border-line">Only the first 2 MB are shown.</div>}
            <CodeView text={f.text} />
          </>
        )}
      </div>
      {modal === "encrypt" && (
        <EncryptModal
          path={path}
          onClose={() => setModal("")}
          onDone={async () => {
            setModal("");
            await file.reload();
            onChanged();
          }}
        />
      )}
      {modal === "rekey" && <RekeyModal path={path} vaultId={f.vaultId || "default"} onClose={() => setModal("")} onDone={() => { setModal(""); setPlain(null); void file.reload(); onChanged(); }} />}
      {modal === "set" && (
        <SetVarModal
          path={path}
          encrypted={!!f.encrypted}
          onClose={() => setModal("")}
          onDone={async () => {
            setModal("");
            setPlain(null);
            await file.reload();
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
    </>
  );
}

function FileMenu({
  encrypted,
  busy,
  raw,
  onEncrypt,
  onDecrypt,
  onRekey,
  onRename,
  onDelete,
  onRaw,
}: {
  encrypted: boolean;
  busy: string;
  raw: boolean;
  onEncrypt: () => void;
  onDecrypt: () => void;
  onRekey: () => void;
  onRename: () => void;
  onDelete: () => void;
  onRaw?: () => void;
}) {
  return (
    <div className="flex items-center gap-0.5 ml-1">
      {encrypted ? (
        <>
          <IconButton title="Change the vault password of this file" onClick={onRekey}>
            <RefreshCw className="size-4" />
          </IconButton>
          <IconButton title="Store decrypted (removes the encryption)" onClick={onDecrypt}>
            <LockOpen className={cx("size-4", busy === "decrypt" && "animate-pulse")} />
          </IconButton>
          {onRaw && (
            <IconButton title={raw ? "Hide the encrypted text" : "Show the encrypted text"} active={raw} onClick={onRaw}>
              <FileText className="size-4" />
            </IconButton>
          )}
        </>
      ) : (
        <IconButton title="Encrypt this file with Ansible Vault" onClick={onEncrypt}>
          <Lock className="size-4" />
        </IconButton>
      )}
      <IconButton title="Rename or move" onClick={onRename}>
        <TextCursorInput className="size-4" />
      </IconButton>
      <IconButton title="Delete" onClick={onDelete}>
        <Trash2 className="size-4" />
      </IconButton>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Dialogs

function VaultIdPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { project: p } = useAnsible();
  const ids = Array.from(new Set(["default", ...p.vaultIds]));
  const custom = !ids.includes(value);
  return (
    <FormField label="Vault id" help="Projects with one password use “default”. Other ids are labels for separate passwords (e.g. prod, staging).">
      <div className="flex gap-2">
        <Select value={custom ? "__custom" : value} onChange={(e) => onChange(e.target.value === "__custom" ? "" : e.target.value)} className="flex-1">
          {ids.map((id) => (
            <option key={id} value={id}>
              {id}
              {p.unlocked.includes(id) ? " · unlocked" : ""}
            </option>
          ))}
          <option value="__custom">Another id…</option>
        </Select>
        {custom && <Input mono autoFocus value={value} onChange={(e) => onChange(e.target.value.replace(/[;\s]/g, ""))} placeholder="prod" className="w-36" />}
      </div>
    </FormField>
  );
}

function EncryptModal({ path, onClose, onDone }: { path: string; onClose: () => void; onDone: () => void }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [vaultId, setVaultId] = useState(p.vaultIds[0] ?? "default");
  const [busy, setBusy] = useState(false);
  async function go() {
    setBusy(true);
    try {
      const r = await withPassword((pw) => api.ansibleVault(ws, p.name, "encrypt", { path, vaultId: vaultId || "default", password: pw?.password }));
      if (r) {
        toast.success(`${path} encrypted`);
        onDone();
      }
    } catch (e) {
      toast.error("Could not encrypt", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={`Encrypt ${path}`}
      icon={<Lock />}
      width={480}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} onClick={() => void go()}>
            Encrypt
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">The whole file is encrypted in place (ansible-vault encrypt). Ansible decrypts it when it runs.</p>
        <VaultIdPicker value={vaultId} onChange={setVaultId} />
      </div>
    </Modal>
  );
}

function RekeyModal({ path, vaultId, onClose, onDone }: { path: string; vaultId: string; onClose: () => void; onDone: () => void }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [newId, setNewId] = useState(vaultId);
  const [busy, setBusy] = useState(false);
  const ok = pw.length > 0 && pw === pw2;
  async function go() {
    setBusy(true);
    try {
      const r = await withPassword((cur) => api.ansibleVault(ws, p.name, "rekey", { path, vaultId, password: cur?.password, newPassword: pw, newVaultId: newId }));
      if (r) {
        toast.success("Password changed", `${path} is now encrypted with the new password. Other files with vault id ${vaultId} keep the old one.`);
        onDone();
      }
    } catch (e) {
      toast.error("Could not change the password", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title="Change the vault password"
      icon={<RefreshCw />}
      width={480}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!ok} onClick={() => void go()}>
            Re-encrypt
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">
          Re-encrypts <span className="font-mono">{path}</span> (ansible-vault rekey). You are asked for the current password if it is not unlocked.
        </p>
        <FormField label="New password">
          <Input type="password" autoComplete="new-password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} />
        </FormField>
        <FormField label="Repeat">
          <Input type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} />
        </FormField>
        {pw2 && pw !== pw2 && <div className="text-xs text-err">The passwords differ.</div>}
        <VaultIdPicker value={newId} onChange={setNewId} />
      </div>
    </Modal>
  );
}

function genPassword(n = 24): string {
  const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  const buf = new Uint32Array(n);
  crypto.getRandomValues(buf);
  return Array.from(buf, (x) => chars[x % chars.length]).join("");
}

function SetVarModal({ path, encrypted, onClose, onDone }: { path: string; encrypted: boolean; onClose: () => void; onDone: () => void }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [show, setShow] = useState(false);
  const [vaultId, setVaultId] = useState(p.vaultIds[0] ?? "default");
  const [busy, setBusy] = useState(false);
  const valid = /^[A-Za-z_][A-Za-z0-9_]*$/.test(key) && value !== "";
  async function go() {
    setBusy(true);
    try {
      const r = await withPassword((pw) => api.ansibleVault(ws, p.name, "set", { path, key, value, vaultId: vaultId || "default", password: pw?.password }));
      if (r) {
        toast.success(`${key} set`, encrypted ? "Inside the encrypted file." : "As an encrypted !vault value.");
        onDone();
      }
    } catch (e) {
      toast.error("Could not set it", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={encrypted ? "Set a variable" : "Add an encrypted variable"}
      icon={<KeyRound />}
      width={500}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={() => void go()}>
            Save
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">
          {encrypted ? (
            <>
              Sets a top-level variable inside <span className="font-mono">{path}</span>, which stays encrypted.
            </>
          ) : (
            <>
              Writes <span className="font-mono">name: !vault |…</span> into <span className="font-mono">{path}</span>; the rest of the file stays readable.
            </>
          )}{" "}
          An existing variable of that name is replaced.
        </p>
        <FormField label="Name">
          <Input mono autoFocus value={key} onChange={(e) => setKey(e.target.value.trim())} placeholder="db_password" />
        </FormField>
        <FormField label="Value">
          <div className="flex gap-2">
            <Input mono type={show ? "text" : "password"} autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)} className="flex-1" />
            <IconButton title={show ? "Hide" : "Show"} onClick={() => setShow(!show)}>
              {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </IconButton>
            <Button size="md" onClick={() => { setValue(genPassword()); setShow(true); }}>
              Generate
            </Button>
          </div>
        </FormField>
        {!encrypted && <VaultIdPicker value={vaultId} onChange={setVaultId} />}
      </div>
    </Modal>
  );
}

function EncryptStringModal({ onClose }: { onClose: () => void }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [vaultId, setVaultId] = useState(p.vaultIds[0] ?? "default");
  const [out, setOut] = useState("");
  const [busy, setBusy] = useState(false);
  async function go() {
    setBusy(true);
    try {
      const r = await withPassword((pw) => api.ansibleVault<{ text: string }>(ws, p.name, "encrypt-string", { name, value, vaultId: vaultId || "default", password: pw?.password }));
      if (r) setOut(r.text);
    } catch (e) {
      toast.error("Could not encrypt", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title="Encrypt a value"
      icon={<TextCursorInput />}
      width={620}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button variant="primary" loading={busy} disabled={!value} onClick={() => void go()}>
            Encrypt
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">Like ansible-vault encrypt_string: paste the result into any YAML variables file.</p>
        <div className="grid grid-cols-2 gap-2">
          <FormField label="Variable name (optional)">
            <Input mono value={name} onChange={(e) => setName(e.target.value.trim())} placeholder="api_token" />
          </FormField>
          <VaultIdPicker value={vaultId} onChange={setVaultId} />
        </div>
        <FormField label="Value">
          <Textarea mono rows={3} value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off" />
        </FormField>
        {out && <CodeBlock maxHeight={260}>{out}</CodeBlock>}
      </div>
    </Modal>
  );
}

function NewFileModal({ open, dir, vaultIds, onClose, onCreated }: { open: boolean; dir: string; vaultIds: string[]; onClose: () => void; onCreated: (p: string) => Promise<void> }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [path, setPath] = useState("");
  const [encrypted, setEncrypted] = useState(false);
  const [vaultId, setVaultId] = useState(vaultIds[0] ?? "default");
  const [template, setTemplate] = useState<"empty" | "playbook" | "vars">("empty");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setPath(dir);
      setEncrypted(false);
    }
  }, [open, dir]);
  const clean = path.trim().replace(/^\/+/, "");
  const valid = clean !== "" && !clean.endsWith("/") && !clean.split("/").some((s) => s === ".." || s.toLowerCase() === ".git" || s === "");
  const content =
    template === "playbook"
      ? "---\n- name: Describe what this play does\n  hosts: all\n  become: true\n  tasks:\n    - name: Example task\n      ansible.builtin.debug:\n        msg: \"hello from {{ inventory_hostname }}\"\n"
      : template === "vars"
        ? "---\n# variables\n"
        : "";
  async function create() {
    setBusy(true);
    try {
      if (encrypted) {
        const r = await withPassword((pw) => api.ansibleVault(ws, p.name, "save", { path: clean, text: content || "---\n", vaultId: vaultId || "default", password: pw?.password }));
        if (!r) return;
      } else {
        await api.ansibleWrite(ws, p.name, clean, content, true);
      }
      await onCreated(clean);
    } catch (e) {
      toast.error("Cannot create", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New file"
      icon={<FilePlus2 />}
      width={500}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={() => void create()}>
            Create
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <FormField label="Path" help="Relative to the project folder; folders are created as needed.">
          <Input mono autoFocus value={path} onChange={(e) => setPath(e.target.value)} placeholder="group_vars/prod/vault.yml" />
        </FormField>
        <FormField label="Start from">
          <Segmented
            value={template}
            onChange={setTemplate}
            options={[
              { id: "empty", label: "Empty" },
              { id: "playbook", label: "Playbook" },
              { id: "vars", label: "Variables" },
            ]}
          />
        </FormField>
        <Toggle checked={encrypted} onChange={setEncrypted} label="Encrypt with Ansible Vault" />
        {encrypted && <VaultIdPicker value={vaultId} onChange={setVaultId} />}
      </div>
    </Modal>
  );
}

function RenameModal({ path, onClose, onDone }: { path: string; onClose: () => void; onDone: (to: string) => void }) {
  const { ws, project: p } = useAnsible();
  const toast = useToast();
  const [to, setTo] = useState(path);
  const [busy, setBusy] = useState(false);
  async function go() {
    setBusy(true);
    try {
      await api.ansibleRename(ws, p.name, path, to.trim());
      onDone(to.trim());
    } catch (e) {
      toast.error("Cannot rename", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title="Rename"
      icon={<TextCursorInput />}
      width={480}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!to.trim() || to.trim() === path} onClick={() => void go()}>
            Rename
          </Button>
        </>
      }
    >
      <FormField label="New path">
        <Input mono autoFocus value={to} onChange={(e) => setTo(e.target.value)} onKeyDown={(e) => e.key === "Enter" && void go()} />
      </FormField>
    </Modal>
  );
}

function SearchModal({ onClose, onOpen }: { onClose: () => void; onOpen: (path: string) => void }) {
  const { ws, project: p } = useAnsible();
  const [q, setQ] = useState("");
  const [regex, setRegex] = useState(false);
  const [hits, setHits] = useState<{ path: string; line: number; text: string }[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  async function go() {
    if (!q) return;
    setBusy(true);
    setErr("");
    try {
      setHits(await api.ansibleSearch(ws, p.name, q, regex));
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal open onClose={onClose} title="Search in files" icon={<Search />} width={760}>
      <div className="space-y-3">
        <div className="flex gap-2 items-center">
          <Input mono autoFocus value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === "Enter" && void go()} placeholder="nginx_port" className="flex-1" />
          <Toggle checked={regex} onChange={setRegex} label="Regex" />
          <Button variant="primary" loading={busy} onClick={() => void go()}>
            Search
          </Button>
        </div>
        {err && <div className="text-xs text-err">{err}</div>}
        {hits && (
          <div className="max-h-[60vh] overflow-y-auto rounded-md border border-line divide-y divide-line">
            {hits.length === 0 && <div className="p-3 text-xs text-muted">No matches (vault files are not searched).</div>}
            {hits.map((h, i) => (
              <button key={i} onClick={() => onOpen(h.path)} className="w-full text-left px-3 py-1.5 hover:bg-panel-2 font-mono text-[11.5px] flex gap-2">
                <span className="text-accent shrink-0">
                  {h.path}:{h.line}
                </span>
                <span className="truncate text-muted">{h.text}</span>
              </button>
            ))}
          </div>
        )}
      </div>
    </Modal>
  );
}
