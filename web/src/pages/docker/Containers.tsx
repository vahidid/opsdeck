import { useMemo, useState } from "react";
import { Container as ContainerIcon, Layers, MoreHorizontal, Pause, Play, RefreshCw, RotateCw, ScrollText, Skull, Square, SquareTerminal, Trash2 } from "lucide-react";
import { api } from "../../api/client";
import type { Container, DockerStat } from "../../api/types";
import { Badge, EmptyState, ErrorBox, IconButton, Loading, SearchInput, Table, Td, Th, Toggle, Tr, cx } from "../../components/ui";
import { MenuItem, Popover, useConfirm } from "../../components/overlay";
import { useAsync, useInterval, useLocalStorage } from "../../hooks";
import { StateBadge, UsageBar, pct, shortPorts, useDocker, useRun } from "./shared";

export function Containers() {
  const { ws, host, openContainer, goto } = useDocker();
  const [all, setAll] = useLocalStorage("opsdeck.docker.all", true);
  const [group, setGroup] = useLocalStorage("opsdeck.docker.group", true);
  const [live, setLive] = useLocalStorage("opsdeck.docker.live", true);
  const [q, setQ] = useState("");
  const c = useAsync(() => api.containers(ws, host, all), [ws, host, all]);
  const stats = useAsync(() => (live ? api.dockerStats(ws, host) : Promise.resolve([] as DockerStat[])), [ws, host, live]);
  useInterval(() => {
    void c.reload();
    if (live) void stats.reload();
  }, live ? 5000 : 30000);
  const { busy, run } = useRun();
  const confirm = useConfirm();

  const byId = useMemo(() => {
    const m = new Map<string, DockerStat>();
    for (const s of stats.data ?? []) m.set(s.id.slice(0, 12), s);
    return m;
  }, [stats.data]);

  async function act(ct: Container, action: string) {
    if (["stop", "kill", "rm", "restart", "pause"].includes(action)) {
      const ok = await confirm({
        title: `${action === "rm" ? "Remove" : action[0].toUpperCase() + action.slice(1)} ${ct.name}?`,
        message: (
          <>
            On <span className="font-mono text-fg">{host}</span>.{" "}
            {action === "rm" && "The container is deleted (named volumes are kept). It is stopped first if running."}
            {action === "kill" && "The main process gets SIGKILL: no graceful shutdown."}
            {ct.project && action === "rm" && ` It belongs to compose project ${ct.project}; "up" there would recreate it.`}
          </>
        ),
        danger: action !== "restart" && action !== "pause",
        confirmLabel: action === "rm" ? "Remove" : action[0].toUpperCase() + action.slice(1),
      });
      if (!ok) return;
    }
    await run(`${ct.id}:${action}`, `${action} ${ct.name}`, () => api.containerAction(ws, host, ct.id, action), () => c.reload());
  }

  const rows = (c.data ?? []).filter(
    (x) => !q || [x.name, x.image, x.project, x.service, x.stack].some((v) => v?.toLowerCase().includes(q.toLowerCase())),
  );
  const groups = useMemo(() => {
    if (!group) return [{ key: "", label: "", rows }];
    const m = new Map<string, Container[]>();
    for (const r of rows) {
      const k = r.project ? `compose:${r.project}` : r.stack ? `stack:${r.stack}` : "";
      if (!m.has(k)) m.set(k, []);
      m.get(k)!.push(r);
    }
    return [...m.entries()]
      .sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)))
      .map(([k, rs]) => ({ key: k, label: k, rows: rs }));
  }, [rows, group]);
  const running = (c.data ?? []).filter((x) => x.state === "running").length;

  return (
    <div className="h-full flex flex-col gap-3 min-h-0">
      <div className="flex items-center gap-4 flex-wrap">
        <SearchInput value={q} onChange={setQ} placeholder="Filter by name, image, project…" className="w-72" />
        <Toggle checked={all} onChange={setAll} label="Show stopped" />
        <Toggle checked={group} onChange={setGroup} label="Group by project" />
        <Toggle checked={live} onChange={setLive} label="Live usage" />
        <div className="flex-1" />
        <span className="text-xs text-faint">
          {running} running · {(c.data ?? []).length} total
        </span>
        <IconButton title="Refresh" onClick={() => void c.reload()}>
          <RefreshCw className={c.loading ? "size-4 animate-spin" : "size-4"} />
        </IconButton>
      </div>
      <div className="flex-1 min-h-0 rounded-lg border border-line bg-panel overflow-hidden flex flex-col">
        {c.error && <ErrorBox error={c.error} onRetry={() => void c.reload()} />}
        {!c.data && c.loading && <Loading label={`Listing containers on ${host}…`} />}
        {c.data && rows.length === 0 && <EmptyState icon={<ContainerIcon />} title="No containers" />}
        {rows.length > 0 && (
          <Table className="flex-1">
            <thead>
              <tr>
                <Th>Name</Th>
                <Th>Image</Th>
                <Th>State</Th>
                <Th>Status</Th>
                <Th>Ports</Th>
                {live && <Th>CPU</Th>}
                {live && <Th>Memory</Th>}
                <Th className="w-[1%]" />
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <GroupRows key={g.key} label={g.label} cols={live ? 8 : 6} onOpen={() => g.key.startsWith("compose:") ? goto("compose", { project: g.key.slice(8) }) : goto("swarm", { view: "stacks", stack: g.key.slice(6) })}>
                  {g.rows.map((ct) => {
                    const st = byId.get(ct.id);
                    return (
                      <Tr key={ct.id} className={cx(ct.state !== "running" && "opacity-75")}>
                        <Td>
                          <button className="font-medium hover:text-accent text-left" onClick={() => openContainer(ct.id, ct.name, "overview")}>
                            {ct.name}
                          </button>
                          <div className="flex items-center gap-1.5 font-mono text-[10.5px] text-faint whitespace-nowrap">
                            {ct.id.slice(0, 12)}
                            {!group && ct.project && <Badge>{ct.project}</Badge>}
                            {ct.service && <span className="text-muted">· {ct.service}</span>}
                          </div>
                        </Td>
                        <Td mono className="max-w-64 truncate" title={ct.image}>
                          {ct.image}
                        </Td>
                        <Td>
                          <StateBadge state={ct.state} health={ct.health} />
                        </Td>
                        <Td className="text-muted whitespace-nowrap">{ct.status.replace(/ \((healthy|unhealthy|health: starting)\)/, "")}</Td>
                        <Td>
                          <div className="flex flex-wrap gap-1 max-w-56" title={ct.ports}>
                            {shortPorts(ct.ports).map((p) => (
                              <Badge key={p} mono>
                                {p}
                              </Badge>
                            ))}
                            {!ct.ports && <span className="text-faint">—</span>}
                          </div>
                        </Td>
                        {live && <Td>{st ? <UsageBar v={pct(st.cpu)} label={st.cpu} width="w-32" /> : <span className="text-faint">—</span>}</Td>}
                        {live && (
                          <Td title={st?.mem}>{st ? <UsageBar v={pct(st.memPct)} label={st.mem.split(" / ")[0]} width="w-36" /> : <span className="text-faint">—</span>}</Td>
                        )}
                        <Td>
                          <div className="flex items-center gap-0.5 justify-end">
                            <IconButton title="Logs" onClick={() => openContainer(ct.id, ct.name, "logs")}>
                              <ScrollText className="size-4" />
                            </IconButton>
                            <IconButton title="Terminal (docker exec)" disabled={ct.state !== "running"} onClick={() => openContainer(ct.id, ct.name, "terminal")}>
                              <SquareTerminal className="size-4" />
                            </IconButton>
                            {ct.state === "running" ? (
                              <IconButton title="Restart" disabled={!!busy} onClick={() => void act(ct, "restart")}>
                                <RotateCw className={busy === `${ct.id}:restart` ? "size-4 animate-spin" : "size-4"} />
                              </IconButton>
                            ) : (
                              <IconButton title="Start" disabled={!!busy} onClick={() => void act(ct, ct.state === "paused" ? "unpause" : "start")}>
                                <Play className="size-4" />
                              </IconButton>
                            )}
                            <More ct={ct} onAct={(a) => void act(ct, a)} />
                          </div>
                        </Td>
                      </Tr>
                    );
                  })}
                </GroupRows>
              ))}
            </tbody>
          </Table>
        )}
      </div>
    </div>
  );
}

function GroupRows({ label, cols, children, onOpen }: { label: string; cols: number; children: React.ReactNode; onOpen: () => void }) {
  if (!label) return <>{children}</>;
  const [kind, name] = [label.split(":")[0], label.slice(label.indexOf(":") + 1)];
  return (
    <>
      <tr>
        <td colSpan={cols} className="px-3 pt-3 pb-1 border-b border-line/70">
          <button className="inline-flex items-center gap-1.5 text-[11.5px] font-medium hover:text-accent" onClick={onOpen}>
            <Layers className="size-3.5 text-muted" />
            {name}
            <span className="text-faint font-normal">{kind === "compose" ? "compose project" : "swarm stack"}</span>
          </button>
        </td>
      </tr>
      {children}
    </>
  );
}

function More({ ct, onAct }: { ct: Container; onAct: (a: string) => void }) {
  const [open, setOpen] = useState(false);
  const pick = (a: string) => {
    setOpen(false);
    onAct(a);
  };
  return (
    <div className="relative">
      <IconButton title="More" onClick={() => setOpen((o) => !o)}>
        <MoreHorizontal className="size-4" />
      </IconButton>
      <Popover open={open} onClose={() => setOpen(false)} className="right-0 mt-1 w-44">
        {ct.state === "running" && (
          <>
            <MenuItem icon={<Square />} onClick={() => pick("stop")}>
              Stop
            </MenuItem>
            <MenuItem icon={<Pause />} onClick={() => pick("pause")}>
              Pause
            </MenuItem>
            <MenuItem icon={<Skull />} onClick={() => pick("kill")}>
              Kill
            </MenuItem>
          </>
        )}
        {ct.state === "paused" && (
          <MenuItem icon={<Play />} onClick={() => pick("unpause")}>
            Unpause
          </MenuItem>
        )}
        {ct.state !== "running" && ct.state !== "paused" && (
          <MenuItem icon={<Play />} onClick={() => pick("start")}>
            Start
          </MenuItem>
        )}
        <MenuItem icon={<Trash2 />} danger onClick={() => pick("rm")}>
          Remove
        </MenuItem>
      </Popover>
    </div>
  );
}
