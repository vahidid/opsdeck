import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { CircleCheck, CircleX, ExternalLink, Pencil, Plus, Power, PowerOff, ScrollText, ShieldCheck, TriangleAlert } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { LogLine, NetworkStatus, Resource } from "../api/types";
import { useApp, useWs } from "../context/app";
import { KindIcon } from "../components/icons";
import { Badge, Button, Card, CopyButton, EmptyState, IconButton, PageHeader, StatusDot, cx } from "../components/ui";
import type { Tone } from "../components/ui";
import { MenuItem, Popover, useConfirm, useToast } from "../components/overlay";
import { useAsync, useInterval } from "../hooks";
import { fmtBytes, timeAgo } from "../lib/format";
import { ResourceDrawer } from "./Resources";

type Preset = { id: string; label: string; kind: string; spec: Record<string, unknown>; hint: string };

// Vendors map onto drivers: OpenConnect covers AnyConnect, ocserv, FortiGate,
// GlobalProtect, Pulse and F5; Tailscale also talks to Headscale.
export const VPN_PRESETS: Preset[] = [
  { id: "wireguard", label: "WireGuard", kind: "WireGuard", spec: { mode: "userspace" }, hint: "wg-quick .conf" },
  { id: "tailscale", label: "Tailscale", kind: "Tailscale", spec: {}, hint: "auth key or login link" },
  { id: "headscale", label: "Headscale", kind: "Tailscale", spec: { controlUrl: "https://" }, hint: "self-hosted control server" },
  { id: "netbird", label: "NetBird", kind: "NetBird", spec: { mode: "userspace" }, hint: "cloud or self-hosted" },
  { id: "openvpn", label: "OpenVPN", kind: "OpenVPN", spec: {}, hint: ".ovpn · system mode" },
  { id: "anyconnect", label: "Cisco AnyConnect", kind: "OpenConnect", spec: { protocol: "anyconnect", mode: "userspace" }, hint: "ASA / Firepower" },
  { id: "ocserv", label: "ocserv", kind: "OpenConnect", spec: { protocol: "anyconnect", mode: "userspace" }, hint: "OpenConnect server" },
  { id: "fortinet", label: "FortiGate", kind: "OpenConnect", spec: { protocol: "fortinet", mode: "userspace" }, hint: "FortiClient SSL VPN" },
  { id: "openfortivpn", label: "FortiGate (openfortivpn)", kind: "OpenFortiVPN", spec: {}, hint: "system mode" },
  { id: "gp", label: "Palo Alto GlobalProtect", kind: "OpenConnect", spec: { protocol: "gp", mode: "userspace" }, hint: "" },
  { id: "pulse", label: "Pulse / Ivanti Secure", kind: "OpenConnect", spec: { protocol: "pulse", mode: "userspace" }, hint: "" },
  { id: "f5", label: "F5 BIG-IP", kind: "OpenConnect", spec: { protocol: "f5", mode: "userspace" }, hint: "" },
  { id: "socks", label: "SOCKS5 proxy", kind: "SocksProxy", spec: {}, hint: "e.g. ssh -D" },
];

const PROTOCOL_LABEL: Record<string, string> = {
  anyconnect: "AnyConnect / ocserv",
  fortinet: "FortiGate",
  gp: "GlobalProtect",
  pulse: "Pulse Secure",
  nc: "Juniper",
  f5: "F5 BIG-IP",
  array: "Array",
};

export function vpnLabel(r: Resource | undefined, kind: string): string {
  switch (kind) {
    case "OpenConnect":
      return PROTOCOL_LABEL[String(r?.spec.protocol ?? "anyconnect")] ?? "OpenConnect";
    case "Tailscale":
      return r?.spec.controlUrl ? "Headscale" : "Tailscale";
    case "OpenFortiVPN":
      return "FortiGate";
    case "SocksProxy":
      return "SOCKS5";
  }
  return kind;
}

export function netTone(s: NetworkStatus["status"]): Tone {
  return s === "up" ? "ok" : s === "error" ? "err" : s === "connecting" || s === "auth" ? "warn" : "neutral";
}

export function netStateLabel(n: NetworkStatus): string {
  switch (n.status) {
    case "auth":
      return "waiting for login";
    case "connecting":
      return "connecting…";
  }
  return n.status;
}

export function VPNPage() {
  const { kindMap } = useApp();
  const { name: ws, resources, reloadResources } = useWs();
  const toast = useToast();
  const confirm = useConfirm();
  const nets = useAsync(() => api.networks(ws), [ws, resources.length]);
  const drivers = useAsync(() => api.vpnDrivers(), []);
  const [busy, setBusy] = useState<string | null>(null);
  const [menu, setMenu] = useState(false);
  const [edit, setEdit] = useState<{ resource: Resource | null; preset?: Preset; kind: string } | null>(null);
  const [logOpen, setLogOpen] = useState<string | null>(null);
  const anyActive = (nets.data ?? []).some((n) => n.status === "connecting" || n.status === "auth");
  useInterval(() => void nets.reload(), anyActive ? 1500 : 5000);

  const resOf = (name: string) => resources.find((r) => r.name === name && (r.kind === "SocksProxy" || kindMap[r.kind]?.category === "network"));

  async function connect(n: NetworkStatus) {
    if (n.mode === "system") {
      const ok = await confirm({
        title: `Connect ${n.name} system-wide?`,
        message: (
          <>
            System mode creates a network interface and changes this computer's routes{n.kind === "OpenVPN" ? " (and DNS, if the server pushes it)" : ""} for every app.
            {drivers.data?.elevate ? (
              <>
                {" "}
                The VPN client runs as root through <span className="font-mono">{drivers.data.elevate}</span>.
              </>
            ) : null}{" "}
            Other system VPNs with overlapping routes may conflict.
          </>
        ),
        confirmLabel: "Connect",
      });
      if (!ok) return;
    }
    setBusy(n.name);
    try {
      await api.networkUp(ws, n.name);
    } catch (e) {
      toast.error(`Could not connect ${n.name}`, errMsg(e));
      setLogOpen(n.name);
    } finally {
      setBusy(null);
      await nets.reload();
    }
  }

  async function disconnect(n: NetworkStatus) {
    setBusy(n.name);
    try {
      await api.networkDown(ws, n.name);
    } catch (e) {
      toast.error(`Network ${n.name}`, errMsg(e));
    } finally {
      setBusy(null);
      await nets.reload();
    }
  }

  async function remove(r: Resource) {
    const used = nets.data?.find((n) => n.name === r.name)?.usedBy ?? [];
    const ok = await confirm({
      title: `Delete ${r.name}?`,
      message: used.length ? `It is used by ${used.join(", ")}; they will lose their path.` : "The profile is removed from the workspace. Vault items stay.",
      confirmLabel: "Delete",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteResource(ws, r.kind, r.name);
      setEdit(null);
      await reloadResources();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  const list = nets.data ?? [];
  const newMenu = (
    <div className="relative">
      <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setMenu((v) => !v)}>
        New VPN
      </Button>
      <Popover open={menu} onClose={() => setMenu(false)} className="right-0 mt-1 w-72 max-h-[70vh] overflow-y-auto">
        {VPN_PRESETS.map((p) => (
          <MenuItem
            key={p.id}
            icon={<KindIcon kind={p.kind} />}
            hint={p.hint}
            onClick={() => {
              setMenu(false);
              setEdit({ resource: null, preset: p, kind: p.kind });
            }}
          >
            {p.label}
          </MenuItem>
        ))}
      </Popover>
    </div>
  );

  return (
    <div className="flex-1 overflow-y-auto">
      <PageHeader
        icon={<ShieldCheck />}
        title="VPN & networks"
        subtitle="Private networks this workspace is reached through. Userspace tunnels live inside opsdeck; system tunnels route the whole computer."
        actions={newMenu}
      />
      <div className="px-5 pb-8 space-y-4 max-w-5xl">
        {nets.data && list.length === 0 && (
          <EmptyState icon={<ShieldCheck />} title="No VPN profiles yet">
            <div className="mt-3 grid grid-cols-2 md:grid-cols-4 gap-2 text-left">
              {VPN_PRESETS.map((p) => (
                <button
                  key={p.id}
                  onClick={() => setEdit({ resource: null, preset: p, kind: p.kind })}
                  className="rounded-md border border-line bg-panel-2 hover:border-line-strong px-3 py-2"
                >
                  <div className="text-[13px] font-medium">{p.label}</div>
                  <div className="text-[11px] text-faint">{p.hint || p.kind}</div>
                </button>
              ))}
            </div>
          </EmptyState>
        )}

        {list.map((n) => {
          const r = resOf(n.name);
          return (
            <NetworkCard
              key={n.name}
              ws={ws}
              n={n}
              r={r}
              busy={busy === n.name}
              logOpen={logOpen === n.name}
              onLog={() => setLogOpen(logOpen === n.name ? null : n.name)}
              onConnect={() => void connect(n)}
              onDisconnect={() => void disconnect(n)}
              onEdit={() => r && setEdit({ resource: r, kind: r.kind })}
            />
          );
        })}

        {drivers.data && (
          <Card
            title={
              <>
                <ShieldCheck className="size-4 text-muted" /> On this computer
              </>
            }
          >
            <div className="text-xs text-muted mb-3">
              {drivers.data.privileged ? (
                <>opsdeck runs with network privileges: system-mode VPNs start directly.</>
              ) : drivers.data.elevate ? (
                <>
                  System-mode VPN clients run as root through <span className="font-mono text-fg">{drivers.data.elevate}</span>
                  {drivers.data.elevate.startsWith("sudo") ? " (needs passwordless sudo for openvpn, openconnect and wg-quick)" : ""}.{" "}
                </>
              ) : (
                <>System-mode VPNs are off: opsdeck is not root and no elevation is set. </>
              )}{" "}
              <Link to="/settings" className="text-accent hover:underline">
                Change in App settings
              </Link>
            </div>
            <table className="w-full text-xs">
              <thead>
                <tr className="text-faint text-left">
                  <th className="font-normal pb-1.5">Type</th>
                  <th className="font-normal pb-1.5">Userspace (no root)</th>
                  <th className="font-normal pb-1.5">System (TUN)</th>
                </tr>
              </thead>
              <tbody>
                {drivers.data.drivers.map((d) => (
                  <tr key={d.kind} className="border-t border-line">
                    <td className="py-1.5 pr-3">{d.label}</td>
                    {(["userspace", "system"] as const).map((m) => (
                      <td key={m} className="py-1.5 pr-3">
                        {!d.modes.includes(m) ? (
                          <span className="text-faint">not available for this type</span>
                        ) : d.missing[m] ? (
                          <span className="inline-flex items-center gap-1 text-warn">
                            <CircleX className="size-3.5 shrink-0" /> {d.missing[m]}
                          </span>
                        ) : (
                          <span className="inline-flex items-center gap-1 text-ok">
                            <CircleCheck className="size-3.5" /> ready
                          </span>
                        )}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        )}
      </div>

      {edit && kindMap[edit.kind] && (
        <ResourceDrawer
          schema={kindMap[edit.kind]}
          resource={edit.resource}
          initial={edit.preset ? { spec: edit.preset.spec } : undefined}
          title={edit.preset ? `New ${edit.preset.label} profile` : undefined}
          onClose={() => setEdit(null)}
          onSaved={async () => {
            setEdit(null);
            await reloadResources();
            await nets.reload();
          }}
          onDelete={edit.resource ? () => void remove(edit.resource!) : undefined}
        />
      )}
    </div>
  );
}

function NetworkCard({
  ws,
  n,
  r,
  busy,
  logOpen,
  onLog,
  onConnect,
  onDisconnect,
  onEdit,
}: {
  ws: string;
  n: NetworkStatus;
  r: Resource | undefined;
  busy: boolean;
  logOpen: boolean;
  onLog: () => void;
  onConnect: () => void;
  onDisconnect: () => void;
  onEdit: () => void;
}) {
  const active = n.status === "up" || n.status === "connecting" || n.status === "auth";
  const facts: [string, React.ReactNode][] = [];
  if (n.address) facts.push(["Address", <span className="font-mono">{n.address}</span>]);
  if (n.interface) facts.push(["Interface", <span className="font-mono">{n.interface}</span>]);
  if (n.server) facts.push(["Server", <span className="font-mono">{n.server}</span>]);
  if (n.status === "up" && n.since) facts.push(["Connected", timeAgo(n.since)]);
  if (n.status === "up" && (n.rxBytes || n.txBytes)) facts.push(["Traffic", `↓ ${fmtBytes(n.rxBytes)} ↑ ${fmtBytes(n.txBytes)}`]);
  if (n.lastHandshake && !n.lastHandshake.startsWith("0001")) facts.push(["Handshake", timeAgo(n.lastHandshake)]);
  if (n.detail) facts.push(["Status", n.detail]);
  return (
    <div className="rounded-lg border border-line bg-panel">
      <div className="flex items-center gap-3 px-4 h-14">
        <span className="text-muted">
          <KindIcon kind={n.kind} className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="text-[14px] font-semibold truncate">{n.name}</span>
            <Badge tone="neutral">{vpnLabel(r, n.kind)}</Badge>
            {n.mode && (
              <Badge tone={n.mode === "system" ? "warn" : "info"} >
                {n.mode === "system" ? "system · TUN" : "userspace"}
              </Badge>
            )}
          </div>
          {r?.description && <div className="text-[11px] text-faint truncate">{r.description}</div>}
        </div>
        <div className="flex items-center gap-1.5 text-xs">
          <StatusDot tone={netTone(n.status)} pulse={n.status === "connecting" || n.status === "auth"} />
          <span className={cx(n.status === "error" && "text-err", n.status === "up" && "text-ok")}>{netStateLabel(n)}</span>
        </div>
        {active ? (
          <Button size="sm" variant="secondary" loading={busy} icon={<PowerOff className="size-3.5" />} onClick={onDisconnect}>
            Disconnect
          </Button>
        ) : (
          <Button size="sm" variant="primary" loading={busy} disabled={!!n.missing} icon={<Power className="size-3.5" />} onClick={onConnect}>
            Connect
          </Button>
        )}
        <IconButton title="Log" onClick={onLog} className={cx(logOpen && "text-accent")}>
          <ScrollText className="size-4" />
        </IconButton>
        <IconButton title="Edit profile" onClick={onEdit} disabled={!r}>
          <Pencil className="size-4" />
        </IconButton>
      </div>

      {n.status === "auth" && n.authUrl && (
        <div className="mx-4 mb-3 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-xs flex items-center gap-2">
          <span className="flex-1">Log in to finish connecting. It continues automatically once you are signed in.</span>
          <a href={n.authUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-accent hover:underline">
            Open login page <ExternalLink className="size-3" />
          </a>
          <CopyButton text={n.authUrl} />
        </div>
      )}
      {n.status === "error" && n.error && (
        <div className="mx-4 mb-3 rounded-md border border-err/40 bg-err/10 px-3 py-2 text-xs text-err whitespace-pre-wrap break-words">{n.error}</div>
      )}
      {n.missing && (
        <div className="mx-4 mb-3 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-xs flex items-start gap-2">
          <TriangleAlert className="size-3.5 text-warn shrink-0 mt-0.5" />
          <span>
            {n.mode ? `${n.mode} mode` : "This profile"} can't run on this computer: {n.missing}
          </span>
        </div>
      )}

      {(facts.length > 0 || n.proxy || n.usedBy.length > 0) && (
        <div className="px-4 pb-3 grid grid-cols-2 md:grid-cols-4 gap-x-6 gap-y-2 text-xs">
          {facts.map(([k, v]) => (
            <div key={k} className="min-w-0">
              <div className="text-[11px] text-faint">{k}</div>
              <div className="truncate">{v}</div>
            </div>
          ))}
          {n.proxy && (
            <div className="min-w-0 col-span-2">
              <div className="text-[11px] text-faint">SOCKS5 proxy for other apps</div>
              <div className="flex items-center gap-1 font-mono">
                socks5h://{n.proxy} <CopyButton text={`socks5h://${n.proxy}`} />
              </div>
            </div>
          )}
          {n.usedBy.length > 0 && (
            <div className="min-w-0 col-span-2 md:col-span-4">
              <div className="text-[11px] text-faint">Reached through it</div>
              <div className="flex flex-wrap gap-1 mt-0.5">
                {n.usedBy.map((u) => (
                  <Badge key={u} tone="neutral">
                    <span className="font-mono">{u}</span>
                  </Badge>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
      {logOpen && <NetLog ws={ws} name={n.name} live={active} />}
    </div>
  );
}

function NetLog({ ws, name, live }: { ws: string; name: string; live: boolean }) {
  const [lines, setLines] = useState<LogLine[]>([]);
  const box = useRef<HTMLDivElement>(null);
  const load = () =>
    api
      .networkLog(ws, name)
      .then(setLines)
      .catch(() => {});
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, name]);
  useInterval(() => void load(), live ? 2000 : null);
  useEffect(() => {
    box.current?.scrollTo({ top: box.current.scrollHeight });
  }, [lines.length]);
  return (
    <div ref={box} className="border-t border-line bg-bg/60 max-h-72 overflow-y-auto px-4 py-2 font-mono text-[11px] leading-5">
      {lines.length === 0 && <div className="text-faint">No log yet.</div>}
      {lines.map((l, i) => (
        <div key={i} className={cx("whitespace-pre-wrap break-words", /error|fail|denied/i.test(l.text) && "text-err", l.text.startsWith("—") && "text-faint mt-1")}>
          <span className="text-faint mr-2">{l.time.slice(11, 19)}</span>
          {l.text}
        </div>
      ))}
    </div>
  );
}


