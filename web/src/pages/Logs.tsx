import { useEffect, useState } from "react";
import { AskAgentButton } from "../components/AskAgent";
import { Play, ScrollText, Square } from "lucide-react";
import { api } from "../api/client";
import type { Container } from "../api/types";
import { useWs } from "../context/app";
import { Button, FormField, Input, PageHeader, Segmented, Select, Toggle } from "../components/ui";
import { LogViewer } from "../components/LogViewer";

type Source = "journal" | "file" | "docker";

const FILE_PRESETS = ["/var/log/syslog", "/var/log/messages", "/var/log/nginx/error.log", "/var/log/nginx/access.log", "/var/log/auth.log"];

export function LogsPage() {
  const { name: ws, hosts } = useWs();
  const [host, setHost] = useState(hosts[0]?.name ?? "");
  const [source, setSource] = useState<Source>("journal");
  const [unit, setUnit] = useState("");
  const [path, setPath] = useState("/var/log/syslog");
  const [container, setContainer] = useState("");
  const [lines, setLines] = useState(200);
  const [follow, setFollow] = useState(true);
  const [containers, setContainers] = useState<Container[]>([]);
  const [url, setUrl] = useState<string | null>(null);

  useEffect(() => {
    if (!host && hosts[0]) setHost(hosts[0].name);
  }, [hosts, host]);

  useEffect(() => {
    if (source === "docker" && host) {
      api
        .containers(ws, host, false)
        .then((c) => {
          setContainers(c);
          if (!container && c[0]) setContainer(c[0].name);
        })
        .catch(() => setContainers([]));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source, host, ws]);

  function start() {
    if (!host) return;
    setUrl(
      api.logStreamUrl(ws, {
        host,
        source,
        unit: source === "journal" ? unit : undefined,
        path: source === "file" ? path : undefined,
        container: source === "docker" ? container : undefined,
        lines,
        follow,
      }) + `&_=${Date.now()}`,
    );
  }

  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<ScrollText />}
        title="Logs"
        subtitle="Stream journald units, log files or container logs over SSH."
        actions={
          <AskAgentButton
            label="Explain errors"
            prompt={`Read the recent logs of ${source === "docker" ? `the container ${container || "…"}` : source === "journal" ? `the unit ${unit || "…"}` : "this log"} on ${host || "the host"} and explain the errors and their cause.`}
          />
        }
      />
      <div className="px-5 pb-3 flex flex-wrap items-end gap-3">
        <FormField label="Host" className="w-48">
          <Select value={host} onChange={(e) => setHost(e.target.value)}>
            {hosts.map((h) => (
              <option key={h.name} value={h.name}>
                {h.name}
              </option>
            ))}
          </Select>
        </FormField>
        <div>
          <div className="text-xs font-medium text-muted mb-1">Source</div>
          <div className="h-8 flex items-center">
            <Segmented
              value={source}
              onChange={setSource}
              options={[
                { id: "journal", label: "journald" },
                { id: "file", label: "File" },
                { id: "docker", label: "Container" },
              ]}
            />
          </div>
        </div>
        {source === "journal" && (
          <FormField label="Unit (empty = all)" className="w-56">
            <Input mono value={unit} onChange={(e) => setUnit(e.target.value)} placeholder="nginx.service" onKeyDown={(e) => e.key === "Enter" && start()} />
          </FormField>
        )}
        {source === "file" && (
          <FormField label="Path" className="w-72">
            <Input mono list="od-logfiles" value={path} onChange={(e) => setPath(e.target.value)} onKeyDown={(e) => e.key === "Enter" && start()} />
            <datalist id="od-logfiles">
              {FILE_PRESETS.map((p) => (
                <option key={p} value={p} />
              ))}
            </datalist>
          </FormField>
        )}
        {source === "docker" && (
          <FormField label="Container" className="w-56">
            <Select value={container} onChange={(e) => setContainer(e.target.value)}>
              {containers.length === 0 && <option value="">(no running containers)</option>}
              {containers.map((c) => (
                <option key={c.id} value={c.name}>
                  {c.name}
                </option>
              ))}
            </Select>
          </FormField>
        )}
        <FormField label="Initial lines" className="w-28">
          <Input type="number" value={lines} min={0} max={10000} onChange={(e) => setLines(Number(e.target.value) || 0)} />
        </FormField>
        <div className="h-8 flex items-center">
          <Toggle checked={follow} onChange={setFollow} label="Follow" />
        </div>
        <Button variant="primary" icon={<Play className="size-3.5" />} onClick={start} disabled={!host || (source === "docker" && !container)}>
          Stream
        </Button>
        {url && (
          <Button icon={<Square className="size-3.5" />} onClick={() => setUrl(null)}>
            Stop
          </Button>
        )}
      </div>
      <div className="flex-1 min-h-0 px-5 pb-5">
        <LogViewer url={url} className="h-full" />
      </div>
    </div>
  );
}
