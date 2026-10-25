import { useEffect, useRef, useState } from "react";
import { Plus, X } from "lucide-react";
import { useTabs, tabLabel } from "../context/tabs";
import { NAV, NAV_GROUPS, sectionInfo } from "../lib/nav";
import { MenuItem, Popover } from "./overlay";
import { cx } from "./ui";

// TabStrip shows a workspace's tabs. Click switches, middle-click or × closes,
// drag reorders, + opens a section in a new tab. Alt+1…9 switch tabs.
export function TabStrip({ ws }: { ws: string }) {
  const tabs = useTabs();
  const { tabs: list, active } = tabs.get(ws);
  const [menu, setMenu] = useState(false);
  const [drag, setDrag] = useState<number | null>(null);
  const strip = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (!e.altKey || e.ctrlKey || e.metaKey) return;
      const n = Number(e.key);
      if (n >= 1 && n <= 9) {
        const t = tabs.get(ws).tabs[n - 1];
        if (t) {
          e.preventDefault();
          tabs.activate(ws, t.id);
        }
      }
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [tabs, ws]);

  useEffect(() => {
    strip.current?.querySelector("[data-active=true]")?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [active]);

  return (
    <div className="h-full flex-1 min-w-0 flex items-center">
      <div
        ref={strip}
        className="flex items-center gap-1 overflow-x-auto min-w-0 h-full [scrollbar-width:none]"
        onDoubleClick={(e) => {
          if (e.target === e.currentTarget) setMenu(true);
        }}
      >
        {list.map((t, i) => {
          const isActive = t.id === active;
          const info = sectionInfo(t.path);
          return (
            <div
              key={t.id}
              data-active={isActive}
              draggable
              onDragStart={() => setDrag(i)}
              onDragOver={(e) => e.preventDefault()}
              onDrop={() => {
                if (drag !== null && drag !== i) tabs.move(ws, drag, i);
                setDrag(null);
              }}
              onMouseDown={(e) => {
                if (e.button === 0) tabs.activate(ws, t.id);
              }}
              onAuxClick={(e) => {
                if (e.button === 1) {
                  e.preventDefault();
                  tabs.close(ws, t.id);
                }
              }}
              title={tabLabel(t) + (i < 9 ? `  (Alt+${i + 1})` : "")}
              className={cx(
                "group relative shrink-0 flex items-center gap-1.5 h-8 pl-2.5 pr-1 min-w-[96px] max-w-[220px] rounded-lg cursor-pointer select-none text-[12.5px] font-medium [&_svg]:size-3.5",
                isActive ? "bg-panel-3 text-fg ring-1 ring-line-strong" : "text-muted hover:text-fg hover:bg-panel-2",
              )}
            >
              <span className={cx("shrink-0", t.path.startsWith("agent") ? "[&_svg]:text-agent-ink" : isActive ? "[&_svg]:text-accent" : "[&_svg]:text-faint")}>{info?.icon}</span>
              <span className="truncate flex-1" dir="auto">
                {tabLabel(t)}
              </span>
              <button
                onMouseDown={(e) => e.stopPropagation()}
                onClick={(e) => {
                  e.stopPropagation();
                  tabs.close(ws, t.id);
                }}
                className={cx("rounded p-0.5 hover:bg-panel-3 text-faint hover:text-fg", isActive ? "opacity-100" : "opacity-0 group-hover:opacity-100")}
                title="Close tab (middle-click)"
              >
                <X className="!size-3" />
              </button>
            </div>
          );
        })}
      </div>
      <div className="relative flex items-center pl-1">
        <button onClick={() => setMenu((v) => !v)} className="rounded-lg p-1.5 text-muted hover:text-fg hover:bg-panel-2" title="New tab">
          <Plus className="size-4" />
        </button>
        <Popover open={menu} onClose={() => setMenu(false)} className="left-0 top-full mt-1 w-56 max-h-[70vh] overflow-y-auto">
          <div className="px-3 pt-1 pb-1.5 text-[11px] text-faint">Open in a new tab</div>
          {NAV_GROUPS.map((g, gi) => (
            <div key={g.id}>
              {gi > 0 && <div className="px-3 pt-2 pb-1 text-[10px] font-medium uppercase tracking-wider text-faint">{g.label}</div>}
              {NAV.filter((n) => n.group === g.id).map((n) => (
                <MenuItem
                  key={n.to}
                  icon={n.icon}
                  onClick={() => {
                    setMenu(false);
                    tabs.open(ws, n.to);
                  }}
                >
                  {n.label}
                </MenuItem>
              ))}
            </div>
          ))}
        </Popover>
      </div>
      <div className="flex-1 h-full" onDoubleClick={() => setMenu(true)} />
    </div>
  );
}
