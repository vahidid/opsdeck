import { useEffect, useRef, useState } from "react";
import { Plus, X } from "lucide-react";
import { useTabs, tabLabel } from "../context/tabs";
import { NAV, sectionInfo } from "../lib/nav";
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
    <div className="h-9 shrink-0 flex items-stretch border-b border-line bg-panel">
      <div
        ref={strip}
        className="flex items-stretch overflow-x-auto min-w-0 [scrollbar-width:none]"
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
                "group relative flex items-center gap-1.5 pl-3 pr-1.5 min-w-[110px] max-w-[220px] border-r border-line cursor-pointer select-none text-[12.5px] [&_svg]:size-3.5",
                isActive ? "bg-bg text-fg" : "text-muted hover:text-fg hover:bg-panel-2",
              )}
            >
              {isActive && <span className="absolute inset-x-0 top-0 h-0.5 bg-accent" />}
              <span className={cx("shrink-0", isActive ? "[&_svg]:text-accent" : "[&_svg]:text-faint")}>{info?.icon}</span>
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
      <div className="relative flex items-center px-1">
        <button onClick={() => setMenu((v) => !v)} className="rounded p-1.5 text-muted hover:text-fg hover:bg-panel-2" title="New tab">
          <Plus className="size-4" />
        </button>
        <Popover open={menu} onClose={() => setMenu(false)} className="left-0 top-full mt-1 w-56">
          <div className="px-3 pt-1 pb-1.5 text-[11px] text-faint">Open in a new tab</div>
          {NAV.map((n) => (
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
        </Popover>
      </div>
      <div className="flex-1" onDoubleClick={() => setMenu(true)} />
    </div>
  );
}
