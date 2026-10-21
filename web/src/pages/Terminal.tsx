import { useEffect, useRef, useState } from "react";
import { Plus, RotateCw, SquareTerminal, X } from "lucide-react";
import { useWs, hostAddress } from "../context/app";
import { useTerminals } from "../context/terminals";
import type { TermTab } from "../context/terminals";
import { Badge, Button, EmptyState, IconButton, SearchInput, StatusDot, cx } from "../components/ui";
import { Popover } from "../components/overlay";

export function TerminalPage() {
  const { name: ws, hosts } = useWs();
  const { tabs, activeId, setActive, open, close, reconnect, attach, fitActive } = useTerminals();
  const hostRef = useRef<HTMLDivElement>(null);
  const [pickOpen, setPickOpen] = useState(false);
  const [q, setQ] = useState("");
  const active = tabs.find((t) => t.id === activeId) ?? null;

  useEffect(() => {
    if (active && hostRef.current) attach(active.id, hostRef.current);
  }, [active, attach]);

  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => fitActive());
    ro.observe(el);
    return () => ro.disconnect();
  }, [fitActive]);

  const filteredHosts = hosts.filter((h) => !q || h.name.includes(q) || hostAddress(h).includes(q));

  const picker = (
    <div className="relative">
      <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setPickOpen((o) => !o)}>
        New
      </Button>
      <Popover open={pickOpen} onClose={() => setPickOpen(false)} className="left-0 mt-1 w-80 p-2">
        <SearchInput value={q} onChange={setQ} placeholder="Find host…" autoFocus />
        <div className="max-h-72 overflow-y-auto mt-1.5">
          {filteredHosts.length === 0 && <div className="text-xs text-faint p-2">No hosts in this workspace</div>}
          {filteredHosts.map((h) => (
            <button
              key={h.name}
              className="w-full flex items-center gap-2 px-2 h-9 rounded hover:bg-panel-3 text-left"
              onClick={() => {
                setPickOpen(false);
                setQ("");
                open(ws, h.name);
              }}
            >
              <SquareTerminal className="size-3.5 text-muted" />
              <span className="text-[13px] font-medium">{h.name}</span>
              <span className="font-mono text-[11px] text-faint truncate flex-1 text-right">{hostAddress(h)}</span>
            </button>
          ))}
        </div>
      </Popover>
    </div>
  );

  return (
    <div className="flex flex-col h-full min-h-0">
      <div className="flex items-center h-10 border-b border-line bg-panel px-2 gap-1 shrink-0">
        <div className="flex items-center gap-0.5 overflow-x-auto flex-1 min-w-0">
          {tabs.map((t) => (
            <TabButton key={t.id} t={t} active={t.id === activeId} currentWs={ws} onClick={() => setActive(t.id)} onClose={() => close(t.id)} />
          ))}
          {picker}
        </div>
        {active && (
          <div className="flex items-center gap-2 pl-2 shrink-0">
            <span className="text-[11px] text-muted truncate max-w-72">{active.message}</span>
            <IconButton title="Reconnect" onClick={() => reconnect(active.id)}>
              <RotateCw className="size-3.5" />
            </IconButton>
          </div>
        )}
      </div>
      <div className="flex-1 min-h-0 relative bg-panel">
        <div ref={hostRef} className={cx("xterm-host absolute inset-0", !active && "hidden")} />
        {!active && (
          <EmptyState icon={<SquareTerminal />} title="No terminal open" className="h-full">
            Open an SSH session to a host. Connections go through the workspace's VPNs and jump hosts automatically, using keys from the vault.
            <div className="mt-4 flex flex-wrap justify-center gap-1.5">
              {hosts.slice(0, 8).map((h) => (
                <Button key={h.name} size="sm" icon={<SquareTerminal className="size-3.5" />} onClick={() => open(ws, h.name)}>
                  {h.name}
                </Button>
              ))}
            </div>
          </EmptyState>
        )}
      </div>
    </div>
  );
}

function TabButton({
  t,
  active,
  onClick,
  onClose,
  currentWs,
}: {
  t: TermTab;
  active: boolean;
  onClick: () => void;
  onClose: () => void;
  currentWs: string;
}) {
  const tone = t.status === "connected" ? "ok" : t.status === "connecting" ? "warn" : t.status === "error" ? "err" : "neutral";
  return (
    <div
      onClick={onClick}
      className={cx(
        "group flex items-center gap-2 h-7 pl-2.5 pr-1 rounded-md cursor-pointer text-xs whitespace-nowrap",
        active ? "bg-panel-3 text-fg" : "text-muted hover:bg-panel-2 hover:text-fg",
      )}
    >
      <StatusDot tone={tone} pulse={t.status === "connecting"} />
      <span className="font-mono">{t.host}</span>
      {t.ws !== currentWs && <Badge className="!h-4">{t.ws}</Badge>}
      <button
        onClick={(e) => {
          e.stopPropagation();
          onClose();
        }}
        className="size-5 grid place-items-center rounded text-faint hover:text-fg hover:bg-panel opacity-60 group-hover:opacity-100"
        aria-label="Close tab"
      >
        <X className="size-3" />
      </button>
    </div>
  );
}
