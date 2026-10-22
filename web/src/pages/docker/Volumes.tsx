import { useRef, useState } from "react";
import { ArrowLeft, Database, Download, File, FileSymlink, Folder, FolderPlus, HardDrive, Pencil, Plus, RefreshCw, Trash2, Upload } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { DockerVolume, VolumeEntry } from "../../api/types";
import { Badge, Button, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, SearchInput, Table, Td, Textarea, Th, Toggle, Tr, cx } from "../../components/ui";
import { Modal, useConfirm, useToast } from "../../components/overlay";
import { useAsync } from "../../hooks";
import { fmtBytes, timeAgo } from "../../lib/format";
import { useDocker, useRun } from "./shared";

export function Volumes({ volume, path, select }: { volume: string; path: string; select: (volume: string, path?: string) => void }) {
  if (volume) return <Browser vol={volume} path={path || "/"} onPath={(p) => select(volume, p)} onBack={() => select("")} />;
  return <VolumeList onOpen={(v) => select(v, "/")} />;
}

function VolumeList({ onOpen }: { onOpen: (v: string) => void }) {
  const { ws, host, openContainer } = useDocker();
  const list = useAsync(() => api.volumes(ws, host), [ws, host]);
  const [q, setQ] = useState("");
  const [anon, setAnon] = useState(false);
  const [sizes, setSizes] = useState<Record<string, number | string>>({});
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const { busy, run } = useRun();
  const confirm = useConfirm();
  const rows = (list.data ?? []).filter((v) => (anon || !v.anonymous) && (!q || v.name.includes(q) || v.project?.includes(q)));
  const hiddenAnon = (list.data ?? []).filter((v) => v.anonymous).length;

  async function size(v: DockerVolume) {
    setSizes((s) => ({ ...s, [v.name]: "…" }));
    try {
      const r = await api.volumeSize(ws, host, v.name);
      setSizes((s) => ({ ...s, [v.name]: r.bytes }));
    } catch (e) {
      setSizes((s) => ({ ...s, [v.name]: errMsg(e) }));
    }
  }
  async function remove(v: DockerVolume) {
    const ok = await confirm({
      title: `Delete volume ${v.name}?`,
      message: v.containers.length ? `It is used by ${v.containers.join(", ")}; docker refuses while a container (even stopped) uses it.` : "Its data is deleted for good.",
      danger: true,
      confirmLabel: "Delete",
      typeToConfirm: v.name,
    });
    if (ok) await run(`rm:${v.name}`, `Delete volume ${v.name}`, () => api.volumeRemove(ws, host, v.name), () => list.reload());
  }

  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-4">
        <SearchInput value={q} onChange={setQ} placeholder="Filter volumes…" className="w-64" />
        {hiddenAnon > 0 && <Toggle checked={anon} onChange={setAnon} label={`Anonymous volumes (${hiddenAnon})`} />}
        <div className="flex-1" />
        <Button size="sm" icon={<Plus className="size-3.5" />} onClick={() => setCreating(true)}>
          New volume
        </Button>
        <IconButton title="Refresh" onClick={() => void list.reload()}>
          <RefreshCw className={cx("size-4", list.loading && "animate-spin")} />
        </IconButton>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {list.error && <ErrorBox error={list.error} />}
        {!list.data && list.loading && <Loading />}
        {list.data && rows.length === 0 && <EmptyState icon={<Database />} title="No volumes" />}
        {rows.length > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>Volume</Th>
                <Th>Driver</Th>
                <Th>Used by</Th>
                <Th>Size</Th>
                <Th className="w-[1%]" />
              </tr>
            </thead>
            <tbody>
              {rows.map((v) => (
                <Tr key={v.name}>
                  <Td>
                    <button className="font-medium font-mono text-[12.5px] hover:text-accent text-left break-all" onClick={() => onOpen(v.name)}>
                      {v.anonymous ? v.name.slice(0, 16) + "…" : v.name}
                    </button>
                    <div className="flex gap-1 mt-0.5">
                      {v.project && <Badge>compose {v.project}</Badge>}
                      {v.stack && <Badge>stack {v.stack}</Badge>}
                    </div>
                  </Td>
                  <Td className="text-muted">{v.driver}</Td>
                  <Td>
                    <div className="flex flex-wrap gap-1">
                      {v.containers.length === 0 && <span className="text-faint">unused</span>}
                      {v.containers.map((c) => (
                        <button key={c} onClick={() => openContainer(c, c, "overview")}>
                          <Badge mono className="hover:border-accent">
                            {c}
                          </Badge>
                        </button>
                      ))}
                    </div>
                  </Td>
                  <Td>
                    {sizes[v.name] === undefined ? (
                      <button className="text-[11.5px] text-accent hover:underline" onClick={() => void size(v)}>
                        measure
                      </button>
                    ) : typeof sizes[v.name] === "number" ? (
                      <span className="tabular-nums">{fmtBytes(sizes[v.name] as number)}</span>
                    ) : (
                      <span className="text-faint text-[11.5px]" title={String(sizes[v.name])}>
                        {String(sizes[v.name]).slice(0, 40)}
                      </span>
                    )}
                  </Td>
                  <Td>
                    <div className="flex items-center gap-0.5 justify-end">
                      <Button size="xs" variant="ghost" icon={<Folder className="size-3" />} onClick={() => onOpen(v.name)}>
                        Browse
                      </Button>
                      <IconButton title="Delete volume" className="hover:!text-err" disabled={!!busy} onClick={() => void remove(v)}>
                        <Trash2 className="size-4" />
                      </IconButton>
                    </div>
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </div>
      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="New volume"
        footer={
          <Button
            variant="primary"
            disabled={!name}
            loading={busy === "create"}
            onClick={() =>
              void run("create", `Create volume ${name}`, () => api.volumeCreate(ws, host, name), () => list.reload()).then((ok) => {
                if (ok) {
                  setCreating(false);
                  setName("");
                }
              })
            }
          >
            Create
          </Button>
        }
      >
        <FormField label="Name">
          <Input mono value={name} onChange={(e) => setName(e.target.value)} placeholder="shop_uploads" />
        </FormField>
      </Modal>
    </div>
  );
}

const ACCESS: Record<string, string> = {
  direct: "read directly on the host",
  sudo: "through sudo on the host",
  container: "through a short-lived helper container",
};

function join(dir: string, name: string) {
  return (dir.endsWith("/") ? dir : dir + "/") + name;
}

function Browser({ vol, path, onPath, onBack }: { vol: string; path: string; onPath: (p: string) => void; onBack: () => void }) {
  const { ws, host } = useDocker();
  const list = useAsync(() => api.volumeFiles(ws, host, vol, path), [ws, host, vol, path]);
  const [file, setFile] = useState<VolumeEntry | null>(null);
  const [mkdir, setMkdir] = useState(false);
  const [dirName, setDirName] = useState("");
  const [drag, setDrag] = useState(false);
  const [uploading, setUploading] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const toast = useToast();
  const confirm = useConfirm();
  const { run } = useRun();
  const parts = path.split("/").filter(Boolean);

  async function upload(files: File[]) {
    if (!files.length) return;
    const existing = new Set((list.data?.entries ?? []).map((e) => e.name));
    const clash = files.filter((f) => existing.has(f.name)).map((f) => f.name);
    if (clash.length) {
      const ok = await confirm({ title: "Replace files?", message: `${clash.join(", ")} already exist${clash.length === 1 ? "s" : ""} here and will be replaced.`, confirmLabel: "Replace", danger: true });
      if (!ok) return;
    }
    setUploading(files.length === 1 ? files[0].name : `${files.length} files`);
    try {
      const r = await api.volumeUpload(ws, host, vol, path, files);
      toast.success(`Uploaded ${r.saved.length} file${r.saved.length === 1 ? "" : "s"}`, `to ${vol}:${path}`);
    } catch (e) {
      toast.error("Upload failed", errMsg(e));
    } finally {
      setUploading(null);
      void list.reload();
    }
  }
  async function remove(e: VolumeEntry) {
    const ok = await confirm({
      title: `Delete ${e.name}?`,
      message: e.type === "dir" ? "The folder and everything in it is deleted from the volume." : "The file is deleted from the volume.",
      danger: true,
      confirmLabel: "Delete",
    });
    if (ok) await run(`rm:${e.name}`, `Delete ${e.name}`, () => api.volumeDelete(ws, host, vol, join(path, e.name)), () => list.reload());
  }

  return (
    <div
      className="h-full flex flex-col gap-2 min-h-0 relative"
      onDragOver={(e) => {
        e.preventDefault();
        setDrag(true);
      }}
      onDragLeave={(e) => {
        if (e.currentTarget === e.target) setDrag(false);
      }}
      onDrop={(e) => {
        e.preventDefault();
        setDrag(false);
        void upload([...e.dataTransfer.files]);
      }}
    >
      <div className="flex items-center gap-2 flex-wrap">
        <IconButton title="All volumes" onClick={onBack}>
          <ArrowLeft className="size-4" />
        </IconButton>
        <HardDrive className="size-4 text-muted" />
        <div className="flex items-center gap-0.5 font-mono text-[12.5px] min-w-0 flex-wrap">
          <button className="font-semibold hover:text-accent" onClick={() => onPath("/")}>
            {vol}
          </button>
          <span className="text-faint">:/</span>
          {parts.map((p, i) => (
            <span key={i} className="inline-flex items-center">
              <button className="hover:text-accent" onClick={() => onPath("/" + parts.slice(0, i + 1).join("/"))}>
                {p}
              </button>
              {i < parts.length - 1 && <span className="text-faint">/</span>}
            </span>
          ))}
        </div>
        {list.data?.access && (
          <Badge tone={list.data.access === "container" ? "info" : list.data.access === "sudo" ? "warn" : "neutral"} title={ACCESS[list.data.access]}>
            {list.data.access}
          </Badge>
        )}
        <div className="flex-1" />
        <input
          ref={input}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => {
            void upload([...(e.target.files ?? [])]);
            e.target.value = "";
          }}
        />
        <Button size="sm" icon={<Upload className="size-3.5" />} loading={!!uploading} onClick={() => input.current?.click()}>
          {uploading ? `Uploading ${uploading}…` : "Upload"}
        </Button>
        <Button size="sm" icon={<FolderPlus className="size-3.5" />} onClick={() => setMkdir(true)}>
          New folder
        </Button>
        <IconButton title="Refresh" onClick={() => void list.reload()}>
          <RefreshCw className={cx("size-4", list.loading && "animate-spin")} />
        </IconButton>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {list.error && <ErrorBox error={list.error} onRetry={() => void list.reload()} />}
        {!list.data && list.loading && <Loading />}
        {list.data && list.data.entries.length === 0 && (
          <EmptyState icon={<Folder />} title="Empty folder">
            Drop files here or use Upload.
          </EmptyState>
        )}
        {list.data && list.data.entries.length > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>Name</Th>
                <Th>Size</Th>
                <Th>Modified</Th>
                <Th>Mode</Th>
                <Th>Owner</Th>
                <Th className="w-[1%]" />
              </tr>
            </thead>
            <tbody>
              {path !== "/" && (
                <Tr onClick={() => onPath("/" + parts.slice(0, -1).join("/"))}>
                  <Td mono colSpan={6}>..</Td>
                </Tr>
              )}
              {list.data.entries.map((e) => (
                <Tr key={e.name} onClick={() => (e.type === "dir" ? onPath(join(path, e.name)) : e.type === "file" ? setFile(e) : undefined)}>
                  <Td>
                    <span className="inline-flex items-center gap-2 font-mono text-[12.5px]">
                      {e.type === "dir" ? <Folder className="size-4 text-accent" /> : e.type === "link" ? <FileSymlink className="size-4 text-muted" /> : <File className="size-4 text-muted" />}
                      {e.name}
                    </span>
                  </Td>
                  <Td className="tabular-nums text-muted">{e.type === "dir" ? "—" : fmtBytes(e.size)}</Td>
                  <Td className="text-muted" title={e.modified}>
                    {timeAgo(e.modified)}
                  </Td>
                  <Td mono className="text-muted">
                    {e.mode}
                  </Td>
                  <Td mono className="text-muted">
                    {e.owner}
                  </Td>
                  <Td>
                    <div className="flex items-center gap-0.5 justify-end" onClick={(ev) => ev.stopPropagation()}>
                      {e.type === "file" && (
                        <>
                          <IconButton title="View / edit" onClick={() => setFile(e)}>
                            <Pencil className="size-4" />
                          </IconButton>
                          <a href={api.volumeDownloadUrl(ws, host, vol, join(path, e.name))} title="Download" className="inline-flex size-7 items-center justify-center rounded-md text-muted hover:text-fg hover:bg-panel-3">
                            <Download className="size-4" />
                          </a>
                        </>
                      )}
                      <IconButton title="Delete" className="hover:!text-err" onClick={() => void remove(e)}>
                        <Trash2 className="size-4" />
                      </IconButton>
                    </div>
                  </Td>
                </Tr>
              ))}
            </tbody>
          </Table>
        )}
      </div>
      {drag && (
        <div className="absolute inset-0 rounded-lg border-2 border-dashed border-accent bg-accent/10 flex items-center justify-center text-accent font-medium pointer-events-none">
          Drop to upload into {vol}:{path}
        </div>
      )}
      {file && <FileModal vol={vol} path={join(path, file.name)} onClose={() => setFile(null)} onSaved={() => void list.reload()} />}
      <Modal
        open={mkdir}
        onClose={() => setMkdir(false)}
        title="New folder"
        footer={
          <Button
            variant="primary"
            disabled={!dirName}
            onClick={() =>
              void run("mkdir", `Create ${dirName}`, () => api.volumeMkdir(ws, host, vol, path, dirName), () => list.reload()).then((ok) => {
                if (ok) {
                  setMkdir(false);
                  setDirName("");
                }
              })
            }
          >
            Create
          </Button>
        }
      >
        <FormField label={`In ${vol}:${path}`}>
          <Input mono value={dirName} onChange={(e) => setDirName(e.target.value)} placeholder="config" autoFocus />
        </FormField>
      </Modal>
    </div>
  );
}

function FileModal({ vol, path, onClose, onSaved }: { vol: string; path: string; onClose: () => void; onSaved: () => void }) {
  const { ws, host } = useDocker();
  const f = useAsync(() => api.volumeFile(ws, host, vol, path), [ws, host, vol, path]);
  const [edit, setEdit] = useState<string | null>(null);
  const { busy, run } = useRun();
  const editable = f.data && !f.data.binary && !f.data.truncated;
  return (
    <Modal
      open
      onClose={onClose}
      title={`${vol}:${path}`}
      width={960}
      footer={
        <>
          <a href={api.volumeDownloadUrl(ws, host, vol, path)} className="mr-auto">
            <Button variant="ghost" icon={<Download className="size-3.5" />}>
              Download
            </Button>
          </a>
          {edit === null ? (
            <Button disabled={!editable} icon={<Pencil className="size-3.5" />} onClick={() => setEdit(f.data!.content)}>
              Edit
            </Button>
          ) : (
            <>
              <Button variant="ghost" onClick={() => setEdit(null)}>
                Cancel
              </Button>
              <Button
                variant="primary"
                loading={busy === "save"}
                disabled={edit === f.data?.content}
                onClick={() =>
                  void run("save", `Save ${path}`, () => api.volumeSaveFile(ws, host, vol, path, edit)).then((ok) => {
                    if (ok) {
                      onSaved();
                      void f.reload();
                      setEdit(null);
                    }
                  })
                }
              >
                Save
              </Button>
            </>
          )}
        </>
      }
    >
      {f.error && <ErrorBox error={f.error} />}
      {!f.data && !f.error && <Loading />}
      {f.data &&
        (f.data.binary ? (
          <EmptyState icon={<File />} title="Binary file">
            {fmtBytes(f.data.size)}{f.data.truncated ? "+" : ""} — download it to open.
          </EmptyState>
        ) : edit !== null ? (
          <Textarea mono dir="ltr" spellCheck={false} className="min-h-[60vh] !text-[12px] leading-relaxed" value={edit} onChange={(e) => setEdit(e.target.value)} />
        ) : (
          <>
            {f.data.truncated && <div className="text-xs text-warn mb-2">Showing the first 1 MB; download it for the rest. Larger files cannot be edited here.</div>}
            <pre className="rounded-md border border-line bg-panel-2 px-3 py-2 font-mono text-[11.5px] leading-relaxed overflow-auto whitespace-pre max-h-[65vh]" dir="ltr">
              {f.data.content || <span className="text-faint">(empty)</span>}
            </pre>
          </>
        ))}
    </Modal>
  );
}
