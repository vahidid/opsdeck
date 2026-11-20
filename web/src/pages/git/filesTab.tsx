import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ChevronDown,
  ChevronRight,
  FilePlus2,
  FileText,
  Folder,
  Pencil,
  Save,
  Trash2,
  X,
} from "lucide-react";
import { api, errMsg } from "../../api/client";
import { Badge, Button, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, SearchInput, Segmented, Textarea, cx } from "../../components/ui";
import { CodeView } from "../../components/CodeView";
import { Markdown } from "../../components/Markdown";
import { Modal, useConfirm, useToast } from "../../components/overlay";
import { useAsync } from "../../hooks";

// ---------------------------------------------------------------------------
// Files

type TreeNode = { name: string; path: string; dir: boolean; children: TreeNode[] };

function buildTree(files: string[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", dir: true, children: [] };
  for (const f of files) {
    const parts = f.split("/");
    let node = root;
    parts.forEach((p, i) => {
      const path = parts.slice(0, i + 1).join("/");
      const dir = i < parts.length - 1;
      let child = node.children.find((c) => c.name === p && c.dir === dir);
      if (!child) {
        child = { name: p, path, dir, children: [] };
        node.children.push(child);
      }
      node = child;
    });
  }
  const sort = (n: TreeNode) => {
    n.children.sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : a.name.localeCompare(b.name)));
    n.children.forEach(sort);
  };
  sort(root);
  return root.children;
}

export function FilesTab({ ws, repo, rev, onChanged }: { ws: string; repo: string; rev: number; onChanged: () => void }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [params, setParams] = useSearchParams();
  const sel = params.get("path") ?? "";
  const setSel = (p: string) => setParams({ tab: "files", path: p }, { replace: true });
  const files = useAsync(() => api.repoFiles(ws, repo), [ws, repo, rev]);
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [file, setFile] = useState<{ text: string; binary?: boolean; truncated?: boolean; size: number } | null>(null);
  const [fileErr, setFileErr] = useState("");
  const [edit, setEdit] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [newOpen, setNewOpen] = useState(false);
  const [preview, setPreview] = useState(true);
  const all = files.data?.files ?? [];
  const tree = useMemo(() => buildTree(q ? all.filter((f) => f.toLowerCase().includes(q.toLowerCase())) : all), [all, q]);
  const dirty = edit !== null && edit !== file?.text;

  useEffect(() => {
    if (!sel) {
      setFile(null);
      return;
    }
    let cancel = false;
    setFile(null);
    setFileErr("");
    setEdit(null);
    api
      .repoFile(ws, repo, sel)
      .then((f) => !cancel && setFile(f))
      .catch((e) => !cancel && setFileErr(errMsg(e)));
    // open the folders of the selected file
    const parts = sel.split("/");
    setOpen((o) => ({ ...o, ...Object.fromEntries(parts.slice(0, -1).map((_, i) => [parts.slice(0, i + 1).join("/"), true])) }));
    return () => {
      cancel = true;
    };
  }, [ws, repo, sel, rev]);

  async function choose(p: string) {
    if (dirty && !(await confirm({ title: "Discard unsaved changes?", danger: true, confirmLabel: "Discard" }))) return;
    setSel(p);
  }

  async function save(content = edit) {
    if (content === null) return;
    setSaving(true);
    try {
      await api.repoWrite(ws, repo, sel, content);
      setFile((f) => (f ? { ...f, text: content } : f));
      setEdit(null);
      onChanged();
      void files.reload();
    } catch (e) {
      toast.error("Save failed", errMsg(e));
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (!(await confirm({ title: `Delete ${sel}?`, message: "The file is removed from the working copy; commit to make it permanent, or discard to bring it back.", danger: true, confirmLabel: "Delete" }))) return;
    try {
      await api.repoDelete(ws, repo, sel);
      setSel("");
      onChanged();
      void files.reload();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  const isMd = /\.(md|markdown)$/i.test(sel);
  const renderNode = (n: TreeNode, depth: number): React.ReactNode =>
    n.dir ? (
      <div key={"d:" + n.path}>
        <button
          onClick={() => setOpen((o) => ({ ...o, [n.path]: !(o[n.path] ?? !!q) }))}
          className="flex w-full items-center gap-1 py-0.5 pr-2 text-left text-muted hover:text-fg"
          style={{ paddingInlineStart: depth * 12 + 6 }}
        >
          {(open[n.path] ?? !!q) ? <ChevronDown className="size-3 shrink-0" /> : <ChevronRight className="size-3 shrink-0" />}
          <Folder className="size-3.5 shrink-0 text-faint" />
          <span className="truncate text-[12.5px]">{n.name}</span>
        </button>
        {(open[n.path] ?? !!q) && n.children.map((c) => renderNode(c, depth + 1))}
      </div>
    ) : (
      <button
        key={"f:" + n.path}
        onClick={() => void choose(n.path)}
        className={cx("flex w-full items-center gap-1.5 py-0.5 pr-2 text-left rounded", sel === n.path ? "bg-panel-3 text-fg" : "text-muted hover:text-fg hover:bg-panel-2")}
        style={{ paddingInlineStart: depth * 12 + 22 }}
      >
        <FileText className={cx("size-3.5 shrink-0", sel === n.path ? "text-accent" : "text-faint")} />
        <span className="truncate text-[12.5px] font-mono">{n.name}</span>
      </button>
    );

  return (
    <div className="flex h-full min-h-0">
      <aside className="w-72 shrink-0 border-r border-line bg-panel flex flex-col">
        <div className="flex items-center gap-1 p-2 border-b border-line">
          <div className="flex-1">
            <SearchInput value={q} onChange={setQ} placeholder="Filter files…" />
          </div>
          <IconButton title="New file" onClick={() => setNewOpen(true)}>
            <FilePlus2 className="size-4" />
          </IconButton>
        </div>
        <div className="flex-1 overflow-y-auto py-1">
          {files.loading && !files.data && <Loading />}
          {files.error && <div className="p-2 text-xs text-err">{files.error}</div>}
          {tree.map((n) => renderNode(n, 0))}
          {files.data?.truncated && <div className="px-3 py-2 text-[11px] text-faint">Only the first 20 000 files are listed.</div>}
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col">
        {!sel ? (
          <EmptyState icon={<FileText />} title="Pick a file">
            Edits go to the working copy. Commit and push them from <b>Changes</b>.
          </EmptyState>
        ) : (
          <>
            <div className="flex items-center gap-2 px-4 h-11 border-b border-line shrink-0">
              <span className="font-mono text-[12.5px] truncate">{sel}</span>
              {dirty && <Badge tone="warn">unsaved</Badge>}
              {file?.truncated && <Badge tone="warn">first 2 MB</Badge>}
              <div className="flex-1" />
              {edit !== null ? (
                <>
                  <Button size="sm" variant="ghost" icon={<X className="size-3.5" />} onClick={() => setEdit(null)}>
                    Cancel
                  </Button>
                  <Button size="sm" variant="primary" icon={<Save className="size-3.5" />} loading={saving} disabled={!dirty} onClick={() => void save()}>
                    Save
                  </Button>
                </>
              ) : (
                <>
                  {isMd && (
                    <Segmented
                      size="xs"
                      value={preview ? "preview" : "source"}
                      onChange={(v) => setPreview(v === "preview")}
                      options={[
                        { id: "preview", label: "Preview" },
                        { id: "source", label: "Source" },
                      ]}
                    />
                  )}
                  <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} onClick={() => void remove()} disabled={!file}>
                    Delete
                  </Button>
                  <Button size="sm" icon={<Pencil className="size-3.5" />} onClick={() => setEdit(file?.text ?? "")} disabled={!file || file.binary || file.truncated}>
                    Edit
                  </Button>
                </>
              )}
            </div>
            <div className="flex-1 min-h-0 overflow-auto">
              {fileErr && <ErrorBox error={fileErr} />}
              {!file && !fileErr && <Loading />}
              {file &&
                (edit !== null ? (
                  <div className="h-full p-3">
                    <Textarea
                      mono
                      autoFocus
                      spellCheck={false}
                      value={edit}
                      onChange={(e) => setEdit(e.target.value)}
                      className="h-full resize-none text-[12.5px] leading-relaxed"
                      onKeyDown={(e) => {
                        if ((e.ctrlKey || e.metaKey) && e.key === "s") {
                          e.preventDefault();
                          void save();
                        }
                      }}
                    />
                  </div>
                ) : file.binary ? (
                  <EmptyState title="Binary file">{file.size} bytes</EmptyState>
                ) : isMd && preview ? (
                  <div className="max-w-3xl px-8 py-6">
                    <Markdown>{file.text}</Markdown>
                  </div>
                ) : (
                  <CodeView text={file.text} />
                ))}
            </div>
          </>
        )}
      </section>
      <NewFileModal
        open={newOpen}
        dir={sel.includes("/") ? sel.split("/").slice(0, -1).join("/") + "/" : ""}
        onClose={() => setNewOpen(false)}
        onCreate={async (p) => {
          try {
            await api.repoWrite(ws, repo, p, "");
            setNewOpen(false);
            await files.reload();
            setSel(p);
            onChanged();
          } catch (e) {
            toast.error("Cannot create", errMsg(e));
          }
        }}
      />
    </div>
  );
}

function NewFileModal({ open, dir, onClose, onCreate }: { open: boolean; dir: string; onClose: () => void; onCreate: (p: string) => Promise<void> }) {
  const [p, setP] = useState("");
  useEffect(() => {
    if (open) setP(dir);
  }, [open, dir]);
  const clean = p.trim().replace(/^\/+/, "");
  const valid = clean !== "" && !clean.endsWith("/") && !clean.split("/").some((s) => s === ".." || s.toLowerCase() === ".git" || s === "");
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New file"
      icon={<FilePlus2 />}
      width={460}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!valid} onClick={() => void onCreate(clean)}>
            Create
          </Button>
        </>
      }
    >
      <FormField label="Path" help="Relative to the repository root; folders are created as needed.">
        <Input mono autoFocus value={p} onChange={(e) => setP(e.target.value)} placeholder="k8s/overlays/prod/patch.yaml" />
      </FormField>
    </Modal>
  );
}
