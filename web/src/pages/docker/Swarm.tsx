import { Fragment, useState } from "react";
import { ChevronDown, ChevronRight, Network, Rocket, RotateCcw, RefreshCw, ScrollText, Server, Tag, Trash2, Upload } from "lucide-react";
import { api } from "../../api/client";
import type { SwarmService, SwarmTask } from "../../api/types";
import { Badge, Button, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, SearchInput, Segmented, Select, Table, Td, Textarea, Th, Toggle, Tr, cx } from "../../components/ui";
import { Drawer, Modal, useConfirm } from "../../components/overlay";
import { useAsync, useInterval } from "../../hooks";
import { DockerLogs, stateTone, useDocker, useRun } from "./shared";

type View = "services" | "stacks" | "nodes";

export function Swarm({ view, stack, select }: { view: View; stack: string; select: (v: View, extra?: Record<string, string>) => void }) {
  const { ws, host } = useDocker();
  const info = useAsync(() => api.swarm(ws, host), [ws, host]);
  if (info.error) return <ErrorBox error={info.error} onRetry={() => void info.reload()} />;
  if (!info.data) return <Loading />;
  const sw = info.data;
  if (sw.state !== "active") {
    return (
      <EmptyState icon={<Network />} title={`Swarm is ${sw.state || "inactive"} on ${host}`}>
        Services, stacks and nodes appear here when this host is part of a Docker Swarm. {sw.error}
      </EmptyState>
    );
  }
  if (!sw.manager) {
    return (
      <EmptyState icon={<Network />} title={`${host} is a swarm worker`}>
        Services and stacks are managed from a manager node. Add a manager host to this workspace and pick it above.
      </EmptyState>
    );
  }
  return (
    <div className="h-full flex flex-col min-h-0">
      <div className="flex items-center gap-3 pb-2">
        <Segmented
          value={view}
          onChange={(v) => select(v)}
          options={[
            { id: "services", label: "Services" },
            { id: "stacks", label: "Stacks" },
            { id: "nodes", label: `Nodes (${sw.nodes})` },
          ]}
        />
        <span className="text-xs text-faint">
          {sw.managers} manager{sw.managers === 1 ? "" : "s"} · {sw.nodes} node{sw.nodes === 1 ? "" : "s"}
        </span>
      </div>
      <div className="flex-1 min-h-0">
        {view === "services" && <Services stackFilter={stack} onClearStack={() => select("services")} />}
        {view === "stacks" && <Stacks onOpen={(s) => select("services", { stack: s })} />}
        {view === "nodes" && <Nodes />}
      </div>
    </div>
  );
}

function Services({ stackFilter, onClearStack }: { stackFilter: string; onClearStack: () => void }) {
  const { ws, host } = useDocker();
  const list = useAsync(() => api.services(ws, host), [ws, host]);
  useInterval(() => void list.reload(), 5000);
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [logs, setLogs] = useState<SwarmService | null>(null);
  const [image, setImage] = useState<SwarmService | null>(null);
  const { busy, run } = useRun();
  const confirm = useConfirm();
  const rows = (list.data ?? []).filter((s) => (!stackFilter || s.stack === stackFilter) && (!q || s.name.includes(q) || s.image.includes(q)));

  async function act(s: SwarmService, action: string, arg = "") {
    if (action === "rm" || action === "rollback") {
      const ok = await confirm({
        title: action === "rm" ? `Remove service ${s.name}?` : `Roll back ${s.name}?`,
        message: action === "rm" ? "All its tasks stop and the service is deleted." : "The service returns to its previous specification.",
        danger: action === "rm",
        confirmLabel: action === "rm" ? "Remove" : "Roll back",
      });
      if (!ok) return;
    }
    await run(`${s.name}:${action}`, `${action} ${s.name}${arg ? " → " + arg : ""}`, () => api.serviceAction(ws, host, s.name, action, arg), () => list.reload());
  }

  return (
    <div className="h-full flex flex-col gap-2 min-h-0">
      <div className="flex items-center gap-3">
        <SearchInput value={q} onChange={setQ} placeholder="Filter services…" className="w-64" />
        {stackFilter && (
          <Badge tone="accent">
            stack {stackFilter}
            <button className="ml-1" onClick={onClearStack}>
              ×
            </button>
          </Badge>
        )}
        <div className="flex-1" />
        <IconButton title="Refresh" onClick={() => void list.reload()}>
          <RefreshCw className={cx("size-4", list.loading && "animate-spin")} />
        </IconButton>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {list.error && <ErrorBox error={list.error} />}
        {!list.data && list.loading && <Loading />}
        {list.data && rows.length === 0 && <EmptyState icon={<Server />} title="No services" />}
        {rows.length > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th className="w-6" />
                <Th>Service</Th>
                <Th>Mode</Th>
                <Th>Replicas</Th>
                <Th>Image</Th>
                <Th>Ports</Th>
                <Th className="w-[1%]" />
              </tr>
            </thead>
            <tbody>
              {rows.map((s) => (
                <Fragment key={s.id}>
                  <Tr onClick={() => setOpen(open === s.name ? null : s.name)} selected={open === s.name}>
                    <Td>{open === s.name ? <ChevronDown className="size-3.5 text-muted" /> : <ChevronRight className="size-3.5 text-muted" />}</Td>
                    <Td>
                      <div className="font-medium">{s.stack ? s.name.slice(s.stack.length + 1) : s.name}</div>
                      {s.stack && <div className="text-[10.5px] text-faint">stack {s.stack}</div>}
                    </Td>
                    <Td className="text-muted">{s.mode}</Td>
                    <Td>
                      <div className="flex items-center gap-2 w-40">
                        <div className="flex-1 h-1.5 rounded-full bg-panel-3 overflow-hidden">
                          <div className={cx("h-full transition-[width] duration-500", s.running >= s.desired ? "bg-ok" : "bg-warn")} style={{ width: `${s.desired ? Math.min(100, (100 * s.running) / s.desired) : 0}%` }} />
                        </div>
                        <span className={cx("font-mono text-[11.5px] tabular-nums", s.running < s.desired ? "text-warn" : "text-ok")}>{s.replicas}</span>
                      </div>
                    </Td>
                    <Td mono className="max-w-72 truncate" title={s.image}>
                      {s.image.replace(/@sha256:.*/, "")}
                    </Td>
                    <Td mono className="text-muted">
                      {s.ports || "—"}
                    </Td>
                    <Td>
                      <div className="flex items-center gap-0.5 justify-end" onClick={(e) => e.stopPropagation()}>
                        {s.mode === "replicated" && <Scale s={s} busy={busy === `${s.name}:scale`} onScale={(n) => void act(s, "scale", String(n))} />}
                        <IconButton title="Logs" onClick={() => setLogs(s)}>
                          <ScrollText className="size-4" />
                        </IconButton>
                        <IconButton title="Redeploy (update --force: restart every task)" disabled={!!busy} onClick={() => void act(s, "redeploy")}>
                          <RefreshCw className={cx("size-4", busy === `${s.name}:redeploy` && "animate-spin")} />
                        </IconButton>
                        <IconButton title="Change image" onClick={() => setImage(s)}>
                          <Tag className="size-4" />
                        </IconButton>
                        <IconButton title="Roll back to the previous version" disabled={!!busy} onClick={() => void act(s, "rollback")}>
                          <RotateCcw className="size-4" />
                        </IconButton>
                        <IconButton title="Remove" className="hover:!text-err" disabled={!!busy} onClick={() => void act(s, "rm")}>
                          <Trash2 className="size-4" />
                        </IconButton>
                      </div>
                    </Td>
                  </Tr>
                  {open === s.name && (
                    <tr>
                      <td colSpan={7} className="bg-panel-2/60 border-b border-line px-6 py-2">
                        <Tasks load={() => api.serviceTasks(ws, host, s.name)} k={s.name} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </Table>
        )}
      </div>
      {logs && (
        <Drawer open onClose={() => setLogs(null)} title={`Logs · ${logs.name}`} subtitle={`every task of the service, on ${host}`} width={1100}>
          <div className="-m-4 p-3 h-[calc(100vh-48px)]">
            <DockerLogs ws={ws} host={host} target={{ source: "service", service: logs.name }} className="h-full" name={logs.name} />
          </div>
        </Drawer>
      )}
      {image && <ImageModal s={image} onClose={() => setImage(null)} onSet={(ref) => (setImage(null), void act(image, "image", ref))} />}
    </div>
  );
}

function Scale({ s, busy, onScale }: { s: SwarmService; busy: boolean; onScale: (n: number) => void }) {
  const [n, setN] = useState(String(s.desired));
  const changed = n !== String(s.desired) && n !== "";
  return (
    <div className="flex items-center gap-1 mr-1">
      <Input type="number" min={0} max={1000} className="!w-16 !h-7 text-xs" value={n} onChange={(e) => setN(e.target.value)} title="Replicas" />
      {changed && (
        <Button size="xs" variant="primary" loading={busy} onClick={() => onScale(Number(n))}>
          Scale
        </Button>
      )}
    </div>
  );
}

function ImageModal({ s, onClose, onSet }: { s: SwarmService; onClose: () => void; onSet: (ref: string) => void }) {
  const [ref, setRef] = useState(s.image.replace(/@sha256:.*/, ""));
  return (
    <Modal
      open
      onClose={onClose}
      title={`Update image of ${s.name}`}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" icon={<Rocket className="size-3.5" />} disabled={!ref} onClick={() => onSet(ref)}>
            Roll out
          </Button>
        </>
      }
    >
      <FormField label="Image" help="Tasks are replaced following the service's update policy; roll back from the list if it goes wrong.">
        <Input mono value={ref} onChange={(e) => setRef(e.target.value)} />
      </FormField>
    </Modal>
  );
}

function Tasks({ load, k }: { load: () => Promise<SwarmTask[]>; k: string }) {
  const t = useAsync(load, [k]);
  useInterval(() => void t.reload(), 3000);
  const [history, setHistory] = useState(false);
  if (t.error) return <div className="text-err text-xs">{t.error}</div>;
  if (!t.data) return <Loading />;
  const rows = history ? t.data : t.data.filter((x) => x.desiredState === "Running" || x.currentState.startsWith("Running"));
  return (
    <div>
      <div className="flex items-center gap-3 mb-1">
        <span className="text-[11px] uppercase tracking-wide text-faint">Tasks</span>
        <Toggle checked={history} onChange={setHistory} label={<span className="text-[11.5px]">Show shut down</span>} />
      </div>
      <table className="w-full text-[12px]">
        <tbody>
          {rows.map((x) => (
            <tr key={x.id} className="border-b border-line/40 last:border-0">
              <td className="py-1 pr-3 font-mono">{x.name}</td>
              <td className="pr-3 font-mono text-faint">{x.id}</td>
              <td className="pr-3">{x.node}</td>
              <td className="pr-3">
                <Badge tone={stateTone(x.currentState.split(" ")[0])}>{x.currentState}</Badge>
              </td>
              <td className="pr-3 text-faint">desired {x.desiredState.toLowerCase()}</td>
              <td className="text-err">{x.error}</td>
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td className="text-faint py-1">No running tasks.</td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

function Stacks({ onOpen }: { onOpen: (stack: string) => void }) {
  const { ws, host } = useDocker();
  const list = useAsync(() => api.stacks(ws, host), [ws, host]);
  useInterval(() => void list.reload(), 10000);
  const [deploy, setDeploy] = useState<{ name: string } | null>(null);
  const [tasks, setTasks] = useState<string | null>(null);
  const { busy, run } = useRun();
  const confirm = useConfirm();
  async function remove(name: string) {
    const ok = await confirm({ title: `Remove stack ${name}?`, message: "Its services and networks are removed. Volumes are kept.", danger: true, confirmLabel: "Remove", typeToConfirm: name });
    if (ok) await run(`rm:${name}`, `Remove stack ${name}`, () => api.stackRemove(ws, host, name), () => list.reload());
  }
  return (
    <div className="h-full flex flex-col gap-2 min-h-0">
      <div className="flex items-center">
        <div className="flex-1" />
        <Button size="sm" variant="primary" icon={<Upload className="size-3.5" />} onClick={() => setDeploy({ name: "" })}>
          Deploy stack
        </Button>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {list.error && <ErrorBox error={list.error} />}
        {!list.data && list.loading && <Loading />}
        {list.data?.length === 0 && <EmptyState icon={<Network />} title="No stacks">Deploy one from a compose file: on the host or pasted here.</EmptyState>}
        {(list.data?.length ?? 0) > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>Stack</Th>
                <Th>Services</Th>
                <Th className="w-[1%]" />
              </tr>
            </thead>
            <tbody>
              {list.data!.map((s) => (
                <Fragment key={s.name}>
                  <Tr>
                    <Td>
                      <button className="font-medium hover:text-accent" onClick={() => onOpen(s.name)}>
                        {s.name}
                      </button>
                    </Td>
                    <Td>{s.services}</Td>
                    <Td>
                      <div className="flex items-center gap-1 justify-end">
                        <Button size="xs" variant="ghost" onClick={() => setTasks(tasks === s.name ? null : s.name)}>
                          Tasks
                        </Button>
                        <Button size="xs" variant="ghost" onClick={() => onOpen(s.name)}>
                          Services
                        </Button>
                        <Button size="xs" variant="ghost" icon={<Rocket className="size-3" />} onClick={() => setDeploy({ name: s.name })}>
                          Update
                        </Button>
                        <IconButton title="Remove" className="hover:!text-err" disabled={!!busy} onClick={() => void remove(s.name)}>
                          <Trash2 className="size-4" />
                        </IconButton>
                      </div>
                    </Td>
                  </Tr>
                  {tasks === s.name && (
                    <tr>
                      <td colSpan={3} className="bg-panel-2/60 border-b border-line px-6 py-2">
                        <Tasks load={() => api.stackTasks(ws, host, s.name)} k={"stack/" + s.name} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </Table>
        )}
      </div>
      {deploy && <DeployStack initial={deploy.name} onClose={() => setDeploy(null)} onDone={() => (setDeploy(null), void list.reload())} />}
    </div>
  );
}

function DeployStack({ initial, onClose, onDone }: { initial: string; onClose: () => void; onDone: () => void }) {
  const { ws, host } = useDocker();
  const key = `opsdeck.stackfile.${ws}.${host}.${initial}`;
  const [name, setName] = useState(initial);
  const [mode, setMode] = useState<"file" | "paste">(() => (localStorage.getItem(key) ? "file" : initial ? "file" : "paste"));
  const [file, setFile] = useState(() => localStorage.getItem(key) ?? "");
  const [content, setContent] = useState('version: "3.8"\nservices:\n  web:\n    image: nginx:alpine\n    deploy:\n      replicas: 2\n    ports:\n      - "8080:80"\n');
  const [prune, setPrune] = useState(false);
  const { busy, run } = useRun();
  async function go() {
    const ok = await run("deploy", `Deploy stack ${name}`, () => api.stackDeploy(ws, host, mode === "file" ? { name, file, prune } : { name, content, prune }));
    if (ok) {
      if (mode === "file") localStorage.setItem(`opsdeck.stackfile.${ws}.${host}.${name}`, file);
      onDone();
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={initial ? `Update stack ${initial}` : "Deploy a stack"}
      width={760}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" icon={<Rocket className="size-3.5" />} loading={!!busy} disabled={!name || (mode === "file" ? !file : !content.trim())} onClick={() => void go()}>
            Deploy
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="grid grid-cols-[1fr_auto] gap-3 items-end">
          <FormField label="Stack name" required>
            <Input mono value={name} disabled={!!initial} onChange={(e) => setName(e.target.value)} />
          </FormField>
          <Segmented
            value={mode}
            onChange={setMode}
            options={[
              { id: "file", label: "File on the host" },
              { id: "paste", label: "Paste" },
            ]}
          />
        </div>
        {mode === "file" ? (
          <FormField label="Compose file on the host" help="Relative paths in it (configs, env files) resolve next to it. Remembered for the next update.">
            <Input mono value={file} onChange={(e) => setFile(e.target.value)} placeholder="/opt/stacks/blog/docker-stack.yml" />
          </FormField>
        ) : (
          <FormField label="Compose file">
            <Textarea mono dir="ltr" spellCheck={false} className="min-h-72 !text-[12px]" value={content} onChange={(e) => setContent(e.target.value)} />
          </FormField>
        )}
        <Toggle checked={prune} onChange={setPrune} label="Remove services that are no longer in the file (--prune)" />
      </div>
    </Modal>
  );
}

function Nodes() {
  const { ws, host } = useDocker();
  const list = useAsync(() => api.nodes(ws, host), [ws, host]);
  useInterval(() => void list.reload(), 10000);
  const { run } = useRun();
  const confirm = useConfirm();
  async function setAvail(id: string, hostname: string, a: string) {
    if (a === "drain") {
      const ok = await confirm({ title: `Drain ${hostname}?`, message: "Its tasks are moved to other nodes and no new ones are scheduled on it.", danger: true, confirmLabel: "Drain" });
      if (!ok) return;
    }
    await run(id, `${hostname}: ${a}`, () => api.nodeAvailability(ws, host, id, a), () => list.reload());
  }
  return (
    <div className="h-full rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
      {list.error && <ErrorBox error={list.error} />}
      {!list.data && list.loading && <Loading />}
      {list.data && (
        <Table className="flex-1">
          <thead>
            <tr>
              <Th>Node</Th>
              <Th>Status</Th>
              <Th>Availability</Th>
              <Th>Manager</Th>
              <Th>Engine</Th>
            </tr>
          </thead>
          <tbody>
            {list.data.map((n) => (
              <Tr key={n.id}>
                <Td>
                  <div className="font-medium">
                    {n.hostname} {n.self && <Badge tone="accent">this host</Badge>}
                  </div>
                  <div className="font-mono text-[10.5px] text-faint">{n.id.slice(0, 12)}</div>
                </Td>
                <Td>
                  <Badge tone={stateTone(n.status)}>{n.status}</Badge>
                </Td>
                <Td>
                  <Select className="!w-28 !h-7 text-xs" value={n.availability.toLowerCase()} onChange={(e) => void setAvail(n.id, n.hostname, e.target.value)}>
                    <option value="active">active</option>
                    <option value="pause">pause</option>
                    <option value="drain">drain</option>
                  </Select>
                </Td>
                <Td>{n.managerStatus ? <Badge tone={n.managerStatus === "Leader" ? "accent" : "neutral"}>{n.managerStatus}</Badge> : <span className="text-faint">worker</span>}</Td>
                <Td mono>{n.engineVersion}</Td>
              </Tr>
            ))}
          </tbody>
        </Table>
      )}
    </div>
  );
}

export type { View as SwarmView };
