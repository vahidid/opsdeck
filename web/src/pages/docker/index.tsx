import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Container as ContainerIcon, RefreshCw } from "lucide-react";
import { api } from "../../api/client";
import { hostRoles, useWs } from "../../context/app";
import { useTabTitle } from "../../context/tabs";
import { Badge, EmptyState, IconButton, PageHeader, Select, Tabs, cx } from "../../components/ui";
import { useAsync, useLocalStorage } from "../../hooks";
import { ContainerDrawer, DockerProvider, OutputProvider } from "./shared";
import type { ContainerView } from "./shared";
import { Containers } from "./Containers";
import { Compose } from "./Compose";
import { Swarm } from "./Swarm";
import type { SwarmView } from "./Swarm";
import { Volumes } from "./Volumes";
import { Events, Images, Networks, Stats, System } from "./Engine";

type Tab = "containers" | "compose" | "swarm" | "volumes" | "images" | "networks" | "stats" | "events" | "system";

// Docker on a workspace host, over SSH with the docker CLI: containers,
// compose projects, swarm services and stacks, volumes (with their files),
// images, networks, live events and clean-up. The tab and what is open in
// it live in the URL, so each can be its own workspace tab.
export function DockerPage() {
  const { name: ws, hosts } = useWs();
  const sorted = useMemo(
    () => [...hosts].sort((a, b) => Number(hostRoles(b).includes("docker")) - Number(hostRoles(a).includes("docker")) || a.name.localeCompare(b.name)),
    [hosts],
  );
  const [params, setParams] = useSearchParams();
  const [saved, setSaved] = useLocalStorage<string>(`opsdeck.docker.${ws}`, "");
  const host = params.get("host") || (sorted.some((h) => h.name === saved) ? saved : sorted[0]?.name) || "";
  const tab = (params.get("tab") as Tab) || "containers";
  const set = useCallback(
    (patch: Record<string, string | null>) => {
      setParams(
        (p) => {
          const n = new URLSearchParams(p);
          for (const [k, v] of Object.entries(patch)) {
            if (v === null || v === "") n.delete(k);
            else n.set(k, v);
          }
          return n;
        },
        { replace: true },
      );
    },
    [setParams],
  );
  useEffect(() => {
    if (host) setSaved(host);
  }, [host, setSaved]);
  const info = useAsync(() => (host ? api.dockerInfo(ws, host) : Promise.resolve(undefined)), [ws, host]);
  const [drawer, setDrawer] = useState<{ id: string; name: string; view: ContainerView } | null>(null);
  useTabTitle(
    tab === "compose" && params.get("project") ? `${params.get("project")} · compose` : tab === "volumes" && params.get("volume") ? `${params.get("volume")} · volume` : `Docker · ${host}`,
  );

  const goto = useCallback(
    (t: string, extra: Record<string, string> = {}) => {
      setDrawer(null);
      set({ tab: t, project: null, pview: null, sview: null, stack: null, volume: null, path: null, ...extra });
    },
    [set],
  );
  const ctx = useMemo(
    () => ({ ws, host, goto, openContainer: (id: string, name: string, view: ContainerView = "overview") => setDrawer({ id, name, view }) }),
    [ws, host, goto],
  );

  const swarmActive = info.data?.swarm.state === "active";
  const tabs: { id: Tab; label: React.ReactNode }[] = [
    { id: "containers", label: "Containers" },
    { id: "compose", label: "Compose" },
    { id: "swarm", label: <span className={cx(!swarmActive && "opacity-50")}>Swarm</span> },
    { id: "volumes", label: "Volumes" },
    { id: "images", label: "Images" },
    { id: "networks", label: "Networks" },
    { id: "stats", label: "Stats" },
    { id: "events", label: "Events" },
    { id: "system", label: "System" },
  ];

  return (
    <DockerProvider value={ctx}>
      <OutputProvider>
        <div className="flex flex-col h-full min-h-0">
          <PageHeader
            icon={<ContainerIcon />}
            title="Docker"
            subtitle={
              info.data ? (
                <span className="inline-flex items-center gap-1.5 flex-wrap">
                  <span>Engine {info.data.serverVersion}</span>
                  {info.data.compose ? <Badge>compose {info.data.compose}</Badge> : <Badge tone="warn">no compose plugin</Badge>}
                  {swarmActive && <Badge tone="accent">swarm {info.data.swarm.manager ? "manager" : "worker"}</Badge>}
                  <Badge tone="ok">{info.data.running} running</Badge>
                  {info.data.stopped > 0 && <Badge>{info.data.stopped} stopped</Badge>}
                  {info.data.sudo && <Badge tone="warn">via sudo</Badge>}
                </span>
              ) : info.error ? (
                <span className="text-err">{info.error}</span>
              ) : (
                "Containers, compose projects, swarm services, volumes and images on a host, over SSH."
              )
            }
            actions={
              <>
                <IconButton title="Refresh engine info" onClick={() => void info.reload()}>
                  <RefreshCw className={cx("size-4", info.loading && "animate-spin")} />
                </IconButton>
                <Select value={host} onChange={(e) => set({ host: e.target.value, project: null, volume: null, path: null, stack: null })} className="w-56">
                  {sorted.length === 0 && <option value="">No hosts</option>}
                  {sorted.map((h) => (
                    <option key={h.name} value={h.name}>
                      {h.name}
                      {hostRoles(h).includes("docker") ? " · docker" : ""}
                    </option>
                  ))}
                </Select>
              </>
            }
          />
          <Tabs className="px-3" value={tab} onChange={(t) => goto(t)} tabs={tabs} />
          <div className="flex-1 min-h-0 p-5">
            {!host ? (
              <EmptyState icon={<ContainerIcon />} title="No hosts">
                Add a host (tip: give it the role <span className="font-mono">docker</span>) to manage its containers.
              </EmptyState>
            ) : tab === "containers" ? (
              <Containers />
            ) : tab === "compose" ? (
              <Compose
                project={params.get("project") ?? ""}
                view={(params.get("pview") as "services" | "logs" | "files") || "services"}
                select={(project, view) => set({ project, pview: view && view !== "services" ? view : null })}
              />
            ) : tab === "swarm" ? (
              <Swarm view={(params.get("sview") as SwarmView) || "services"} stack={params.get("stack") ?? ""} select={(v, extra) => set({ sview: v === "services" ? null : v, stack: null, ...extra })} />
            ) : tab === "volumes" ? (
              <Volumes volume={params.get("volume") ?? ""} path={params.get("path") ?? "/"} select={(volume, path) => set({ volume, path: path && path !== "/" ? path : null })} />
            ) : tab === "images" ? (
              <Images />
            ) : tab === "networks" ? (
              <Networks />
            ) : tab === "stats" ? (
              <Stats />
            ) : tab === "events" ? (
              <Events />
            ) : (
              <System info={info.data} />
            )}
          </div>
        </div>
        {drawer && <ContainerDrawer id={drawer.id} name={drawer.name} view={drawer.view} onView={(v) => setDrawer({ ...drawer, view: v })} onClose={() => setDrawer(null)} />}
      </OutputProvider>
    </DockerProvider>
  );
}
