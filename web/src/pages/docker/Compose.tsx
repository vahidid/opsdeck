import { useEffect, useState } from "react";
import { ArrowDownToLine, FileCode2, Layers, MoreHorizontal, Play, Plus, RefreshCw, RotateCw, Rocket, ScrollText, Square, SquareTerminal, Trash2 } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { ComposeContainer, ComposeProject } from "../../api/types";
import { Badge, Button, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, Select, Table, Tabs, Td, Textarea, Th, Toggle, Tr, cx } from "../../components/ui";
import { MenuItem, Modal, Popover, useConfirm } from "../../components/overlay";
import { useAsync, useInterval } from "../../hooks";
import { ComposeStatus, DockerLogs, StateBadge, shortPorts, useDocker, useRun } from "./shared";

type View = "services" | "logs" | "files";

const TEMPLATE = `services:
  app:
    image: nginx:alpine
    restart: unless-stopped
    ports:
      - "8080:80"
    volumes:
      - data:/usr/share/nginx/html

volumes:
  data:
`;

export function Compose({ project, view, select }: { project: string; view: View; select: (project: string, view?: View) => void }) {
  const { ws, host } = useDocker();
  const list = useAsync(() => api.composeProjects(ws, host), [ws, host]);
  useInterval(() => void list.reload(), 15000);
  const [creating, setCreating] = useState(false);
  const current = (list.data ?? []).find((p) => p.name === project);
  useEffect(() => {
    if (!project && list.data?.length) select(list.data[0].name, view);
  }, [project, list.data]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="h-full flex min-h-0 gap-4">
      <aside className="w-64 shrink-0 rounded-lg border border-line bg-panel flex flex-col min-h-0">
        <div className="flex items-center gap-2 px-3 h-10 border-b border-line">
          <span className="text-[12px] font-medium flex-1">Projects</span>
          <IconButton title="Refresh" onClick={() => void list.reload()}>
            <RefreshCw className={cx("size-3.5", list.loading && "animate-spin")} />
          </IconButton>
          <IconButton title="New project" onClick={() => setCreating(true)}>
            <Plus className="size-4" />
          </IconButton>
        </div>
        <div className="flex-1 overflow-y-auto p-1.5 space-y-px">
          {list.error && <div className="text-xs text-err p-2 whitespace-pre-wrap">{list.error}</div>}
          {!list.data && list.loading && <Loading />}
          {list.data?.length === 0 && <div className="text-[11.5px] text-faint p-2">No compose projects on {host}. Create one with +.</div>}
          {(list.data ?? []).map((p) => (
            <button key={p.name} onClick={() => select(p.name, view)} className={cx("w-full text-left px-2 py-1.5 rounded-md", p.name === project ? "bg-panel-3" : "hover:bg-panel-2")}>
              <div className="flex items-center gap-1.5">
                <Layers className="size-3.5 text-muted shrink-0" />
                <span className="font-mono text-[12.5px] truncate flex-1">{p.name}</span>
              </div>
              <div className="mt-1 pl-5">
                <ComposeStatus status={p.status} />
              </div>
            </button>
          ))}
        </div>
      </aside>
      <section className="flex-1 min-w-0 flex flex-col min-h-0">
        {current ? (
          <ProjectView key={current.name} p={current} view={view} onView={(v) => select(current.name, v)} reloadList={() => void list.reload()} onGone={() => select("")} />
        ) : project && list.data ? (
          <EmptyState icon={<Layers />} title={`No project ${project} on ${host}`} />
        ) : (
          !list.loading && (
            <EmptyState icon={<Layers />} title="Docker Compose projects" action={<Button icon={<Plus className="size-3.5" />} onClick={() => setCreating(true)}>New project</Button>}>
              Projects started with <span className="font-mono">docker compose</span> on this host appear here: their services, logs, compose files and .env.
            </EmptyState>
          )
        )}
      </section>
      {creating && (
        <NewProject
          onClose={() => setCreating(false)}
          onCreated={(name) => {
            setCreating(false);
            void list.reload().then(() => select(name, "services"));
          }}
        />
      )}
    </div>
  );
}

function ProjectView({ p, view, onView, reloadList, onGone }: { p: ComposeProject; view: View; onView: (v: View) => void; reloadList: () => void; onGone: () => void }) {
  const { ws, host, openContainer } = useDocker();
  const ps = useAsync(() => api.composePs(ws, host, p.name), [ws, host, p.name]);
  useInterval(() => void ps.reload(), 5000);
  const { busy, run } = useRun();
  const confirm = useConfirm();
  const [menu, setMenu] = useState(false);
  const after = async () => {
    await ps.reload();
    reloadList();
  };
  async function action(a: string, services: string[] = []) {
    const label = services.length ? `${a} ${services.join(", ")}` : `${a} ${p.name}`;
    if (a === "down" || a === "down-volumes" || a === "stop") {
      const ok = await confirm({
        title: a === "down-volumes" ? `Remove ${p.name} and its volumes?` : a === "down" ? `Take ${p.name} down?` : `Stop ${services.length ? services.join(", ") : p.name}?`,
        message:
          a === "down-volumes"
            ? "Containers, networks and the project's named volumes are deleted. The data in those volumes is lost."
            : a === "down"
              ? "Containers and networks are removed; named volumes and the compose files stay. Up brings it back."
              : "The containers stop until started again.",
        danger: true,
        confirmLabel: a === "down-volumes" ? "Delete everything" : a === "down" ? "Down" : "Stop",
        typeToConfirm: a === "down-volumes" ? p.name : undefined,
      });
      if (!ok) return;
    }
    const ok = await run(`${a}:${services.join(",")}`, label, () => api.composeAction(ws, host, p.name, a, services), after);
    if (ok && a.startsWith("down")) onGone();
  }
  const services = [...new Set((ps.data ?? []).map((c) => c.service))];
  return (
    <div className="flex flex-col min-h-0 h-full">
      <div className="flex items-center gap-3 pb-3 flex-wrap">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-mono text-[15px] font-semibold">{p.name}</span>
            <ComposeStatus status={p.status} />
          </div>
          <div className="text-[11.5px] text-faint font-mono truncate" title={p.configFiles.join("\n")}>
            {p.workingDir || "no compose file known"}
          </div>
        </div>
        <div className="flex-1" />
        <Button size="sm" variant="primary" icon={<Rocket className="size-3.5" />} loading={busy === "up:"} disabled={!!busy} onClick={() => void action("up")} title="docker compose up -d: create or update what changed">
          Up
        </Button>
        <Button size="sm" icon={<ArrowDownToLine className="size-3.5" />} loading={busy === "pull:"} disabled={!!busy} onClick={() => void action("pull")} title="Pull the images">
          Pull
        </Button>
        <Button size="sm" icon={<RotateCw className="size-3.5" />} loading={busy === "restart:"} disabled={!!busy} onClick={() => void action("restart")}>
          Restart
        </Button>
        <div className="relative">
          <IconButton title="More" onClick={() => setMenu((m) => !m)}>
            <MoreHorizontal className="size-4" />
          </IconButton>
          <Popover open={menu} onClose={() => setMenu(false)} className="right-0 mt-1 w-64">
            <MenuItem icon={<RefreshCw />} onClick={() => (setMenu(false), void action("recreate"))} hint="--force-recreate">
              Recreate containers
            </MenuItem>
            <MenuItem icon={<Play />} onClick={() => (setMenu(false), void action("start"))}>
              Start
            </MenuItem>
            <MenuItem icon={<Square />} onClick={() => (setMenu(false), void action("stop"))}>
              Stop
            </MenuItem>
            <MenuItem icon={<Trash2 />} danger onClick={() => (setMenu(false), void action("down"))}>
              Down (remove containers)
            </MenuItem>
            <MenuItem icon={<Trash2 />} danger onClick={() => (setMenu(false), void action("down-volumes"))}>
              Down and delete volumes
            </MenuItem>
          </Popover>
        </div>
      </div>
      <Tabs<View>
        className="shrink-0"
        value={view}
        onChange={onView}
        tabs={[
          { id: "services", label: "Services", count: services.length },
          { id: "logs", label: "Logs" },
          { id: "files", label: "Files" },
        ]}
      />
      <div className="flex-1 min-h-0 pt-3">
        {view === "services" && (
          <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
            {ps.error && <ErrorBox error={ps.error} />}
            {!ps.data && ps.loading && <Loading />}
            {ps.data && ps.data.length === 0 && <EmptyState title="No containers" icon={<Layers />}>The project has no containers right now. Up creates them.</EmptyState>}
            {ps.data && ps.data.length > 0 && (
              <Table className="flex-1">
                <thead>
                  <tr>
                    <Th>Service</Th>
                    <Th>Container</Th>
                    <Th>Image</Th>
                    <Th>State</Th>
                    <Th>Status</Th>
                    <Th>Ports</Th>
                    <Th className="w-[1%]" />
                  </tr>
                </thead>
                <tbody>
                  {ps.data.map((c: ComposeContainer) => (
                    <Tr key={c.id || c.name}>
                      <Td className="font-medium">{c.service}</Td>
                      <Td mono>
                        <button className="hover:text-accent" onClick={() => openContainer(c.id || c.name, c.name, "overview")}>
                          {c.name}
                        </button>
                      </Td>
                      <Td mono className="max-w-60 truncate" title={c.image}>
                        {c.image}
                      </Td>
                      <Td>
                        <StateBadge state={c.state} health={c.health} />
                        {c.state === "exited" && c.exitCode !== 0 && (
                          <Badge tone="err" className="ml-1">
                            code {c.exitCode}
                          </Badge>
                        )}
                      </Td>
                      <Td className="text-muted whitespace-nowrap">{c.status}</Td>
                      <Td>
                        <div className="flex flex-wrap gap-1">
                          {shortPorts(c.ports ?? "").map((x) => (
                            <Badge key={x} mono>
                              {x}
                            </Badge>
                          ))}
                        </div>
                      </Td>
                      <Td>
                        <div className="flex items-center gap-0.5 justify-end">
                          <IconButton title="Logs" onClick={() => openContainer(c.id || c.name, c.name, "logs")}>
                            <ScrollText className="size-4" />
                          </IconButton>
                          <IconButton title="Terminal" disabled={c.state !== "running"} onClick={() => openContainer(c.id || c.name, c.name, "terminal")}>
                            <SquareTerminal className="size-4" />
                          </IconButton>
                          <IconButton title={`Restart ${c.service}`} disabled={!!busy} onClick={() => void action("restart", [c.service])}>
                            <RotateCw className={cx("size-4", busy === `restart:${c.service}` && "animate-spin")} />
                          </IconButton>
                          {c.state === "running" ? (
                            <IconButton title={`Stop ${c.service}`} disabled={!!busy} onClick={() => void action("stop", [c.service])}>
                              <Square className="size-4" />
                            </IconButton>
                          ) : (
                            <IconButton title={`Start ${c.service}`} disabled={!!busy} onClick={() => void action("up", [c.service])}>
                              <Play className="size-4" />
                            </IconButton>
                          )}
                        </div>
                      </Td>
                    </Tr>
                  ))}
                </tbody>
              </Table>
            )}
          </div>
        )}
        {view === "logs" && <DockerLogs ws={ws} host={host} target={{ source: "compose", project: p.name }} className="h-full" name={p.name} />}
        {view === "files" && <Files p={p} onDeployed={after} />}
      </div>
    </div>
  );
}

function Files({ p, onDeployed }: { p: ComposeProject; onDeployed: () => Promise<void> }) {
  const { ws, host } = useDocker();
  const files = useAsync(() => api.composeFiles(ws, host, p.name), [ws, host, p.name]);
  const [path, setPath] = useState("");
  const [content, setContent] = useState<string | null>(null);
  const [orig, setOrig] = useState("");
  const [err, setErr] = useState("");
  const { busy, run } = useRun();
  useEffect(() => {
    if (!path && files.data?.length) setPath(files.data[0].path);
  }, [files.data, path]);
  useEffect(() => {
    if (!path) return;
    let cancel = false;
    setContent(null);
    setErr("");
    api
      .composeFile(ws, host, p.name, path)
      .then((r) => {
        if (cancel) return;
        setContent(r.content);
        setOrig(r.content);
      })
      .catch((e) => !cancel && setErr(errMsg(e)));
    return () => {
      cancel = true;
    };
  }, [ws, host, p.name, path]);
  const file = files.data?.find((f) => f.path === path);
  const dirty = content !== null && content !== orig;
  async function save(deploy: boolean) {
    if (content === null) return;
    const ok = await run(deploy ? "deploy" : "save", deploy ? `Save and deploy ${p.name}` : `Save ${path.split("/").pop()}`, () =>
      api.composeSaveFile(ws, host, p.name, path, content, deploy),
    );
    if (ok) {
      setOrig(content);
      void files.reload();
      if (deploy) await onDeployed();
    }
  }
  return (
    <div className="h-full flex flex-col min-h-0 gap-2">
      <div className="flex items-center gap-2">
        <Select className="!w-auto font-mono text-xs" value={path} onChange={(e) => setPath(e.target.value)}>
          {(files.data ?? []).map((f) => (
            <option key={f.path} value={f.path}>
              {f.path}
              {f.kind === "env" && !f.exists ? " (new)" : ""}
            </option>
          ))}
        </Select>
        {file?.kind === "env" && <Badge tone="warn">may contain secrets</Badge>}
        {dirty && <Badge tone="warn">unsaved</Badge>}
        <div className="flex-1" />
        <span className="text-[11px] text-faint">Checked with docker compose config before it is saved; the old file is kept as .opsdeck-bak-*</span>
        <Button size="sm" disabled={!dirty || !!busy} loading={busy === "save"} onClick={() => void save(false)}>
          Save
        </Button>
        <Button size="sm" variant="primary" icon={<Rocket className="size-3.5" />} disabled={content === null || !!busy} loading={busy === "deploy"} onClick={() => void save(true)}>
          Save & up
        </Button>
      </div>
      {err && <ErrorBox error={err} />}
      {content === null && !err ? (
        <Loading />
      ) : (
        <Textarea mono dir="ltr" spellCheck={false} className="flex-1 !min-h-0 !text-[12px] leading-relaxed resize-none" value={content ?? ""} onChange={(e) => setContent(e.target.value)} />
      )}
    </div>
  );
}

function NewProject({ onClose, onCreated }: { onClose: () => void; onCreated: (name: string) => void }) {
  const { ws, host } = useDocker();
  const [name, setName] = useState("");
  const [dir, setDir] = useState("");
  const [content, setContent] = useState(TEMPLATE);
  const [start, setStart] = useState(true);
  const { busy, run } = useRun();
  const d = dir || (name ? `/opt/${name}` : "");
  return (
    <Modal
      open
      onClose={onClose}
      title={`New compose project on ${host}`}
      icon={<FileCode2 />}
      width={760}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            loading={!!busy}
            disabled={!name || !d}
            onClick={() => void run("create", `Create ${name}`, () => api.composeCreate(ws, host, { name, dir: d, content, start })).then((ok) => ok && onCreated(name))}
          >
            {start ? "Create and start" : "Create"}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Project name" required help="lowercase letters, digits, - and _">
            <Input mono value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="shop" />
          </FormField>
          <FormField label="Directory on the host" help="compose.yaml is written here">
            <Input mono value={dir} onChange={(e) => setDir(e.target.value)} placeholder={name ? `/opt/${name}` : "/opt/<name>"} />
          </FormField>
        </div>
        <FormField label="compose.yaml">
          <Textarea mono dir="ltr" spellCheck={false} className="min-h-80 !text-[12px] leading-relaxed" value={content} onChange={(e) => setContent(e.target.value)} />
        </FormField>
        <Toggle checked={start} onChange={setStart} label="Start it (docker compose up -d)" />
      </div>
    </Modal>
  );
}
